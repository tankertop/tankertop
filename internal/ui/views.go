package ui

import (
	"fmt"
	"net"
	"sort"
	"strconv"
	"strings"

	"github.com/charmbracelet/lipgloss"

	"github.com/tpenzkofer/kubeview/internal/cluster"
)

func (m Model) View() string {
	if m.width == 0 {
		return "initialising…"
	}
	if !m.loaded {
		return "connecting to cluster…"
	}
	if m.snap.Err != nil {
		return frame.Width(m.width - 2).Render(
			lipgloss.NewStyle().Foreground(colRed).Render("cluster error: "+m.snap.Err.Error()) +
				"\n" + styDim.Render("retrying every "+m.interval.String()+" — press q to quit"))
	}

	footer := m.renderFooter()
	bodyH := m.height - lipgloss.Height(footer)

	if m.modal != nil {
		return lipgloss.JoinVertical(lipgloss.Left, m.renderModal(bodyH), footer)
	}

	var content string
	switch m.view {
	case viewText:
		content = m.renderTextView(bodyH)
	case viewNet:
		content = m.renderNetView(bodyH)
	case viewEvents:
		content = m.renderEventsView(bodyH)
	case viewPressure:
		content = m.renderPressureView(bodyH)
	case viewNodes:
		content = m.renderNodesView(bodyH)
	case viewForwards:
		content = m.renderForwardsView(bodyH)
	default:
		content = m.renderDashboard(bodyH)
	}
	return lipgloss.JoinVertical(lipgloss.Left, content, footer)
}

func (m Model) renderDashboard(bodyH int) string {
	W := m.width
	clusterH, nsH, midH, leftW, rightW := m.dashLayout(bodyH)

	cluster := box("cluster", m.clusterLines(W-2, clusterH-2), W, clusterH)

	podsBorder := colFrame
	if m.focus == focusPods {
		podsBorder = colCyan
	}
	title := fmt.Sprintf("pods (%d)", len(m.rows))
	var podBody []string
	if m.tree {
		title = fmt.Sprintf("pods ⤷ tree (%d)", len(m.rows))
		podBody = m.treeLines(leftW-2, midH-2)
	} else {
		if m.sort != sortDefault {
			title = fmt.Sprintf("pods (%d) ↕%s", len(m.rows), m.sort.label())
		}
		podBody = m.podLines(leftW-2, midH-2)
	}
	pods := boxColored(title, podBody, leftW, midH, podsBorder)

	detailH := 6
	if midH < 14 {
		detailH = midH / 2
	}
	logsH := midH - detailH

	var detail string
	if p, ok := m.selectedPod(); ok {
		detail = box("details: "+p.Name, m.detailLines(p, rightW-2), rightW, detailH)
	} else {
		detail = box("details", nil, rightW, detailH)
	}

	paneBorder := colFrame
	if m.focus == focusPane {
		paneBorder = colCyan
	}
	var paneBody []string
	if m.pane == paneEnv {
		paneBody = m.envPaneLines(rightW-2, logsH-2)
	} else {
		paneBody = m.logPaneLines(rightW-2, logsH-2)
	}
	pane := boxColored(m.paneTitle(), paneBody, rightW, logsH, paneBorder)

	right := lipgloss.JoinVertical(lipgloss.Left, detail, pane)
	mid := lipgloss.JoinHorizontal(lipgloss.Top, pods, right)

	parts := []string{cluster, mid}
	if nsH > 0 {
		parts = append(parts, box("namespaces", m.namespaceLines(W-2, nsH-2), W, nsH))
	}
	return lipgloss.JoinVertical(lipgloss.Left, parts...)
}

// clusterLines: big CPU/MEM braille graphs + per-node panel.
func (m Model) clusterLines(inner, rows int) []string {
	if rows < 4 {
		rows = 4
	}
	nodeW := 30
	if nodeW > inner-20 {
		nodeW = inner - 20
	}
	if nodeW < 16 {
		nodeW = 16
	}
	graphW := inner - nodeW - 1

	cpuFrac, memFrac, cpuDetail, memDetail := m.clusterUsage()
	body := rows - 2
	cg := (body + 1) / 2
	mg := body - cg

	compact := func(label string, frac float64, detail string) string {
		return fmt.Sprintf("%s %s %s",
			styText.Bold(true).Render(label),
			lipgloss.NewStyle().Foreground(loadColor(frac)).Bold(true).Render(fmt.Sprintf("%3.0f%%", frac*100)),
			styDim.Render(detail))
	}

	left := make([]string, 0, rows)
	left = append(left, compact("CPU", cpuFrac, cpuDetail))
	left = append(left, brailleGraph(m.cpuHist, graphW, cg)...)
	left = append(left, compact("MEM", memFrac, memDetail))
	left = append(left, brailleGraph(m.memHist, graphW, mg)...)

	right := make([]string, 0, rows)
	right = append(right, styHeader.Render(fmt.Sprintf("%-12s %s", "NODE", "live")))
	for _, n := range m.snap.Nodes {
		dot := lipgloss.NewStyle().Foreground(colGreen).Render("●")
		if !n.Ready {
			dot = lipgloss.NewStyle().Foreground(colRed).Render("●")
		}
		right = append(right, fmt.Sprintf("%s %s", dot, styText.Render(pad(n.Name, nodeW-2))))
		var cf, mf float64
		if n.CPUCapacityMilli > 0 {
			cf = float64(n.CPUUsedMilli) / float64(n.CPUCapacityMilli)
		}
		if n.MemCapacityBytes > 0 {
			mf = float64(n.MemUsedBytes) / float64(n.MemCapacityBytes)
		}
		spark := brailleGraph(m.nodeCPU[n.Name], 6, 1)
		s := ""
		if len(spark) > 0 {
			s = spark[0]
		}
		right = append(right, fmt.Sprintf(" cpu %s %s", s, miniMeter(cf, nodeW-14)))
		right = append(right, fmt.Sprintf(" mem %s %s", strings.Repeat(" ", 6), miniMeter(mf, nodeW-14)))
		right = append(right, styDim.Render(fmt.Sprintf(" pods %d", n.PodCount)))
	}

	return joinRows(left, graphW, right, nodeW, 1)
}

