package cluster

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"os/exec"
	"sort"
	"strconv"
	"strings"
	"time"

	"sigs.k8s.io/yaml"
)

// The Docker backend maps a container host onto tankertop's Kubernetes model:
// a container is a "pod", the Compose project is its "namespace", the Compose
// service is its "workload". It shells out to the docker CLI (so podman and
// nerdctl work too, via --docker-bin), optionally wrapped in ssh — which is how
// `--docker --ssh host` monitors a remote engine without installing anything.

// NewDocker builds a client backed by a container engine. target, when set,
// runs every docker command on that ssh host (no tunnel — docker speaks over the
// ssh session itself).
func NewDocker(bin, target string, opts []string) (*Client, error) {
	if bin == "" {
		bin = "docker"
	}
	c := &Client{docker: true, dockerBin: bin, Context: bin}
	if target != "" {
		c.ssh = &sshTarget{target: target, opts: opts}
		c.Context = bin + " via ssh://" + target
	}
	if _, err := exec.LookPath("ssh"); err != nil && target != "" {
		return nil, fmt.Errorf("--docker --ssh needs the ssh client in PATH: %w", err)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	if _, err := c.dockerOut(ctx, "version", "--format", "{{.Server.Version}}"); err != nil {
		where := "locally"
		if target != "" {
			where = "on " + target
		}
		return nil, fmt.Errorf("cannot reach the %s engine %s: %w\n"+
			"hint: is the daemon running and is %q the right binary?", bin, where, err, bin)
	}
	return c, nil
}

// dockerCmd builds a docker invocation, wrapped in ssh when remote.
func (c *Client) dockerCmd(tty bool, args ...string) *exec.Cmd {
	return c.remoteCmd(tty, append([]string{c.dockerBin}, args...))
}

// dockerOut runs a docker command and returns stdout, surfacing stderr on error.
func (c *Client) dockerOut(ctx context.Context, args ...string) ([]byte, error) {
	return runCapture(ctx, c.dockerCmd(false, args...))
}

// hostOut runs a shell snippet on the host that owns the engine (this machine,
// or the remote under --ssh), for reading /proc.
func (c *Client) hostOut(ctx context.Context, script string) ([]byte, error) {
	return runCapture(ctx, c.remoteCmd(false, []string{"sh", "-c", script}))
}

// capWriter buffers up to limit bytes and silently discards the rest, keeping
// the pipe drained so the child never blocks. It bounds memory when a hostile
// container streams an unbounded amount (e.g. a few gigantic log lines).
type capWriter struct {
	buf   bytes.Buffer
	limit int
}

func (w *capWriter) Write(p []byte) (int, error) {
	if room := w.limit - w.buf.Len(); room > 0 {
		if len(p) > room {
			w.buf.Write(p[:room])
		} else {
			w.buf.Write(p)
		}
	}
	return len(p), nil
}

// runCaptureLimit is runCapture with a cap on how many stdout bytes are retained.
func runCaptureLimit(ctx context.Context, cmd *exec.Cmd, limit int) ([]byte, error) {
	out := &capWriter{limit: limit}
	var errb bytes.Buffer
	cmd.Stdout, cmd.Stderr = out, &errb
	done := make(chan error, 1)
	if err := cmd.Start(); err != nil {
		return nil, err
	}
	go func() { done <- cmd.Wait() }()
	select {
	case <-ctx.Done():
		_ = cmd.Process.Kill()
		return nil, ctx.Err()
	case err := <-done:
		if err != nil {
			if msg := strings.TrimSpace(errb.String()); msg != "" {
				return out.buf.Bytes(), fmt.Errorf("%s", msg)
			}
			return out.buf.Bytes(), err
		}
		return out.buf.Bytes(), nil
	}
}

// runCapture runs cmd with a context deadline, returning stdout and surfacing
// stderr on error.
func runCapture(ctx context.Context, cmd *exec.Cmd) ([]byte, error) {
	var out, errb bytes.Buffer
	cmd.Stdout, cmd.Stderr = &out, &errb
	done := make(chan error, 1)
	if err := cmd.Start(); err != nil {
		return nil, err
	}
	go func() { done <- cmd.Wait() }()
	select {
	case <-ctx.Done():
		_ = cmd.Process.Kill()
		return nil, ctx.Err()
	case err := <-done:
		if err != nil {
			if msg := strings.TrimSpace(errb.String()); msg != "" {
				return out.Bytes(), fmt.Errorf("%s", msg)
			}
			return out.Bytes(), err
		}
		return out.Bytes(), nil
	}
}

// ---- docker inspect / stats shapes (the subset we read) ----

type dockerInspect struct {
	ID      string `json:"Id"`
	Name    string `json:"Name"`
	Created string `json:"Created"`
	State   struct {
		Status   string `json:"Status"`
		ExitCode int    `json:"ExitCode"`
		Health   *struct {
			Status string `json:"Status"`
		} `json:"Health"`
	} `json:"State"`
	RestartCount int `json:"RestartCount"`
	Config       struct {
		Image  string            `json:"Image"`
		Env    []string          `json:"Env"`
		Labels map[string]string `json:"Labels"`
	} `json:"Config"`
	HostConfig struct {
		Memory   int64 `json:"Memory"`
		NanoCpus int64 `json:"NanoCpus"`
	} `json:"HostConfig"`
	NetworkSettings struct {
		Networks map[string]struct {
			IPAddress string `json:"IPAddress"`
		} `json:"Networks"`
		Ports map[string][]struct {
			HostPort string `json:"HostPort"`
		} `json:"Ports"`
	} `json:"NetworkSettings"`
}

type dockerStat struct {
	ID       string `json:"ID"`
	Name     string `json:"Name"`
	CPUPerc  string `json:"CPUPerc"`
	MemUsage string `json:"MemUsage"`
	NetIO    string `json:"NetIO"` // "1.52kB / 126B"  (rx / tx, cumulative)
	PIDs     string `json:"PIDs"`
}

type dockerInfo struct {
	Name              string `json:"Name"`
	NCPU              int    `json:"NCPU"`
	MemTotal          int64  `json:"MemTotal"`
	Containers        int    `json:"Containers"`
	ContainersRunning int    `json:"ContainersRunning"`
	ServerVersion     string `json:"ServerVersion"`
}

// dockerCollect builds a Snapshot from `docker inspect` + `docker stats` +
// `docker info`. namespace, when set, keeps only that Compose project.
func (c *Client) dockerCollect(ctx context.Context, namespace string) Snapshot {
	snap := Snapshot{Context: c.Context, CollectedAt: time.Now(), MetricsOK: true}

	var info dockerInfo
	if b, err := c.dockerOut(ctx, "info", "--format", "{{json .}}"); err == nil {
		_ = json.Unmarshal(b, &info)
	}
	hostName := info.Name
	if hostName == "" {
		hostName = "docker"
	}

	ids, err := c.dockerOut(ctx, "ps", "-aq", "--no-trunc")
	if err != nil {
		snap.Err = err
		return snap
	}
	idList := strings.Fields(string(ids))

	var inspected []dockerInspect
	if len(idList) > 0 {
		b, err := c.dockerOut(ctx, append([]string{"inspect"}, idList...)...)
		if err != nil {
			snap.Err = err
			return snap
		}
		if err := json.Unmarshal(b, &inspected); err != nil {
			snap.Err = fmt.Errorf("parsing docker inspect: %w", err)
			return snap
		}
	}

	// live cpu/mem, keyed by container id
	stats := map[string]dockerStat{}
	if b, err := c.dockerOut(ctx, "stats", "--no-stream", "--format", "{{json .}}"); err == nil {
		for _, line := range strings.Split(strings.TrimSpace(string(b)), "\n") {
			if line == "" {
				continue
			}
			var s dockerStat
			if json.Unmarshal([]byte(line), &s) == nil {
				stats[s.ID] = s
			}
		}
	}

	nsSet := map[string]struct{}{}
	var totCPU, totMem int64
	for i := range inspected {
		pi := containerToPod(&inspected[i], stats, hostName)
		if namespace != "" && pi.Namespace != namespace {
			continue
		}
		totCPU += pi.CPUMilli
		totMem += pi.MemBytes
		nsSet[pi.Namespace] = struct{}{}
		snap.Pods = append(snap.Pods, pi)
	}

	sort.Slice(snap.Pods, func(i, j int) bool {
		if snap.Pods[i].Namespace != snap.Pods[j].Namespace {
			return snap.Pods[i].Namespace < snap.Pods[j].Namespace
		}
		return snap.Pods[i].Name < snap.Pods[j].Name
	})
	for ns := range nsSet {
		snap.Namespaces = append(snap.Namespaces, ns)
	}
	sort.Strings(snap.Namespaces)

	cpuCap := int64(info.NCPU) * 1000
	snap.Nodes = []NodeInfo{{
		Name:             hostName,
		Ready:            true,
		CPUCapacityMilli: cpuCap, CPUAllocMilli: cpuCap,
		MemCapacityBytes: info.MemTotal, MemAllocBytes: info.MemTotal,
		CPUUsedMilli: totCPU, MemUsedBytes: totMem,
		PodCount:     info.ContainersRunning,
		PodsCapacity: info.Containers,
	}}
	if len(snap.Nodes) > 0 {
		c.readHostStats(ctx, &snap.Nodes[0])
	}
	snap.Networks = c.collectDockerNetworks(ctx)
	return snap
}

// readHostStats fills load average and swap from the engine host's /proc. Best
// effort — a daemon reached via DOCKER_HOST may not be this machine, in which
// case /proc is simply irrelevant and the fields stay zero.
func (c *Client) readHostStats(ctx context.Context, n *NodeInfo) {
	out, err := c.hostOut(ctx, "cat /proc/loadavg; echo '@@'; grep -E '^Swap(Total|Free):' /proc/meminfo")
	if err != nil {
		return
	}
	parts := strings.SplitN(string(out), "@@", 2)
	if f := strings.Fields(parts[0]); len(f) >= 3 {
		n.Load1, _ = strconv.ParseFloat(f[0], 64)
		n.Load5, _ = strconv.ParseFloat(f[1], 64)
		n.Load15, _ = strconv.ParseFloat(f[2], 64)
	}
	if len(parts) == 2 {
		var total, free int64
		for _, line := range strings.Split(parts[1], "\n") {
			fields := strings.Fields(line)
			if len(fields) < 2 {
				continue
			}
			kb, _ := strconv.ParseInt(fields[1], 10, 64) // /proc/meminfo is in kB
			switch fields[0] {
			case "SwapTotal:":
				total = kb * 1024
			case "SwapFree:":
				free = kb * 1024
			}
		}
		n.SwapTotalBytes = total
		n.SwapUsedBytes = total - free
	}
}

// DockerNetwork is one docker network and the containers attached to it.
type DockerNetwork struct {
	Name       string
	Driver     string
	Subnet     string
	Containers []DockerNetEndpoint
}

// DockerNetEndpoint is a container's attachment to a network.
type DockerNetEndpoint struct {
	Name string
	IPv4 string
}

type dockerNetInspect struct {
	Name string `json:"Name"`
	Driver string `json:"Driver"`
	IPAM struct {
		Config []struct {
			Subnet string `json:"Subnet"`
		} `json:"Config"`
	} `json:"IPAM"`
	Containers map[string]struct {
		Name string `json:"Name"`
		IPv4Address string `json:"IPv4Address"`
	} `json:"Containers"`
}

// collectDockerNetworks lists docker networks and which containers are on each.
func (c *Client) collectDockerNetworks(ctx context.Context) []DockerNetwork {
	ids, err := c.dockerOut(ctx, "network", "ls", "-q")
	if err != nil {
		return nil
	}
	idList := strings.Fields(string(ids))
	if len(idList) == 0 {
		return nil
	}
	b, err := c.dockerOut(ctx, append([]string{"network", "inspect"}, idList...)...)
	if err != nil {
		return nil
	}
	var raw []dockerNetInspect
	if json.Unmarshal(b, &raw) != nil {
		return nil
	}
	out := make([]DockerNetwork, 0, len(raw))
	for _, n := range raw {
		dn := DockerNetwork{Name: n.Name, Driver: n.Driver}
		if len(n.IPAM.Config) > 0 {
			dn.Subnet = n.IPAM.Config[0].Subnet
		}
		for _, ep := range n.Containers {
			dn.Containers = append(dn.Containers, DockerNetEndpoint{
				Name: ep.Name, IPv4: strings.SplitN(ep.IPv4Address, "/", 2)[0],
			})
		}
		sort.Slice(dn.Containers, func(i, j int) bool { return dn.Containers[i].Name < dn.Containers[j].Name })
		out = append(out, dn)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Name < out[j].Name })
	return out
}

