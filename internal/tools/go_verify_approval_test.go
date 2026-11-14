package tools

import (
	"context"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

func approvalRegistry(t *testing.T, dir string) *Registry {
	t.Helper()
	r, err := NewRegistry(dir)
	if err != nil {
		t.Fatalf("NewRegistry: %v", err)
	}
	return r
}

func installProbeGo(t *testing.T, script string) {
	t.Helper()
	sh, err := exec.LookPath("sh")
	if err != nil {
		t.Fatalf("sh not found: %v", err)
	}
	dir := t.TempDir()
	path := filepath.Join(dir, "go")
	if err := os.WriteFile(path, []byte("#!"+sh+"\n"+script), 0755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", dir+string(os.PathListSeparator)+os.Getenv("PATH"))
}

func TestGoVerifyNeedsApproval(t *testing.T) {
	r := approvalRegistry(t, t.TempDir())
	if !r.NeedsApproval("go_verify") {
		t.Fatal("go_verify must require approval")
	}
}

func TestGoVerifyRunWithoutApprovalDoesNotExecute(t *testing.T) {
	dir := t.TempDir()
	marker := filepath.Join(dir, "ran.txt")
	t.Setenv("NABD_PROBE_OUT", marker)
	installProbeGo(t, `printf ran > "$NABD_PROBE_OUT"`)

	r := approvalRegistry(t, dir)
	out := r.Run(context.Background(), "go_verify", json.RawMessage(`{"pkg":"./..."}`))
	if !strings.Contains(out, "requires approval") {
		t.Fatalf("unexpected output: %q", out)
	}
	if _, err := os.Stat(marker); err == nil {
		t.Fatal("go test executed without approval")
	}
}

func TestGoVerifyRunApprovedExecutes(t *testing.T) {
	dir := t.TempDir()
	marker := filepath.Join(dir, "ran.txt")
	t.Setenv("NABD_PROBE_OUT", marker)
	installProbeGo(t, `printf ran > "$NABD_PROBE_OUT"`)

	r := approvalRegistry(t, dir)
	out := r.Run(context.Background(), "go_verify", json.RawMessage(`{"pkg":"./..."}`))
	if !strings.Contains(out, "requires approval") {
		t.Fatalf("Run did not fail closed: %q", out)
	}
	if _, err := os.Stat(marker); err == nil {
		t.Fatal("go test executed despite rejection")
	}

	out = r.RunApproved(context.Background(), "go_verify", json.RawMessage(`{"pkg":"./..."}`), ApprovalState{})
	if strings.Contains(out, "Error") {
		t.Fatalf("RunApproved failed: %q", out)
	}
	if _, err := os.Stat(marker); err != nil {
		t.Fatalf("RunApproved did not execute: %v", err)
	}
}
