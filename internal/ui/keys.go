package ui

import (
	tea "github.com/charmbracelet/bubbletea"

	"github.com/tpenzkofer/kubeview/internal/cluster"
)

func (m Model) handleKey(msg tea.KeyMsg) (tea.Model, tea.Cmd) {
	// Modal (confirm / scale input) captures everything.
	if m.modal != nil {
		return m.handleModalKey(msg)
	}
	// Filter entry on the pods list.
	if m.filtering {
		return m.handleFilterKey(msg)
	}
	// In-log search entry.
	if m.logSearching {
		return m.handleLogSearchKey(msg)
	}

	// Global keys.
	switch msg.String() {
	case "q", "ctrl+c":
		m.saveConfig()
		return m, tea.Quit
	case "1":
		m.view = viewDash
		return m, nil
	case "2":
		m.view = viewNet
		m.netScroll = 0
		return m, nil
	case "3":
		m.view = viewEvents
		m.eventScroll = 0
		return m, nil
	case "4":
		m.view = viewPressure
		m.pressureScroll = 0
		return m, nil
	case "5":
		m.view = viewNodes
		m.nodeScroll = 0
		return m, nil
	case "6":
		m.view = viewForwards
		return m, nil
	case "T":
		names := ThemeNames()
		idx := 0
		for i, n := range names {
			if n == currentTheme {
				idx = i
				break
			}
		}
		ApplyTheme(names[(idx+1)%len(names)])
		m.status = "theme: " + currentTheme
		m.saveConfig()
		return m, nil
	case "?":
		m.textTitle = "kubeview — keys & plain-language hints"
		m.textLines = helpLines()
		m.textScroll = 0
		m.view = viewText
		return m, nil
	case "r":
		return m, m.collectCmd()
	}

	switch m.view {
	case viewText:
		m.handleScrollKey(msg, &m.textScroll, len(m.textLines))
		return m, nil
	case viewNet:
		m.handleScrollKey(msg, &m.netScroll, m.netTotalLines())
		return m, nil
	case viewEvents:
		m.handleScrollKey(msg, &m.eventScroll, len(m.snap.Events))
		return m, nil
	case viewPressure:
		m.handleScrollKey(msg, &m.pressureScroll, len(m.snap.Pods))
		return m, nil
	case viewNodes:
		m.handleScrollKey(msg, &m.nodeScroll, len(m.snap.Nodes)*6)
		return m, nil
	case viewForwards:
		return m.handleForwardsKey(msg)
	default:
		return m.handleDashKey(msg)
	}
}

