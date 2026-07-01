package ui

import (
	"context"
	"fmt"
	"math"
	"os/exec"
	"sort"
	"strings"
	"time"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"

	"github.com/tpenzkofer/kubeview/internal/cluster"
)

type viewMode int

const (
	viewDash viewMode = iota
	viewNet
	viewEvents
	viewPressure
	viewNodes
	viewForwards
	viewText // yaml / describe / inspect / help
)

type focusKind int

const (
	focusPods focusKind = iota
	focusLogs
)

type sortMode int

const (
	sortDefault sortMode = iota // namespace, name
	sortCPU
	sortMem
	sortRestarts
	sortAge
	sortStatus
	sortModeCount
)

func (s sortMode) label() string {
	switch s {
	case sortCPU:
		return "cpu↓"
	case sortMem:
		return "mem↓"
	case sortRestarts:
		return "restarts↓"
	case sortAge:
		return "age↓"
	case sortStatus:
		return "status"
	default:
		return "name"
	}
}

type modalKind int

const (
	mDelete modalKind = iota
	mRestart
	mScale
	mPortFwd
)

const (
	histLen    = 240
	podHistLen = 40
	logTail    = 1000
)

// Model is the bubbletea application state.
type Model struct {
	client    *cluster.Client
	interval  time.Duration
	namespace string

	snap   cluster.Snapshot
	rows   []cluster.PodInfo
	width  int
	height int

	view   viewMode
	focus  focusKind
	cursor    int
	offset    int
	tree      bool
	sort      sortMode
	collapsed map[string]bool

	filter    string
	filtering bool

	cpuHist []float64
	memHist []float64
	nodeCPU map[string][]float64
	nodeMEM map[string][]float64
	podCPU  map[string][]float64

	// logs pane
	selContainer int
	logBuf       []string
	logTitle     string
	logKey       string
	logFollow    bool
	logWrap      bool
	logScroll    int
	logPrevious  bool
	logSearch    string
	logSearching bool

	// text view (yaml/describe/inspect/help)
	textTitle  string
	textLines  []string
	textScroll int

	netScroll      int
	eventScroll    int
	pressureScroll int
	nodeScroll     int

	forwards  []*forward
	fwdCh      chan fwdEvent
	fwdSeq     int
	fwdCursor  int

	modal  *modalState
	status string
	loaded bool
}

type modalState struct {
	kind    modalKind
	pod     cluster.PodInfo
	target  cluster.ScaleTarget
	prompt  string
	input   string
	isInput bool
}

type snapshotMsg cluster.Snapshot
type tickMsg struct{}
type logsMsg struct {
	key, title, body string
	err              error
	focusLogs        bool
}
type actionResultMsg struct {
	note string
	err  error
}
type scaleInfoMsg struct {
	pod    cluster.PodInfo
	target cluster.ScaleTarget
	ok     bool
}
type textMsg struct {
	title, body string
	err         error
}
type execDoneMsg struct{ err error }

// New constructs the initial model.
func New(c *cluster.Client, interval time.Duration, namespace string) Model {
	return Model{
		client:    c,
		interval:  interval,
		namespace: namespace,
		view:      viewDash,
		focus:     focusPods,
		logFollow: true,
		collapsed: map[string]bool{},
		fwdCh:     make(chan fwdEvent, 16),
		nodeCPU:   map[string][]float64{},
		nodeMEM:   map[string][]float64{},
		podCPU:    map[string][]float64{},
	}
}

func (m Model) Init() tea.Cmd { return tea.Batch(m.collectCmd(), m.listenFwdCmd()) }

func (m Model) collectCmd() tea.Cmd {
	c, ns := m.client, m.namespace
	return func() tea.Msg {
		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		return snapshotMsg(c.Collect(ctx, ns))
	}
}

func (m Model) tickCmd() tea.Cmd {
	return tea.Tick(m.interval, func(time.Time) tea.Msg { return tickMsg{} })
}

func (m Model) selectedPod() (cluster.PodInfo, bool) {
	if m.cursor >= 0 && m.cursor < len(m.rows) {
		return m.rows[m.cursor], true
	}
	return cluster.PodInfo{}, false
}

func (m Model) selectedContainer() string {
	p, ok := m.selectedPod()
	if !ok || len(p.Containers) == 0 {
		return ""
	}
	i := m.selContainer
	if i < 0 || i >= len(p.Containers) {
		i = 0
	}
	return p.Containers[i].Name
}

