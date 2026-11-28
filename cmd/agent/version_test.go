package main

import (
	"bytes"
	"os"
	"runtime"
	"strings"
	"testing"
)

func TestPrintVersion(t *testing.T) {
	var buf bytes.Buffer
	if code := printVersion(&buf); code != 0 {
		t.Fatalf("printVersion exit code = %d, want 0", code)
	}
	out := buf.String()
	if !strings.Contains(out, "nabd-agent ") {
		t.Fatalf("missing program name: %q", out)
	}
	if !strings.Contains(out, currentVersionInfo().version) {
		t.Fatalf("missing version: %q", out)
	}
	if !strings.Contains(out, runtime.GOOS+"/"+runtime.GOARCH) {
		t.Fatalf("missing platform: %q", out)
	}
	if !strings.Contains(out, runtime.Version()) {
		t.Fatalf("missing go version: %q", out)
	}
}

func TestVersionInfoUsesBuildInfoWithoutState(t *testing.T) {
	dir := t.TempDir()
	t.Chdir(dir)
	info := currentVersionInfo()
	if info.version == "" {
		t.Fatal("version must never be empty")
	}
	if info.platform != runtime.GOOS+"/"+runtime.GOARCH {
		t.Fatalf("platform = %q", info.platform)
	}
	// printVersion must not create .nabd/ or any other on-disk state.
	var buf bytes.Buffer
	printVersion(&buf)
	if _, err := os.Stat(".nabd"); err == nil {
		t.Fatal("printVersion created .nabd")
	}
}
