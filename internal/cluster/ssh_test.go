package cluster

import "testing"

func TestShellQuote(t *testing.T) {
	cases := []struct{ in, want string }{
		{"kubectl", "kubectl"},
		{"pod/web-1", "pod/web-1"},
		{"", "''"},
		{"exec bash 2>/dev/null || exec sh", `'exec bash 2>/dev/null || exec sh'`},
		{"it's", `'it'\''s'`},
		{"$(rm -rf /)", `'$(rm -rf /)'`},
		{"a;b", "'a;b'"},
	}
	for _, c := range cases {
		if got := shellQuote(c.in); got != c.want {
			t.Errorf("shellQuote(%q) = %q, want %q", c.in, got, c.want)
		}
	}
}

func TestShellJoinKeepsTheShellCommandIntact(t *testing.T) {
	got := shellJoin([]string{"microk8s", "kubectl", "exec", "-it", "-n", "default", "web-1",
		"--", "sh", "-c", "exec bash 2>/dev/null || exec sh"})
	want := `microk8s kubectl exec -it -n default web-1 -- sh -c 'exec bash 2>/dev/null || exec sh'`
	if got != want {
		t.Errorf("shellJoin =\n  %s\nwant\n  %s", got, want)
	}
}

func TestSplitHostPort(t *testing.T) {
	cases := []struct {
		server, host, port string
		wantErr            bool
	}{
		{server: "https://192.0.2.10:16443", host: "192.0.2.10", port: "16443"},
		{server: "https://k8s.example.com", host: "k8s.example.com", port: "443"},
		{server: "https://[::1]:6443", host: "::1", port: "6443"},
		{server: "not a url", wantErr: true},
	}
	for _, c := range cases {
		host, port, err := splitHostPort(c.server)
		if c.wantErr {
			if err == nil {
				t.Errorf("splitHostPort(%q) should have failed", c.server)
			}
			continue
		}
		if err != nil {
			t.Errorf("splitHostPort(%q): %v", c.server, err)
			continue
		}
		if host != c.host || port != c.port {
			t.Errorf("splitHostPort(%q) = %q,%q want %q,%q", c.server, host, port, c.host, c.port)
		}
	}
}

// A local client must not grow an ssh prefix, and uses its resolved kubectl.
func TestLocalCommandsAreNotWrappedInSSH(t *testing.T) {
	c := &Client{kubectlCmd: []string{"microk8s", "kubectl"}}
	if got := c.ShellCommand("default", "web-1", "web").Args[0]; got != "microk8s" {
		t.Errorf("local shell command starts with %q, want microk8s", got)
	}
	if got := c.ForwardBindHost(); got != "this host" {
		t.Errorf("ForwardBindHost = %q", got)
	}
}

// The default, with nothing resolved, is plain kubectl.
func TestKubectlDefaultsToPlainKubectl(t *testing.T) {
	c := &Client{}
	if got := c.ShellCommand("default", "web-1", "web").Args[0]; got != "kubectl" {
		t.Errorf("default shell command starts with %q, want kubectl", got)
	}
}

func TestSSHClientWrapsCommands(t *testing.T) {
	c := &Client{
		ssh:        &sshTarget{target: "user@node", opts: []string{"-J", "bastion"}},
		kubectlCmd: []string{"kubectl"},
	}

	sh := c.ShellCommand("default", "web-1", "web")
	if sh.Args[0] != "ssh" || sh.Args[1] != "-t" {
		t.Fatalf("shell over ssh needs a tty: %v", sh.Args)
	}
	if sh.Args[2] != "-J" || sh.Args[3] != "bastion" || sh.Args[4] != "user@node" {
		t.Fatalf("ssh opts/target misplaced: %v", sh.Args)
	}

	pf := c.PortForwardCommand("default", "web-1", 80, 80)
	if pf.Args[0] != "ssh" || pf.Args[1] == "-t" {
		t.Fatalf("port-forward should not allocate a tty: %v", pf.Args)
	}
	if got := c.ForwardBindHost(); got != "user@node" {
		t.Errorf("ForwardBindHost = %q, want user@node", got)
	}
}