func (m Model) handleDashKey(msg tea.KeyMsg) (tea.Model, tea.Cmd) {
	// Bottom-right pane focused: scroll & per-mode controls.
	if m.focus == focusPane {
		if msg.String() == "e" {
			return m.togglePane()
		}
		if m.pane == paneEnv {
			return m.handleEnvPaneKey(msg)
		}
		return m.handleLogPaneKey(msg)
	}

	// Pods pane focused.
	switch msg.String() {
	case "tab":
		m.focus = focusPane
	case "e":
		return m.togglePane()
	case "m":
		// mask/reveal belongs to the env pane, but it is visible from here too
		if m.pane == paneEnv {
			m.envReveal = !m.envReveal
		}
	case "up", "k":
		m.move(-1)
		return m.onSelectionChange()
	case "down", "j":
		m.move(1)
		return m.onSelectionChange()
	case "pgup":
		m.move(-m.podPageSize())
		return m.onSelectionChange()
	case "pgdown":
		m.move(m.podPageSize())
		return m.onSelectionChange()
	case "g", "home":
		m.toEdge(true)
		return m.onSelectionChange()
	case "G", "end":
		m.toEdge(false)
		return m.onSelectionChange()
	case " ", "z":
		// Fold the node under the cursor. On a pod, fold the workload holding it
		// and select that workload, so space folds and unfolds symmetrically.
		if r, ok := m.currentTreeRow(); ok && m.tree {
			switch r.kind {
			case rowPod:
				m.foldNode(groupKey(m.rows[r.pod]))
			default:
				m.foldNode(r.key)
			}
			return m.onSelectionChange()
		}
	case "n":
		if r, ok := m.currentTreeRow(); ok && m.tree {
			m.foldNode(nsKey(m.rows[r.pod].Namespace))
			return m.onSelectionChange()
		}
	case "N":
		if m.tree {
			collapsing := m.anyNamespaceExpanded()
			if r, ok := m.currentTreeRow(); ok && collapsing {
				m.treeSel = nsKey(m.rows[r.pod].Namespace) // land on a row that stays
			}
			m.setAllNamespacesCollapsed(collapsing)
			m.syncCursor()
			return m.onSelectionChange()
		}
	case "t":
		key := m.selectedKey()
		m.tree = !m.tree
		m.applyFilter()
		m.reselect(key)
		return m, m.logsCmd(false)
	case "o":
		key := m.selectedKey()
		m.sort = (m.sort + 1) % sortModeCount
		m.applyFilter()
		m.reselect(key)
		m.status = "sort: " + m.sort.label()
		return m, m.logsCmd(false)
	case "/":
		m.filtering, m.filter = true, ""
	case "enter":
		m.focus = focusPane
	// actions on the selected pod
	case "S":
		if p, ok := m.actionPod(); ok && len(p.Containers) > 0 {
			return m, m.shellCmd(p, m.selectedContainer())
		}
	case "i":
		if p, ok := m.actionPod(); ok && len(p.Containers) > 0 {
			return m, m.inspectCmd(p, m.selectedContainer())
		}
	case "y":
		if p, ok := m.actionPod(); ok {
			return m, m.yamlCmd(p)
		}
	case "D":
		if p, ok := m.actionPod(); ok {
			return m, m.describeCmd(p)
		}
	case "d":
		if p, ok := m.actionPod(); ok {
			m.modal = &modalState{kind: mDelete, pod: p,
				prompt: "Delete pod " + p.Name + "?"}
		}
	case "R":
		if p, ok := m.actionPod(); ok {
			m.modal = &modalState{kind: mRestart, pod: p,
				prompt: "Restart workload behind " + p.Name + "?"}
		}
	case "s":
		if m.client.IsDocker() {
			m.status = "scaling isn't a Docker concept — that's Compose or Swarm"
			return m, nil
		}
		if p, ok := m.actionPod(); ok {
			return m, m.scaleInfoCmd(p)
		}
	case "P":
		if m.client.IsDocker() {
			m.status = "Docker containers already publish their ports — no forward needed"
			return m, nil
		}
		p, ok := m.actionPod()
		switch {
		case !ok:
		case len(p.ContainerPorts) == 0:
			m.status = "no container ports declared on this pod"
		default:
			m.modal = &modalState{kind: mPortFwd, pod: p, prompt: portFwdPrompt(p)}
		}
	}
	return m, nil
}

// togglePane switches the bottom-right pane between logs and env, fetching the
// runtime env the first time it is needed for this container.
func (m Model) togglePane() (tea.Model, tea.Cmd) {
	if m.pane == paneLogs {
		m.pane = paneEnv
		m.envScroll = 0
		cmd := m.refreshEnv()
		return m, cmd
	}
	m.pane = paneLogs
	return m, nil
}

// refreshEnv fetches the runtime env unless it is already cached for the
// selected container.
func (m *Model) refreshEnv() tea.Cmd {
	key := m.paneKey()
	if key == "" || (m.envKey == key && m.envErr == nil) {
		return nil
	}
	m.envLoading = true
	m.envRuntime, m.envErr = nil, nil
	return m.envCmd()
}

