package ui

import (
	"context"
	"fmt"
	"math"
	"sort"
	"strings"
	"time"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"

	"github.com/tankertop/tankertop/internal/cluster"
)

type viewMode int

const (
	viewDash viewMode = iota
	viewNet
	viewEvents
	viewPressure
	viewNodes
	viewForwards
	viewText  // yaml / describe / inspect / help
	viewFiles // container filesystem browser
)

type focusKind int

const (
	focusPods focusKind = iota
	focusPane
)

// paneMode selects what the bottom-right pane shows for the selected container.
type paneMode int

const (
	paneLogs paneMode = iota
	paneEnv
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
	mKill
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
	treeSel   string // key of the selected tree row (namespace, workload or pod)
	sort      sortMode
	collapsed map[string]bool

	filter    string
	filtering bool

	cpuHist []float64
	memHist []float64
	nodeCPU map[string][]float64
	nodeMEM map[string][]float64
	podCPU  map[string][]float64
	podMEM  map[string][]float64
	podNet  map[string][]float64 // rate history (bytes/sec), docker only

	detailGraph bool // detail pane shows braille plots instead of the text summary

	// Docker network rate: cumulative counters differenced across snapshots.
	netPrev map[string]netSample
	netRate map[string]netSample

	// bottom-right pane (logs or env)
	pane         paneMode
	selContainer int
	logBuf       []string
	logTitle     string
	logKey       string
	logFollow    bool
	logWrap      bool
	logScroll    int
	logHScroll   int // horizontal scroll (columns) when not wrapping
	logPrevious  bool
	logSearch    string
	logSearching bool

	// env pane: runtime env read out of the container, plus the spec env
	envScroll  int
	envHScroll int // horizontal scroll (columns)
	envRuntime []string
	envKey     string // pod/container the runtime env belongs to
	envErr     error
	envLoading bool
	envReveal  bool // show secret-looking values instead of masking them

	// text view (yaml/describe/inspect/help)
	textTitle  string
	textLines  []string
	textScroll int

	// filesystem browser
	fsPod       cluster.PodInfo
	fsContainer string
	fsPath      string
	fsEntries   []fsEntry
	fsCursor    int
	fsScroll    int
	fsErr       error

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
type envMsg struct {
	key  string
	body string
	err  error
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

// fsEntry is one directory entry in the filesystem browser.
type fsEntry struct {
	name string
	dir  bool
}

type fsListMsg struct {
	path    string
	entries []fsEntry
	err     error
}

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
		podMEM:    map[string][]float64{},
		podNet:    map[string][]float64{},
		netPrev:   map[string]netSample{},
		netRate:   map[string]netSample{},
	}
}

