//go:build unix

package main

import (
	"context"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"
)

func buildClipboardStubBinary(t *testing.T, dir, name string) string {
	t.Helper()
	srcDir := t.TempDir()
	srcFile := filepath.Join(srcDir, "main.go")
	// Reads mode from TMPDIR/stub_mode and writes outputs to TMPDIR/stub_env and TMPDIR/stub_pid
	// so tests work strictly through the allowlisted environment (TMPDIR is allowed across Unix platforms).
	src := `package main

import (
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"time"
)

func main() {
	if len(os.Args) > 1 && os.Args[1] == "--child" {
		time.Sleep(15 * time.Second)
		return
	}
	tmpDir := os.Getenv("TMPDIR")
	modeBytes, _ := os.ReadFile(filepath.Join(tmpDir, "stub_mode"))
	mode := strings.TrimSpace(string(modeBytes))

	switch mode {
	case "capture":
		var keys []string
		for _, kv := range os.Environ() {
			if k, _, ok := strings.Cut(kv, "="); ok {
				keys = append(keys, k)
			}
		}
		_ = os.WriteFile(filepath.Join(tmpDir, "stub_env_keys"), []byte(strings.Join(keys, "\n")), 0o600)
		data, _ := io.ReadAll(os.Stdin)
		_ = os.WriteFile(filepath.Join(tmpDir, "stub_stdin"), data, 0o600)
	case "hang_with_child":
		cmd := exec.Command(os.Args[0], "--child")
		cmd.Stdin = os.Stdin
		cmd.Stdout = os.Stdout
		cmd.Stderr = os.Stderr
		if err := cmd.Start(); err != nil {
			os.Exit(2)
		}
		_ = os.WriteFile(filepath.Join(tmpDir, "stub_pid"), []byte(strconv.Itoa(cmd.Process.Pid)), 0o600)
		time.Sleep(15 * time.Second)
	case "exit0_child_keeps_stdin":
		cmd := exec.Command(os.Args[0], "--child")
		cmd.Stdin = os.Stdin
		cmd.Stdout = os.Stdout
		cmd.Stderr = os.Stderr
		if err := cmd.Start(); err != nil {
			os.Exit(2)
		}
		_ = os.WriteFile(filepath.Join(tmpDir, "stub_pid"), []byte(strconv.Itoa(cmd.Process.Pid)), 0o600)
		os.Exit(0)
	case "exit0_no_read":
		os.Exit(0)
	}
}
`
	if err := os.WriteFile(srcFile, []byte(src), 0o600); err != nil {
		t.Fatal(err)
	}
	outPath := filepath.Join(dir, name)
	buildCmd := exec.Command("go", "build", "-o", outPath, srcFile)
	if out, err := buildCmd.CombinedOutput(); err != nil {
		t.Fatalf("build clipboard stub: %v\n%s", err, out)
	}
	return outPath
}

func nativeClipboardStubSpec(t *testing.T) (prog string, args []string) {
	t.Helper()
	t.Setenv("SSH_CLIENT", "")
	t.Setenv("SSH_TTY", "")
	t.Setenv("SSH_CONNECTION", "")
	if runtime.GOOS == "android" || os.Getenv("TERMUX_VERSION") != "" || strings.Contains(os.Getenv("PREFIX"), "com.termux") {
		return "termux-clipboard-set", nil
	}
	switch runtime.GOOS {
	case "linux":
		t.Setenv("WAYLAND_DISPLAY", "wayland-test")
		return "wl-copy", nil
	case "darwin":
		return "pbcopy", nil
	default:
		t.Skipf("unsupported unix GOOS %s for native stub spec", runtime.GOOS)
		return "", nil
	}
}

func isUnixProcessAlive(pid int) bool {
	if pid <= 0 {
		return false
	}
	err := syscall.Kill(pid, 0)
	return err == nil || err == syscall.EPERM
}

func TestClipboardDefaultCmdRunnerEnvIsolation(t *testing.T) {
	binDir := t.TempDir()
	workTmp := t.TempDir()
	prog, args := nativeClipboardStubSpec(t)
	buildClipboardStubBinary(t, binDir, prog)

	if err := os.WriteFile(filepath.Join(workTmp, "stub_mode"), []byte("capture"), 0o600); err != nil {
		t.Fatal(err)
	}

	t.Setenv("PATH", binDir+":"+os.Getenv("PATH"))
	t.Setenv("TMPDIR", workTmp)
	t.Setenv("LANG", "C.UTF-8")
	t.Setenv("NABD_API_KEY", "dummy-api-key")
	t.Setenv("NABD_AUDIT_ANCHOR", "dummy-anchor")
	t.Setenv("NABD_FIRECRAWL_API_KEY", "dummy-firecrawl")
	t.Setenv("CUSTOM_SECRET", "dummy-custom-secret")
	t.Setenv("SERVICE_TOKEN", "dummy-service-token")
	t.Setenv("TERMUX_SECRET", "dummy-termux-secret")
	t.Setenv("ANDROID_TOKEN", "dummy-android-token")

	wantText := "مرحباً بالاختبار 🚀"
	if err := defaultCmdRunner(context.Background(), prog, args, wantText); err != nil {
		t.Fatalf("defaultCmdRunner failed: %v", err)
	}

	envData, err := os.ReadFile(filepath.Join(workTmp, "stub_env_keys"))
	if err != nil {
		t.Fatal(err)
	}
	envKeys := make(map[string]bool)
	for _, line := range strings.Split(strings.TrimSpace(string(envData)), "\n") {
		if line != "" {
			envKeys[line] = true
		}
	}
	for _, secretKey := range []string{
		"NABD_API_KEY",
		"NABD_AUDIT_ANCHOR",
		"NABD_FIRECRAWL_API_KEY",
		"CUSTOM_SECRET",
		"SERVICE_TOKEN",
		"TERMUX_SECRET",
		"ANDROID_TOKEN",
	} {
		if envKeys[secretKey] {
			t.Fatalf("secret key %q leaked into subprocess environment (keys=%d)", secretKey, len(envKeys))
		}
	}
	if !envKeys["PATH"] || !envKeys["TMPDIR"] {
		t.Fatalf("expected PATH and TMPDIR in allowlisted child env (got keys: %v)", envKeys)
	}

	stdinData, err := os.ReadFile(filepath.Join(workTmp, "stub_stdin"))
	if err != nil {
		t.Fatal(err)
	}
	if string(stdinData) != wantText {
		t.Fatalf("stdin mismatch: got %q, want %q", string(stdinData), wantText)
	}
}

