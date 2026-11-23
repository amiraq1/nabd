//go:build linux

package sandbox

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"time"
)

const (
	bwrapBinary    = "bwrap"
	limiterBinary  = "nabd-limit"
	probeBinary    = "nabd-sandbox-probe"
	defaultMemory  = int64(2) << 30
	defaultProcs   = 64
	proofMemory    = int64(2) << 30
	proofProcesses = 64
)

// limiterOverride and probeOverride are test seams. No environment variable
// can redirect them: a user-controlled path would let the child replace the
// verifier itself.
var (
	limiterOverride string
	probeOverride   string
	bwrapOverride   string
)

type linuxBackend struct {
	bwrap string
}

func current() Backend {
	path, err := exec.LookPath(bwrapBinary)
	if err != nil {
		return &linuxBackend{}
	}
	if resolved, err := filepath.EvalSymlinks(path); err == nil {
		path = resolved
	}
	return &linuxBackend{bwrap: path}
}

func (b *linuxBackend) Name() string { return "linux-user-namespace+bubblewrap" }

func (b *linuxBackend) Available() bool {
	return b != nil && b.bwrap != ""
}

func siblingBinary(name string) (string, error) {
	exe, err := os.Executable()
	if err != nil {
		return "", err
	}
	path := filepath.Join(filepath.Dir(exe), name)
	info, err := os.Stat(path)
	if err != nil || !info.Mode().IsRegular() || info.Mode().Perm()&0111 == 0 {
		return "", fmt.Errorf("isolation helper %s is missing or not executable next to the agent", name)
	}
	return path, nil
}

func (b *linuxBackend) Wrap(ctx context.Context, p Profile, executable string, args ...string) (*exec.Cmd, error) {
	if b == nil || !b.Available() {
		return nil, ErrUnavailable
	}
	if err := Validate(p); err != nil {
		return nil, err
	}
	abs, err := filepath.Abs(executable)
	if err != nil {
		return nil, err
	}
	info, err := os.Stat(abs)
	if err != nil || !info.Mode().IsRegular() || info.Mode().Perm()&0111 == 0 {
		return nil, fmt.Errorf("sandbox target must be an executable regular file")
	}
	limiter := limiterOverride
	if limiter == "" {
		limiter, err = siblingBinary(limiterBinary)
		if err != nil {
			return nil, err
		}
	}

	// The child sees an almost empty root filesystem: only /workspace, the
	// read-only binds, /proc and a tmpfs /tmp. There is no host /etc, /home
	// or network namespace.
	bwrapArgs := []string{
		"--unshare-user",
		"--unshare-pid",
		"--unshare-net",
		"--unshare-ipc",
		"--unshare-uts",
		"--die-with-parent",
		"--clearenv",
		"--proc", "/proc",
		"--dev", "/dev",
		"--tmpfs", "/tmp",
		"--ro-bind", limiter, "/nabd/limiter",
		"--ro-bind", abs, "/nabd/target",
	}
	if p.WritableWorkspace {
		bwrapArgs = append(bwrapArgs, "--bind", p.WorkDir, "/workspace")
	} else {
		bwrapArgs = append(bwrapArgs, "--ro-bind", p.WorkDir, "/workspace")
	}
	for _, bind := range p.ExtraReadonlyBinds {
		bwrapArgs = append(bwrapArgs, "--ro-bind", bind, bind)
	}
	for _, entry := range p.Env {
		key, value, _ := strings.Cut(entry, "=")
		bwrapArgs = append(bwrapArgs, "--setenv", key, value)
	}
	bwrapArgs = append(bwrapArgs,
		"--chdir", "/workspace",
		"--",
		"/nabd/limiter",
		strconv.FormatInt(p.MemoryLimitBytes, 10),
		strconv.Itoa(p.ProcessLimit),
		"/nabd/target",
	)
	bwrapArgs = append(bwrapArgs, args...)

	cmd := exec.CommandContext(ctx, b.bwrap, bwrapArgs...)
	cmd.WaitDelay = 2 * time.Second
	return cmd, nil
}

// Prove runs the shipped probe inside the real sandbox. The probe fails if it
// can read host files, write outside its allowed area, open a socket, or sees
// different resource limits. A failure here leaves both execution tools off.
func (b *linuxBackend) Prove(ctx context.Context) error {
	if b == nil || !b.Available() {
		return ErrUnavailable
	}
	if bwrapOverride != "" {
		b.bwrap = bwrapOverride
	}
	probe := probeOverride
	if probe == "" {
		var err error
		probe, err = siblingBinary(probeBinary)
		if err != nil {
			return err
		}
	}
	tmp, err := os.MkdirTemp("", "nabd-sandbox-proof-")
	if err != nil {
		return err
	}
	defer os.RemoveAll(tmp)

	proveCtx, cancel := context.WithTimeout(ctx, 20*time.Second)
	defer cancel()

	cmd, err := b.Wrap(proveCtx, Profile{
		WorkDir:           tmp,
		WritableWorkspace: false,
		Env:               []string{"EXPECT_MEM=" + strconv.FormatInt(proofMemory, 10), "EXPECT_PROCS=" + strconv.Itoa(proofProcesses)},
		MemoryLimitBytes:  proofMemory,
		ProcessLimit:      proofProcesses,
	}, probe)
	if err != nil {
		return err
	}
	out, err := cmd.CombinedOutput()
	if err != nil {
		return fmt.Errorf("sandbox proof rejected: %v: %s", err, strings.TrimSpace(string(out)))
	}
	if !strings.Contains(string(out), "sandbox-proof-ok") {
		return fmt.Errorf("sandbox probe did not confirm its restrictions: %q", strings.TrimSpace(string(out)))
	}
	return nil
}
