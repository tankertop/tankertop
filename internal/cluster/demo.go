package cluster

import (
	"fmt"
	"math"
	"sort"
	"strings"
	"time"
)

const (
	kib = 1024
	mib = 1024 * kib
	gib = 1024 * mib
)

// demoPodSpec is a compact description of a synthetic pod.
type demoPodSpec struct {
	ns, name, ctrl, status  string
	ready, total            int
	restarts                int32
	ageMin                  int
	cpuBase, cpuAmp         float64 // milli
	memMB                   int64
	reqCPU, limCPU          int64 // milli, 0 = unset
	reqMemMB, limMemMB      int64 // MiB, 0 = unset
	image                   string
	ports                   []int32
	ip                      string
}

var demoPods = []demoPodSpec{
	{"default", "web-7d9f8c6b5-abcde", "Deployment/web", "Running", 1, 1, 0, 42, 120, 60, 84, 100, 500, 64, 256, "nginx:1.27-alpine", []int32{80}, "10.1.4.21"},
	{"default", "web-7d9f8c6b5-fghij", "Deployment/web", "Running", 1, 1, 0, 42, 110, 55, 79, 100, 500, 64, 256, "nginx:1.27-alpine", []int32{80}, "10.1.4.22"},
	{"default", "api-5c7d4f9b8-klmno", "Deployment/api", "Running", 1, 1, 0, 37, 340, 180, 210, 250, 1000, 256, 512, "ghcr.io/acme/api:2.3.1", []int32{8080}, "10.1.4.23"},
	{"default", "worker-6b8c9d7e5-pqrst", "Deployment/worker", "Running", 1, 1, 2, 37, 610, 300, 512, 500, 2000, 512, 1024, "ghcr.io/acme/worker:2.3.1", nil, "10.1.4.24"},
	{"default", "redis-0", "StatefulSet/redis", "Running", 1, 1, 0, 88, 45, 20, 96, 100, 0, 128, 256, "redis:7-alpine", []int32{6379}, "10.1.4.25"},
	{"default", "bad-config-xyz12", "(standalone)", "CrashLoopBackOff", 0, 1, 27, 63, 0, 0, 0, 0, 0, 0, 0, "ghcr.io/acme/legacy:0.1", nil, "10.1.4.26"},
	{"default", "image-puller-9a8b7", "Job/image-puller", "ImagePullBackOff", 0, 1, 0, 4, 0, 0, 0, 0, 0, 0, 0, "ghcr.io/acme/missing:latest", nil, ""},
	{"default", "db-migrate-5f6g7", "Job/db-migrate", "Completed", 0, 1, 0, 51, 0, 0, 0, 0, 0, 0, 0, "ghcr.io/acme/migrate:2.3.1", nil, ""},

	{"monitoring", "prometheus-0", "StatefulSet/prometheus", "Running", 2, 2, 0, 240, 480, 160, 1740, 500, 0, 1024, 3072, "prom/prometheus:v2.53", []int32{9090}, "10.1.5.11"},
	{"monitoring", "grafana-8c7b6a5d4-uvwxy", "Deployment/grafana", "Running", 1, 1, 1, 240, 70, 40, 190, 100, 500, 128, 512, "grafana/grafana:11.1.0", []int32{3000}, "10.1.5.12"},
	{"monitoring", "loki-0", "StatefulSet/loki", "Running", 1, 1, 0, 240, 130, 70, 640, 200, 0, 512, 1024, "grafana/loki:3.1.0", []int32{3100}, "10.1.5.13"},

	{"kube-system", "coredns-668d6bf9bc-aaa11", "Deployment/coredns", "Running", 1, 1, 0, 1440, 15, 8, 42, 100, 0, 70, 170, "registry.k8s.io/coredns:1.11.1", []int32{53}, "10.1.0.10"},
	{"kube-system", "calico-node-bbb22", "DaemonSet/calico-node", "Running", 1, 1, 0, 1440, 95, 30, 158, 250, 0, 0, 0, "calico/node:v3.28.0", nil, "192.168.64.7"},
	{"kube-system", "metrics-server-7cf8c8f6d-ccc33", "Deployment/metrics-server", "Running", 1, 1, 0, 300, 18, 10, 34, 100, 0, 200, 0, "registry.k8s.io/metrics-server:0.7.1", []int32{10250}, "10.1.0.11"},
	{"kube-system", "hostpath-provisioner-ddd44", "Deployment/hostpath-provisioner", "Running", 1, 1, 0, 1400, 5, 3, 12, 0, 0, 0, 0, "cdkbot/hostpath-provisioner:1.5.0", nil, "10.1.0.12"},
}