func (m Model) logsCmd(focus bool) tea.Cmd {
	p, ok := m.selectedPod()
	if !ok || len(p.Containers) == 0 {
		return nil
	}
	container := m.selectedContainer()
	previous := m.logPrevious
	key := p.Namespace + "/" + p.Name + "/" + container
	c := m.client
	return func() tea.Msg {
		ctx, cancel := context.WithTimeout(context.Background(), 8*time.Second)
		defer cancel()
		body, err := c.Logs(ctx, p.Namespace, p.Name, container, logTail, previous)
		tag := ""
		if previous {
			tag = " (previous)"
		}
		return logsMsg{
			key:       key,
			title:     p.Name + " [" + container + "]" + tag,
			body:      body, err: err, focusLogs: focus,
		}
	}
}

func (m Model) deleteCmd(p cluster.PodInfo) tea.Cmd {
	c := m.client
	return func() tea.Msg {
		ctx, cancel := context.WithTimeout(context.Background(), 8*time.Second)
		defer cancel()
		err := c.DeletePod(ctx, p.Namespace, p.Name)
		return actionResultMsg{note: "deleted pod " + p.Name, err: err}
	}
}

func (m Model) restartCmd(p cluster.PodInfo) tea.Cmd {
	c := m.client
	return func() tea.Msg {
		ctx, cancel := context.WithTimeout(context.Background(), 8*time.Second)
		defer cancel()
		note, err := c.RestartWorkload(ctx, p)
		return actionResultMsg{note: note, err: err}
	}
}

func (m Model) scaleInfoCmd(p cluster.PodInfo) tea.Cmd {
	c := m.client
	return func() tea.Msg {
		ctx, cancel := context.WithTimeout(context.Background(), 8*time.Second)
		defer cancel()
		t, ok := c.ScaleInfo(ctx, p)
		return scaleInfoMsg{pod: p, target: t, ok: ok}
	}
}

func (m Model) scaleCmd(t cluster.ScaleTarget, replicas int32) tea.Cmd {
	c := m.client
	return func() tea.Msg {
		ctx, cancel := context.WithTimeout(context.Background(), 8*time.Second)
		defer cancel()
		err := c.Scale(ctx, t.Namespace, t.Name, replicas)
		return actionResultMsg{note: fmt.Sprintf("scaled %s to %d", t.Name, replicas), err: err}
	}
}

func (m Model) textCmd(title string, fn func(ctx context.Context) (string, error)) tea.Cmd {
	return func() tea.Msg {
		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		body, err := fn(ctx)
		return textMsg{title: title, body: body, err: err}
	}
}

func (m Model) yamlCmd(p cluster.PodInfo) tea.Cmd {
	c := m.client
	return m.textCmd("yaml: "+p.Name, func(ctx context.Context) (string, error) {
		return c.PodYAML(ctx, p.Namespace, p.Name)
	})
}

func (m Model) describeCmd(p cluster.PodInfo) tea.Cmd {
	c := m.client
	return m.textCmd("describe: "+p.Name, func(ctx context.Context) (string, error) {
		var b strings.Builder
		fmt.Fprintf(&b, "Name:       %s\nNamespace:  %s\nNode:       %s\nStatus:     %s\nPod IP:     %s\nHost IP:    %s\nControlled: %s/%s\n\n",
			p.Name, p.Namespace, p.Node, p.Status, p.PodIP, p.HostIP, p.OwnerKind, p.OwnerName)
		b.WriteString("Containers:\n")
		for _, c := range p.Containers {
			st := c.State
			if c.Reason != "" {
				st += " (" + c.Reason + ")"
			}
			fmt.Fprintf(&b, "  - %s\n      image:    %s\n      state:    %s\n      restarts: %d\n      cpu/mem:  %s / %s\n",
				c.Name, c.Image, st, c.Restarts, humanCPU(c.CPUMilli), humanBytes(c.MemBytes))
		}
		b.WriteString("\nEvents:\n")
		events, err := c.PodEvents(ctx, p.Namespace, p.Name)
		if err != nil {
			return b.String(), err
		}
		if len(events) == 0 {
			b.WriteString("  <none>\n")
		}
		for _, e := range events {
			fmt.Fprintf(&b, "  %s\n", e)
		}
		return b.String(), nil
	})
}

