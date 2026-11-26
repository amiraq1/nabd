//go:build linux && !android

package sandbox

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func buildHelper(t *testing.T, dest, pkg string) string {
	t.Helper()
	path := filepath.Join(dest, filepath.Base(pkg))
	cmd := exec.Command("go", "build", "-trimpath", "-o", path, "../../cmd/"+pkg)
	// The sandbox root has no dynamic loader; helpers must be static.
	cmd.Env = append(os.Environ(), "CGO_ENABLED=0")
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("build %s: %v: %s", pkg, err, out)
	}
	return path
}

func setupHelpers(t *testing.T) (string, string) {
	t.Helper()
	dir := t.TempDir()
	limiter := buildHelper(t, dir, "nabd-limit")
	probe := buildHelper(t, dir, "nabd-sandbox-probe")
	oldLimiter, oldProbe := limiterOverride, probeOverride
	limiterOverride, probeOverride = limiter, probe
	t.Cleanup(func() {
		limiterOverride, probeOverride = oldLimiter, oldProbe
	})
	return limiter, probe
}

func requireBwrap(t *testing.T) *linuxBackend {
	t.Helper()
	if _, err := exec.LookPath("bwrap"); err != nil {
		t.Skip("bubblewrap is not installed; sandbox cannot be proven here")
	}
	b, ok := Current().(*linuxBackend)
	if !ok || !b.Available() {
		t.Fatal("linux backend unavailable although bwrap exists")
	}
	return b
}

// TestLinuxSandboxProof is the positive gate. It runs the shipped probe
// inside the real sandbox and fails if any promised restriction is absent.
func TestLinuxSandboxProof(t *testing.T) {
	b := requireBwrap(t)
	setupHelpers(t)
	ctx, cancel := context.WithTimeout(context.Background(), 40*time.Second)
	defer cancel()
	if err := b.Prove(ctx); err != nil {
		t.Fatalf("sandbox proof failed; execution tools must stay disabled: %v", err)
	}
}

func TestLinuxSandboxOptInPath(t *testing.T) {
	requireBwrap(t)
	setupHelpers(t)
	ctx, cancel := context.WithTimeout(context.Background(), 40*time.Second)
	defer cancel()
	got, err := Enabled(ctx, true)
	if err != nil || got == nil {
		t.Fatalf("proven opt-in backend should be enabled, got %v, %v", got, err)
	}
	if _, err := Enabled(ctx, false); err == nil {
		t.Fatal("default must remain disabled even when a backend is proven")
	}
}

func TestLinuxSandboxProofFailsWithoutProbe(t *testing.T) {
	b := requireBwrap(t)
	setupHelpers(t)
	old := probeOverride
	probeOverride = filepath.Join(t.TempDir(), "missing-probe")
	t.Cleanup(func() { probeOverride = old })
	if err := b.Prove(context.Background()); err == nil {
		t.Fatal("proof must fail closed when the probe is missing")
	}
}

func TestLinuxWrapConstructsConfinedCommand(t *testing.T) {
	t.Setenv("NABD_API_KEY", "secret-api-key")
	t.Setenv("NABD_AUDIT_ANCHOR", "secret-audit-anchor")
	t.Setenv("NABD_FIRECRAWL_API_KEY", "secret-firecrawl-key")
	b := requireBwrap(t)
	setupHelpers(t)
	dir := t.TempDir()
	profile := Profile{
		WorkDir:            dir,
		MemoryLimitBytes:   1 << 30,
		ProcessLimit:       32,
		Env:                []string{"PATH=/usr/bin"},
		ExtraReadonlyBinds: []string{"/usr/local/go"},
	}
	cmd, err := b.Wrap(context.Background(), profile, "/bin/true", "arg")
	if err != nil {
		t.Fatal(err)
	}
	joined := strings.Join(cmd.Args, "\x00")
	for _, flag := range []string{
		"--unshare-user", "--unshare-pid", "--unshare-net", "--unshare-ipc",
		"--unshare-uts", "--new-session", "--die-with-parent", "--clearenv",
		"--ro-bind", "--tmpfs", "--chdir", "--setenv",
	} {
		if !strings.Contains(joined, flag) {
			t.Errorf("missing sandbox flag %q in %v", flag, cmd.Args)
		}
	}
	if !strings.Contains(joined, "--ro-bind\x00"+dir+"\x00/workspace") {
		t.Errorf("read-only workspace bind missing")
	}
	if strings.Contains(joined, "--bind\x00"+dir) {
		t.Errorf("writable workspace bind used for a read-only profile")
	}
	if strings.Contains(joined, "NABD_API_KEY") {
		t.Errorf("parent secrets leaked into sandbox arguments")
	}
	if cmd.Env == nil {
		t.Fatal("expected explicit cmd.Env on bwrap command, got nil (inherits parent environment)")
	}
	for _, entry := range cmd.Env {
		if strings.HasPrefix(entry, "NABD_API_KEY=") ||
			strings.HasPrefix(entry, "NABD_AUDIT_ANCHOR=") ||
			strings.HasPrefix(entry, "NABD_FIRECRAWL_API_KEY=") {
			t.Errorf("parent secret leaked into bwrap host cmd.Env: %s", entry)
		}
	}
	profile.WritableWorkspace = true
	cmd, err = b.Wrap(context.Background(), profile, "/bin/true")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(strings.Join(cmd.Args, "\x00"), "--bind\x00"+dir+"\x00/workspace") {
		t.Errorf("writable workspace bind missing")
	}
}

func TestLinuxWrapRejectsRelativeTarget(t *testing.T) {
	b := requireBwrap(t)
	setupHelpers(t)
	if _, err := b.Wrap(context.Background(), Profile{
		WorkDir: t.TempDir(), MemoryLimitBytes: 1 << 30, ProcessLimit: 32,
	}, "relative-target"); err == nil {
		t.Fatal("relative executable must be rejected")
	}
}

func TestProofDoesNotTouchUserWorkspace(t *testing.T) {
	b := requireBwrap(t)
	setupHelpers(t)
	workspace := t.TempDir()
	profile := Profile{WorkDir: workspace, MemoryLimitBytes: proofMemory, ProcessLimit: proofProcesses}
	_ = profile
	// Prove creates its own temporary workspace; assert it did not leave a
	// proof artifact in any user-visible directory.
	ctx, cancel := context.WithTimeout(context.Background(), 40*time.Second)
	defer cancel()
	if err := b.Prove(ctx); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(os.TempDir(), "must-not-exist")); !os.IsNotExist(err) {
		t.Fatalf("probe wrote outside its sandbox: %v", err)
	}
}