// containerToPod maps one inspected container onto tankertop's PodInfo.
func containerToPod(d *dockerInspect, stats map[string]dockerStat, host string) PodInfo {
	name := strings.TrimPrefix(d.Name, "/")
	project := d.Config.Labels["com.docker.compose.project"]
	if project == "" {
		project = "(no project)"
	}
	service := d.Config.Labels["com.docker.compose.service"]
	controller := "(standalone)"
	if service != "" {
		controller = "compose/" + service
	}

	status, ready := dockerStatus(d)
	pi := PodInfo{
		Namespace:  project,
		Name:       name,
		Node:       host,
		Phase:      d.State.Status,
		Status:     status,
		Ready:      ready,
		Total:      1,
		Restarts:   int32(d.RestartCount),
		Controller: controller,
		OwnerKind:  "container",
		OwnerName:  name,
	}
	if t, err := time.Parse(time.RFC3339Nano, d.Created); err == nil {
		pi.Age = time.Since(t)
	}
	for _, n := range d.NetworkSettings.Networks {
		if n.IPAddress != "" {
			pi.PodIP = n.IPAddress
			break
		}
	}
	for portProto := range d.NetworkSettings.Ports {
		if p := parsePortProto(portProto); p > 0 {
			pi.ContainerPorts = append(pi.ContainerPorts, p)
		}
	}
	sort.Slice(pi.ContainerPorts, func(i, j int) bool { return pi.ContainerPorts[i] < pi.ContainerPorts[j] })

	if d.HostConfig.NanoCpus > 0 {
		pi.CPULimMilli = d.HostConfig.NanoCpus / 1_000_000 // 1e-9 cores -> milli
	}
	pi.MemLimBytes = d.HostConfig.Memory // 0 == unlimited

	if s, ok := statByID(stats, d.ID); ok {
		pi.CPUMilli = parseCPUPerc(s.CPUPerc)
		pi.MemBytes = parseSize(firstField(s.MemUsage))
		pi.NetRxBytes, pi.NetTxBytes = parseNetIO(s.NetIO)
		pi.PIDs, _ = strconv.Atoi(strings.TrimSpace(s.PIDs))
	}

	pi.Containers = []ContainerInfo{{
		Name:     name,
		Image:    d.Config.Image,
		Ready:    ready == 1,
		State:    d.State.Status,
		Restarts: int32(d.RestartCount),
		CPUMilli: pi.CPUMilli,
		MemBytes: pi.MemBytes,
		Env:      dockerEnv(d.Config.Env),
	}}
	return pi
}