func (m Model) handleLogPaneKey(msg tea.KeyMsg) (tea.Model, tea.Cmd) {
	page := m.panePageSize()
	switch msg.String() {
	case "tab", "esc":
		m.focus = focusPods
	case "up", "k":
		m.logScroll++
		m.logFollow = false
	case "down", "j":
		if m.logScroll > 0 {
			m.logScroll--
		}
	case "pgup":
		m.logScroll += page
		m.logFollow = false
	case "pgdown":
		m.logScroll -= page
		if m.logScroll < 0 {
			m.logScroll = 0
		}
	case "g", "home":
		m.logScroll = 1 << 30
		m.logFollow = false
	case "G", "end":
		m.logScroll = 0
		m.logFollow = true
	case "f":
		m.logFollow = !m.logFollow
		if m.logFollow {
			m.logScroll = 0
		}
	case "w":
		m.logWrap = !m.logWrap
	case "p":
		m.logPrevious = !m.logPrevious
		return m, m.logsCmd(false)
	case "[":
		m = m.cycleContainer(-1)
		return m, m.logsCmd(false)
	case "]":
		m = m.cycleContainer(1)
		return m, m.logsCmd(false)
	case "/":
		m.logSearching = true
		m.logSearch = ""
	case "q", "ctrl+c":
		m.saveConfig()
		return m, tea.Quit
	}
	return m, nil
}

func (m Model) handleEnvPaneKey(msg tea.KeyMsg) (tea.Model, tea.Cmd) {
	last := len(m.buildEnvLines(m.paneInnerWidth())) - m.panePageSize()
	if last < 0 {
		last = 0
	}
	m.handleScrollKey(msg, &m.envScroll, last)
	switch msg.String() {
	case "tab", "esc":
		m.focus = focusPods
	case "m":
		m.envReveal = !m.envReveal
	case "R":
		m.envKey = "" // force a re-exec
		cmd := m.refreshEnv()
		return m, cmd
	case "[", "]":
		delta := 1
		if msg.String() == "[" {
			delta = -1
		}
		m = m.cycleContainer(delta)
		m.envScroll, m.envKey = 0, ""
		cmd := m.refreshEnv()
		return m, tea.Batch(cmd, m.logsCmd(false))
	case "q", "ctrl+c":
		m.saveConfig()
		return m, tea.Quit
	}
	return m, nil
}

// move and toEdge dispatch to the tree's node navigation or the flat list's.
func (m *Model) move(delta int) {
	if m.tree {
		m.moveTree(delta)
		return
	}
	m.moveCursor(delta)
}

func (m *Model) toEdge(top bool) {
	if m.tree {
		m.treeToEdge(top)
		return
	}
	m.cursorToEdge(top)
}

// actionPod is the pod a pod action applies to. The panes happily follow a
// header row to the first pod beneath it, but deleting or restarting that pod
// because the cursor was on its namespace would be a nasty surprise.
func (m *Model) actionPod() (cluster.PodInfo, bool) {
	if m.tree {
		if r, ok := m.currentTreeRow(); !ok || r.kind != rowPod {
			m.status = "select a pod — this row is a namespace or a workload"
			return cluster.PodInfo{}, false
		}
	}
	return m.selectedPod()
}

// onSelectionChange resets the bottom-right pane for the newly selected pod.
func (m Model) onSelectionChange() (tea.Model, tea.Cmd) {
	m.selContainer = 0
	m.logScroll = 0
	m.logFollow = true
	m.logPrevious = false
	m.envScroll = 0
	cmds := []tea.Cmd{m.logsCmd(false)}
	if m.pane == paneEnv {
		cmds = append(cmds, m.refreshEnv())
	}
	return m, tea.Batch(cmds...)
}

func (m Model) cycleContainer(delta int) Model {
	p, ok := m.selectedPod()
	if !ok || len(p.Containers) == 0 {
		return m
	}
	n := len(p.Containers)
	m.selContainer = ((m.selContainer+delta)%n + n) % n
	return m
}

