package ui

import "testing"

func TestLooksSecret(t *testing.T) {
	cases := []struct {
		name, value string
		want        bool
	}{
		{"POSTGRES_PASSWORD", "hunter2", true},
		{"API_TOKEN", "s3cr3t-9f4b21ce", true},
		{"GF_SECURITY_ADMIN_PASSWORD", "admin", true},
		{"APP_SENTRY_DSN", "https://8f2c1e@sentry.example.com/42", true},
		{"SIGNING_KEY", "abc", true},
		{"DATABASE_URL", "postgres://api:hunter2@db.default.svc:5432/app", true},

		{"LOG_LEVEL", "info", false},
		{"POSTGRES_USER", "keycloak", false},
		{"GF_SERVER_ROOT_URL", "https://grafana.example.com", false},
		{"KEYCLOAK_ADMIN", "admin", false},
		{"PATH", "/usr/bin:/bin", false},
		{"MEMORY_LIMIT", "536870912", false},

		// Kubernetes service links: credential-ish names, never credentials.
		{"AUTH_BACKEND_SERVICE_PORT", "5000", false},
		{"AUTH_BACKEND_SERVICE_PORT_5000_TCP", "tcp://10.152.183.7:5000", false},
		{"AUTH_BACKEND_SERVICE_PORT_5000_TCP_ADDR", "10.152.183.7", false},
		{"AUTH_BACKEND_SERVICE_SERVICE_HOST", "10.152.183.7", false},

		{"PASSWORD", "", false}, // unset: nothing to hide
	}
	for _, c := range cases {
		if got := looksSecret(c.name, c.value); got != c.want {
			t.Errorf("looksSecret(%q, %q) = %v, want %v", c.name, c.value, got, c.want)
		}
	}
}
