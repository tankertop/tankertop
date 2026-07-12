package cluster

import (
	"context"
	"os"
	"strings"
	"testing"
	"time"
)

// Gated live test: TANKERTOP_LIVE=1 go test ./internal/cluster -run Exec -v
func TestExecLive(t *testing.T) {
	if os.Getenv("TANKERTOP_LIVE") == "" {
		t.Skip("set TANKERTOP_LIVE=1 to run against the real cluster")
	}
	c, err := New("", "")
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()

	snap := c.Collect(ctx, "demo")
	var target PodInfo
	for _, p := range snap.Pods {
		if p.Status == "Running" && len(p.Containers) > 0 {
			target = p
			break
		}
	}
	if target.Name == "" {
		t.Skip("no running pod in demo namespace")
	}
	out, err := c.Exec(ctx, target.Namespace, target.Name, target.Containers[0].Name,
		[]string{"sh", "-c", "echo EXEC_OK; id -u"})
	if err != nil {
		t.Fatalf("exec error: %v (out=%q)", err, out)
	}
	if !strings.Contains(out, "EXEC_OK") {
		t.Fatalf("unexpected exec output: %q", out)
	}
	t.Logf("exec into %s/%s ok:\n%s", target.Namespace, target.Name, out)
}