// handleScrollKey mutates the model in place (pointer receiver) so scroll
// changes survive — callers return their own m.
func (m *Model) handleScrollKey(msg tea.KeyMsg, scroll *int, total int) {
	page := m.scrollPage()
	switch msg.String() {
	case "esc", "1":
		m.view = viewDash
	case "up", "k":
		*scroll--
	case "down", "j":
		*scroll++
	case "pgup":
		*scroll -= page
	case "pgdown":
		*scroll += page
	case "g", "home":
		*scroll = 0
	case "G", "end":
		*scroll = total
	}
	if *scroll > total {
		*scroll = total
	}
	if *scroll < 0 {
		*scroll = 0
	}
}

// scrollPage is a page size for PgUp/PgDn in the fullscreen views.
func (m Model) scrollPage() int {
	p := m.bodyHeight() - 4
	if p < 1 {
		p = 1
	}
	return p
}

func (m Model) handleModalKey(msg tea.KeyMsg) (tea.Model, tea.Cmd) {
	md := m.modal
	if md.isInput { // scale input
		switch msg.Type {
		case tea.KeyEnter:
			m.modal = nil
			n := atoiSafe(md.input)
			return m, m.scaleCmd(md.target, int32(n))
		case tea.KeyEsc:
			m.modal = nil
		case tea.KeyBackspace:
			if len(md.input) > 0 {
				md.input = md.input[:len(md.input)-1]
			}
		case tea.KeyRunes:
			for _, r := range msg.Runes {
				if r >= '0' && r <= '9' {
					md.input += string(r)
				}
			}
		}
		return m, nil
	}

	switch msg.String() {
	case "y", "enter":
		kind, pod := md.kind, md.pod
		m.modal = nil
		switch kind {
		case mDelete:
			return m, m.deleteCmd(pod)
		case mRestart:
			return m, m.restartCmd(pod)
		case mPortFwd:
			m.startForward(pod)
			m.view = viewForwards
			return m, nil
		}
	case "n", "esc", "q":
		m.modal = nil
	}
	return m, nil
}

func (m Model) handleFilterKey(msg tea.KeyMsg) (tea.Model, tea.Cmd) {
	switch msg.Type {
	case tea.KeyEnter:
		m.filtering = false
	case tea.KeyEsc:
		m.filtering, m.filter = false, ""
		m.applyFilter()
	case tea.KeyBackspace:
		if len(m.filter) > 0 {
			m.filter = m.filter[:len(m.filter)-1]
			m.applyFilter()
		}
	case tea.KeyRunes, tea.KeySpace:
		m.filter += string(msg.Runes)
		m.applyFilter()
	}
	return m, m.logsCmd(false)
}

func (m Model) handleLogSearchKey(msg tea.KeyMsg) (tea.Model, tea.Cmd) {
	switch msg.Type {
	case tea.KeyEnter:
		m.logSearching = false
	case tea.KeyEsc:
		m.logSearching, m.logSearch = false, ""
	case tea.KeyBackspace:
		if len(m.logSearch) > 0 {
			m.logSearch = m.logSearch[:len(m.logSearch)-1]
		}
	case tea.KeyRunes, tea.KeySpace:
		m.logSearch += string(msg.Runes)
	}
	return m, nil
}

func (m Model) selectedKey() string {
	if p, ok := m.selectedPod(); ok {
		return podKey(p)
	}
	return ""
}

// reselect restores the selection on the same pod after a re-sort or a
// flat/tree switch. A pod row's tree key is its pod key, so one lookup does both.
func (m *Model) reselect(key string) {
	if key == "" {
		return
	}
	for i, p := range m.rows {
		if podKey(p) == key {
			m.cursor = i
			break
		}
	}
	if m.tree {
		m.treeSel = key
	}
}

func atoiSafe(s string) int {
	n := 0
	for _, r := range s {
		if r < '0' || r > '9' {
			continue
		}
		n = n*10 + int(r-'0')
	}
	return n
}

func firstPort(p cluster.PodInfo) int32 {
	if len(p.ContainerPorts) > 0 {
		return p.ContainerPorts[0]
	}
	return 0
}