func (m Model) podLines(inner, rows int) []string {
	wNS, wRdy, wStatus, wRst, wCPU, wMem, wAge, wSpark := 11, 5, 15, 3, 6, 7, 4, 9
	gaps := 8
	wPod := inner - wNS - wRdy - wStatus - wRst - wCPU - wMem - wAge - wSpark - gaps
	if wPod < 10 {
		wPod = 10
	}

	head := strings.Join([]string{
		pad("NAMESPACE", wNS), pad("POD", wPod), pad("READY", wRdy),
		pad("STATUS", wStatus), pad("RST", wRst), pad("CPU", wCPU),
		pad("MEM", wMem), pad("AGE", wAge), pad("CPU~", wSpark),
	}, " ")
	lines := []string{styHeader.Render(head)}

	visible := rows - 1
	if visible < 1 {
		visible = 1
	}
	m.clampOffset(visible)
	refCPU := m.refCPU()

	for i := m.offset; i < len(m.rows) && i < m.offset+visible; i++ {
		p := m.rows[i]
		spark := podSpark(m.podCPU[p.Namespace+"/"+p.Name], refCPU, wSpark)
		text := strings.Join([]string{
			pad(p.Namespace, wNS), pad(p.Name, wPod),
			pad(fmt.Sprintf("%d/%d", p.Ready, p.Total), wRdy),
			pad(p.Status, wStatus), pad(fmt.Sprintf("%d", p.Restarts), wRst),
			pad(humanCPU(p.CPUMilli), wCPU), pad(humanBytes(p.MemBytes), wMem),
			pad(humanAge(p.Age.Seconds()), wAge),
		}, " ")
		if i == m.cursor {
			lines = append(lines, stySelect.Render(fit(text, inner-wSpark-1))+" "+spark)
			continue
		}
		fade := lipgloss.NewStyle().Foreground(fadeColor(i-m.offset, visible))
		colored := strings.Join([]string{
			fade.Render(pad(p.Namespace, wNS)), fade.Render(pad(p.Name, wPod)),
			fade.Render(pad(fmt.Sprintf("%d/%d", p.Ready, p.Total), wRdy)),
			lipgloss.NewStyle().Foreground(statusColor(p.Status)).Render(pad(p.Status, wStatus)),
			restartCell(p.Restarts, wRst, fade),
			valueCell(humanCPU(p.CPUMilli), p.CPUMilli == 0, wCPU, fade),
			valueCell(humanBytes(p.MemBytes), p.MemBytes == 0, wMem, fade),
			fade.Render(pad(humanAge(p.Age.Seconds()), wAge)),
		}, " ")
		lines = append(lines, colored+" "+spark)
	}
	if len(m.rows) == 0 {
		lines = append(lines, styDim.Render("  no pods match"))
	}
	return lines
}

// valueCell greys out a zero value, otherwise uses the row's fade colour.
func valueCell(s string, zero bool, w int, fade lipgloss.Style) string {
	if zero {
		return styDim.Render(pad(s, w))
	}
	return fade.Render(pad(s, w))
}

// restartCell highlights non-zero restart counts.
func restartCell(n int32, w int, fade lipgloss.Style) string {
	s := pad(fmt.Sprintf("%d", n), w)
	switch {
	case n == 0:
		return styDim.Render(s)
	case n >= 5:
		return lipgloss.NewStyle().Foreground(colRed).Render(s)
	default:
		return lipgloss.NewStyle().Foreground(colYellow).Render(s)
	}
}

func (m Model) refCPU() float64 {
	ref := 1.0
	for _, p := range m.rows {
		if float64(p.CPUMilli) > ref {
			ref = float64(p.CPUMilli)
		}
	}
	return ref
}

// treeLines renders the tree rows: Namespace ▸ Workload ▸ Pod.
func (m Model) treeLines(inner, rows int) []string {
	if rows < 1 {
		rows = 1
	}
	disp := m.treeRows()
	sel := m.treeIndex()
	refCPU := m.refCPU()

	// window so the selected row stays visible
	start := 0
	if sel >= rows {
		start = sel - rows + 1
	}
	end := start + rows
	if end > len(disp) {
		end = len(disp)
	}
	out := make([]string, 0, end-start)
	for i := start; i < end; i++ {
		out = append(out, m.treeRowLine(disp[i], i == sel, inner, refCPU))
	}
	return out
}

func (m Model) treeRowLine(r treeRow, selected bool, inner int, refCPU float64) string {
	p := m.rows[r.pod]
	switch r.kind {
	case rowNS:
		pods, ready, cpu, mem, workloads := nsAgg(m.rows, p.Namespace)
		meta := fmt.Sprintf("  (%d workload%s, %d pod%s, %d ready)  cpu %s  mem %s",
			workloads, plural(workloads), pods, plural(pods), ready, humanCPU(cpu), humanBytes(mem))
		if selected {
			return stySelect.Render(fit(caretFor(m.collapsed[r.key])+" "+p.Namespace+meta, inner))
		}
		return styDim.Render(caretFor(m.collapsed[r.key])) + " " +
			styHeader.Render(p.Namespace) + styDim.Render(meta)

	case rowGroup:
		cnt, ready, cpu, mem := groupAgg(m.rows, p.Namespace, p.Controller)
		meta := fmt.Sprintf("  (%d pod%s, %d ready)  cpu %s  mem %s",
			cnt, plural(cnt), ready, humanCPU(cpu), humanBytes(mem))
		if selected {
			return "  " + stySelect.Render(fit(caretFor(m.collapsed[r.key])+" "+p.Controller+meta, inner-2))
		}
		return "  " + styDim.Render(caretFor(m.collapsed[r.key])) + " " +
			lipgloss.NewStyle().Foreground(colMagenta).Render(p.Controller) + styDim.Render(meta)
	}
	return m.treePodRow(p, selected, inner, refCPU)
}

func caretFor(collapsed bool) string {
	if collapsed {
		return "▸"
	}
	return "▾"
}

