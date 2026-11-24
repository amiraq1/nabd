//go:build linux && !android

package sandbox

import (
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestLoaderBindsReturnsInterpreterAndLibraries(t *testing.T) {
	binds, err := LoaderBinds("/bin/true")
	if err != nil {
		t.Fatal(err)
	}
	if len(binds) < 2 {
		t.Fatalf("expected a loader and at least one library, got %v", binds)
	}
	for _, bind := range binds {
		if !filepath.IsAbs(bind) {
			t.Errorf("bind %q is not absolute", bind)
		}
		info, err := os.Stat(bind)
		if err != nil || !info.Mode().IsRegular() {
			t.Errorf("bind %q is not a readable regular file: %v", bind, err)
		}
	}
	if filepath.Base(binds[0]) == filepath.Base(binds[1]) {
		t.Fatalf("duplicate binds: %v", binds)
	}
}

// A static executable needs no host loader, so the bind list must stay empty
// rather than exposing system directories opportunistically.
func TestLoaderBindsStaticBinaryNeedsNothing(t *testing.T) {
	static := buildHelper(t, t.TempDir(), "nabd-limit")
	binds, err := LoaderBinds(static)
	if err != nil {
		t.Fatal(err)
	}
	if len(binds) != 0 {
		t.Fatalf("static binary must need no loader binds, got %v", binds)
	}
}

func TestLoaderBindsIgnoresNonELF(t *testing.T) {
	path := filepath.Join(t.TempDir(), "script")
	if err := os.WriteFile(path, []byte("#!/bin/sh\necho hi\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	binds, err := LoaderBinds(path)
	if err != nil {
		t.Fatal(err)
	}
	if len(binds) != 0 {
		t.Fatalf("non-ELF file must need no loader binds, got %v", binds)
	}
}

func TestLoaderBindsRejectsMissingTarget(t *testing.T) {
	if _, err := LoaderBinds(filepath.Join(t.TempDir(), "absent")); err == nil {
		t.Fatal("a missing executable must be reported, not treated as loader-free")
	}
}

// TestLinuxSandboxRunsDynamicallyLinkedTarget closes the gap this batch is
// about: before the loader binds existed, a dynamically linked binary could
// not start inside the sandbox at all because the root has no loader or libs.
func TestLinuxSandboxRunsDynamicallyLinkedTarget(t *testing.T) {
	b := requireBwrap(t)
	setupHelpers(t)
	dynamic := "/bin/true"
	if _, err := os.Stat(dynamic); err != nil {
		t.Skip("/bin/true is unavailable on this host")
	}
	binds, err := LoaderBinds(dynamic)
	if err != nil {
		t.Fatal(err)
	}
	if len(binds) == 0 {
		t.Skip("/bin/true is statically linked on this host")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 40*time.Second)
	defer cancel()
	cmd, err := b.Wrap(ctx, Profile{
		WorkDir:            t.TempDir(),
		ExtraReadonlyBinds: binds,
		Env:                []string{"PATH=/usr/bin:/bin"},
		MemoryLimitBytes:   1 << 30,
		ProcessLimit:       32,
	}, dynamic)
	if err != nil {
		t.Fatal(err)
	}
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("dynamically linked target failed inside the sandbox: %v: %s", err, out)
	}
}