// netSample holds bytes (cumulative) or bytes/sec (rate), with the sample time.
type netSample struct {
	rx, tx float64
	t      time.Time
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

// paneKey identifies the selected container; the runtime env is cached against it.
func (m Model) paneKey() string {
	p, ok := m.selectedPod()
	if !ok {
		return ""
	}
	return p.Namespace + "/" + p.Name + "/" + m.selectedContainer()
}

// envCmd execs `env` in the selected container. Unlike logs it is not refreshed
// on every tick — an exec per refresh would hammer the API server.
func (m Model) envCmd() tea.Cmd {
	p, ok := m.selectedPod()
	if !ok || len(p.Containers) == 0 {
		return nil
	}
	container, key, c := m.selectedContainer(), m.paneKey(), m.client
	return func() tea.Msg {
		ctx, cancel := context.WithTimeout(context.Background(), 8*time.Second)
		defer cancel()
		body, err := c.RuntimeEnv(ctx, p.Namespace, p.Name, container)
		return envMsg{key: key, body: body, err: err}
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

// lifecycleCmd runs a Docker lifecycle verb (start/stop/pause/unpause/kill).
func (m Model) lifecycleCmd(verb string, p cluster.PodInfo) tea.Cmd {
	c := m.client
	return func() tea.Msg {
		ctx, cancel := context.WithTimeout(context.Background(), 12*time.Second)
		defer cancel()
		note, err := c.ContainerLifecycle(ctx, verb, p.Name)
		return actionResultMsg{note: note, err: err}
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

// fsListCmd lists a directory inside the container. `ls -1Ap` is portable across
// busybox and coreutils: one entry per line, no . or .., a trailing / on dirs.
func (m Model) fsListCmd(p cluster.PodInfo, container, path string) tea.Cmd {
	c := m.client
	return func() tea.Msg {
		ctx, cancel := context.WithTimeout(context.Background(), 8*time.Second)
		defer cancel()
		out, err := c.Exec(ctx, p.Namespace, p.Name, container, []string{"ls", "-1Ap", path})
		if err != nil {
			return fsListMsg{path: path, err: err}
		}
		return fsListMsg{path: path, entries: parseLsEntries(out)}
	}
}

// fsCatCmd reads a file's head into the text view.
func (m Model) fsCatCmd(p cluster.PodInfo, container, path string) tea.Cmd {
	c := m.client
	return m.textCmd("file: "+path, func(ctx context.Context) (string, error) {
		return c.Exec(ctx, p.Namespace, p.Name, container,
			[]string{"sh", "-c", `head -c 65536 -- "$1" 2>/dev/null || cat -- "$1"`, "_", path})
	})
}

// parseLsEntries turns `ls -1Ap` output into entries, dirs first.
func parseLsEntries(out string) []fsEntry {
	var entries []fsEntry
	for _, line := range strings.Split(out, "\n") {
		name := strings.TrimRight(line, "\r")
		if name == "" {
			continue
		}
		if dir := strings.HasSuffix(name, "/"); dir {
			entries = append(entries, fsEntry{name: strings.TrimSuffix(name, "/"), dir: true})
		} else {
			entries = append(entries, fsEntry{name: name})
		}
	}
	sort.SliceStable(entries, func(i, j int) bool {
		if entries[i].dir != entries[j].dir {
			return entries[i].dir // dirs first
		}
		return entries[i].name < entries[j].name
	})
	return entries
}

// pathJoin joins an absolute dir and a child, keeping it clean.
func pathJoin(dir, child string) string {
	if child == ".." {
		if dir == "/" || dir == "" {
			return "/"
		}
		i := strings.LastIndexByte(strings.TrimRight(dir, "/"), '/')
		if i <= 0 {
			return "/"
		}
		return dir[:i]
	}
	if !strings.HasSuffix(dir, "/") {
		dir += "/"
	}
	return dir + child
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
// Under --ssh the command runs on the remote node, over its own ssh session.
func (m Model) shellCmd(p cluster.PodInfo, container string) tea.Cmd {
	c := m.client.ShellCommand(p.Namespace, p.Name, container)
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
		// Aggregate per namespace and per group so both tree levels can be
		// ordered by the active sort.
		nsCPU, nsMem := map[string]int64{}, map[string]int64{}
		gCPU, gMem := map[string]int64{}, map[string]int64{}
		for _, p := range m.rows {
			nsCPU[p.Namespace] += p.CPUMilli
			nsMem[p.Namespace] += p.MemBytes
			k := groupKey(p)
			gCPU[k] += p.CPUMilli
			gMem[k] += p.MemBytes
		}
		metric := func(cpu, mem map[string]int64, k string) float64 {
			switch m.sort {
			case sortCPU:
				return float64(cpu[k])
			case sortMem:
				return float64(mem[k])
			}
			return 0
		}
		sort.SliceStable(m.rows, func(i, j int) bool {
			a, b := m.rows[i], m.rows[j]
			if a.Namespace != b.Namespace {
				if na, nb := metric(nsCPU, nsMem, a.Namespace), metric(nsCPU, nsMem, b.Namespace); na != nb {
					return na > nb // busiest namespace first
				}
				return a.Namespace < b.Namespace
			}
			if ga, gb := metric(gCPU, gMem, groupKey(a)), metric(gCPU, gMem, groupKey(b)); ga != gb {
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

// nsKey names the namespace node of the tree. The \x00ns prefix keeps it from
// ever colliding with a groupKey.
func nsKey(ns string) string { return "\x00ns\x00" + ns }

func podKey(p cluster.PodInfo) string { return p.Namespace + "/" + p.Name }

type treeRowKind int

const (
	rowNS treeRowKind = iota
	rowGroup
	rowPod
)

// treeRow is one displayed line of the tree. Every row is selectable, including
// the namespace and workload headers — otherwise folding a node would make it
// impossible to select, and so impossible to unfold.
type treeRow struct {
	kind treeRowKind
	key  string // nsKey / groupKey / podKey — stable across refreshes
	pod  int    // index into m.rows; for a header, the first pod beneath it
}

// treeRows is the single source of truth for what the tree displays: the
// renderer draws these rows and the keys navigate them.
func (m Model) treeRows() []treeRow {
	out := make([]treeRow, 0, len(m.rows))
	lastNS, lastCtl := "\x00", "\x00"
	for i, p := range m.rows {
		if p.Namespace != lastNS {
			out = append(out, treeRow{rowNS, nsKey(p.Namespace), i})
			lastNS, lastCtl = p.Namespace, "\x00"
		}
		if m.collapsed[nsKey(p.Namespace)] {
			continue
		}
		if p.Controller != lastCtl {
			out = append(out, treeRow{rowGroup, groupKey(p), i})
			lastCtl = p.Controller
		}
		if m.collapsed[groupKey(p)] {
			continue
		}
		out = append(out, treeRow{rowPod, podKey(p), i})
	}
	return out
}

// treeIndex locates the selected row. Selection is stored as a key rather than
// an index so it survives pods coming and going between refreshes.
func (m Model) treeIndex() int {
	for i, r := range m.treeRows() {
		if r.key == m.treeSel {
			return i
		}
	}
	return 0
}

func (m Model) currentTreeRow() (treeRow, bool) {
	rows := m.treeRows()
	if len(rows) == 0 {
		return treeRow{}, false
	}
	return rows[m.treeIndex()], true
}

// syncCursor points m.cursor at the pod the selected node stands for, so the
// detail, logs and env panes keep working while the cursor sits on a header.
func (m *Model) syncCursor() {
	if r, ok := m.currentTreeRow(); ok {
		m.cursor = r.pod
	}
}

// moveTree walks the displayed tree rows, headers included.
func (m *Model) moveTree(delta int) {
	rows := m.treeRows()
	if len(rows) == 0 {
		return
	}
	i := m.treeIndex() + delta
	if i < 0 {
		i = 0
	}
	if i >= len(rows) {
		i = len(rows) - 1
	}
	m.treeSel, m.cursor = rows[i].key, rows[i].pod
}

func (m *Model) treeToEdge(top bool) {
	rows := m.treeRows()
	if len(rows) == 0 {
		return
	}
	r := rows[0]
	if !top {
		r = rows[len(rows)-1]
	}
	m.treeSel, m.cursor = r.key, r.pod
}

// foldNode toggles a node. When it folds, the selection lands on the node
// itself — the row that is still on screen — so the same key unfolds it.
func (m *Model) foldNode(key string) {
	m.collapsed[key] = !m.collapsed[key]
	if m.collapsed[key] {
		m.treeSel = key
	}
	m.syncCursor()
}

// anyNamespaceExpanded reports whether at least one namespace in the filtered
// rows is currently unfolded — it decides which way N toggles.
func (m Model) anyNamespaceExpanded() bool {
	for _, p := range m.rows {
		if !m.collapsed[nsKey(p.Namespace)] {
			return true
		}
	}
	return false
}

func (m *Model) setAllNamespacesCollapsed(collapsed bool) {
	for _, p := range m.rows {
		m.collapsed[nsKey(p.Namespace)] = collapsed
	}
}

// moveCursor moves the selection by delta rows (flat list).
func (m *Model) moveCursor(delta int) {
	if len(m.rows) == 0 {
		return
	}
	m.cursor += delta
	if m.cursor < 0 {
		m.cursor = 0
	}
	if m.cursor >= len(m.rows) {
		m.cursor = len(m.rows) - 1
	}
}

func (m *Model) cursorToEdge(top bool) {
	if len(m.rows) == 0 {
		return
	}
	m.cursor = 0
	if !top {
		m.cursor = len(m.rows) - 1
	}
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

// paneInnerWidth is the text width inside the bottom-right pane.
func (m Model) paneInnerWidth() int {
	_, _, _, _, rightW := m.dashLayout(m.bodyHeight())
	if rightW < 4 {
		return 1
	}
	return rightW - 2
}

// panePageSize is the number of rows visible in the bottom-right pane.
func (m Model) panePageSize() int {
	_, _, midH, _, _ := m.dashLayout(m.bodyHeight())
	detailH := 6
	if midH < 14 {
		detailH = midH / 2
	}
	p := midH - detailH - 2
	if p < 1 {
		p = 1
	}
	return p
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
			m.focus = focusPane
		}
		return m, nil

	case envMsg:
		if msg.key != m.paneKey() {
			return m, nil // selection moved on while the exec was in flight
		}
		m.envLoading = false
		m.envErr = msg.err
		m.envRuntime = nil
		if body := strings.TrimRight(msg.body, "\n"); body != "" {
			m.envRuntime = strings.Split(body, "\n")
		}
		m.envKey = msg.key
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

	case fsListMsg:
		m.fsErr = msg.err
		if msg.err == nil {
			m.fsPath, m.fsEntries = msg.path, msg.entries
			m.fsCursor, m.fsScroll = 0, 0
		}
		m.view = viewFiles
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

	case tea.MouseMsg:
		return m.handleMouse(msg)
	}
	return m, nil
}

// handleMouse routes the scroll wheel to whatever list currently has focus by
// synthesising the arrow key the same handlers already understand.
func (m Model) handleMouse(msg tea.MouseMsg) (tea.Model, tea.Cmd) {
	// A modal or text-entry owns the screen; don't scroll underneath it.
	if m.modal != nil || m.filtering || m.logSearching {
		return m, nil
	}

	// Horizontal wheel pans the log pane sideways.
	if msg.Button == tea.MouseButtonWheelLeft || msg.Button == tea.MouseButtonWheelRight {
		if m.view == viewDash && m.focus == focusPane && m.pane == paneLogs {
			if msg.Button == tea.MouseButtonWheelRight && !m.logWrap {
				m.logHScroll += 8
			} else if msg.Button == tea.MouseButtonWheelLeft {
				m.logHScroll -= 8
				if m.logHScroll < 0 {
					m.logHScroll = 0
				}
			}
		}
		return m, nil
	}

	var key tea.KeyMsg
	switch msg.Button {
	case tea.MouseButtonWheelUp:
		key = tea.KeyMsg{Type: tea.KeyUp}
	case tea.MouseButtonWheelDown:
		key = tea.KeyMsg{Type: tea.KeyDown}
	default:
		return m, nil
	}

	switch m.view {
	case viewText:
		m.handleScrollKey(key, &m.textScroll, len(m.textLines))
		return m, nil
	case viewNet:
		m.handleScrollKey(key, &m.netScroll, m.netTotalLines())
		return m, nil
	case viewEvents:
		m.handleScrollKey(key, &m.eventScroll, len(m.snap.Events))
		return m, nil
	case viewPressure:
		m.handleScrollKey(key, &m.pressureScroll, len(m.snap.Pods))
		return m, nil
	case viewNodes:
		m.handleScrollKey(key, &m.nodeScroll, len(m.snap.Nodes)*8)
		return m, nil
	case viewForwards:
		return m.handleForwardsKey(key)
	case viewFiles:
		return m.handleFilesKey(key)
	default:
		return m.handleDashKey(key)
	}
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
		m.podMEM[key] = appendHist(m.podMEM[key], float64(p.MemBytes), podHistLen)
	}
	for k := range m.podCPU {
		if _, ok := seen[k]; !ok {
			delete(m.podCPU, k)
			delete(m.podMEM, k)
		}
	}
	m.recordNetRates(seen)
}

// recordNetRates differences Docker's cumulative rx/tx counters between
// snapshots into a per-second rate. A counter that goes backwards (a restarted
// container) is treated as a fresh start rather than a negative rate.
func (m *Model) recordNetRates(live map[string]struct{}) {
	now := m.snap.CollectedAt
	for _, p := range m.snap.Pods {
		if p.NetRxBytes == 0 && p.NetTxBytes == 0 {
			continue
		}
		key := p.Namespace + "/" + p.Name
		cur := netSample{rx: float64(p.NetRxBytes), tx: float64(p.NetTxBytes), t: now}
		if prev, ok := m.netPrev[key]; ok {
			if dt := now.Sub(prev.t).Seconds(); dt > 0 {
				r := netSample{
					rx: math.Max(0, (cur.rx-prev.rx)/dt),
					tx: math.Max(0, (cur.tx-prev.tx)/dt),
				}
				m.netRate[key] = r
				m.podNet[key] = appendHist(m.podNet[key], r.rx+r.tx, podHistLen)
			}
		}
		m.netPrev[key] = cur
	}
	for k := range m.netPrev {
		if _, ok := live[k]; !ok {
			delete(m.netPrev, k)
			delete(m.netRate, k)
			delete(m.podNet, k)
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