func (m Model) inspectCmd(p cluster.PodInfo, container string) tea.Cmd {
	c := m.client
	script := "echo '### identity'; id; echo; " +
		"echo '### env'; env | sort; echo; " +
		"echo '### mounts'; (mount || cat /proc/mounts) 2>/dev/null; echo; " +
		"echo '### disk'; df -h 2>/dev/null; echo; " +
		"echo '### processes'; (ps -ef || ps aux) 2>/dev/null; echo; " +
		"echo '### / (root filesystem)'; ls -la / 2>/dev/null"
	return m.textCmd("inspect: "+p.Name+" ["+container+"]", func(ctx context.Context) (string, error) {
		return c.Exec(ctx, p.Namespace, p.Name, container, []string{"sh", "-c", script})
	})
}

// shellCmd suspends the TUI and drops into an interactive shell in the container.
func shellCmd(p cluster.PodInfo, container string) tea.Cmd {
	args := []string{"kubectl", "exec", "-it", "-n", p.Namespace, p.Name, "-c", container,
		"--", "sh", "-c", "exec bash 2>/dev/null || exec sh"}
	c := exec.Command("microk8s", args...)
	return tea.ExecProcess(c, func(err error) tea.Msg { return execDoneMsg{err} })
}

func (m *Model) applyFilter() {
	m.rows = m.rows[:0]
	f := strings.ToLower(m.filter)
	for _, p := range m.snap.Pods {
		if f != "" && !strings.Contains(strings.ToLower(p.Namespace+"/"+p.Name), f) {
			continue
		}
		m.rows = append(m.rows, p)
	}
	if m.tree {
		// Aggregate per group so groups can be ordered by the active sort.
		gCPU := map[string]int64{}
		gMem := map[string]int64{}
		for _, p := range m.rows {
			k := p.Namespace + "\x00" + p.Controller
			gCPU[k] += p.CPUMilli
			gMem[k] += p.MemBytes
		}
		groupMetric := func(p cluster.PodInfo) float64 {
			k := p.Namespace + "\x00" + p.Controller
			switch m.sort {
			case sortCPU:
				return float64(gCPU[k])
			case sortMem:
				return float64(gMem[k])
			}
			return 0
		}
		sort.SliceStable(m.rows, func(i, j int) bool {
			a, b := m.rows[i], m.rows[j]
			if a.Namespace != b.Namespace {
				return a.Namespace < b.Namespace
			}
			if ga, gb := groupMetric(a), groupMetric(b); ga != gb {
				return ga > gb // bigger groups first
			}
			if a.Controller != b.Controller {
				return a.Controller < b.Controller
			}
			return podLess(a, b, m.sort)
		})
	} else if m.sort != sortDefault {
		sort.SliceStable(m.rows, func(i, j int) bool {
			return podLess(m.rows[i], m.rows[j], m.sort)
		})
	}
	if m.cursor >= len(m.rows) {
		m.cursor = len(m.rows) - 1
	}
	if m.cursor < 0 {
		m.cursor = 0
	}
}

func podLess(a, b cluster.PodInfo, s sortMode) bool {
	switch s {
	case sortCPU:
		return a.CPUMilli > b.CPUMilli
	case sortMem:
		return a.MemBytes > b.MemBytes
	case sortRestarts:
		return a.Restarts > b.Restarts
	case sortAge:
		return a.Age > b.Age
	case sortStatus:
		if a.Status != b.Status {
			return a.Status < b.Status
		}
		return a.Name < b.Name
	}
	return a.Name < b.Name
}

func groupKey(p cluster.PodInfo) string { return p.Namespace + "\x00" + p.Controller }

// visibleIndices lists rows that are currently shown (collapsed tree groups hide
// their pods).
func (m Model) visibleIndices() []int {
	out := make([]int, 0, len(m.rows))
	for i, p := range m.rows {
		if m.tree && m.collapsed[groupKey(p)] {
			continue
		}
		out = append(out, i)
	}
	return out
}

// moveCursor moves the selection by delta steps over the visible rows.
func (m *Model) moveCursor(delta int) {
	vis := m.visibleIndices()
	if len(vis) == 0 {
		return
	}
	pos := 0
	for k, idx := range vis {
		if idx == m.cursor {
			pos = k
			break
		}
		if idx > m.cursor { // current row hidden: snap here
			pos = k
			break
		}
	}
	pos += delta
	if pos < 0 {
		pos = 0
	}
	if pos >= len(vis) {
		pos = len(vis) - 1
	}
	m.cursor = vis[pos]
}

