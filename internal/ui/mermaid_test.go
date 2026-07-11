package ui

import (
	"strings"
	"testing"

	"github.com/tpenzkofer/kubeview/internal/cluster"
)

func TestMermaidID(t *testing.T) {
	cases := map[string]string{
		"kvtest_web":                 "kvtest_web",
		"04-guacamole-keycloak_data": "n04_guacamole_keycloak_data", // digit-leading -> prefixed
		"a.b/c":                      "a_b_c",
	}
	for in, want := range cases {
		if got := mermaidID(in); got != want {
			t.Errorf("mermaidID(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestMermaidNetworkStructure(t *testing.T) {
	nets := []cluster.DockerNetwork{
		{Name: "kvnet", Driver: "bridge", Containers: []cluster.DockerNetEndpoint{
			{Name: "web", IPv4: "172.18.0.2"}, {Name: "db", IPv4: "172.18.0.3"},
		}},
		{Name: "empty", Driver: "bridge"}, // no containers -> omitted
	}
	got := strings.Join(mermaidNetwork(nets), "\n")
	for _, want := range []string{"graph LR", `subgraph net_kvnet["kvnet"]`, `web["web"]`, `db["db"]`, "end"} {
		if !strings.Contains(got, want) {
			t.Errorf("mermaid output missing %q:\n%s", want, got)
		}
	}
	if strings.Contains(got, "empty") {
		t.Errorf("empty network should be omitted:\n%s", got)
	}
}
