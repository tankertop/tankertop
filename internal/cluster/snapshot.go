package cluster

import (
	"context"
	"fmt"
	"sort"
	"time"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

// Snapshot is a single point-in-time view of the cluster.
type Snapshot struct {
	Context     string
	CollectedAt time.Time
	Nodes       []NodeInfo
	Pods        []PodInfo
	Namespaces  []string
	Services    []ServiceInfo
	Ingresses   []IngressInfo
	NetPols     []NetPolInfo
	Events      []EventInfo
	MetricsOK   bool
	Err         error
}

// NodeInfo summarises a node's capacity, allocatable, requests and usage.
type NodeInfo struct {
	Name              string
	Ready             bool
	CPUCapacityMilli  int64
	MemCapacityBytes  int64
	CPUAllocMilli     int64 // allocatable
	MemAllocBytes     int64
	CPUUsedMilli      int64 // live usage (metrics)
	MemUsedBytes      int64
	CPUReqMilli       int64 // sum of scheduled pod requests
	MemReqBytes       int64
	PodCount          int
	PodsCapacity      int
	EphemeralCapBytes int64
	Conditions        []string // pressure conditions currently true
}

// PodInfo summarises a pod and aggregates its containers.
type PodInfo struct {
	Namespace  string
	Name       string
	Node       string
	Phase      string
	Status     string // display status (CrashLoopBackOff, Running, Completed, ...)
	Ready      int
	Total      int
	Restarts   int32
	Age        time.Duration
	CPUMilli   int64
	MemBytes   int64
	PodIP          string
	HostIP         string
	OwnerKind      string
	OwnerName      string
	Controller     string // top-level workload, e.g. "Deployment/web" or "(standalone)"
	ContainerPorts []int32
	CPUReqMilli    int64
	CPULimMilli    int64
	MemReqBytes    int64
	MemLimBytes    int64
	Containers     []ContainerInfo
}

// ContainerInfo describes one container within a pod.
type ContainerInfo struct {
	Name     string
	Image    string
	Ready    bool
	State    string // Running / Waiting / Terminated
	Reason   string
	Restarts int32
	CPUMilli int64
	MemBytes int64
	Env      []EnvVar
	EnvFrom  []EnvSource
}

// EnvVar is one environment variable declared in a container's spec. Value is
// empty when the variable is sourced indirectly — From then says from where.
type EnvVar struct {
	Name  string
	Value string
	From  string // "secret db-creds/password", "field spec.nodeName", …
}

// EnvSource is a whole ConfigMap/Secret imported into the container env (envFrom).
type EnvSource struct {
	Kind   string // configMap | secret
	Name   string
	Prefix string
}

// declaredEnv flattens a container's env: literals keep their value, indirect
// ones (configMap/secret/downward API) record where the kubelet reads them from.
func declaredEnv(vars []corev1.EnvVar) []EnvVar {
	if len(vars) == 0 {
		return nil
	}
	out := make([]EnvVar, 0, len(vars))
	for _, v := range vars {
		e := EnvVar{Name: v.Name, Value: v.Value}
		switch src := v.ValueFrom; {
		case src == nil:
		case src.ConfigMapKeyRef != nil:
			e.From = "configMap " + src.ConfigMapKeyRef.Name + "/" + src.ConfigMapKeyRef.Key
		case src.SecretKeyRef != nil:
			e.From = "secret " + src.SecretKeyRef.Name + "/" + src.SecretKeyRef.Key
		case src.FieldRef != nil:
			e.From = "field " + src.FieldRef.FieldPath
		case src.ResourceFieldRef != nil:
			e.From = "resource " + src.ResourceFieldRef.Resource
		}
		out = append(out, e)
	}
	return out
}

func envSources(refs []corev1.EnvFromSource) []EnvSource {
	if len(refs) == 0 {
		return nil
	}
	out := make([]EnvSource, 0, len(refs))
	for _, r := range refs {
		switch {
		case r.ConfigMapRef != nil:
			out = append(out, EnvSource{"configMap", r.ConfigMapRef.Name, r.Prefix})
		case r.SecretRef != nil:
			out = append(out, EnvSource{"secret", r.SecretRef.Name, r.Prefix})
		}
	}
	return out
}

type resourcePair struct {
	cpu int64
	mem int64
}

// Collect gathers pods, nodes and (if available) metrics into a Snapshot.
// namespace == "" means all namespaces.
func (c *Client) Collect(ctx context.Context, namespace string) Snapshot {
	if c.demo {
		c.demoTick++
		return DemoSnapshot(c.demoTick)
	}
	snap := Snapshot{Context: c.Context, CollectedAt: time.Now()}

	pods, err := c.Clientset.CoreV1().Pods(namespace).List(ctx, metav1.ListOptions{})
	if err != nil {
		snap.Err = err
		return snap
	}
	nodes, err := c.Clientset.CoreV1().Nodes().List(ctx, metav1.ListOptions{})
	if err != nil {
		snap.Err = err
		return snap
	}

	// Map ReplicaSet -> owning Deployment so pods can show their top controller.
	rsToDep := map[string]string{}
	if rsl, rerr := c.Clientset.AppsV1().ReplicaSets(namespace).List(ctx, metav1.ListOptions{}); rerr == nil {
		for _, rs := range rsl.Items {
			for _, o := range rs.OwnerReferences {
				if o.Kind == "Deployment" {
					rsToDep[rs.Namespace+"/"+rs.Name] = o.Name
					break
				}
			}
		}
	}

	podMetrics := map[string]resourcePair{}
	containerMetrics := map[string]resourcePair{}
	if pm, merr := c.Metrics.MetricsV1beta1().PodMetricses(namespace).List(ctx, metav1.ListOptions{}); merr == nil {
		snap.MetricsOK = true
		for _, m := range pm.Items {
			key := m.Namespace + "/" + m.Name
			agg := resourcePair{}
			for _, cm := range m.Containers {
				cpu := cm.Usage.Cpu().MilliValue()
				mem := cm.Usage.Memory().Value()
				agg.cpu += cpu
				agg.mem += mem
				containerMetrics[key+"/"+cm.Name] = resourcePair{cpu, mem}
			}
			podMetrics[key] = agg
		}
	}
	nodeMetrics := map[string]resourcePair{}
	if nm, merr := c.Metrics.MetricsV1beta1().NodeMetricses().List(ctx, metav1.ListOptions{}); merr == nil {
		for _, m := range nm.Items {
			nodeMetrics[m.Name] = resourcePair{m.Usage.Cpu().MilliValue(), m.Usage.Memory().Value()}
		}
	}

	podsByNode := map[string]int{}
	reqCPUByNode := map[string]int64{}
	reqMemByNode := map[string]int64{}
	nsSet := map[string]struct{}{}

	for i := range pods.Items {
		p := &pods.Items[i]
		key := p.Namespace + "/" + p.Name

		pi := PodInfo{
			Namespace: p.Namespace,
			Name:      p.Name,
			Node:      p.Spec.NodeName,
			Phase:     string(p.Status.Phase),
			Age:       time.Since(p.CreationTimestamp.Time),
			Status:    podDisplayStatus(p),
			PodIP:     p.Status.PodIP,
			HostIP:    p.Status.HostIP,
		}
		if len(p.OwnerReferences) > 0 {
			pi.OwnerKind = p.OwnerReferences[0].Kind
			pi.OwnerName = p.OwnerReferences[0].Name
		}
		pi.Controller = controllerOf(p, rsToDep)
		if m, ok := podMetrics[key]; ok {
			pi.CPUMilli = m.cpu
			pi.MemBytes = m.mem
		}

		statusByName := map[string]corev1.ContainerStatus{}
		for _, cs := range p.Status.ContainerStatuses {
			statusByName[cs.Name] = cs
		}

		for _, spec := range p.Spec.Containers {
			ci := ContainerInfo{
				Name: spec.Name, Image: spec.Image, State: "Waiting",
				Env: declaredEnv(spec.Env), EnvFrom: envSources(spec.EnvFrom),
			}
			if cs, ok := statusByName[spec.Name]; ok {
				ci.Ready = cs.Ready
				ci.Restarts = cs.RestartCount
				ci.State, ci.Reason = containerState(cs)
				pi.Restarts += cs.RestartCount
				if cs.Ready {
					pi.Ready++
				}
			}
			if m, ok := containerMetrics[key+"/"+spec.Name]; ok {
				ci.CPUMilli = m.cpu
				ci.MemBytes = m.mem
			}
			for _, port := range spec.Ports {
				pi.ContainerPorts = append(pi.ContainerPorts, port.ContainerPort)
			}
			pi.CPUReqMilli += spec.Resources.Requests.Cpu().MilliValue()
			pi.CPULimMilli += spec.Resources.Limits.Cpu().MilliValue()
			pi.MemReqBytes += spec.Resources.Requests.Memory().Value()
			pi.MemLimBytes += spec.Resources.Limits.Memory().Value()
			pi.Containers = append(pi.Containers, ci)
		}
		pi.Total = len(p.Spec.Containers)

		snap.Pods = append(snap.Pods, pi)
		if p.Spec.NodeName != "" {
			podsByNode[p.Spec.NodeName]++
			reqCPUByNode[p.Spec.NodeName] += pi.CPUReqMilli
			reqMemByNode[p.Spec.NodeName] += pi.MemReqBytes
		}
		nsSet[p.Namespace] = struct{}{}
	}

	for i := range nodes.Items {
		n := &nodes.Items[i]
		ni := NodeInfo{
			Name:              n.Name,
			CPUCapacityMilli:  n.Status.Capacity.Cpu().MilliValue(),
			MemCapacityBytes:  n.Status.Capacity.Memory().Value(),
			CPUAllocMilli:     n.Status.Allocatable.Cpu().MilliValue(),
			MemAllocBytes:     n.Status.Allocatable.Memory().Value(),
			CPUReqMilli:       reqCPUByNode[n.Name],
			MemReqBytes:       reqMemByNode[n.Name],
			PodCount:          podsByNode[n.Name],
			PodsCapacity:      int(n.Status.Allocatable.Pods().Value()),
			EphemeralCapBytes: n.Status.Capacity.StorageEphemeral().Value(),
		}
		for _, cond := range n.Status.Conditions {
			switch {
			case cond.Type == corev1.NodeReady:
				ni.Ready = cond.Status == corev1.ConditionTrue
			case cond.Status == corev1.ConditionTrue:
				// MemoryPressure / DiskPressure / PIDPressure
				ni.Conditions = append(ni.Conditions, string(cond.Type))
			}
		}
		if m, ok := nodeMetrics[n.Name]; ok {
			ni.CPUUsedMilli = m.cpu
			ni.MemUsedBytes = m.mem
		}
		snap.Nodes = append(snap.Nodes, ni)
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

	c.collectNetwork(ctx, namespace, &snap)
	c.collectEvents(ctx, namespace, &snap)

	return snap
}

// controllerOf returns the pod's top-level workload, resolving ReplicaSet to
// its Deployment when possible.
func controllerOf(p *corev1.Pod, rsToDep map[string]string) string {
	if len(p.OwnerReferences) == 0 {
		return "(standalone)"
	}
	o := p.OwnerReferences[0]
	if o.Kind == "ReplicaSet" {
		if dep, ok := rsToDep[p.Namespace+"/"+o.Name]; ok {
			return "Deployment/" + dep
		}
		return "ReplicaSet/" + o.Name
	}
	return o.Kind + "/" + o.Name
}

func containerState(cs corev1.ContainerStatus) (string, string) {
	switch {
	case cs.State.Running != nil:
		return "Running", ""
	case cs.State.Waiting != nil:
		return "Waiting", cs.State.Waiting.Reason
	case cs.State.Terminated != nil:
		r := cs.State.Terminated.Reason
		if r == "" {
			r = fmt.Sprintf("ExitCode:%d", cs.State.Terminated.ExitCode)
		}
		return "Terminated", r
	}
	return "Unknown", ""
}

// podDisplayStatus reproduces (a practical subset of) kubectl's pod STATUS column.
func podDisplayStatus(pod *corev1.Pod) string {
	reason := string(pod.Status.Phase)
	if pod.Status.Reason != "" {
		reason = pod.Status.Reason
	}

	initializing := false
	for i := range pod.Status.InitContainerStatuses {
		cs := pod.Status.InitContainerStatuses[i]
		switch {
		case cs.State.Terminated != nil && cs.State.Terminated.ExitCode == 0:
			continue
		case cs.State.Terminated != nil:
			if cs.State.Terminated.Reason == "" {
				if cs.State.Terminated.Signal != 0 {
					reason = fmt.Sprintf("Init:Signal:%d", cs.State.Terminated.Signal)
				} else {
					reason = fmt.Sprintf("Init:ExitCode:%d", cs.State.Terminated.ExitCode)
				}
			} else {
				reason = "Init:" + cs.State.Terminated.Reason
			}
			initializing = true
		case cs.State.Waiting != nil && cs.State.Waiting.Reason != "" && cs.State.Waiting.Reason != "PodInitializing":
			reason = "Init:" + cs.State.Waiting.Reason
			initializing = true
		default:
			reason = fmt.Sprintf("Init:%d/%d", i, len(pod.Spec.InitContainers))
			initializing = true
		}
		break
	}

	if !initializing {
		for i := len(pod.Status.ContainerStatuses) - 1; i >= 0; i-- {
			cs := pod.Status.ContainerStatuses[i]
			switch {
			case cs.State.Waiting != nil && cs.State.Waiting.Reason != "":
				reason = cs.State.Waiting.Reason
			case cs.State.Terminated != nil && cs.State.Terminated.Reason != "":
				reason = cs.State.Terminated.Reason
			case cs.State.Terminated != nil:
				if cs.State.Terminated.Signal != 0 {
					reason = fmt.Sprintf("Signal:%d", cs.State.Terminated.Signal)
				} else {
					reason = fmt.Sprintf("ExitCode:%d", cs.State.Terminated.ExitCode)
				}
			}
		}
	}

	if pod.DeletionTimestamp != nil {
		reason = "Terminating"
	}
	return reason
}