// DemoSnapshot builds a synthetic, time-varying cluster snapshot for `--demo`.
func DemoSnapshot(tick int) Snapshot {
	t := float64(tick)
	snap := Snapshot{Context: "demo-cluster", CollectedAt: time.Now(), MetricsOK: true}

	// node
	nodeCPU := int64(2600 + 900*math.Sin(t*0.20) + 300*math.Sin(t*0.7))
	snap.Nodes = []NodeInfo{{
		Name: "kube-demo-1", Ready: true,
		CPUCapacityMilli: 8000, MemCapacityBytes: 32 * gib,
		CPUAllocMilli: 7800, MemAllocBytes: 31 * gib,
		CPUUsedMilli: nodeCPU, MemUsedBytes: 15*gib + int64(tick%6)*256*mib,
		CPUReqMilli: 5200, MemReqBytes: 34 * gib, // intentionally overcommitted
		PodCount: len(demoPods), PodsCapacity: 110,
		EphemeralCapBytes: 200 * gib,
	}}

	nsSet := map[string]struct{}{}
	for i, s := range demoPods {
		cpu := int64(math.Max(0, s.cpuBase+s.cpuAmp*math.Sin(t*0.3+float64(i))))
		pi := PodInfo{
			Namespace: s.ns, Name: s.name, Node: "kube-demo-1",
			Status: s.status, Ready: s.ready, Total: s.total, Restarts: s.restarts,
			Age:         time.Duration(s.ageMin) * time.Minute,
			CPUMilli:    cpu,
			MemBytes:    s.memMB * mib,
			PodIP:       s.ip, HostIP: "192.168.64.7",
			Controller:     s.ctrl,
			ContainerPorts: s.ports,
			CPUReqMilli:    s.reqCPU, CPULimMilli: s.limCPU,
			MemReqBytes: s.reqMemMB * mib, MemLimBytes: s.limMemMB * mib,
		}
		if s.ctrl != "(standalone)" {
			parts := s.ctrl
			for j := 0; j < len(parts); j++ {
				if parts[j] == '/' {
					pi.OwnerKind, pi.OwnerName = parts[:j], parts[j+1:]
					break
				}
			}
		}
		cname := containerName(s.name)
		state, reason := "Running", ""
		switch s.status {
		case "CrashLoopBackOff":
			state, reason = "Waiting", "CrashLoopBackOff"
		case "ImagePullBackOff":
			state, reason = "Waiting", "ImagePullBackOff"
		case "Completed":
			state, reason = "Terminated", "Completed"
		}
		pi.Containers = []ContainerInfo{{
			Name: cname, Image: s.image, Ready: s.ready == s.total && s.total > 0,
			State: state, Reason: reason, Restarts: s.restarts,
			CPUMilli: cpu, MemBytes: s.memMB * mib,
			Env: demoDeclaredEnv(s.name), EnvFrom: demoEnvSources(s.name),
		}}
		snap.Pods = append(snap.Pods, pi)
		nsSet[s.ns] = struct{}{}
	}
	for ns := range nsSet {
		snap.Namespaces = append(snap.Namespaces, ns)
	}

	snap.Services = []ServiceInfo{
		{"default", "web", "ClusterIP", "10.152.183.10", []string{"80/TCP→8080"}, "app=web",
			[]EndpointInfo{{"10.1.4.21", "web-7d9f8c6b5-abcde", true}, {"10.1.4.22", "web-7d9f8c6b5-fghij", true}}},
		{"default", "api", "ClusterIP", "10.152.183.11", []string{"8080/TCP"}, "app=api",
			[]EndpointInfo{{"10.1.4.23", "api-5c7d4f9b8-klmno", true}}},
		{"default", "redis", "ClusterIP", "None", []string{"6379/TCP"}, "app=redis",
			[]EndpointInfo{{"10.1.4.25", "redis-0", true}}},
		{"monitoring", "grafana", "NodePort", "10.152.183.20", []string{"3000/TCP (node:30300)"}, "app=grafana",
			[]EndpointInfo{{"10.1.5.12", "grafana-8c7b6a5d4-uvwxy", true}}},
		{"monitoring", "prometheus", "ClusterIP", "10.152.183.21", []string{"9090/TCP"}, "app=prometheus",
			[]EndpointInfo{{"10.1.5.11", "prometheus-0", true}}},
	}
	snap.Ingresses = []IngressInfo{
		{"default", "web", "public", []string{"demo.example.com/ → web:80", "demo.example.com/api → api:8080"}, "192.168.64.7"},
		{"monitoring", "grafana", "public", []string{"grafana.example.com/ → grafana:3000"}, "192.168.64.7"},
	}
	snap.NetPols = []NetPolInfo{
		{"default", "default-deny", "<all>", 0, 0},
		{"default", "allow-web", "app=web", 1, 1},
	}
	snap.Events = demoEvents()
	return snap
}