func (m Model) treePodRow(p cluster.PodInfo, selected bool, inner int, refCPU float64) string {
	const indent = "      "
	cw := inner - len(indent)
	wStatus, wRst, wCPU, wMem, wSpark := 15, 4, 6, 7, 8
	wName := cw - wStatus - wRst - wCPU - wMem - wSpark - 5
	if wName < 8 {
		wName = 8
	}
	dotCol := colGreen
	if p.Total == 0 || p.Ready < p.Total {
		dotCol = colYellow
	}
	if strings.Contains(p.Status, "BackOff") || p.Status == "Error" || strings.Contains(p.Status, "Failed") {
		dotCol = colRed
	}
	name := "● " + p.Name
	spark := podSpark(m.podCPU[p.Namespace+"/"+p.Name], refCPU, wSpark)
	plain := strings.Join([]string{
		pad(name, wName), pad(p.Status, wStatus),
		pad(fmt.Sprintf("%d", p.Restarts), wRst),
		pad(humanCPU(p.CPUMilli), wCPU), pad(humanBytes(p.MemBytes), wMem),
	}, " ")
	if selected {
		return indent + stySelect.Render(fit(plain, cw-wSpark-1)) + " " + spark
	}
	nameCol := colText
	switch {
	case dotCol == colRed:
		nameCol = colRed
	case p.Status == "Completed" || p.Status == "Succeeded":
		nameCol = colDim
	}
	neutral := lipgloss.NewStyle().Foreground(colText)
	colored := strings.Join([]string{
		lipgloss.NewStyle().Foreground(nameCol).Render(pad(name, wName)),
		lipgloss.NewStyle().Foreground(statusColor(p.Status)).Render(pad(p.Status, wStatus)),
		restartCell(p.Restarts, wRst, neutral),
		valueCell(humanCPU(p.CPUMilli), p.CPUMilli == 0, wCPU, neutral),
		valueCell(humanBytes(p.MemBytes), p.MemBytes == 0, wMem, neutral),
	}, " ")
	return indent + colored + " " + spark
}

// nsAgg totals one namespace across the filtered rows.
func nsAgg(rows []cluster.PodInfo, ns string) (count, ready int, cpu, mem int64, workloads int) {
	seen := map[string]struct{}{}
	for _, p := range rows {
		if p.Namespace != ns {
			continue
		}
		count++
		cpu += p.CPUMilli
		mem += p.MemBytes
		if p.Total > 0 && p.Ready == p.Total {
			ready++
		}
		if _, ok := seen[p.Controller]; !ok {
			seen[p.Controller] = struct{}{}
			workloads++
		}
	}
	return
}

func groupAgg(rows []cluster.PodInfo, ns, ctl string) (count, ready int, cpu, mem int64) {
	for _, p := range rows {
		if p.Namespace == ns && p.Controller == ctl {
			count++
			cpu += p.CPUMilli
			mem += p.MemBytes
			if p.Total > 0 && p.Ready == p.Total {
				ready++
			}
		}
	}
	return
}

func plural(n int) string {
	if n == 1 {
		return ""
	}
	return "s"
}

// detailLines is a compact summary strip (with inline plain-language hints).
func (m Model) detailLines(p cluster.PodInfo, inner int) []string {
	owner := "via " + p.Controller
	if p.Controller == "" || p.Controller == "(standalone)" {
		owner = "standalone pod"
	}
	ports := "—"
	if len(p.ContainerPorts) > 0 {
		ps := make([]string, len(p.ContainerPorts))
		for i, pt := range p.ContainerPorts {
			ps[i] = fmt.Sprintf("%d", pt)
		}
		ports = strings.Join(ps, ",")
	}
	cname := m.selectedContainer()
	cinfo := ""
	for _, c := range p.Containers {
		if c.Name == cname {
			cinfo = c.Image
		}
	}
	sel := fmt.Sprintf("container %d/%d: %s", m.selContainer+1, len(p.Containers), cname)
	if len(p.Containers) > 1 {
		sel += styDim.Render("  ([ ] switch)")
	}

	// The resource line differs by backend: Docker exposes network I/O and PIDs
	// (which the k8s metrics API does not), while ready/restarts matter more for
	// pods than for a single container.
	var resLine string
	if m.client.IsDocker() {
		net := "—"
		if r, ok := m.netRate[p.Namespace+"/"+p.Name]; ok {
			net = fmt.Sprintf("↓%s/s ↑%s/s", humanBytes(int64(r.rx)), humanBytes(int64(r.tx)))
		}
		resLine = fmt.Sprintf("cpu %s  mem %s  net %s  pids %d  ports %s",
			humanCPU(p.CPUMilli), humanBytes(p.MemBytes), net, p.PIDs, ports)
	} else {
		resLine = fmt.Sprintf("cpu %s  mem %s  ready %d/%d  restarts %d  ports %s",
			humanCPU(p.CPUMilli), humanBytes(p.MemBytes), p.Ready, p.Total, p.Restarts, ports)
	}

	return []string{
		fmt.Sprintf("%s  %s  %s",
			lipgloss.NewStyle().Foreground(statusColor(p.Status)).Bold(true).Render(p.Status),
			styText.Render(podIPLabel(p)),
			styDim.Render("on "+p.Node+"  "+owner)),
		styDim.Render(resLine),
		styText.Render(sel),
		styDim.Render("image: " + cinfo),
	}
}

func podIPLabel(p cluster.PodInfo) string {
	if p.PodIP == "" {
		return "no IP yet"
	}
	return "ip " + p.PodIP
}

// paneTitle is the title of the bottom-right pane, for whichever mode is active.
func (m Model) paneTitle() string {
	if m.pane == paneEnv {
		t := "env"
		if p, ok := m.selectedPod(); ok {
			t = "env: " + p.Name + " [" + m.selectedContainer() + "]"
		}
		state := "masked"
		if m.envReveal {
			state = "revealed"
		}
		return t + "  · " + state + " (e logs)"
	}
	return m.logsTitle()
}

func (m Model) logsTitle() string {
	t := "logs"
	if m.logTitle != "" {
		t = "logs: " + m.logTitle
	}
	if m.logSearching {
		return t + "  /" + m.logSearch + "█"
	}
	state := "follow"
	if !m.logFollow {
		state = fmt.Sprintf("scroll +%d", m.logScroll)
	}
	if m.logSearch != "" {
		state += " /" + m.logSearch
	}
	return t + "  · " + state
}

// logPaneLines builds the visible window of the log buffer.
func (m Model) logPaneLines(inner, rows int) []string {
	if rows < 1 {
		rows = 1
	}
	lines := m.logBuf
	if m.logSearch != "" {
		needle := strings.ToLower(m.logSearch)
		filtered := lines[:0:0]
		for _, l := range lines {
			if strings.Contains(strings.ToLower(l), needle) {
				filtered = append(filtered, l)
			}
		}
		lines = filtered
	}
	if m.logWrap {
		wrapped := make([]string, 0, len(lines))
		for _, l := range lines {
			wrapped = append(wrapped, wrapLine(l, inner)...)
		}
		lines = wrapped
	}

	total := len(lines)
	s := m.logScroll
	if max := total - rows; s > max {
		s = max
	}
	if s < 0 {
		s = 0
	}
	end := total - s
	start := end - rows
	if start < 0 {
		start = 0
	}
	out := make([]string, 0, end-start)
	for _, l := range lines[start:end] {
		out = append(out, logColor(l).Render(fit(l, inner)))
	}
	return out
}

