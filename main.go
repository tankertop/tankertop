package main

import (
	"flag"
	"fmt"
	"io"
	"os"
	"os/signal"
	"strings"
	"syscall"
	"time"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
	"github.com/muesli/termenv"
	"k8s.io/klog/v2"

	"github.com/tankertop/tankertop/internal/cluster"
	"github.com/tankertop/tankertop/internal/ui"
)

// version is overridden at build time via -ldflags "-X main.version=...".
var version = "dev"

// repeatable collects a flag that may be given more than once, e.g.
// --ssh-opt -J --ssh-opt bastion.corp
type repeatable []string

func (r *repeatable) String() string     { return strings.Join(*r, " ") }
func (r *repeatable) Set(v string) error { *r = append(*r, v); return nil }

// main defers to run so that `defer client.Close()` — which tears down the ssh
// tunnel — is not skipped by an os.Exit on an error path.
func main() { os.Exit(run()) }

// cleanupOnSignal tears the ssh tunnel down on the signals that would otherwise
// kill us without running deferred functions. SIGPIPE is the one that bites in
// practice: `tankertop --ssh host --snapshot | head` closes the pipe, and Go's
// default handling for a broken stdout is to die on the spot, orphaning `ssh -N`.
// SIGINT is left to bubbletea, which quits cleanly on ctrl-c.
func cleanupOnSignal(c *cluster.Client) {
	sigs := make(chan os.Signal, 1)
	signal.Notify(sigs, syscall.SIGTERM, syscall.SIGHUP, syscall.SIGPIPE)
	go func() {
		sig := <-sigs
		c.Close()
		if s, ok := sig.(syscall.Signal); ok {
			os.Exit(128 + int(s))
		}
		os.Exit(1)
	}()
}

func run() int {
	cfg := ui.LoadConfig()

	kubeconfig := flag.String("kubeconfig", "", "path to kubeconfig (default: $KUBECONFIG or ~/.kube/config)")
	sshTarget := flag.String("ssh", "", "monitor a cluster on [user@]host over ssh (installs nothing there)")
	sshKubeconfigCmd := flag.String("ssh-kubeconfig-cmd", "", "command run on the ssh host to print a kubeconfig")
	kubectlCmd := flag.String("kubectl", "", "kubectl invocation for shell/port-forward (default: auto-detect kubectl or microk8s kubectl)")
	var sshOpts repeatable
	flag.Var(&sshOpts, "ssh-opt", "extra argument passed to ssh (repeatable), e.g. --ssh-opt -J --ssh-opt bastion")
	showVersion := flag.Bool("version", false, "print version and exit")
	interval := flag.Duration("interval", time.Duration(cfg.IntervalMs)*time.Millisecond, "refresh interval")
	namespace := flag.String("namespace", cfg.Namespace, "limit to a single namespace (default: all)")
	snapshot := flag.Bool("snapshot", false, "print one plain-text frame and exit (non-interactive)")
	dumpFrame := flag.String("dump-frame", "", "render one TUI frame at WxH (e.g. 140x40) and exit; optional mode via -frame-mode")
	frameMode := flag.String("frame-mode", "list", "frame mode for -dump-frame: list|detail")
	truecolor := flag.Bool("truecolor", true, "force 24-bit colour (btop-style gradients)")
	theme := flag.String("theme", cfg.Theme, "colour theme: tokyonight|gruvbox|nord|dracula|mono")
	demo := flag.Bool("demo", false, "run against a synthetic demo cluster (no kubeconfig needed)")
	dockerMode := flag.Bool("docker", false, "monitor a container engine (Docker/Podman/nerdctl) instead of Kubernetes")
	dockerBin := flag.String("docker-bin", "docker", "container CLI for --docker: docker|podman|nerdctl")
	flag.Parse()

	if *showVersion {
		fmt.Println("tankertop", version)
		return 0
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

	if *demo && (*sshTarget != "" || *dockerMode) {
		fmt.Fprintln(os.Stderr, "tankertop: --demo cannot be combined with --ssh or --docker")
		return 1
	}

	var client *cluster.Client
	var err error
	switch {
	case *demo:
		client = cluster.NewDemo()
	case *dockerMode:
		// --docker --ssh host runs docker on that host (no kube tunnel).
		client, err = cluster.NewDocker(*dockerBin, *sshTarget, sshOpts)
		if err != nil {
			fmt.Fprintf(os.Stderr, "tankertop: %v\n", err)
			return 1
		}
	case *sshTarget != "":
		// Set up before the TUI takes the screen, so ssh can prompt for a
		// passphrase, a password or 2FA on the real terminal.
		client, err = cluster.NewSSH(*sshTarget, sshOpts, *sshKubeconfigCmd, *kubectlCmd)
		if err != nil {
			fmt.Fprintf(os.Stderr, "tankertop: %v\n", err)
			return 1
		}
	default:
		client, err = cluster.New(*kubeconfig, *kubectlCmd)
		if err != nil {
			fmt.Fprintf(os.Stderr, "tankertop: cannot connect to cluster: %v\n", err)
			fmt.Fprintln(os.Stderr, "hint: on a microk8s node run:  microk8s config > ~/.kube/config")
			fmt.Fprintln(os.Stderr, "  or monitor it from here:  tankertop --ssh user@node")
			fmt.Fprintln(os.Stderr, "  or try:  tankertop --demo")
			return 1
		}
	}
	defer client.Close()
	cleanupOnSignal(client)

	if *snapshot {
		if err := ui.PrintSnapshot(os.Stdout, client, *namespace); err != nil {
			fmt.Fprintf(os.Stderr, "tankertop: %v\n", err)
			return 1
		}
		return 0
	}

	if *dumpFrame != "" {
		var w, h int
		if _, err := fmt.Sscanf(*dumpFrame, "%dx%d", &w, &h); err != nil || w <= 0 || h <= 0 {
			fmt.Fprintln(os.Stderr, "tankertop: -dump-frame expects WxH, e.g. 140x40")
			return 1
		}
		fmt.Println(ui.RenderOnce(client, *namespace, w, h, *frameMode))
		return 0
	}

	model := ui.New(client, *interval, *namespace).WithConfig(cfg)
	p := tea.NewProgram(model, tea.WithAltScreen(), tea.WithMouseCellMotion())
	if _, err := p.Run(); err != nil {
		fmt.Fprintf(os.Stderr, "tankertop: %v\n", err)
		return 1
	}
	return 0
}
