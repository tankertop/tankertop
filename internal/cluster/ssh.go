package cluster

import (
	"bytes"
	"fmt"
	"net"
	"net/url"
	"os"
	"os/exec"
	"strings"
	"time"

	"k8s.io/client-go/tools/clientcmd"
)

// DefaultKubeconfigCmd is run on the remote host to print a kubeconfig. microk8s
// ships `microk8s config`; the fallbacks cover a stock kubectl host.
const DefaultKubeconfigCmd = `microk8s config 2>/dev/null || kubectl config view --raw 2>/dev/null || cat "${KUBECONFIG:-$HOME/.kube/config}"`

// sshTarget is the ssh destination plus any extra flags the user passed. We
// shell out to the system ssh rather than speaking the protocol ourselves, so
// authentication is exactly whatever `ssh <target>` already does on this
// machine: agent keys, ~/.ssh/config, ProxyJump, known_hosts, password and 2FA
// prompts. tankertop never sees, asks for, or stores a credential.
type sshTarget struct {
	target string
	opts   []string
}

// tunnel is a `ssh -N -L` child process forwarding a local port to the remote
// API server.
type tunnel struct {
	cmd  *exec.Cmd
	done chan error
	port int
}

func (t *tunnel) Close() {
	if t == nil || t.cmd == nil || t.cmd.Process == nil {
		return
	}
	_ = t.cmd.Process.Kill()
	select {
	case <-t.done:
	case <-time.After(2 * time.Second):
	}
}

// NewSSH monitors a cluster over an SSH tunnel, installing nothing on the
// remote host. It reads the remote kubeconfig, forwards a local port to the API
// server, and points client-go at that port. Only sshd needs to be reachable —
// the API server port can stay firewalled.
//
// It must be called before the TUI starts: ssh may prompt on the terminal.
func NewSSH(target string, opts []string, kubeconfigCmd, kubectlOverride string) (*Client, error) {
	if _, err := exec.LookPath("ssh"); err != nil {
		return nil, fmt.Errorf("--ssh needs the ssh client in PATH: %w", err)
	}
	if kubeconfigCmd == "" {
		kubeconfigCmd = DefaultKubeconfigCmd
	}

	fmt.Fprintf(os.Stderr, "tankertop: reading kubeconfig from %s…\n", target)
	raw, err := fetchKubeconfig(target, opts, kubeconfigCmd)
	if err != nil {
		return nil, err
	}

	cfg, err := clientcmd.RESTConfigFromKubeConfig(raw)
	if err != nil {
		return nil, fmt.Errorf("parsing the remote kubeconfig: %w", err)
	}
	ctxName := "remote"
	if apiCfg, err := clientcmd.Load(raw); err == nil && apiCfg.CurrentContext != "" {
		ctxName = apiCfg.CurrentContext
	}

	apiHost, apiPort, err := splitHostPort(cfg.Host)
	if err != nil {
		return nil, err
	}

	fmt.Fprintf(os.Stderr, "tankertop: tunnelling to %s:%s via %s…\n", apiHost, apiPort, target)
	tun, err := startTunnel(target, opts, apiHost, apiPort)
	if err != nil {
		return nil, err
	}

	// Talk to the tunnel, but keep verifying the certificate against the name the
	// cluster actually issued it for — not every API server lists 127.0.0.1.
	cfg.Host = fmt.Sprintf("https://127.0.0.1:%d", tun.port)
	if cfg.TLSClientConfig.ServerName == "" {
		cfg.TLSClientConfig.ServerName = apiHost
	}

	c, err := newFromConfig(cfg, ctxName+" via ssh://"+target)
	if err != nil {
		tun.Close()
		return nil, err
	}
	c.ssh = &sshTarget{target: target, opts: opts}
	c.tunnel = tun
	c.kubectlCmd = detectKubectlSSH(target, opts, kubectlOverride)
	return c, nil
}

// detectKubectlSSH picks a kubectl invocation on the remote host: an explicit
// override, else it asks the far side whether it has plain kubectl or microk8s.
func detectKubectlSSH(target string, opts []string, override string) []string {
	if override != "" {
		return strings.Fields(override)
	}
	const probe = `command -v kubectl >/dev/null 2>&1 && echo kubectl || ` +
		`{ command -v microk8s >/dev/null 2>&1 && echo "microk8s kubectl" || echo kubectl; }`
	args := append(append([]string{}, opts...), target, probe)
	if out, err := exec.Command("ssh", args...).Output(); err == nil {
		if f := strings.Fields(strings.TrimSpace(string(out))); len(f) > 0 {
			return f
		}
	}
	return []string{"kubectl"}
}

func fetchKubeconfig(target string, opts []string, remoteCmd string) ([]byte, error) {
	args := append(append([]string{}, opts...), target, remoteCmd)
	cmd := exec.Command("ssh", args...)
	var out bytes.Buffer
	cmd.Stdin, cmd.Stdout, cmd.Stderr = os.Stdin, &out, os.Stderr
	if err := cmd.Run(); err != nil {
		return nil, fmt.Errorf("reading a kubeconfig from %s: %w\n"+
			"hint: tankertop ran %q there; override it with --ssh-kubeconfig-cmd", target, err, remoteCmd)
	}
	if len(bytes.TrimSpace(out.Bytes())) == 0 {
		return nil, fmt.Errorf("%s printed an empty kubeconfig", target)
	}
	return out.Bytes(), nil
}