// logColor tints a log line by apparent severity — error/warn lines are where
// you look first. Works for any backend's logs.
func logColor(line string) lipgloss.Style {
	switch logSeverity(line) {
	case sevError:
		return lipgloss.NewStyle().Foreground(colRed)
	case sevWarn:
		return lipgloss.NewStyle().Foreground(colYellow)
	}
	return styText
}

type severity int

const (
	sevNone severity = iota
	sevWarn
	sevError
)

var (
	errorTokens = []string{"error", "fatal", "panic", "exception", "failed", `"error"`, "level=error"}
	warnTokens  = []string{"warn", "deprecat", `"warn"`, "level=warn"}
)

func logSeverity(line string) severity {
	l := strings.ToLower(line)
	for _, t := range errorTokens {
		if strings.Contains(l, t) {
			return sevError
		}
	}
	for _, t := range warnTokens {
		if strings.Contains(l, t) {
			return sevWarn
		}
	}
	return sevNone
}

// buildEnvLines shows the container's environment twice over: what the pod spec
// declares (instant, from the snapshot) and what the process actually sees
// (an `env` exec, cached per container).
func (m Model) buildEnvLines(inner int) []string {
	p, ok := m.selectedPod()
	if !ok {
		return []string{styDim.Render("no pod selected")}
	}
	var ci cluster.ContainerInfo
	cname := m.selectedContainer()
	for _, c := range p.Containers {
		if c.Name == cname {
			ci = c
		}
	}

	out := []string{styHeader.Render("DECLARED IN THE POD SPEC")}
	if len(ci.Env) == 0 && len(ci.EnvFrom) == 0 {
		out = append(out, styDim.Render("  (none — every variable below is injected by the kubelet or the image)"))
	}
	for _, e := range ci.Env {
		if e.From != "" {
			out = append(out, "  "+styText.Render(e.Name)+styDim.Render("  ← ")+
				lipgloss.NewStyle().Foreground(colMagenta).Render(e.From))
			continue
		}
		out = append(out, "  "+envAssign(e.Name, e.Value, m.envReveal))
	}
	for _, s := range ci.EnvFrom {
		prefix := ""
		if s.Prefix != "" {
			prefix = ", prefixed " + s.Prefix
		}
		out = append(out, "  "+styDim.Render("envFrom ")+
			lipgloss.NewStyle().Foreground(colMagenta).Render(s.Kind+" "+s.Name)+
			styDim.Render("  (all keys"+prefix+")"))
	}

	out = append(out, "", styHeader.Render("RUNTIME (env inside the container)"))
	switch {
	case m.envLoading:
		out = append(out, styDim.Render("  reading…"))
	case m.envErr != nil:
		out = append(out,
			lipgloss.NewStyle().Foreground(colYellow).Render("  "+m.envErr.Error()),
			styDim.Render("  (no shell in the image, or the container isn't running — the spec env above still applies)"))
	case len(m.envRuntime) == 0:
		out = append(out, styDim.Render("  press e to read it"))
	default:
		for _, l := range m.envRuntime {
			name, value, found := strings.Cut(l, "=")
			if !found {
				out = append(out, styText.Render("  "+l))
				continue
			}
			out = append(out, "  "+envAssign(name, value, m.envReveal))
		}
	}
	return out
}

// envAssign renders NAME=value, masking values that look like credentials
// unless the user asked to reveal them.
func envAssign(name, value string, reveal bool) string {
	if !reveal && looksSecret(name, value) {
		return lipgloss.NewStyle().Foreground(colCyan).Render(name) +
			styDim.Render("=") + lipgloss.NewStyle().Foreground(colYellow).Render("••••••••")
	}
	return lipgloss.NewStyle().Foreground(colCyan).Render(name) +
		styDim.Render("=") + styText.Render(escapeCtl(value))
}

var ctlEscaper = strings.NewReplacer("\n", `\n`, "\r", `\r`, "\t", `\t`)

// escapeCtl makes line breaks in an env value visible rather than letting them
// silently vanish — a trailing \n in a manifest is usually a YAML accident.
func escapeCtl(s string) string {
	if !strings.ContainsAny(s, "\n\r\t") {
		return s
	}
	return ctlEscaper.Replace(s)
}

var secretNameHints = []string{"PASS", "SECRET", "TOKEN", "CRED", "DSN", "SALT", "PRIVATE"}

// looksSecret is a heuristic: a credential-ish name, or a URL carrying userinfo.
// Values that are plainly not credentials (a port number, an IP, a service URL)
// are never masked — Kubernetes injects a pile of FOO_SERVICE_PORT_5000_TCP_ADDR
// variables that would otherwise trip the name check.
func looksSecret(name, value string) bool {
	if value == "" || benignValue(value) {
		return false
	}
	up := strings.ToUpper(name)
	for _, h := range secretNameHints {
		if strings.Contains(up, h) {
			return true
		}
	}
	if strings.HasSuffix(up, "_KEY") || strings.Contains(up, "API_KEY") || strings.Contains(up, "APIKEY") {
		return true
	}
	if i := strings.Index(value, "://"); i >= 0 {
		if rest := value[i+3:]; strings.Contains(rest, ":") && strings.Contains(rest, "@") {
			return true // scheme://user:password@host
		}
	}
	return false
}

// benignValue reports values whose shape rules out a credential.
func benignValue(v string) bool {
	if _, err := strconv.Atoi(v); err == nil {
		return true
	}
	if net.ParseIP(v) != nil {
		return true
	}
	return strings.HasPrefix(v, "tcp://") || strings.HasPrefix(v, "udp://")
}

// envPaneLines is the scrolled window of the env pane.
func (m Model) envPaneLines(inner, rows int) []string {
	lines := scrollWindow(m.buildEnvLines(inner), m.envScroll, rows)
	for i := range lines {
		lines[i] = fit(lines[i], inner)
	}
	return lines
}

func wrapLine(s string, width int) []string {
	if width < 1 {
		width = 1
	}
	r := []rune(s)
	if len(r) <= width {
		return []string{s}
	}
	var out []string
	for len(r) > width {
		out = append(out, string(r[:width]))
		r = r[width:]
	}
	if len(r) > 0 {
		out = append(out, string(r))
	}
	return out
}

