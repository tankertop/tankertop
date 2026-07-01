package main

import (
	"flag"
	"fmt"
	"io"
	"os"
	"time"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
	"github.com/muesli/termenv"
	"k8s.io/klog/v2"

	"github.com/tpenzkofer/kubeview/internal/cluster"
	"github.com/tpenzkofer/kubeview/internal/ui"
)

// version is overridden at build time via -ldflags "-X main.version=...".
var version = "dev"

func main() {
	cfg := ui.LoadConfig()

	kubeconfig := flag.String("kubeconfig", "", "path to kubeconfig (default: $KUBECONFIG or ~/.kube/config)")
	showVersion := flag.Bool("version", false, "print version and exit")
	interval := flag.Duration("interval", time.Duration(cfg.IntervalMs)*time.Millisecond, "refresh interval")
	namespace := flag.String("namespace", cfg.Namespace, "limit to a single namespace (default: all)")
	snapshot := flag.Bool("snapshot", false, "print one plain-text frame and exit (non-interactive)")
	dumpFrame := flag.String("dump-frame", "", "render one TUI frame at WxH (e.g. 140x40) and exit; optional mode via -frame-mode")
	frameMode := flag.String("frame-mode", "list", "frame mode for -dump-frame: list|detail")
	truecolor := flag.Bool("truecolor", true, "force 24-bit colour (btop-style gradients)")
	theme := flag.String("theme", cfg.Theme, "colour theme: tokyonight|gruvbox|nord|dracula|mono")
	demo := flag.Bool("demo", false, "run against a synthetic demo cluster (no kubeconfig needed)")
	flag.Parse()

	if *showVersion {
		fmt.Println("kubeview", version)
		return
	}

	// Force a 24-bit colour profile so gradients aren't quantised to 256 colours
	// (SSH sessions often don't advertise truecolor via $COLORTERM).
	if *truecolor {
		lipgloss.SetColorProfile(termenv.TrueColor)
	}
	ui.ApplyTheme(*theme)

	// klog (used deep inside client-go) must never write to the terminal or it
	// corrupts the TUI. Send it to the void.
	klog.SetOutput(io.Discard)
	klog.LogToStderr(false)

	var client *cluster.Client
	if *demo {
		client = cluster.NewDemo()
	} else {
		var err error
		client, err = cluster.New(*kubeconfig)
		if err != nil {
			fmt.Fprintf(os.Stderr, "kubeview: cannot connect to cluster: %v\n", err)
			fmt.Fprintln(os.Stderr, "hint: on a microk8s node run:  microk8s config > ~/.kube/config")
			fmt.Fprintln(os.Stderr, "  or try:  kubeview --demo")
			os.Exit(1)
		}
	}

	if *snapshot {
		if err := ui.PrintSnapshot(os.Stdout, client, *namespace); err != nil {
			fmt.Fprintf(os.Stderr, "kubeview: %v\n", err)
			os.Exit(1)
		}
		return
	}

	if *dumpFrame != "" {
		var w, h int
		if _, err := fmt.Sscanf(*dumpFrame, "%dx%d", &w, &h); err != nil || w <= 0 || h <= 0 {
			fmt.Fprintln(os.Stderr, "kubeview: -dump-frame expects WxH, e.g. 140x40")
			os.Exit(1)
		}
		fmt.Println(ui.RenderOnce(client, *namespace, w, h, *frameMode))
		return
	}

	model := ui.New(client, *interval, *namespace).WithConfig(cfg)
	p := tea.NewProgram(model, tea.WithAltScreen(), tea.WithMouseCellMotion())
	if _, err := p.Run(); err != nil {
		fmt.Fprintf(os.Stderr, "kubeview: %v\n", err)
		os.Exit(1)
	}
}