func containerName(pod string) string {
	// strip the trailing generated suffix(es) for a plausible container name
	base := pod
	for i := len(pod) - 1; i >= 0; i-- {
		if pod[i] == '-' {
			base = pod[:i]
			break
		}
	}
	return base
}

func demoEvents() []EventInfo {
	return []EventInfo{
		{"Warning", "BackOff", "default", "Pod/bad-config-xyz12", "Back-off restarting failed container legacy in pod bad-config-xyz12", 214, 15 * time.Second},
		{"Warning", "Failed", "default", "Pod/image-puller-9a8b7", `Failed to pull image "ghcr.io/acme/missing:latest": not found`, 6, 40 * time.Second},
		{"Normal", "Scheduled", "default", "Pod/api-5c7d4f9b8-klmno", "Successfully assigned default/api-5c7d4f9b8-klmno to kube-demo-1", 1, 37 * time.Minute},
		{"Normal", "Pulled", "monitoring", "Pod/grafana-8c7b6a5d4-uvwxy", `Container image "grafana/grafana:11.1.0" already present on machine`, 1, 4 * time.Hour},
		{"Normal", "Created", "default", "Pod/worker-6b8c9d7e5-pqrst", "Created container worker", 1, 37 * time.Minute},
		{"Normal", "Started", "default", "Pod/web-7d9f8c6b5-abcde", "Started container web", 1, 42 * time.Minute},
		{"Warning", "Unhealthy", "monitoring", "Pod/loki-0", "Readiness probe failed: HTTP probe failed with statuscode: 503", 3, 6 * time.Minute},
		{"Normal", "Completed", "default", "Job/db-migrate", "Job completed", 1, 51 * time.Minute},
	}
}

