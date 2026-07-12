package ui

import (
	"encoding/json"
	"os"
	"path/filepath"
	"time"
)

// Config is the small slice of state tankertop remembers between runs.
type Config struct {
	Theme      string `json:"theme"`
	Sort       int    `json:"sort"`
	Tree       bool   `json:"tree"`
	Namespace  string `json:"namespace"`
	IntervalMs int    `json:"intervalMs"`
}

func configPath() string {
	if dir, err := os.UserConfigDir(); err == nil {
		return filepath.Join(dir, "tankertop", "config.json")
	}
	home, _ := os.UserHomeDir()
	return filepath.Join(home, ".tankertop.json")
}

// LoadConfig reads the saved config, falling back to sensible defaults.
func LoadConfig() Config {
	c := Config{Theme: "tokyonight", IntervalMs: 2000}
	if data, err := os.ReadFile(configPath()); err == nil {
		_ = json.Unmarshal(data, &c)
	}
	if c.IntervalMs <= 0 {
		c.IntervalMs = 2000
	}
	if c.Theme == "" {
		c.Theme = "tokyonight"
	}
	return c
}

// WithConfig seeds runtime state (sort/tree) from a saved config.
func (m Model) WithConfig(c Config) Model {
	m.sort = sortMode(c.Sort) % sortModeCount
	if m.sort < 0 {
		m.sort = sortDefault
	}
	m.tree = c.Tree
	return m
}

// saveConfig persists the current UI preferences (best-effort).
func (m Model) saveConfig() {
	c := Config{
		Theme:      currentTheme,
		Sort:       int(m.sort),
		Tree:       m.tree,
		Namespace:  m.namespace,
		IntervalMs: int(m.interval / time.Millisecond),
	}
	data, err := json.MarshalIndent(c, "", "  ")
	if err != nil {
		return
	}
	p := configPath()
	_ = os.MkdirAll(filepath.Dir(p), 0o755)
	_ = os.WriteFile(p, data, 0o644)
}