// dockerStatus maps docker's state to tankertop's status vocabulary so the
// existing colour coding applies.
func dockerStatus(d *dockerInspect) (status string, ready int) {
	switch d.State.Status {
	case "running":
		if d.State.Health != nil {
			switch d.State.Health.Status {
			case "healthy":
				return "Running", 1
			case "unhealthy":
				return "Unhealthy", 0
			case "starting":
				return "Starting", 0
			}
		}
		return "Running", 1
	case "restarting":
		return "Restarting", 0
	case "paused":
		return "Paused", 0
	case "created":
		return "Created", 0
	case "exited":
		if d.State.ExitCode == 0 {
			return "Completed", 0
		}
		return fmt.Sprintf("Exited(%d)", d.State.ExitCode), 0
	case "dead":
		return "Dead", 0
	}
	return d.State.Status, 0
}

// dockerEnv turns docker's KEY=value env into tankertop's EnvVar list. Docker has
// no indirect sources, so every value is literal.
func dockerEnv(env []string) []EnvVar {
	if len(env) == 0 {
		return nil
	}
	out := make([]EnvVar, 0, len(env))
	for _, e := range env {
		name, value, _ := strings.Cut(e, "=")
		out = append(out, EnvVar{Name: name, Value: value})
	}
	return out
}