func (m Model) namespaceLines(inner, rows int) []string {
	type agg struct {
		pods, running int
		cpu, mem      int64
	}
	byNS := map[string]*agg{}
	for _, p := range m.snap.Pods {
		a := byNS[p.Namespace]
		if a == nil {
			a = &agg{}
			byNS[p.Namespace] = a
		}
		a.pods++
		if p.Status == "Running" || p.Status == "Completed" {
			a.running++
		}
		a.cpu += p.CPUMilli
		a.mem += p.MemBytes
	}
	names := make([]string, 0, len(byNS))
	for n := range byNS {
		names = append(names, n)
	}
	sort.Slice(names, func(i, j int) bool { return byNS[names[i]].cpu > byNS[names[j]].cpu })

	// Size the counts column to the widest one on screen: a namespace with 207
	// pods is three characters wider than one with 5, and a %2d minimum width
	// would let it push every bar to its right out of alignment.
	counts := make(map[string]string, len(names))
	countW := 0
	for _, n := range names {
		c := fmt.Sprintf("%d/%d", byNS[n].running, byNS[n].pods)
		counts[n] = c
		if len(c) > countW {
			countW = len(c)
		}
	}

	cpuCap, memCap := m.clusterCaps()
	lines := make([]string, 0, rows)
	for _, n := range names {
		if len(lines) >= rows {
			break
		}
		a := byNS[n]
		var cf, mf float64
		if cpuCap > 0 {
			cf = float64(a.cpu) / float64(cpuCap)
		}
		if memCap > 0 {
			mf = float64(a.mem) / float64(memCap)
		}
		lines = append(lines, fmt.Sprintf("%s %s pods   %s cpu %s %s   %s mem %s %s",
			styText.Render(pad(n, 16)),
			styDim.Render(padLeft(counts[n], countW)),
			styDim.Render("│"), miniMeter(cf, 16), styDim.Render(pad(humanCPU(a.cpu), 7)),
			styDim.Render("│"), miniMeter(mf, 16), styDim.Render(pad(humanBytes(a.mem), 7)),
		))
	}
	return lines
}

// ---- network view ----

func (m Model) buildNetLines() []string {
	var out []string
	out = append(out, styDim.Render("A Service is a stable virtual IP that load-balances to the pods matching its selector — those pods are its \"endpoints\"."))
	out = append(out, "")
	out = append(out, styHeader.Render("SERVICES"))
	for _, s := range m.snap.Services {
		out = append(out, fmt.Sprintf("%s  %s  %s  %s  %s",
			styText.Bold(true).Render(s.Namespace+"/"+s.Name),
			lipgloss.NewStyle().Foreground(colMagenta).Render(s.Type),
			styText.Render(s.ClusterIP),
			styText.Render(strings.Join(s.Ports, ",")),
			styDim.Render("selector "+s.Selector)))
		if len(s.Endpoints) == 0 {
			out = append(out, styDim.Render("    └ endpoints: <none>  "+lipgloss.NewStyle().Foreground(colYellow).Render("(nothing is backing this service)")))
		}
		for _, e := range s.Endpoints {
			mark := lipgloss.NewStyle().Foreground(colGreen).Render("ready")
			if !e.Ready {
				mark = lipgloss.NewStyle().Foreground(colRed).Render("notready")
			}
			out = append(out, fmt.Sprintf("    └ %s  %s  %s", styText.Render(e.IP), styDim.Render(e.PodName), mark))
		}
	}

	out = append(out, "")
	out = append(out, styHeader.Render("INGRESS")+styDim.Render("   (external HTTP routes → services)"))
	if len(m.snap.Ingresses) == 0 {
		out = append(out, styDim.Render("  <none>"))
	}
	for _, ing := range m.snap.Ingresses {
		out = append(out, fmt.Sprintf("%s  class:%s  addr:%s",
			styText.Bold(true).Render(ing.Namespace+"/"+ing.Name), ing.Class, ing.Address))
		for _, r := range ing.Rules {
			out = append(out, styDim.Render("    "+r))
		}
	}

	out = append(out, "")
	out = append(out, styHeader.Render("NETWORK POLICIES")+styDim.Render("   (firewall rules between pods)"))
	if len(m.snap.NetPols) == 0 {
		out = append(out, styDim.Render("  <none> — all pod-to-pod traffic is allowed by default"))
	}
	for _, np := range m.snap.NetPols {
		out = append(out, fmt.Sprintf("%s  selector:%s  ingress-rules:%d  egress-rules:%d",
			styText.Bold(true).Render(np.Namespace+"/"+np.Name), np.PodSelector, np.Ingress, np.Egress))
	}
	return out
}

func (m Model) netLines() []string {
	if m.client.IsDocker() {
		return m.buildDockerNetLines()
	}
	return m.buildNetLines()
}

func (m Model) netTotalLines() int { return len(m.netLines()) }

// buildDockerNetLines renders docker networks, the containers on each, and a
// copy-paste Mermaid diagram of the topology.
func (m Model) buildDockerNetLines() []string {
	out := []string{
		styDim.Render("Docker networks and the containers attached to each. A container can be on several networks; they can reach each other by name within a network."),
		"",
	}
	if len(m.snap.Networks) == 0 {
		out = append(out, styDim.Render("  <none>"))
		return out
	}
	for _, n := range m.snap.Networks {
		subnet := ""
		if n.Subnet != "" {
			subnet = "  " + n.Subnet
		}
		out = append(out, fmt.Sprintf("%s  %s%s",
			styText.Bold(true).Render(n.Name),
			lipgloss.NewStyle().Foreground(colMagenta).Render(n.Driver),
			styDim.Render(subnet)))
		if len(n.Containers) == 0 {
			out = append(out, styDim.Render("    └ (no containers)"))
		}
		for _, ep := range n.Containers {
			out = append(out, fmt.Sprintf("    └ %s  %s",
				styText.Render(pad(ep.Name, 28)), styDim.Render(ep.IPv4)))
		}
		out = append(out, "")
	}

	out = append(out, styHeader.Render("MERMAID  (copy into a Markdown/Mermaid renderer)"), "")
	for _, l := range mermaidNetwork(m.snap.Networks) {
		out = append(out, styText.Render(l))
	}
	return out
}

// mermaidNetwork emits a Mermaid graph: each network is a subgraph holding the
// containers attached to it.
func mermaidNetwork(nets []cluster.DockerNetwork) []string {
	lines := []string{"graph LR"}
	for _, n := range nets {
		if len(n.Containers) == 0 {
			continue
		}
		lines = append(lines, "  subgraph "+mermaidID("net_"+n.Name)+"[\""+n.Name+"\"]")
		for _, ep := range n.Containers {
			lines = append(lines, "    "+mermaidID(ep.Name)+"[\""+ep.Name+"\"]")
		}
		lines = append(lines, "  end")
	}
	return lines
}

