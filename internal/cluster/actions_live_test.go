package cluster

import (
	"context"
	"os"
	"testing"
	"time"
)

// TANKERTOP_LIVE=1 go test ./internal/cluster -run Actions -v
func TestActionsLive(t *testing.T) {
	if os.Getenv("TANKERTOP_LIVE") == "" {
		t.Skip("set TANKERTOP_LIVE=1 to run against the real cluster")
	}
	c, err := New("", "")
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()

	snap := c.Collect(ctx, "demo")
	var web PodInfo
	for _, p := range snap.Pods {
		if p.OwnerKind == "ReplicaSet" && len(p.Name) >= 3 && p.Name[:3] == "web" {
			web = p
			break
		}
	}
	if web.Name == "" {
		t.Skip("no web-* pod in demo namespace")
	}

	st, ok := c.ScaleInfo(ctx, web)
	if !ok {
		t.Fatalf("ScaleInfo failed to resolve Deployment for %s (owner %s/%s)", web.Name, web.OwnerKind, web.OwnerName)
	}
	t.Logf("resolved deployment %s/%s replicas=%d", st.Namespace, st.Name, st.Replicas)

	orig := st.Replicas
	if err := c.Scale(ctx, st.Namespace, st.Name, orig+1); err != nil {
		t.Fatalf("scale up: %v", err)
	}
	if err := c.Scale(ctx, st.Namespace, st.Name, orig); err != nil {
		t.Fatalf("scale back: %v", err)
	}

	note, err := c.RestartWorkload(ctx, web)
	if err != nil {
		t.Fatalf("restart: %v", err)
	}
	t.Logf("restart note: %s", note)
}