// ---- docker live operations ----

func (c *Client) dockerLogs(ctx context.Context, name string, lines int64) (string, error) {
	cmd := c.dockerCmd(false, "logs", "--tail", strconv.FormatInt(lines, 10), "--timestamps", name)
	b, err := runCaptureLimit(ctx, cmd, maxLogBytes)
	return string(b), err
}

func (c *Client) dockerRuntimeEnv(ctx context.Context, name string) (string, error) {
	b, err := c.dockerOut(ctx, "exec", name, "sh", "-c", "env | sort")
	return string(b), err
}

func (c *Client) dockerExec(ctx context.Context, name string, command []string) (string, error) {
	b, err := c.dockerOut(ctx, append([]string{"exec", name}, command...)...)
	return string(b), err
}

// dockerYAML renders `docker inspect` as YAML (tankertop's `y` shows it in place
// of a pod manifest).
func (c *Client) dockerYAML(ctx context.Context, name string) (string, error) {
	b, err := c.dockerOut(ctx, "inspect", name)
	if err != nil {
		return "", err
	}
	y, err := yaml.JSONToYAML(b)
	if err != nil {
		return string(b), nil // fall back to raw JSON
	}
	return string(y), nil
}

func (c *Client) dockerEvents(ctx context.Context, name string) ([]string, error) {
	b, err := c.dockerOut(ctx, "events", "--since", "24h", "--until", "0s",
		"--filter", "container="+name, "--format", "{{.Time}}  {{.Action}}")
	if err != nil {
		return nil, err
	}
	var out []string
	for _, line := range strings.Split(strings.TrimSpace(string(b)), "\n") {
		if line != "" {
			out = append(out, line)
		}
	}
	return out, nil
}