func (m *Model) cursorToEdge(top bool) {
	vis := m.visibleIndices()
	if len(vis) == 0 {
		return
	}
	if top {
		m.cursor = vis[0]
	} else {
		m.cursor = vis[len(vis)-1]
	}
}

// snapVisible ensures the cursor sits on a visible row after a collapse.
func (m *Model) snapVisible() {
	vis := m.visibleIndices()
	if len(vis) == 0 {
		return
	}
	for _, idx := range vis {
		if idx >= m.cursor {
			m.cursor = idx
			return
		}
	}
	m.cursor = vis[len(vis)-1]
}

// bodyHeight is the screen height available above the footer.
func (m Model) bodyHeight() int {
	return m.height - lipgloss.Height(m.renderFooter())
}

// dashLayout returns the dashboard panel sizes for a given body height.
func (m Model) dashLayout(bodyH int) (clusterH, nsH, midH, leftW, rightW int) {
	clusterH, nsH = 9, 7
	if bodyH < 32 {
		clusterH, nsH = 8, 5
	}
	midH = bodyH - clusterH - nsH
	if midH < 8 {
		nsH = 0
		midH = bodyH - clusterH
	}
	W := m.width
	leftW = W * 56 / 100
	if leftW < 50 {
		leftW = 50
	}
	if leftW > W-36 {
		leftW = W - 36
	}
	rightW = W - leftW
	return
}

// podPageSize is the number of pod rows visible in the list (for PgUp/PgDn).
func (m Model) podPageSize() int {
	_, _, midH, _, _ := m.dashLayout(m.bodyHeight())
	p := midH - 3 // borders + header row
	if p < 1 {
		p = 1
	}
	return p
}

func (m Model) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch msg := msg.(type) {
	case tea.WindowSizeMsg:
		m.width, m.height = msg.Width, msg.Height
		return m, nil

	case snapshotMsg:
		m.snap = cluster.Snapshot(msg)
		m.loaded = true
		m.recordHistory()
		m.applyFilter()
		return m, tea.Batch(m.tickCmd(), m.logsCmd(false))

	case tickMsg:
		return m, m.collectCmd()

	case logsMsg:
		body := msg.body
		switch {
		case msg.err != nil:
			body = "— " + msg.err.Error()
		case strings.TrimSpace(body) == "":
			body = "(no log output)"
		}
		m.logBuf = strings.Split(strings.TrimRight(body, "\n"), "\n")
		m.logTitle, m.logKey = msg.title, msg.key
		if m.logFollow {
			m.logScroll = 0
		}
		if msg.focusLogs {
			m.focus = focusLogs
		}
		return m, nil

	case actionResultMsg:
		if msg.err != nil {
			m.status = "✗ " + msg.err.Error()
		} else {
			m.status = "✓ " + msg.note
		}
		return m, m.collectCmd()

	case scaleInfoMsg:
		if !msg.ok {
			m.status = "✗ " + msg.pod.Name + " is not part of a scalable Deployment"
			return m, nil
		}
		m.modal = &modalState{
			kind: mScale, pod: msg.pod, target: msg.target, isInput: true,
			input:  fmt.Sprintf("%d", msg.target.Replicas),
			prompt: fmt.Sprintf("Scale deployment/%s to:", msg.target.Name),
		}
		return m, nil

	case textMsg:
		body := msg.body
		if msg.err != nil {
			body += "\n\n— error: " + msg.err.Error()
		}
		m.textTitle = msg.title
		m.textLines = strings.Split(strings.TrimRight(body, "\n"), "\n")
		m.textScroll = 0
		m.view = viewText
		return m, nil

	case execDoneMsg:
		if msg.err != nil {
			m.status = "✗ session ended: " + msg.err.Error()
		}
		return m, m.collectCmd()

	case fwdEvent:
		for _, f := range m.forwards {
			if f.id == msg.id && f.status == "running" {
				f.status = "exited"
				f.err = msg.err
			}
		}
		return m, m.listenFwdCmd()

	case tea.KeyMsg:
		return m.handleKey(msg)
	}
	return m, nil
}

