// Package sandbox provides an externally verifiable isolation boundary for
// untrusted child processes.
//
// It deliberately does not treat os.Root, approval prompts, or seccomp filters
// by themselves as a sandbox. A backend is only considered usable after its
// Prove method has demonstrated filesystem, network, memory and process
// restrictions from inside the isolated environment. Callers must fail closed
// when no backend is available.
package sandbox

import (
	"context"
	"errors"
	"fmt"
	"os/exec"
	"strings"
)

// ErrUnavailable is returned when no proven isolation backend exists on the
// current platform. Callers must keep untrusted execution disabled.
var ErrUnavailable = errors.New("no proven execution sandbox is available")

// Profile describes the isolation requested for one child process.
// No parent environment variable is inherited unless it appears in Env.
type Profile struct {
	// WorkDir is an absolute workspace path exposed at /workspace.
	WorkDir string
	// WritableWorkspace makes the /workspace bind writable. All other paths
	// supplied by the caller are read-only.
	WritableWorkspace bool
	// ExtraReadonlyBinds are absolute host paths exposed read-only at the
	// same absolute path inside the sandbox.
	ExtraReadonlyBinds []string
	// Env is the complete child environment, in KEY=VALUE form.
	Env []string
	// MemoryLimitBytes is a hard RLIMIT_AS ceiling in the child.
	MemoryLimitBytes int64
	// ProcessLimit is a hard RLIMIT_NPROC ceiling in the child.
	ProcessLimit int
}

// Backend is an isolation implementation that can prove its own guarantees.
type Backend interface {
	Name() string
	Available() bool
	Wrap(ctx context.Context, p Profile, executable string, args ...string) (*exec.Cmd, error)
	Prove(ctx context.Context) error
}

// Current returns the platform backend. It may be unavailable; callers must
// check Available and Prove before using it.
func Current() Backend { return current() }

// Enabled reports whether untrusted execution may be started. It requires an
// explicit opt-in, a backend, and a successful proof run. Any error fails
// closed. The returned backend is nil when execution must stay disabled.
func Enabled(ctx context.Context, optIn bool) (Backend, error) {
	if !optIn {
		return nil, fmt.Errorf("%w: external execution is disabled by default", ErrUnavailable)
	}
	b := Current()
	if b == nil || !b.Available() {
		return nil, fmt.Errorf("%w: no supported isolation backend", ErrUnavailable)
	}
	if err := b.Prove(ctx); err != nil {
		return nil, fmt.Errorf("%w: proof failed: %v", ErrUnavailable, err)
	}
	return b, nil
}

// Validate checks a profile before a backend uses it. It rejects relative
// paths, unsafe environment entries and nonsensical limits.
func Validate(p Profile) error {
	if p.WorkDir == "" || !strings.HasPrefix(p.WorkDir, "/") {
		return fmt.Errorf("workspace must be an absolute path")
	}
	for _, bind := range p.ExtraReadonlyBinds {
		if bind == "" || !strings.HasPrefix(bind, "/") {
			return fmt.Errorf("extra bind must be an absolute path")
		}
	}
	for _, entry := range p.Env {
		key, value, ok := strings.Cut(entry, "=")
		if !ok || key == "" || strings.ContainsAny(key, "=\x00") || strings.ContainsRune(value, 0) {
			return fmt.Errorf("invalid environment entry")
		}
	}
	if p.MemoryLimitBytes <= 0 || p.ProcessLimit <= 0 {
		return fmt.Errorf("memory and process limits must be positive")
	}
	return nil
}