// mermaidID sanitises a name into a Mermaid-safe node id (alphanumerics kept,
// everything else underscored, and a letter prefixed so it never starts with a
// digit — which some Mermaid versions reject).
func mermaidID(s string) string {
	var b strings.Builder
	for _, r := range s {
		if (r >= 'a' && r <= 'z') || (r >= 'A' && r <= 'Z') || (r >= '0' && r <= '9') {
			b.WriteRune(r)
		} else {
			b.WriteByte('_')
		}
	}
	out := b.String()
	if out == "" || (out[0] >= '0' && out[0] <= '9') {
		out = "n" + out
	}
	return out
}

func (m Model) renderNetView(bodyH int) string {
	lines := m.netLines()
	inner := m.width - 2
	rows := bodyH - 2
	lines = scrollWindow(lines, m.netScroll, rows)
	for i := range lines {
		lines[i] = fit(lines[i], inner)
	}
	title := "network  (esc/1 back · ↑↓ pgup/pgdn scroll)"
	if m.client.IsDocker() {
		title = "networks  (esc/1 back · ↑↓ pgup/pgdn scroll)"
	}
	return box(title, lines, m.width, bodyH)
}

// ---- events view ----

func (m Model) buildEventLines(inner int) []string {
	wType, wReason, wObj, wAge := 8, 20, 34, 5
	wMsg := inner - wType - wReason - wObj - wAge - 4
	if wMsg < 10 {
		wMsg = 10
	}
	head := strings.Join([]string{
		pad("TYPE", wType), pad("REASON", wReason), pad("OBJECT", wObj),
		pad("AGE", wAge), pad("MESSAGE", wMsg),
	}, " ")
	lines := []string{
		styDim.Render("Events are the cluster's log of what the control plane did/tried — Warnings (red) are where to look first."),
		"",
		styHeader.Render(head),
	}
	if len(m.snap.Events) == 0 {
		lines = append(lines, styDim.Render("  <none>"))
		return lines
	}
	for _, e := range m.snap.Events {
		typeCol := colGreen
		if e.Type == "Warning" {
			typeCol = colRed
		}
		reason := e.Reason
		if e.Count > 1 {
			reason = fmt.Sprintf("%s x%d", e.Reason, e.Count)
		}
		ns := ""
		if e.Namespace != "" {
			ns = e.Namespace + "/"
		}
		line := strings.Join([]string{
			lipgloss.NewStyle().Foreground(typeCol).Render(pad(e.Type, wType)),
			pad(reason, wReason),
			styText.Render(pad(ns+e.Object, wObj)),
			styDim.Render(pad(humanAge(e.Age.Seconds()), wAge)),
			styDim.Render(pad(strings.ReplaceAll(e.Message, "\n", " "), wMsg)),
		}, " ")
		lines = append(lines, line)
	}
	return lines
}

func (m Model) renderEventsView(bodyH int) string {
	inner := m.width - 2
	rows := bodyH - 2
	lines := m.buildEventLines(inner)
	lines = scrollWindow(lines, m.eventScroll, rows)
	for i := range lines {
		lines[i] = fit(lines[i], inner)
	}
	warns := 0
	for _, e := range m.snap.Events {
		if e.Type == "Warning" {
			warns++
		}
	}
	title := fmt.Sprintf("events (%d, %d warning)  (esc/1 back · ↑↓ pgup/pgdn scroll)", len(m.snap.Events), warns)
	return box(title, lines, m.width, bodyH)
}

// ---- resource pressure view ----

type pressureRow struct {
	p     cluster.PodInfo
	risk  float64
	flags []string
}

func (m Model) buildPressure() []pressureRow {
	rows := make([]pressureRow, 0, len(m.snap.Pods))
	for _, p := range m.snap.Pods {
		if p.Total == 0 {
			continue
		}
		var pr pressureRow
		pr.p = p
		// memory headroom vs limit
		if p.MemLimBytes > 0 {
			ratio := float64(p.MemBytes) / float64(p.MemLimBytes)
			pr.risk = ratio
			if ratio >= 0.9 {
				pr.flags = append(pr.flags, "near-OOM")
			} else if ratio >= 0.75 {
				pr.flags = append(pr.flags, "mem-tight")
			}
		} else {
			pr.flags = append(pr.flags, "no-mem-limit")
		}
		if p.CPULimMilli == 0 {
			pr.flags = append(pr.flags, "no-cpu-limit")
		}
		if p.MemReqBytes == 0 && p.CPUReqMilli == 0 {
			pr.flags = append(pr.flags, "no-requests")
		} else {
			if p.CPUReqMilli > 0 && p.CPUMilli > p.CPUReqMilli*2 {
				pr.flags = append(pr.flags, "cpu≫req")
			}
			if p.MemReqBytes > 0 && p.MemBytes > p.MemReqBytes {
				pr.flags = append(pr.flags, "mem>req")
			}
		}
		rows = append(rows, pr)
	}
	sort.SliceStable(rows, func(i, j int) bool {
		if rows[i].risk != rows[j].risk {
			return rows[i].risk > rows[j].risk
		}
		return rows[i].p.MemBytes > rows[j].p.MemBytes
	})
	return rows
}

func reqLim(used, req, lim int64, human func(int64) string) string {
	r, l := "—", "—"
	if req > 0 {
		r = human(req)
	}
	if lim > 0 {
		l = human(lim)
	}
	return fmt.Sprintf("%s/%s/%s", human(used), r, l)
}

func (m Model) buildPressureLines(inner int) []string {
	rows := m.buildPressure()
	wName, wCPU, wMem := 34, 20, 26
	wFlags := inner - wName - wCPU - wMem - 3
	if wFlags < 10 {
		wFlags = 10
	}
	lines := []string{
		styDim.Render("used / request / limit.  Flags: near-OOM (>90% of mem limit), no-*-limit (unbounded — can starve neighbours), cpu≫req/mem>req (under-requested)."),
		"",
		styHeader.Render(strings.Join([]string{
			pad("NAMESPACE/POD", wName), pad("CPU u/req/lim", wCPU), pad("MEM u/req/lim", wMem), "FLAGS",
		}, " ")),
	}
	for _, r := range rows {
		p := r.p
		memRatioCol := colText
		if p.MemLimBytes > 0 {
			memRatioCol = gradColor(clamp01(float64(p.MemBytes) / float64(p.MemLimBytes)))
		}
		flagStr := ""
		for _, f := range r.flags {
			fc := colYellow
			if f == "near-OOM" {
				fc = colRed
			}
			flagStr += lipgloss.NewStyle().Foreground(fc).Render(f) + " "
		}
		lines = append(lines, strings.Join([]string{
			styText.Render(pad(p.Namespace+"/"+p.Name, wName)),
			pad(reqLim(p.CPUMilli, p.CPUReqMilli, p.CPULimMilli, humanCPU), wCPU),
			lipgloss.NewStyle().Foreground(memRatioCol).Render(pad(reqLim(p.MemBytes, p.MemReqBytes, p.MemLimBytes, humanBytes), wMem)),
			fit(flagStr, wFlags),
		}, " "))
	}
	return lines
}