// demoDeclaredEnv gives a few pods a plausible spec env, covering every source
// kind (literal, configMap, secret, downward API) so the env pane has something
// to show in `--demo`. Keyed on the pod-name prefix, since demo container names
// still carry the ReplicaSet hash.
func demoDeclaredEnv(pod string) []EnvVar {
	common := []EnvVar{
		{Name: "NODE_NAME", From: "field spec.nodeName"},
		{Name: "POD_IP", From: "field status.podIP"},
	}
	switch demoApp(pod) {
	case "api":
		return append([]EnvVar{
			{Name: "LOG_LEVEL", Value: "info"},
			{Name: "LISTEN_ADDR", Value: ":8080"},
			{Name: "DATABASE_URL", From: "secret db-creds/url"},
			{Name: "REDIS_HOST", Value: "redis.default.svc.cluster.local"},
			{Name: "FEATURE_FLAGS", From: "configMap api-config/flags"},
			{Name: "MEMORY_LIMIT", From: "resource limits.memory"},
		}, common...)
	case "worker":
		return append([]EnvVar{
			{Name: "QUEUE", Value: "orders"},
			{Name: "CONCURRENCY", Value: "8"},
			{Name: "API_TOKEN", From: "secret worker-creds/token"},
		}, common...)
	case "web":
		return append([]EnvVar{{Name: "NGINX_ENTRYPOINT_QUIET_LOGS", Value: "1"}}, common...)
	case "bad-config":
		return []EnvVar{
			{Name: "CONFIG_PATH", Value: "/etc/legacy/config.yaml"},
			{Name: "DB_PASSWORD", From: "secret legacy-creds/password"},
		}
	case "grafana":
		return []EnvVar{
			{Name: "GF_SECURITY_ADMIN_PASSWORD", From: "secret grafana-admin/password"},
			{Name: "GF_SERVER_ROOT_URL", Value: "https://grafana.example.com"},
		}
	}
	return nil
}

func demoEnvSources(pod string) []EnvSource {
	switch demoApp(pod) {
	case "api":
		return []EnvSource{{"configMap", "api-config", ""}, {"secret", "api-secrets", "APP_"}}
	case "worker":
		return []EnvSource{{"configMap", "api-config", ""}}
	}
	return nil
}

// demoApp maps a demo pod name to the app it belongs to.
func demoApp(pod string) string {
	for _, app := range []string{"api", "worker", "web", "bad-config", "grafana"} {
		if pod == app || strings.HasPrefix(pod, app+"-") {
			return app
		}
	}
	return ""
}

// demoResolved supplies the values the kubelet would have read out of a
// ConfigMap/Secret/downward-API reference.
var demoResolved = map[string]string{
	"DATABASE_URL":               "postgres://api:hunter2@db.default.svc:5432/app",
	"FEATURE_FLAGS":              "new-checkout,async-invoices",
	"MEMORY_LIMIT":               "536870912",
	"API_TOKEN":                  "s3cr3t-9f4b21ce",
	"DB_PASSWORD":                "correct-horse-battery-staple",
	"GF_SECURITY_ADMIN_PASSWORD": "admin",
}

// demoBulk are the whole ConfigMaps/Secrets pulled in via envFrom.
var demoBulk = map[string]map[string]string{
	"api-config":  {"FLAGS": "new-checkout,async-invoices", "TRACE_SAMPLE": "0.1"},
	"api-secrets": {"SENTRY_DSN": "https://8f2c1e@sentry.example.com/42"},
}

// demoEnv is what `env` would print inside the container: the kubelet's
// injections, plus the spec env with every reference resolved.
func demoEnv(pod string) string {
	vals := map[string]string{
		"HOME":                    "/root",
		"HOSTNAME":                pod,
		"KUBERNETES_PORT":         "tcp://10.152.183.1:443",
		"KUBERNETES_SERVICE_HOST": "10.152.183.1",
		"KUBERNETES_SERVICE_PORT": "443",
		"NODE_NAME":               "kube-demo-1",
		"PATH":                    "/usr/local/sbin:/usr/local/bin:/usr/sbin:/usr/bin:/sbin:/bin",
		"TERM":                    "xterm",
	}
	for _, s := range demoPods {
		if s.name == pod && s.ip != "" {
			vals["POD_IP"] = s.ip
		}
	}
	for _, e := range demoDeclaredEnv(pod) {
		switch {
		case e.Value != "":
			vals[e.Name] = e.Value
		case demoResolved[e.Name] != "":
			vals[e.Name] = demoResolved[e.Name]
		case e.Name != "NODE_NAME" && e.Name != "POD_IP":
			vals[e.Name] = "<resolved by the kubelet>"
		}
	}
	for _, src := range demoEnvSources(pod) {
		for k, v := range demoBulk[src.Name] {
			vals[src.Prefix+k] = v
		}
	}

	names := make([]string, 0, len(vals))
	for k := range vals {
		names = append(names, k)
	}
	sort.Strings(names)
	var b strings.Builder
	for _, k := range names {
		b.WriteString(k + "=" + vals[k] + "\n")
	}
	return b.String()
}

