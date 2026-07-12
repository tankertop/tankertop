package ui

import (
	"context"
	"fmt"
	"io"
	"strings"
	"time"

	tea "github.com/charmbracelet/bubbletea"

	"github.com/tankertop/tankertop/internal/cluster"
)

// RenderOnce drives the real TUI render path once at the given size and returns
// the resulting frame. Used by `tankertop --dump-frame` for verification.
func RenderOnce(c *cluster.Client, namespace string, width, height int, mode string) string {
	var tm tea.Model = New(c, time.Second, namespace)
	tm, _ = tm.Update(tea.WindowSizeMsg{Width: width, Height: height})

	// Warm up a few samples so the history graphs render meaningfully.
	for i := 0; i < 24; i++ {
		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		snap := c.Collect(ctx, namespace)
		cancel()
		tm, _ = tm.Update(snapshotMsg(snap))
		if i < 23 {
			time.Sleep(120 * time.Millisecond)
		}
	}
	// Synchronously load logs for the selected pod so the logs pane is populated
	// (the interactive app does this via an async command).
	if mm, ok := tm.(Model); ok {
		if p, ok := mm.selectedPod(); ok && len(p.Containers) > 0 {
			ctx, cancel := context.WithTimeout(context.Background(), 8*time.Second)
			body, err := c.Logs(ctx, p.Namespace, p.Name, p.Containers[0].Name, logTail, false)
			cancel()
			tm, _ = tm.Update(logsMsg{
				key:   p.Namespace + "/" + p.Name,
				title: p.Name + " [" + p.Containers[0].Name + "]",
				body:  body, err: err,
			})
		}
	}

	send := func(s string) { tm, _ = tm.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune(s)}) }
	switch mode {
	case "net":
		send("2")
	case "logs":
		tm, _ = tm.Update(tea.KeyMsg{Type: tea.KeyTab})
	case "help":
		send("?")
	case "modal":
		send("d")
	case "tree":
		send("t")
	case "sortcpu":
		send("o")
	case "events":
		send("3")
	case "pressure":
		send("4")
	case "nodes":
		send("5")
	case "forwards":
		send("6")
	case "env", "envreveal", "envscroll", "envtop", "envpan":
		if mode != "envtop" {
			// move to a pod with a rich spec env (demo order: web, web, api, …)
			tm, _ = tm.Update(tea.KeyMsg{Type: tea.KeyDown})
			tm, _ = tm.Update(tea.KeyMsg{Type: tea.KeyDown})
		}
		send("e")
		if mode == "envreveal" {
			send("m")
		}
		// The runtime env arrives via an async command; fetch it inline instead.
		if mm, ok := tm.(Model); ok {
			if p, ok := mm.selectedPod(); ok && len(p.Containers) > 0 {
				ctx, cancel := context.WithTimeout(context.Background(), 8*time.Second)
				body, err := c.RuntimeEnv(ctx, p.Namespace, p.Name, mm.selectedContainer())
				cancel()
				tm, _ = tm.Update(envMsg{key: mm.paneKey(), body: body, err: err})
			}
		}
		if mode == "envscroll" {
			tm, _ = tm.Update(tea.KeyMsg{Type: tea.KeyTab})
			tm, _ = tm.Update(tea.KeyMsg{Type: tea.KeyPgDown})
		}
		if mode == "envpan" {
			send("m") // reveal, so long values are visible to pan across
			tm, _ = tm.Update(tea.KeyMsg{Type: tea.KeyTab})
			for i := 0; i < 4; i++ {
				tm, _ = tm.Update(tea.KeyMsg{Type: tea.KeyRight})
			}
		}
	case "detailgraph":
		send("v")
	case "files":
		// f opens the browser; its listing is async, so fetch it inline.
		send("f")
		if mm, ok := tm.(Model); ok {
			if p, ok := mm.selectedPod(); ok && len(p.Containers) > 0 {
				ctx, cancel := context.WithTimeout(context.Background(), 8*time.Second)
				out, err := c.Exec(ctx, p.Namespace, p.Name, mm.selectedContainer(), []string{"sh", "-c", fsListScript, "_", "/"})
				cancel()
				tm, _ = tm.Update(fsListMsg{path: "/", entries: parseLsEntries(out), err: err})
			}
		}
	case "collapse":
		send("t")
		tm, _ = tm.Update(tea.KeyMsg{Type: tea.KeySpace, Runes: []rune{' '}})
	case "nscollapse":
		send("t")
		send("n")
	case "nscollapseall":
		send("t")
		send("N")
	case "helpscroll":
		send("?")
		for i := 0; i < 3; i++ {
			tm, _ = tm.Update(tea.KeyMsg{Type: tea.KeyPgDown})
		}
	}
	return tm.View()
}

// PrintSnapshot collects one snapshot and writes a plain-text frame to w.
// Used by `tankertop --snapshot` for non-interactive / scriptable output.
func PrintSnapshot(w io.Writer, c *cluster.Client, namespace string) error {
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	snap := c.Collect(ctx, namespace)
	if snap.Err != nil {
		return snap.Err
	}

	var cpuUsed, cpuCap, memUsed, memCap int64
	for _, n := range snap.Nodes {
		cpuUsed += n.CPUUsedMilli
		cpuCap += n.CPUCapacityMilli
		memUsed += n.MemUsedBytes
		memCap += n.MemCapacityBytes
	}
	fmt.Fprintf(w, "tankertop  ctx:%s  nodes:%d  pods:%d  ns:%d  %s\n",
		snap.Context, len(snap.Nodes), len(snap.Pods), len(snap.Namespaces),
		snap.CollectedAt.Format(time.RFC3339))
	if !snap.MetricsOK {
		fmt.Fprintln(w, "  (metrics-server unavailable — CPU/MEM shown as 0)")
	}
	fmt.Fprintf(w, "  CPU %s/%s   MEM %s/%s\n\n",
		humanCPU(cpuUsed), humanCPU(cpuCap), humanBytes(memUsed), humanBytes(memCap))

	cols := []string{"NAMESPACE", "POD", "READY", "STATUS", "RST", "CPU", "MEM", "AGE"}
	widths := []int{14, 34, 6, 18, 4, 8, 9, 5}
	var head strings.Builder
	for i, c := range cols {
		head.WriteString(pad(c, widths[i]) + " ")
	}
	fmt.Fprintln(w, head.String())

	for _, p := range snap.Pods {
		fmt.Fprintf(w, "%s %s %s %s %s %s %s %s\n",
			pad(p.Namespace, widths[0]),
			pad(p.Name, widths[1]),
			pad(fmt.Sprintf("%d/%d", p.Ready, p.Total), widths[2]),
			pad(p.Status, widths[3]),
			pad(fmt.Sprintf("%d", p.Restarts), widths[4]),
			pad(humanCPU(p.CPUMilli), widths[5]),
			pad(humanBytes(p.MemBytes), widths[6]),
			pad(humanAge(p.Age.Seconds()), widths[7]),
		)
	}
	return nil
}
