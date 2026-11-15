package tools

import (
	"path/filepath"
	"strings"
	"testing"
)

func TestIntegrationSensitiveEnvComponents(t *testing.T) {
	cases := []struct {
		path   string
		denied bool
	}{
		{".env", true}, {".envrc", true}, {".ENV-production", true}, {"nested/.EnvLocal", true},
		{".env-private/other.txt", true}, {".env.example/nested.txt", true},
		{"nested/.env.example", false}, {".env.sample", false}, {".env.template", false}, {".env.dist", false},
		{"environment.txt", false},
	}
	for _, tc := range cases {
		t.Run(tc.path, func(t *testing.T) {
			if isSensitivePath(tc.path) != tc.denied {
				t.Errorf("sensitive path policy disagrees for %q", tc.path)
			}
		})
	}
}
func TestIntegrationSensitiveEnvToolPaths(t *testing.T) {
	root, r := newTestRegistry(t)
	for _, path := range []string{".envrc", ".env-production", ".env-private/config.txt", ".env.example"} {
		writeTestFile(t, filepath.Join(root, path), "fixture")
	}
	for _, path := range []string{".envrc", ".env-production", ".env-private/config.txt"} {
		out := runTool(t, r, "read_file", map[string]any{"path": path})
		if !strings.Contains(out, "sensitive path denied") {
			t.Errorf("read_file did not deny %s", path)
		}
		if _, err := r.FileDigest(path); err == nil {
			t.Errorf("digest did not deny %s", path)
		}
	}
	out := runTool(t, r, "list_dir", map[string]any{"path": "."})
	for _, name := range listedNames(t, out) {
		if name == ".envrc" || name == ".env-production" || name == ".env-private" {
			t.Errorf("sensitive entry visible: %s", name)
		}
	}
	if got := runTool(t, r, "read_file", map[string]any{"path": ".env.example"}); got != "fixture" {
		t.Error("template exception unavailable")
	}
}