func demoLogs(pod string) string {
	var b []byte
	ip := "10.1.4.1"
	for i := 0; i < 60; i++ {
		line := fmt.Sprintf(`%s - - [%02d/Jul/2026:21:%02d:%02d +0000] "GET /healthz HTTP/1.1" 200 2 "-" "kube-probe/1.30"`,
			ip, 1, (i*7)%60, (i*13)%60)
		if i%9 == 0 {
			line = fmt.Sprintf(`%s - - [%02d/Jul/2026:21:%02d:%02d +0000] "GET /api/v1/items?page=%d HTTP/1.1" 200 1841 "-" "curl/8.5.0"`,
				ip, 1, (i*7)%60, (i*13)%60, i)
		}
		if i == 47 {
			line = `10.1.4.9 - - [01/Jul/2026:21:03:11 +0000] "POST /api/v1/orders HTTP/1.1" 500 73 "-" "Go-http-client/2.0"`
		}
		b = append(b, line...)
		b = append(b, '\n')
	}
	return string(b)
}

func demoInspect(pod string) string {
	return `### identity
uid=0(root) gid=0(root) groups=0(root)

### env
HOME=/root
HOSTNAME=` + pod + `
KUBERNETES_SERVICE_HOST=10.152.183.1
KUBERNETES_SERVICE_PORT=443
PATH=/usr/local/sbin:/usr/local/bin:/usr/sbin:/usr/bin:/sbin:/bin

### mounts
overlay on / type overlay (rw,relatime)
tmpfs on /dev type tmpfs (rw,nosuid,size=65536k)
/dev/vda1 on /etc/hosts type ext4 (rw,relatime)

### disk
Filesystem      Size  Used Avail Use% Mounted on
overlay         196G   68G  128G  35% /
tmpfs            64M     0   64M   0% /dev

### processes
PID   USER     TIME  COMMAND
    1 root      2:14 nginx: master process nginx -g daemon off;
   31 nginx     0:07 nginx: worker process

### / (root filesystem)
drwxr-xr-x    1 root     root          4096 Jul  1 21:03 .
drwxr-xr-x    2 root     root         12288 Jun  3 00:00 bin
drwxr-xr-x    5 root     root           360 Jul  1 21:00 dev
drwxr-xr-x    1 root     root          4096 Jul  1 21:00 etc
`
}

func demoYAML(ns, name string) string {
	return fmt.Sprintf(`apiVersion: v1
kind: Pod
metadata:
  name: %s
  namespace: %s
  labels:
    app: web
spec:
  containers:
  - name: %s
    image: nginx:1.27-alpine
    ports:
    - containerPort: 80
    resources:
      requests:
        cpu: 100m
        memory: 64Mi
      limits:
        cpu: 500m
        memory: 256Mi
status:
  phase: Running
  podIP: 10.1.4.21
`, name, ns, containerName(name))
}

func demoPodEvents(name string) []string {
	return []string{
		"Normal  Scheduled  Successfully assigned to kube-demo-1",
		"Normal  Pulled  Container image already present on machine",
		"Normal  Created  Created container " + containerName(name),
		"Normal  Started  Started container " + containerName(name),
	}
}