// splitHostPort pulls the API server address out of the kubeconfig server URL.
func splitHostPort(server string) (host, port string, err error) {
	u, err := url.Parse(server)
	if err != nil {
		return "", "", fmt.Errorf("the remote kubeconfig has an unparseable server %q: %w", server, err)
	}
	host, port = u.Hostname(), u.Port()
	if host == "" {
		return "", "", fmt.Errorf("the remote kubeconfig has no host in its server URL %q", server)
	}
	if port == "" {
		port = "443"
	}
	return host, port, nil
}

// startTunnel forwards a free local port to host:port on the far side, and waits
// for it to actually accept before returning — so a failed login or a refused
// forward is reported here rather than as a confusing API timeout later.
func startTunnel(target string, opts []string, host, port string) (*tunnel, error) {
	lport, err := freePort()
	if err != nil {
		return nil, err
	}

	args := []string{
		"-N", // no remote command, just the forward
		"-o", "ExitOnForwardFailure=yes",
		// Notice a dead network instead of hanging on a black-hole connection.
		"-o", "ServerAliveInterval=15",
		"-o", "ServerAliveCountMax=3",
		"-L", fmt.Sprintf("127.0.0.1:%d:%s:%s", lport, host, port),
	}
	args = append(args, opts...)
	args = append(args, target)

	cmd := exec.Command("ssh", args...)
	// Inherit the terminal so ssh can prompt for a passphrase, password or 2FA.
	cmd.Stdin, cmd.Stdout, cmd.Stderr = os.Stdin, os.Stderr, os.Stderr
	if err := cmd.Start(); err != nil {
		return nil, fmt.Errorf("starting the ssh tunnel: %w", err)
	}
	t := &tunnel{cmd: cmd, port: lport, done: make(chan error, 1)}
	go func() { t.done <- cmd.Wait() }()

	deadline := time.Now().Add(30 * time.Second)
	for {
		select {
		case err := <-t.done:
			return nil, fmt.Errorf("the ssh tunnel to %s exited before it came up: %w", target, err)
		default:
		}
		conn, err := net.DialTimeout("tcp", fmt.Sprintf("127.0.0.1:%d", lport), 300*time.Millisecond)
		if err == nil {
			conn.Close()
			return t, nil
		}
		if time.Now().After(deadline) {
			t.Close()
			return nil, fmt.Errorf("the ssh tunnel to %s did not come up within 30s", target)
		}
		time.Sleep(150 * time.Millisecond)
	}
}

func freePort() (int, error) {
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		return 0, fmt.Errorf("finding a free local port: %w", err)
	}
	defer l.Close()
	return l.Addr().(*net.TCPAddr).Port, nil
}

// remoteCmd wraps a command so it runs on the far side of the ssh connection,
// quoting each argument for the remote shell. tty asks ssh for a pty, which an
// interactive `kubectl exec -it` needs.
func (c *Client) remoteCmd(tty bool, argv []string) *exec.Cmd {
	if c.ssh == nil {
		return exec.Command(argv[0], argv[1:]...)
	}
	args := []string{}
	if tty {
		args = append(args, "-t")
	}
	args = append(args, c.ssh.opts...)
	args = append(args, c.ssh.target, shellJoin(argv))
	return exec.Command("ssh", args...)
}

// shellJoin renders argv as a single string safe for the remote shell.
func shellJoin(argv []string) string {
	quoted := make([]string, len(argv))
	for i, a := range argv {
		quoted[i] = shellQuote(a)
	}
	return strings.Join(quoted, " ")
}

func shellQuote(s string) string {
	if s != "" && !strings.ContainsAny(s, " \t\n\"'\\$`|&;<>()*?[]!#~") {
		return s
	}
	return "'" + strings.ReplaceAll(s, "'", `'\''`) + "'"
}

// ShellCommand is an interactive shell inside a container, run on whichever host
// owns the cluster.
func (c *Client) ShellCommand(namespace, pod, container string) *exec.Cmd {
	if c.docker {
		return c.dockerCmd(true, "exec", "-it", pod,
			"sh", "-c", "exec bash 2>/dev/null || exec sh")
	}
	argv := append(c.kubectl(), "exec", "-it", "-n", namespace, pod, "-c", container,
		"--", "sh", "-c", "exec bash 2>/dev/null || exec sh")
	return c.remoteCmd(true, argv)
}

// PortForwardCommand forwards a pod port. It binds on the host running kubectl,
// which under --ssh is the remote node, not this machine.
func (c *Client) PortForwardCommand(namespace, pod string, local, remote int32) *exec.Cmd {
	argv := append(c.kubectl(), "port-forward", "-n", namespace,
		"pod/"+pod, fmt.Sprintf("%d:%d", local, remote), "--address", "0.0.0.0")
	return c.remoteCmd(false, argv)
}

// ForwardBindHost names the machine a port-forward binds on, for the UI.
func (c *Client) ForwardBindHost() string {
	if c.ssh == nil {
		return "this host"
	}
	return c.ssh.target
}

// Close tears down the ssh tunnel, if any.
func (c *Client) Close() {
	if c != nil {
		c.tunnel.Close()
	}
}
