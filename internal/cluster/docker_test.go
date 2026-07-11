package cluster

import "testing"

func TestParseSize(t *testing.T) {
	// Variables (not constants) so the fractional products evaluate at runtime,
	// exactly as parseSize does — a float constant with a fraction cannot be
	// converted to int64 at compile time.
	miB, giB := float64(1<<20), float64(1<<30)
	cases := []struct {
		in   string
		want int64
	}{
		{"332KiB", 332 * 1024},
		{"1.559MiB", int64(1.559 * miB)},
		{"10.79MiB", int64(10.79 * miB)},
		{"31.28GiB", int64(31.28 * giB)},
		{"512MB", 512 * 1e6},
		{"1.5GB", int64(1.5 * 1e9)},
		{"0B", 0},
		{"garbage", 0},
	}
	for _, c := range cases {
		if got := parseSize(c.in); got != c.want {
			t.Errorf("parseSize(%q) = %d, want %d", c.in, got, c.want)
		}
	}
}

func TestParseCPUPerc(t *testing.T) {
	// docker % is per-core×100, so 100% == 1000 milli, 250% == 2500 milli.
	cases := map[string]int64{"0.00%": 0, "12.50%": 125, "100%": 1000, "250.0%": 2500, "": 0}
	for in, want := range cases {
		if got := parseCPUPerc(in); got != want {
			t.Errorf("parseCPUPerc(%q) = %d, want %d", in, got, want)
		}
	}
}

func TestParsePortProto(t *testing.T) {
	cases := map[string]int32{"80/tcp": 80, "5432/tcp": 5432, "53/udp": 53, "": 0}
	for in, want := range cases {
		if got := parsePortProto(in); got != want {
			t.Errorf("parsePortProto(%q) = %d, want %d", in, got, want)
		}
	}
}

func TestStatByIDMatchesShortStatsID(t *testing.T) {
	full := "af742d958027c045f1e8b6196e9eb3ac88b00aaba5f6af4dec8a08ea27a39fca"
	stats := map[string]dockerStat{"af742d958027": {ID: "af742d958027", CPUPerc: "1.00%"}}
	if s, ok := statByID(stats, full); !ok || s.CPUPerc != "1.00%" {
		t.Fatalf("statByID did not match the short id: %v %v", s, ok)
	}
	if _, ok := statByID(stats, "0000deadbeef00000000"); ok {
		t.Fatal("statByID matched an unrelated id")
	}
}

func TestDockerStatus(t *testing.T) {
	mk := func(state string, code int, health string) *dockerInspect {
		d := &dockerInspect{}
		d.State.Status = state
		d.State.ExitCode = code
		if health != "" {
			d.State.Health = &struct {
				Status string `json:"Status"`
			}{Status: health}
		}
		return d
	}
	cases := []struct {
		d          *dockerInspect
		wantStatus string
		wantReady  int
	}{
		{mk("running", 0, ""), "Running", 1},
		{mk("running", 0, "healthy"), "Running", 1},
		{mk("running", 0, "unhealthy"), "Unhealthy", 0},
		{mk("running", 0, "starting"), "Starting", 0},
		{mk("exited", 0, ""), "Completed", 0},
		{mk("exited", 143, ""), "Exited(143)", 0},
		{mk("restarting", 0, ""), "Restarting", 0},
		{mk("paused", 0, ""), "Paused", 0},
	}
	for _, c := range cases {
		gotS, gotR := dockerStatus(c.d)
		if gotS != c.wantStatus || gotR != c.wantReady {
			t.Errorf("dockerStatus(%s/%d/%v) = %q,%d want %q,%d",
				c.d.State.Status, c.d.State.ExitCode, c.d.State.Health, gotS, gotR, c.wantStatus, c.wantReady)
		}
	}
}

func TestContainerToPodMapsComposeLabels(t *testing.T) {
	d := &dockerInspect{
		ID:      "abc123def456aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa",
		Name:    "/kvtest_db",
		Created: "2026-07-11T10:00:00Z",
	}
	d.State.Status = "running"
	d.Config.Labels = map[string]string{
		"com.docker.compose.project": "kvtest",
		"com.docker.compose.service": "db",
	}
	d.Config.Image = "postgres:16"
	d.Config.Env = []string{"POSTGRES_PASSWORD=secret", "LOG_LEVEL=info"}
	d.HostConfig.Memory = 256 << 20
	d.HostConfig.NanoCpus = 1_500_000_000 // 1.5 cores

	stats := map[string]dockerStat{"abc123def456": {ID: "abc123def456", CPUPerc: "5.00%", MemUsage: "1.559MiB / 256MiB"}}
	p := containerToPod(d, stats, "racoon-tre")

	if p.Namespace != "kvtest" || p.Controller != "compose/db" {
		t.Errorf("compose mapping: ns=%q controller=%q", p.Namespace, p.Controller)
	}
	if p.Name != "kvtest_db" {
		t.Errorf("name = %q, want kvtest_db (leading slash stripped)", p.Name)
	}
	miB := float64(1 << 20)
	if p.CPUMilli != 50 || p.MemBytes != int64(1.559*miB) {
		t.Errorf("stats: cpu=%d mem=%d", p.CPUMilli, p.MemBytes)
	}
	if p.CPULimMilli != 1500 || p.MemLimBytes != 256<<20 {
		t.Errorf("limits: cpu=%d mem=%d", p.CPULimMilli, p.MemLimBytes)
	}
	if len(p.Containers) != 1 || len(p.Containers[0].Env) != 2 || p.Containers[0].Env[0].Name != "POSTGRES_PASSWORD" {
		t.Errorf("env not mapped: %+v", p.Containers)
	}
}

func TestContainerToPodStandalone(t *testing.T) {
	d := &dockerInspect{Name: "/loner", Created: "2026-07-11T10:00:00Z"}
	d.State.Status = "running"
	p := containerToPod(d, nil, "host")
	if p.Namespace != "(no project)" || p.Controller != "(standalone)" {
		t.Errorf("standalone mapping: ns=%q controller=%q", p.Namespace, p.Controller)
	}
}
