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

// The Docker backend maps a container host onto kubeview's Kubernetes model:
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
	cmd := c.dockerCmd(false, args...)
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
			msg := strings.TrimSpace(errb.String())
			if msg != "" {
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
	return snap
}

// containerToPod maps one inspected container onto kubeview's PodInfo.
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

// dockerStatus maps docker's state to kubeview's status vocabulary so the
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

// dockerEnv turns docker's KEY=value env into kubeview's EnvVar list. Docker has
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
	b, err := c.dockerOut(ctx, "logs", "--tail", strconv.FormatInt(lines, 10), "--timestamps", name)
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

// dockerYAML renders `docker inspect` as YAML (kubeview's `y` shows it in place
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
