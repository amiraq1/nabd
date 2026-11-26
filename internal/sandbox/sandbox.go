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
	"os"
	"os/exec"
	"strconv"
	"strings"
)

// ErrUnavailable is returned when no proven isolation backend exists on the
// current platform. Callers must keep untrusted execution disabled.
var ErrUnavailable = errors.New("no proven execution sandbox is available")

const (
	// DefaultMemoryLimitBytes is the baseline virtual address space limit (2 GiB).
	DefaultMemoryLimitBytes = int64(2) << 30
	// MinMemoryLimitBytes is the lowest permissible virtual memory ceiling (512 MiB).
	// The Go runtime fails at startup under a lower cap.
	MinMemoryLimitBytes = int64(512) << 20
	// MaxMemoryLimitBytes is the highest permissible virtual memory ceiling (64 GiB).
	MaxMemoryLimitBytes = int64(64) << 30
	// MemoryLimitEnv is the environment variable used to tune the sandbox memory ceiling.
	MemoryLimitEnv = "NABD_SANDBOX_MEMORY_LIMIT"
)

// pageSizeFunc is injectable in unit tests to simulate different kernel page sizes.
var pageSizeFunc = os.Getpagesize

// DefaultMemoryLimit returns the memory ceiling in bytes for the sandbox (RLIMIT_AS).
// If NABD_SANDBOX_MEMORY_LIMIT is set in the environment, it is clamped to
// [MinMemoryLimitBytes, MaxMemoryLimitBytes] (512 MiB to 64 GiB).
// If unset or invalid, it returns DefaultMemoryLimitBytes (2 GiB).
func DefaultMemoryLimit() int64 {
	if v := os.Getenv(MemoryLimitEnv); v != "" {
		if n, err := strconv.ParseInt(v, 10, 64); err == nil && n > 0 {
			if n < MinMemoryLimitBytes {
				return MinMemoryLimitBytes
			}
			if n > MaxMemoryLimitBytes {
				return MaxMemoryLimitBytes
			}
			return n
		}
	}
	return DefaultMemoryLimitBytes
}

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
	// MaskedDirs are workspace-relative directory paths inside /workspace that
	// must be hidden with an empty tmpfs overlay (e.g., ".git", ".nabd").
	MaskedDirs []string
	// MaskedFiles are workspace-relative regular file paths inside /workspace
	// that must be hidden with read-only /dev/null (e.g., ".env", "*.key").
	MaskedFiles []string
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

// SetHelpersForTesting supplies the helper binaries to integration tests in
// other packages. It is deliberately not configurable through the environment:
// a user-controlled helper path would let a child replace its own verifier.
func SetHelpersForTesting(limiter, probe string) { setHelpersForTesting(limiter, probe) }

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
	for _, rel := range p.MaskedDirs {
		if err := validateMaskedRelPath(rel); err != nil {
			return fmt.Errorf("invalid masked directory %q: %w", rel, err)
		}
	}
	for _, rel := range p.MaskedFiles {
		if err := validateMaskedRelPath(rel); err != nil {
			return fmt.Errorf("invalid masked file %q: %w", rel, err)
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

func validateMaskedRelPath(rel string) error {
	if rel == "" || rel == "." {
		return fmt.Errorf("path must be non-empty and not '.'")
	}
	if strings.ContainsAny(rel, "\x00\\") || strings.HasPrefix(rel, "/") || strings.HasSuffix(rel, "/") {
		return fmt.Errorf("path must be a clean slash-separated relative path")
	}
	for _, seg := range strings.Split(rel, "/") {
		if seg == "" || seg == "." || seg == ".." {
			return fmt.Errorf("path contains empty or traversal segment")
		}
	}
	return nil
}