func (m *Model) recordHistory() {
	// In demo mode, backfill a full history on the first sample so the graphs
	// and sparklines are populated immediately (no waiting for real ticks).
	if len(m.cpuHist) == 0 && strings.Contains(m.snap.Context, "demo") {
		m.backfillDemoHistory()
	}

	cpu, mem, _, _ := m.clusterUsage()
	m.cpuHist = appendHist(m.cpuHist, cpu, histLen)
	m.memHist = appendHist(m.memHist, mem, histLen)
	for _, n := range m.snap.Nodes {
		var cf, mf float64
		if n.CPUCapacityMilli > 0 {
			cf = float64(n.CPUUsedMilli) / float64(n.CPUCapacityMilli)
		}
		if n.MemCapacityBytes > 0 {
			mf = float64(n.MemUsedBytes) / float64(n.MemCapacityBytes)
		}
		m.nodeCPU[n.Name] = appendHist(m.nodeCPU[n.Name], cf, histLen)
		m.nodeMEM[n.Name] = appendHist(m.nodeMEM[n.Name], mf, histLen)
	}
	seen := make(map[string]struct{}, len(m.snap.Pods))
	for _, p := range m.snap.Pods {
		key := p.Namespace + "/" + p.Name
		seen[key] = struct{}{}
		m.podCPU[key] = appendHist(m.podCPU[key], float64(p.CPUMilli), podHistLen)
	}
	for k := range m.podCPU {
		if _, ok := seen[k]; !ok {
			delete(m.podCPU, k)
		}
	}
}

// backfillDemoHistory seeds full synthetic history for the demo cluster.
func (m *Model) backfillDemoHistory() {
	cpu, mem, _, _ := m.clusterUsage()
	for i := 0; i < histLen; i++ {
		t := float64(i)
		m.cpuHist = append(m.cpuHist, clamp01(cpu+0.14*math.Sin(t*0.13)+0.05*math.Sin(t*0.5)))
		m.memHist = append(m.memHist, clamp01(mem+0.04*math.Sin(t*0.09)))
	}
	for _, n := range m.snap.Nodes {
		var cf, mf float64
		if n.CPUCapacityMilli > 0 {
			cf = float64(n.CPUUsedMilli) / float64(n.CPUCapacityMilli)
		}
		if n.MemCapacityBytes > 0 {
			mf = float64(n.MemUsedBytes) / float64(n.MemCapacityBytes)
		}
		for i := 0; i < histLen; i++ {
			t := float64(i)
			m.nodeCPU[n.Name] = append(m.nodeCPU[n.Name], clamp01(cf+0.12*math.Sin(t*0.2)))
			m.nodeMEM[n.Name] = append(m.nodeMEM[n.Name], clamp01(mf+0.03*math.Sin(t*0.1)))
		}
	}
	for j, p := range m.snap.Pods {
		key := p.Namespace + "/" + p.Name
		base := float64(p.CPUMilli)
		for i := 0; i < podHistLen; i++ {
			t := float64(i)
			m.podCPU[key] = append(m.podCPU[key], math.Max(0, base+0.35*base*math.Sin(t*0.4+float64(j))))
		}
	}
}

func appendHist(h []float64, v float64, n int) []float64 {
	h = append(h, v)
	if len(h) > n {
		h = h[len(h)-n:]
	}
	return h
}

func (m Model) clusterCaps() (cpuCap, memCap int64) {
	for _, n := range m.snap.Nodes {
		cpuCap += n.CPUCapacityMilli
		memCap += n.MemCapacityBytes
	}
	return
}

func (m Model) clusterUsage() (cpuFrac, memFrac float64, cpuDetail, memDetail string) {
	var cpuUsed, memUsed int64
	for _, n := range m.snap.Nodes {
		cpuUsed += n.CPUUsedMilli
		memUsed += n.MemUsedBytes
	}
	cpuCap, memCap := m.clusterCaps()
	if cpuCap > 0 {
		cpuFrac = float64(cpuUsed) / float64(cpuCap)
	}
	if memCap > 0 {
		memFrac = float64(memUsed) / float64(memCap)
	}
	cpuDetail = fmt.Sprintf("%s/%s", humanCPU(cpuUsed), humanCPU(cpuCap))
	memDetail = fmt.Sprintf("%s/%s", humanBytes(memUsed), humanBytes(memCap))
	return
}

func (m *Model) clampOffset(visible int) {
	if m.cursor < m.offset {
		m.offset = m.cursor
	}
	if m.cursor >= m.offset+visible {
		m.offset = m.cursor - visible + 1
	}
	if m.offset < 0 {
		m.offset = 0
	}
}
