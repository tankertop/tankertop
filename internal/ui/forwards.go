package ui

import (
	"fmt"
	"os/exec"

	tea "github.com/charmbracelet/bubbletea"

	"github.com/tankertop/tankertop/internal/cluster"
)

// forward is a background `kubectl port-forward` process.
type forward struct {
	id            int
	ns, pod       string
	local, remote int32
	cmd           *exec.Cmd
	status        string // running / exited / error / stopped
	err           error
}

type fwdEvent struct {
	id  int
	err error
}

// listenFwdCmd blocks on the forward-events channel and turns the next event
// into a tea.Msg (re-issued after each event).
func (m Model) listenFwdCmd() tea.Cmd {
	ch := m.fwdCh
	return func() tea.Msg { return <-ch }
}

// startForward launches a background port-forward (local==remote port). It binds
// on whichever host runs kubectl — this machine, or the node when using --ssh.
func (m *Model) startForward(p cluster.PodInfo) {
	port := firstPort(p)
	f := &forward{id: m.fwdSeq, ns: p.Namespace, pod: p.Name, local: port, remote: port, status: "running"}
	m.fwdSeq++
	f.cmd = m.client.PortForwardCommand(p.Namespace, p.Name, port, port)
	if err := f.cmd.Start(); err != nil {
		f.status, f.err = "error", err
		m.forwards = append(m.forwards, f)
		return
	}
	ch, id := m.fwdCh, f.id
	go func() { ch <- fwdEvent{id: id, err: f.cmd.Wait()} }()
	m.forwards = append(m.forwards, f)
	m.status = fmt.Sprintf("✓ forwarding %s:%d → %s:%d", m.client.ForwardBindHost(), port, p.Name, port)
}

func (m *Model) stopForward(i int) {
	if i < 0 || i >= len(m.forwards) {
		return
	}
	f := m.forwards[i]
	if f.cmd != nil && f.cmd.Process != nil && f.status == "running" {
		_ = f.cmd.Process.Kill()
	}
	f.status = "stopped"
}

func (m Model) handleForwardsKey(msg tea.KeyMsg) (tea.Model, tea.Cmd) {
	switch msg.String() {
	case "esc", "1":
		m.view = viewDash
	case "up", "k":
		if m.fwdCursor > 0 {
			m.fwdCursor--
		}
	case "down", "j":
		if m.fwdCursor < len(m.forwards)-1 {
			m.fwdCursor++
		}
	case "x", "d":
		m.stopForward(m.fwdCursor)
	case "X":
		for i := range m.forwards {
			m.stopForward(i)
		}
	}
	return m, nil
}