func TestClipboardDefaultCmdRunnerHangAndForkCleanup(t *testing.T) {
	binDir := t.TempDir()
	workTmp := t.TempDir()
	prog, _ := nativeClipboardStubSpec(t)
	buildClipboardStubBinary(t, binDir, prog)

	t.Setenv("PATH", binDir+":"+os.Getenv("PATH"))
	t.Setenv("TMPDIR", workTmp)

	cleanupChild := func() {
		pidBytes, err := os.ReadFile(filepath.Join(workTmp, "stub_pid"))
		if err != nil {
			return
		}
		if pid, err := strconv.Atoi(strings.TrimSpace(string(pidBytes))); err == nil && pid > 0 {
			_ = syscall.Kill(pid, syscall.SIGKILL)
		}
		_ = os.Remove(filepath.Join(workTmp, "stub_pid"))
	}
	t.Cleanup(cleanupChild)

	// 1. Hanging parent + forked grandchild keeping pipes open:
	// copyToClipboard must return explicit timeout error within timeout + WaitDelay and leave 0 orphans.
	if err := os.WriteFile(filepath.Join(workTmp, "stub_mode"), []byte("hang_with_child"), 0o600); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 350*time.Millisecond)
	defer cancel()

	start := time.Now()
	_, err := copyToClipboard(ctx, "hang payload", detectSystemEnv(), nil)
	elapsed := time.Since(start)
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("expected context.DeadlineExceeded, got %v", err)
	}
	if elapsed > 1500*time.Millisecond {
		t.Fatalf("hang_with_child exceeded timeout + WaitDelay bound: %v", elapsed)
	}
	if pidBytes, rErr := os.ReadFile(filepath.Join(workTmp, "stub_pid")); rErr == nil {
		if pid, pErr := strconv.Atoi(strings.TrimSpace(string(pidBytes))); pErr == nil {
			// Give kernel brief moment to reap SIGKILLed process group
			time.Sleep(50 * time.Millisecond)
			if isUnixProcessAlive(pid) {
				t.Fatalf("orphan grandchild pid %d survived process group kill", pid)
			}
		}
	}
	cleanupChild()

	// 2. Parent exits 0 immediately while grandchild keeps fd 0 open with 128 KiB (> 64 KiB pipe buffer):
	// WaitDelay (500ms) must trigger explicit error (never nil) and kill the orphan grandchild.
	if err := os.WriteFile(filepath.Join(workTmp, "stub_mode"), []byte("exit0_child_keeps_stdin"), 0o600); err != nil {
		t.Fatal(err)
	}
	large := strings.Repeat("A", 128*1024)
	start = time.Now()
	_, err = copyToClipboard(context.Background(), large, detectSystemEnv(), nil)
	elapsed = time.Since(start)
	if !errors.Is(err, exec.ErrWaitDelay) {
		t.Fatalf("expected exec.ErrWaitDelay when grandchild holds stdin open, got %v", err)
	}
	if elapsed > 1800*time.Millisecond {
		t.Fatalf("exit0_child_keeps_stdin took too long: %v", elapsed)
	}
	if pidBytes, rErr := os.ReadFile(filepath.Join(workTmp, "stub_pid")); rErr == nil {
		if pid, pErr := strconv.Atoi(strings.TrimSpace(string(pidBytes))); pErr == nil {
			time.Sleep(50 * time.Millisecond)
			if isUnixProcessAlive(pid) {
				t.Fatalf("orphan grandchild pid %d survived WaitDelay cleanup", pid)
			}
		}
	}
	cleanupChild()

	// 3. Early exit 0 without reading 256 KiB stdin (EPIPE handled gracefully)
	if err := os.WriteFile(filepath.Join(workTmp, "stub_mode"), []byte("exit0_no_read"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := defaultCmdRunner(context.Background(), prog, nil, strings.Repeat("Z", maxClipboardSize)); err != nil {
		t.Fatalf("expected early exit 0 EPIPE to be handled cleanly, got %v", err)
	}
}