func (c *Client) dockerDelete(ctx context.Context, name string) error {
	_, err := c.dockerOut(ctx, "rm", "-f", name)
	return err
}

func (c *Client) dockerRestart(ctx context.Context, name string) (string, error) {
	if _, err := c.dockerOut(ctx, "restart", name); err != nil {
		return "", err
	}
	return "restarted container " + name, nil
}

// Lifecycle verbs Docker supports that Kubernetes has no equivalent for. Verbs:
// start | stop | pause | unpause | kill.
var lifecyclePastTense = map[string]string{
	"start": "started", "stop": "stopped", "pause": "paused",
	"unpause": "unpaused", "kill": "killed",
}

// ContainerLifecycle runs a docker lifecycle verb on a container. It errors on a
// non-Docker client, since pods have no start/stop/pause model.
func (c *Client) ContainerLifecycle(ctx context.Context, verb, name string) (string, error) {
	if !c.docker {
		return "", fmt.Errorf("%s is a container operation; Kubernetes pods don't support it", verb)
	}
	if _, ok := lifecyclePastTense[verb]; !ok {
		return "", fmt.Errorf("unknown lifecycle verb %q", verb)
	}
	if _, err := c.dockerOut(ctx, verb, name); err != nil {
		return "", err
	}
	return lifecyclePastTense[verb] + " container " + name, nil
}

// ---- small parsers ----

// statByID matches an inspect's full 64-char id against docker stats, which
// reports the short 12-char id.
func statByID(stats map[string]dockerStat, fullID string) (dockerStat, bool) {
	if s, ok := stats[fullID]; ok {
		return s, true
	}
	if len(fullID) >= 12 {
		if s, ok := stats[fullID[:12]]; ok {
			return s, true
		}
	}
	return dockerStat{}, false
}

func firstField(s string) string {
	if i := strings.IndexByte(s, ' '); i >= 0 {
		return s[:i]
	}
	return s
}

func parseCPUPerc(s string) int64 {
	// docker stats CPU% is relative to one core × 100, so 100% == 1000 milli.
	f, err := strconv.ParseFloat(strings.TrimSuffix(strings.TrimSpace(s), "%"), 64)
	if err != nil {
		return 0
	}
	return int64(f * 10)
}

// parseNetIO parses docker's "1.52kB / 126B" cumulative rx/tx into bytes.
func parseNetIO(s string) (rx, tx int64) {
	parts := strings.SplitN(s, "/", 2)
	if len(parts) != 2 {
		return 0, 0
	}
	return parseSize(strings.TrimSpace(parts[0])), parseSize(strings.TrimSpace(parts[1]))
}

func parsePortProto(s string) int32 {
	p, err := strconv.Atoi(firstField(strings.ReplaceAll(s, "/", " ")))
	if err != nil {
		return 0
	}
	return int32(p)
}

var sizeUnits = []struct {
	suffix string
	mult   float64
}{
	{"GiB", 1 << 30}, {"MiB", 1 << 20}, {"KiB", 1 << 10},
	{"GB", 1e9}, {"MB", 1e6}, {"kB", 1e3}, {"KB", 1e3}, {"B", 1},
}

// parseSize parses a docker size string like "123.4MiB" or "1.5GB" into bytes.
func parseSize(s string) int64 {
	s = strings.TrimSpace(s)
	for _, u := range sizeUnits {
		if strings.HasSuffix(s, u.suffix) {
			if f, err := strconv.ParseFloat(strings.TrimSuffix(s, u.suffix), 64); err == nil {
				return int64(f * u.mult)
			}
		}
	}
	return 0
}
