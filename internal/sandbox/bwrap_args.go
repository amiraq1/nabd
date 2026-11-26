package sandbox

import (
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
)

func buildBwrapArgs(p Profile, limiter, absTarget string, args ...string) []string {
	// The child sees an almost empty root filesystem and a detached session:
	// no controlling terminal (--new-session), no host /etc or /home, no network
	// namespace, and sensitive workspace entries masked below.
	bwrapArgs := []string{
		"--unshare-user",
		"--unshare-pid",
		"--unshare-net",
		"--unshare-ipc",
		"--unshare-uts",
		"--new-session",
		"--die-with-parent",
		"--clearenv",
		"--proc", "/proc",
		"--dev", "/dev",
		"--tmpfs", "/tmp",
		"--ro-bind", limiter, "/nabd/limiter",
		"--ro-bind", absTarget, "/nabd/target",
	}
	if p.WritableWorkspace {
		bwrapArgs = append(bwrapArgs, "--bind", p.WorkDir, "/workspace")
	} else {
		bwrapArgs = append(bwrapArgs, "--ro-bind", p.WorkDir, "/workspace")
	}
	for _, bind := range p.ExtraReadonlyBinds {
		bwrapArgs = append(bwrapArgs, "--ro-bind", bind, bind)
	}
	// Apply workspace masks after all binds so sensitive dirs and files remain
	// obscured inside /workspace regardless of extra mounts.
	for _, rel := range p.MaskedDirs {
		bwrapArgs = append(bwrapArgs, "--tmpfs", "/workspace/"+rel)
	}
	for _, rel := range p.MaskedFiles {
		bwrapArgs = append(bwrapArgs, "--ro-bind", "/dev/null", "/workspace/"+rel)
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
	return bwrapArgs
}

func validateWorkspaceMasksNoFollow(workDir string, dirs, files []string) error {
	for _, rel := range dirs {
		if err := verifyRelativeTargetNoFollow(workDir, rel, true); err != nil {
			return fmt.Errorf("masked directory %q rejected: %w", rel, err)
		}
	}
	for _, rel := range files {
		if err := verifyRelativeTargetNoFollow(workDir, rel, false); err != nil {
			return fmt.Errorf("masked file %q rejected: %w", rel, err)
		}
	}
	return nil
}

func verifyRelativeTargetNoFollow(workDir, rel string, wantDir bool) error {
	if err := validateMaskedRelPath(rel); err != nil {
		return err
	}
	cur := workDir
	info, err := os.Lstat(cur)
	if err != nil {
		return err
	}
	if info.Mode()&os.ModeSymlink != 0 || !info.IsDir() {
		return fmt.Errorf("workspace root must be a non-symlink directory")
	}
	parts := strings.Split(rel, "/")
	for i, part := range parts {
		cur = filepath.Join(cur, part)
		st, err := os.Lstat(cur)
		if err != nil {
			return err
		}
		if st.Mode()&os.ModeSymlink != 0 {
			return fmt.Errorf("refusing symlinked path component %q", rel)
		}
		isLast := i == len(parts)-1
		if !isLast {
			if !st.IsDir() {
				return fmt.Errorf("intermediate path component in %q is not a directory", rel)
			}
			continue
		}
		if wantDir {
			if !st.IsDir() {
				return fmt.Errorf("expected directory at %q", rel)
			}
		} else {
			if !st.Mode().IsRegular() {
				return fmt.Errorf("expected regular file at %q", rel)
			}
		}
	}
	return nil
}