func (m Model) renderPressureView(bodyH int) string {
	inner := m.width - 2
	rows := bodyH - 2
	lines := m.buildPressureLines(inner)
	lines = scrollWindow(lines, m.pressureScroll, rows)
	for i := range lines {
		lines[i] = fit(lines[i], inner)
	}
	return box("resource pressure  (esc/1 back · ↑↓ pgup/pgdn scroll)", lines, m.width, bodyH)
}

// ---- nodes view ----

func (m Model) buildNodeLines(inner int) []string {
	var out []string
	bw := inner - 30
	if bw < 10 {
		bw = 10
	}
	for _, n := range m.snap.Nodes {
		dot := lipgloss.NewStyle().Foreground(colGreen).Render("●")
		state := "Ready"
		if !n.Ready {
			dot = lipgloss.NewStyle().Foreground(colRed).Render("●")
			state = "NotReady"
		}
		cond := lipgloss.NewStyle().Foreground(colGreen).Render("no pressure")
		if len(n.Conditions) > 0 {
			cond = lipgloss.NewStyle().Foreground(colRed).Render(strings.Join(n.Conditions, " "))
		}
		out = append(out, fmt.Sprintf("%s %s   %s   %s",
			dot, styTitle.Render(n.Name), styText.Render(state), cond))

		cpuUse, cpuReq := frac(n.CPUUsedMilli, n.CPUAllocMilli), frac(n.CPUReqMilli, n.CPUAllocMilli)
		out = append(out, "  "+meter("CPU", cpuUse, fmt.Sprintf("use %s / req %s / alloc %s  (req %.0f%%)",
			humanCPU(n.CPUUsedMilli), humanCPU(n.CPUReqMilli), humanCPU(n.CPUAllocMilli), cpuReq*100), bw))

		memUse, memReq := frac(n.MemUsedBytes, n.MemAllocBytes), frac(n.MemReqBytes, n.MemAllocBytes)
		out = append(out, "  "+meter("MEM", memUse, fmt.Sprintf("use %s / req %s / alloc %s  (req %.0f%%)",
			humanBytes(n.MemUsedBytes), humanBytes(n.MemReqBytes), humanBytes(n.MemAllocBytes), memReq*100), bw))

		podFrac := 0.0
		if n.PodsCapacity > 0 {
			podFrac = float64(n.PodCount) / float64(n.PodsCapacity)
		}
		out = append(out, "  "+meter("PODS", podFrac, fmt.Sprintf("%d / %d   ephemeral-storage cap %s",
			n.PodCount, n.PodsCapacity, humanBytes(n.EphemeralCapBytes)), bw))
		out = append(out, "")
	}
	return out
}

func frac(a, b int64) float64 {
	if b <= 0 {
		return 0
	}
	return float64(a) / float64(b)
}

func (m Model) renderNodesView(bodyH int) string {
	inner := m.width - 2
	rows := bodyH - 2
	lines := scrollWindow(m.buildNodeLines(inner), m.nodeScroll, rows)
	for i := range lines {
		lines[i] = fit(lines[i], inner)
	}
	return box(fmt.Sprintf("nodes (%d)  (esc/1 back · ↑↓ scroll)", len(m.snap.Nodes)), lines, m.width, bodyH)
}

// ---- port-forward manager ----

func (m Model) renderForwardsView(bodyH int) string {
	inner := m.width - 2
	lines := []string{
		styDim.Render("Background `kubectl port-forward` processes. They bind 0.0.0.0 on " + m.client.ForwardBindHost() + " so you can curl the pod."),
		styDim.Render("Start one with P on a pod (dashboard).  x stop selected · X stop all."),
		"",
	}
	if len(m.forwards) == 0 {
		lines = append(lines, styDim.Render("  no active forwards"))
	}
	for i, f := range m.forwards {
		col := colGreen
		switch f.status {
		case "error":
			col = colRed
		case "exited", "stopped":
			col = colDim
		}
		detail := ""
		if f.err != nil && f.status == "error" {
			detail = "  " + f.err.Error()
		}
		row := fmt.Sprintf("%s  %s:%d → %s/%s:%d   %s%s",
			pad(fmt.Sprintf("#%d", f.id), 4), m.client.ForwardBindHost(), f.local, f.ns, f.pod, f.remote,
			lipgloss.NewStyle().Foreground(col).Render(f.status), styDim.Render(detail))
		if i == m.fwdCursor {
			row = stySelect.Render(fit(row, inner))
		}
		lines = append(lines, row)
	}
	return box(fmt.Sprintf("port-forwards (%d)  (↑↓ select · x stop · X all · esc/1 back)", len(m.forwards)), lines, m.width, bodyH)
}

func (m Model) renderTextView(bodyH int) string {
	inner := m.width - 2
	rows := bodyH - 2
	lines := scrollWindow(m.textLines, m.textScroll, rows)
	for i := range lines {
		lines[i] = fit(lines[i], inner)
	}
	return box(m.textTitle+"  (esc back · ↑↓ pgup/pgdn scroll)", lines, m.width, bodyH)
}

func scrollWindow(lines []string, scroll, rows int) []string {
	if rows < 1 {
		rows = 1
	}
	if scroll > len(lines)-1 {
		scroll = len(lines) - 1
	}
	if scroll < 0 {
		scroll = 0
	}
	end := scroll + rows
	if end > len(lines) {
		end = len(lines)
	}
	return append([]string{}, lines[scroll:end]...)
}

// ---- modal ----

