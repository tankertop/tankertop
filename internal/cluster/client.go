package cluster

import (
	"bytes"
	"context"
	"io"
	"os/exec"
	"strings"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/kubernetes/scheme"
	"k8s.io/client-go/rest"
	"k8s.io/client-go/tools/clientcmd"
	"k8s.io/client-go/tools/remotecommand"
	metricsv "k8s.io/metrics/pkg/client/clientset/versioned"
	"sigs.k8s.io/yaml"
)

// Client wraps the Kubernetes core API and the metrics API.
type Client struct {
	Clientset *kubernetes.Clientset
	Metrics   *metricsv.Clientset
	Context   string
	cfg       *rest.Config

	ssh    *sshTarget // non-nil when reached over an ssh tunnel
	tunnel *tunnel

	kubectlCmd []string // argv prefix for kubectl on the cluster host

	docker    bool
	dockerBin string

	demo     bool
	demoTick int
}

// IsDocker reports whether this client is backed by a container engine rather
// than Kubernetes — the UI uses it to hide concepts Docker doesn't have.
func (c *Client) IsDocker() bool { return c.docker }

// kubectl returns the argv prefix for running kubectl on whichever host owns the
// cluster. Empty only for the demo client, which never shells out.
func (c *Client) kubectl() []string {
	if len(c.kubectlCmd) > 0 {
		return c.kubectlCmd
	}
	return []string{"kubectl"}
}

// NewDemo returns a client that serves a synthetic cluster (no API access).
// Used by `tankertop --demo` for screenshots, demos and trying it out offline.
func NewDemo() *Client {
	return &Client{Context: "demo", demo: true}
}

// New builds a client from an explicit kubeconfig path, $KUBECONFIG, ~/.kube/config,
// or, failing all of those, the in-cluster service account.
func New(kubeconfigPath, kubectlOverride string) (*Client, error) {
	cfg, ctxName, err := loadConfig(kubeconfigPath)
	if err != nil {
		return nil, err
	}
	c, err := newFromConfig(cfg, ctxName)
	if err != nil {
		return nil, err
	}
	c.kubectlCmd = detectKubectlLocal(kubectlOverride)
	return c, nil
}

// detectKubectlLocal picks a kubectl invocation on this machine: an explicit
// override, else plain kubectl when it is on PATH, else microk8s' bundled one.
func detectKubectlLocal(override string) []string {
	if override != "" {
		return strings.Fields(override)
	}
	if _, err := exec.LookPath("kubectl"); err == nil {
		return []string{"kubectl"}
	}
	if _, err := exec.LookPath("microk8s"); err == nil {
		return []string{"microk8s", "kubectl"}
	}
	return []string{"kubectl"}
}

// newFromConfig builds the clientsets. Shared by the local and ssh paths so both
// get the same QPS limits and warning handling.
func newFromConfig(cfg *rest.Config, ctxName string) (*Client, error) {
	cfg.QPS = 50
	cfg.Burst = 100
	// Silence server-side deprecation warnings; they would corrupt the TUI.
	cfg.WarningHandler = rest.NoWarnings{}

	cs, err := kubernetes.NewForConfig(cfg)
	if err != nil {
		return nil, err
	}
	mc, err := metricsv.NewForConfig(cfg)
	if err != nil {
		return nil, err
	}
	return &Client{Clientset: cs, Metrics: mc, Context: ctxName, cfg: cfg}, nil
}

func loadConfig(path string) (*rest.Config, string, error) {
	rules := clientcmd.NewDefaultClientConfigLoadingRules()
	if path != "" {
		rules.ExplicitPath = path
	}
	cc := clientcmd.NewNonInteractiveDeferredLoadingClientConfig(rules, &clientcmd.ConfigOverrides{})

	cfg, err := cc.ClientConfig()
	if err != nil {
		if ic, ierr := rest.InClusterConfig(); ierr == nil {
			return ic, "in-cluster", nil
		}
		return nil, "", err
	}
	ctxName := "default"
	if raw, rerr := cc.RawConfig(); rerr == nil && raw.CurrentContext != "" {
		ctxName = raw.CurrentContext
	}
	return cfg, ctxName, nil
}

