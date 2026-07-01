package cluster

import (
	"bytes"
	"context"
	"io"
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

	demo     bool
	demoTick int
}

// NewDemo returns a client that serves a synthetic cluster (no API access).
// Used by `kubeview --demo` for screenshots, demos and trying it out offline.
func NewDemo() *Client {
	return &Client{Context: "demo", demo: true}
}

// New builds a client from an explicit kubeconfig path, $KUBECONFIG, ~/.kube/config,
// or, failing all of those, the in-cluster service account.
func New(kubeconfigPath string) (*Client, error) {
	cfg, ctxName, err := loadConfig(kubeconfigPath)
	if err != nil {
		return nil, err
	}
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

// PodYAML returns the pod manifest as YAML (with noisy managedFields stripped).
func (c *Client) PodYAML(ctx context.Context, namespace, name string) (string, error) {
	if c.demo {
		return demoYAML(namespace, name), nil
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