func (m Model) renderModal(bodyH int) string {
	md := m.modal
	var content []string
	content = append(content, styText.Bold(true).Render(md.prompt), "")
	switch {
	case md.isInput:
		content = append(content,
			"  "+styHeader.Render(md.input+"█"),
			"",
			styDim.Render("type a number · enter confirm · esc cancel"))
	case md.kind == mPortFwd:
		content = append(content,
			styDim.Render("forwards 0.0.0.0:"+fmt.Sprintf("%d", firstPort(md.pod))+" on this host → the pod. Ctrl-C in the session to stop."),
			"",
			lipgloss.NewStyle().Foreground(colGreen).Render("  y / enter ")+styDim.Render("start")+"     "+
				lipgloss.NewStyle().Foreground(colRed).Render("n / esc ")+styDim.Render("cancel"))
	default:
		warn := ""
		if md.kind == mDelete || md.kind == mKill {
			warn = "This is destructive."
		}
		content = append(content,
			styDim.Render(warn),
			"",
			lipgloss.NewStyle().Foreground(colGreen).Render("  y / enter ")+styDim.Render("confirm")+"     "+
				lipgloss.NewStyle().Foreground(colRed).Render("n / esc ")+styDim.Render("cancel"))
	}

	w := 60
	if w > m.width-4 {
		w = m.width - 4
	}
	modal := boxColored(" confirm ", content, w, len(content)+2, colYellow)
	return lipgloss.Place(m.width, bodyH, lipgloss.Center, lipgloss.Center, modal)
}

func (m Model) renderFooter() string {
	if m.filtering {
		return styText.Render(" filter: " + m.filter + "█")
	}
	if m.logSearching {
		return styText.Render(" search logs: " + m.logSearch + "█")
	}

	var keys string
	switch m.view {
	case viewText:
		keys = "↑/↓ pgup/pgdn scroll · g/G ends · esc back · q quit"
	case viewNet, viewEvents, viewPressure, viewNodes:
		keys = "↑/↓ pgup/pgdn scroll · 1 dashboard · T theme · q quit"
	case viewForwards:
		keys = "↑/↓ select · x stop · X stop-all · 1 dashboard · q quit"
	default:
		switch {
		case m.focus == focusPane && m.pane == paneEnv:
			keys = "ENV  ↑/↓ pgup/pgdn scroll · m mask/reveal · R re-read · [ ] container · e logs · tab back"
		case m.focus == focusPane:
			keys = "LOGS  ↑/↓ scroll · f follow · w wrap · p prev · [ ] container · / search · e env · tab back"
		case m.tree:
			keys = "↑/↓ move (rows & headers) · space fold/unfold · n namespace · N all · o sort · t flat · tab pane · e env · " + m.actionKeys() + " · ?"
		default:
			keys = "↑/↓ move · o sort · t tree · tab pane · e env · " + m.actionKeys() + " · 2-6 views · T theme · ?"
		}
	}
	bar := styDim.Render(" " + keys)
	if m.status != "" {
		col := colGreen
		if strings.HasPrefix(m.status, "✗") {
			col = colRed
		}
		st := lipgloss.NewStyle().Foreground(col).Render(" " + m.status)
		return lipgloss.JoinVertical(lipgloss.Left, st, bar)
	}
	return bar
}

// actionKeys lists the per-item action keys for the current backend.
func (m Model) actionKeys() string {
	if m.client.IsDocker() {
		return "S shell i inspect y d remove · u start x stop c pause K kill R restart"
	}
	return "S i y D · d R s P"
}

func portFwdPrompt(p cluster.PodInfo) string {
	return fmt.Sprintf("Port-forward pod %s :%d ?", p.Name, firstPort(p))
}

func helpLines() []string {
	return []string{
		styHeader.Render("kubeview — a btop-style control room for Kubernetes"),
		"",
		styText.Render("Mental model for a *nix veteran:"),
		styDim.Render("  • A container is just a process tree in its own mount + network + PID namespaces, fenced off with cgroups."),
		styDim.Render("  • A Pod is one or more containers that SHARE a network namespace (same IP/localhost) — the smallest schedulable unit."),
		styDim.Render("  • A Deployment keeps N identical pods running (a ReplicaSet does the actual counting)."),
		styDim.Render("  • A Service is a stable virtual IP that load-balances to whichever pods match its label selector."),
		styDim.Render("  • A namespace is just a scope/folder for names + a place to hang quotas and policies."),
		"",
		styHeader.Render("Navigation"),
		styText.Render("  1 dashboard  2 network  3 events  4 pressure  5 nodes  6 port-forwards   ? help   r refresh   q quit"),
		styText.Render("  T  cycle colour theme (saved)        P  port-forward selected pod"),
		styDim.Render("  themes: --theme tokyonight|gruvbox|nord|dracula|mono   (current: " + currentTheme + ")"),
		styDim.Render("  preferences (theme, sort, tree, namespace, interval) are saved to " + configPath()),
		styText.Render("  tab           switch focus between the pods list and the bottom-right pane"),
		styText.Render("  e             switch that pane between logs and the container's environment"),
		"",
		styHeader.Render("Pods list (focused)"),
		styText.Render("  ↑/↓ pgup/pgdn g/G  move    / filter pods by name"),
		styText.Render("  o  cycle sort: name → cpu → mem → restarts → age → status"),
		styText.Render("  t  toggle tree view — namespace ▸ workload ▸ pod (what depends on what)"),
		styDim.Render("     ↑/↓ walk every row, headers included; a folded node stays selected so space reopens it"),
		styText.Render("  space  fold/unfold the node under the cursor (on a pod: its workload)"),
		styText.Render("  n  fold/unfold the namespace     N  fold/unfold every namespace at once"),
		styDim.Render("     pod actions (S i y D d R s P) need a pod row, not a header"),
		styText.Render("  S  open an interactive shell inside the selected container (exec -it)"),
		styText.Render("  i  inspect: env, mounts, df, processes, ls /  (read-only)"),
		styText.Render("  y  view live YAML        D  describe + recent events"),
		styText.Render("  d  delete pod            R  restart workload (rollout)"),
		styText.Render("  s  scale deployment      P  port-forward to this host"),
		"",
		styHeader.Render("Logs pane (focused, via tab)"),
		styText.Render("  ↑/↓ pgup/pgdn g/G scroll   f follow tail   w wrap"),
		styText.Render("  p  toggle previous-container logs (why a CrashLoopBackOff died)"),
		styText.Render("  [ ]  switch container      / search within the log"),
		"",
		styHeader.Render("Env pane (e toggles it in place of the logs)"),
		styDim.Render("  Two lists: what the pod spec declares, and what the process really sees."),
		styDim.Render("  The declared list names indirect sources (secret x/y, field spec.nodeName);"),
		styDim.Render("  the runtime list is `env` run inside the container, so it also shows what the"),
		styDim.Render("  kubelet injected (KUBERNETES_SERVICE_HOST, the service links, …) and what the"),
		styDim.Render("  image's own Dockerfile set. It needs a shell in the image."),
		styText.Render("  ↑/↓ pgup/pgdn g/G scroll   m mask/reveal credential-looking values"),
		styText.Render("  R  re-read the runtime env    [ ]  switch container"),
	}
}
