package safefs

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestOpenAndReadFileNoFollowPolicies(t *testing.T) {
	dir := t.TempDir()
	secretPath := filepath.Join(dir, "secret.key")
	if err := os.WriteFile(secretPath, []byte("secret-value"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(secretPath, 0o600); err != nil {
		t.Fatal(err)
	}

	// Valid strict 0600 file
	data, info, err := ReadFileNoFollow(secretPath, OpenPolicy{
		MaxBytes:          4096,
		RequireOwner:      true,
		StrictPrivateMode: true,
		RejectExecutable:  true,
	})
	if err != nil {
		t.Fatalf("expected strict 0600 file to succeed, got %v", err)
	}
	if string(data) != "secret-value" || info.Size() != 12 {
		t.Fatalf("unexpected data/info: %q (%d)", string(data), info.Size())
	}

	// Reject component symlink and dotdot bypass via symlink_dir/../real/file
	realSub := filepath.Join(dir, "real")
	if err := os.MkdirAll(realSub, 0o700); err != nil {
		t.Fatal(err)
	}
	realKey := filepath.Join(realSub, "k")
	if err := os.WriteFile(realKey, []byte("kval"), 0o600); err != nil {
		t.Fatal(err)
	}
	symSub := filepath.Join(dir, "symdir")
	if err := os.Symlink(realSub, symSub); err != nil {
		t.Skipf("symlink unsupported: %v", err)
	}
	if _, _, err := ReadFileNoFollow(filepath.Join(symSub, "k"), OpenPolicy{MaxBytes: 4096}); !errors.Is(err, ErrSymlink) {
		t.Fatalf("expected ErrSymlink for parent symlink, got %v", err)
	}
	dotdotPath := symSub + string(filepath.Separator) + ".." + string(filepath.Separator) + "real" + string(filepath.Separator) + "k"
	if _, _, err := ReadFileNoFollow(dotdotPath, OpenPolicy{MaxBytes: 4096}); !errors.Is(err, ErrSymlink) {
		t.Fatalf("expected ErrSymlink for symlink/../real traversal, got %v", err)
	}

	// Reject leaf symlink
	leafLink := filepath.Join(dir, "leaf.lnk")
	if err := os.Symlink(secretPath, leafLink); err != nil {
		t.Fatal(err)
	}
	if _, _, err := ReadFileNoFollow(leafLink, OpenPolicy{MaxBytes: 4096}); !errors.Is(err, ErrSymlink) {
		t.Fatalf("expected ErrSymlink for leaf symlink, got %v", err)
	}

	// Unix-specific mode & FIFO checks
	if os.PathSeparator != '\\' {
		// 0644 permitted by RejectGroupWrite, rejected by StrictPrivateMode
		sharedPath := filepath.Join(dir, "shared.json")
		if err := os.WriteFile(sharedPath, []byte("{}"), 0o600); err != nil {
			t.Fatal(err)
		}
		if err := os.Chmod(sharedPath, 0o644); err != nil {
			t.Fatal(err)
		}
		if _, _, err := ReadFileNoFollow(sharedPath, OpenPolicy{MaxBytes: 4096, RejectGroupWrite: true, RequireOwner: true}); err != nil {
			t.Fatalf("expected 0644 to pass RejectGroupWrite, got %v", err)
		}
		if _, _, err := ReadFileNoFollow(sharedPath, OpenPolicy{MaxBytes: 4096, StrictPrivateMode: true}); !errors.Is(err, ErrPermission) {
			t.Fatalf("expected 0644 to fail StrictPrivateMode, got %v", err)
		}

		// 0664 rejected by RejectGroupWrite
		gwPath := filepath.Join(dir, "gw.json")
		if err := os.WriteFile(gwPath, []byte("{}"), 0o600); err != nil {
			t.Fatal(err)
		}
		if err := os.Chmod(gwPath, 0o664); err != nil {
			t.Fatal(err)
		}
		if _, _, err := ReadFileNoFollow(gwPath, OpenPolicy{MaxBytes: 4096, RejectGroupWrite: true}); !errors.Is(err, ErrPermission) {
			t.Fatalf("expected 0664 to fail RejectGroupWrite, got %v", err)
		}

		// Executable key rejected by RejectExecutable
		execKey := filepath.Join(dir, "exec.key")
		if err := os.WriteFile(execKey, []byte("k"), 0o600); err != nil {
			t.Fatal(err)
		}
		if err := os.Chmod(execKey, 0o700); err != nil {
			t.Fatal(err)
		}
		if _, _, err := ReadFileNoFollow(execKey, OpenPolicy{MaxBytes: 4096, RejectExecutable: true}); !errors.Is(err, ErrPermission) {
			t.Fatalf("expected 0700 to fail RejectExecutable, got %v", err)
		}
	}

	// Oversized file rejected before full allocation
	bigPath := filepath.Join(dir, "big.bin")
	if err := os.WriteFile(bigPath, []byte(strings.Repeat("x", 100)), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, _, err := ReadFileNoFollow(bigPath, OpenPolicy{MaxBytes: 50}); !errors.Is(err, ErrTooLarge) {
		t.Fatalf("expected ErrTooLarge, got %v", err)
	}
}