// Logs returns the last `lines` log lines of a container in a pod. When
// previous is true it returns logs from the previous terminated instance
// (useful for diagnosing CrashLoopBackOff).
func (c *Client) Logs(ctx context.Context, namespace, pod, container string, lines int64, previous bool) (string, error) {
	if c.demo {
		return demoLogs(pod), nil
	}
	if c.docker {
		// Docker keeps a container's whole history under one id, so there is no
		// separate "previous" stream to fetch.
		return c.dockerLogs(ctx, pod, lines)
	}
	req := c.Clientset.CoreV1().Pods(namespace).GetLogs(pod, &corev1.PodLogOptions{
		Container: container,
		TailLines: &lines,
		Previous:  previous,
	})
	stream, err := req.Stream(ctx)
	if err != nil {
		return "", err
	}
	defer stream.Close()
	data, err := io.ReadAll(stream)
	if err != nil {
		return "", err
	}
	return string(data), nil
}

// Exec runs a non-interactive command in a container and returns combined
// stdout+stderr. Used for quick probes (env, ps, df, ls, …).
func (c *Client) Exec(ctx context.Context, namespace, pod, container string, command []string) (string, error) {
	if c.demo {
		return demoInspect(pod), nil
	}
	if c.docker {
		return c.dockerExec(ctx, pod, command)
	}
	req := c.Clientset.CoreV1().RESTClient().Post().
		Resource("pods").Name(pod).Namespace(namespace).SubResource("exec").
		VersionedParams(&corev1.PodExecOptions{
			Container: container,
			Command:   command,
			Stdout:    true,
			Stderr:    true,
		}, scheme.ParameterCodec)

	exec, err := remotecommand.NewSPDYExecutor(c.cfg, "POST", req.URL())
	if err != nil {
		return "", err
	}
	var stdout, stderr bytes.Buffer
	err = exec.StreamWithContext(ctx, remotecommand.StreamOptions{Stdout: &stdout, Stderr: &stderr})
	out := stdout.String()
	if s := stderr.String(); s != "" {
		out += s
	}
	return out, err
}

// RuntimeEnv returns the environment as the container's PID 1 actually sees it:
// the spec's env plus whatever the kubelet and the image injected. Needs a shell
// in the image, so it fails on distroless — callers fall back to the spec env.
func (c *Client) RuntimeEnv(ctx context.Context, namespace, pod, container string) (string, error) {
	if c.demo {
		return demoEnv(pod), nil
	}
	if c.docker {
		return c.dockerRuntimeEnv(ctx, pod)
	}
	return c.Exec(ctx, namespace, pod, container, []string{"sh", "-c", "env | sort"})
}

// PodYAML returns the pod manifest as YAML (with noisy managedFields stripped).
func (c *Client) PodYAML(ctx context.Context, namespace, name string) (string, error) {
	if c.demo {
		return demoYAML(namespace, name), nil
	}
	if c.docker {
		return c.dockerYAML(ctx, name)
	}
	p, err := c.Clientset.CoreV1().Pods(namespace).Get(ctx, name, metav1.GetOptions{})
	if err != nil {
		return "", err
	}
	p.ManagedFields = nil
	p.APIVersion, p.Kind = "v1", "Pod"
	b, err := yaml.Marshal(p)
	if err != nil {
		return "", err
	}
	return string(b), nil
}

// PodEvents returns recent events for a pod, newest last, formatted for display.
func (c *Client) PodEvents(ctx context.Context, namespace, name string) ([]string, error) {
	if c.demo {
		return demoPodEvents(name), nil
	}
	if c.docker {
		return c.dockerEvents(ctx, name)
	}
	list, err := c.Clientset.CoreV1().Events(namespace).List(ctx, metav1.ListOptions{
		FieldSelector: "involvedObject.name=" + name,
	})
	if err != nil {
		return nil, err
	}
	out := make([]string, 0, len(list.Items))
	for _, e := range list.Items {
		count := ""
		if e.Count > 1 {
			count = "x" + itoa(int(e.Count)) + " "
		}
		out = append(out, strings.TrimSpace(e.Type+"  "+e.Reason+"  "+count+e.Message))
	}
	return out, nil
}

func itoa(n int) string {
	if n == 0 {
		return "0"
	}
	var b [20]byte
	i := len(b)
	for n > 0 {
		i--
		b[i] = byte('0' + n%10)
		n /= 10
	}
	return string(b[i:])
}
