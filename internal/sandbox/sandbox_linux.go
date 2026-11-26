//go:build linux && !android

package sandbox

import (
	"context"
	"fmt"
	"net"
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

func setHelpersForTesting(limiter, probe string) {
	limiterOverride, probeOverride = limiter, probe
}

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
	if err := validateWorkspaceMasksNoFollow(p.WorkDir, p.MaskedDirs, p.MaskedFiles); err != nil {
		return nil, err
	}

	bwrapArgs := buildBwrapArgs(p, limiter, abs, args...)

	cmd := exec.CommandContext(ctx, b.bwrap, bwrapArgs...)
	cmd.WaitDelay = 2 * time.Second
	return cmd, nil
}

// Prove runs the shipped probe inside the real sandbox. The probe fails if it
// can read host files, write outside its allowed area, reach the host network
// namespace or host loopback listener, or sees different resource limits.
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

	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		return fmt.Errorf("bind host loopback listener for netns proof: %w", err)
	}
	acceptDone := make(chan struct{})
	go func() {
		defer close(acceptDone)
		for {
			c, aErr := ln.Accept()
			if aErr != nil {
				return
			}
			_ = c.Close()
		}
	}()
	defer func() {
		_ = ln.Close()
		<-acceptDone
	}()

	// Positive control on host: dialing ln.Addr() MUST succeed from the host namespace
	// so the probe's failure inside the sandbox proves namespace isolation.
	hostConn, err := net.DialTimeout("tcp", ln.Addr().String(), 500*time.Millisecond)
	if err != nil {
		return fmt.Errorf("host loopback positive control failed: %w", err)
	}
	_ = hostConn.Close()

	hostNetNS, err := os.Readlink("/proc/self/ns/net")
	if err != nil || hostNetNS == "" {
		return fmt.Errorf("read host network namespace: %w", err)
	}

	proveCtx, cancel := context.WithTimeout(ctx, 20*time.Second)
	defer cancel()

	mem := DefaultMemoryLimit()
	cmd, err := b.Wrap(proveCtx, Profile{
		WorkDir:           tmp,
		WritableWorkspace: false,
		Env: []string{
			"EXPECT_MEM=" + strconv.FormatInt(mem, 10),
			"EXPECT_PROCS=" + strconv.Itoa(proofProcesses),
			"EXPECT_BLOCKED_ADDR=" + ln.Addr().String(),
			"EXPECT_HOST_NETNS=" + hostNetNS,
		},
		MemoryLimitBytes: mem,
		ProcessLimit:     proofProcesses,
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
