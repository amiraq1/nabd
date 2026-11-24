package sandbox

import (
	"context"
	"errors"
	"strings"
	"testing"
)

func TestExternalExecutionDisabledByDefault(t *testing.T) {
	b, err := Enabled(context.Background(), false)
	if b != nil || !errors.Is(err, ErrUnavailable) {
		t.Fatalf("default must fail closed, got backend=%v err=%v", b, err)
	}
}

func TestValidateRejectsUnsafeProfiles(t *testing.T) {
	cases := []struct {
		name    string
		profile Profile
		want    string
	}{
		{"relative workspace", Profile{WorkDir: "workspace", MemoryLimitBytes: 1, ProcessLimit: 1}, "absolute"},
		{"relative bind", Profile{WorkDir: "/w", ExtraReadonlyBinds: []string{"etc"}, MemoryLimitBytes: 1, ProcessLimit: 1}, "absolute"},
		{"bad env", Profile{WorkDir: "/w", Env: []string{"BAD"}, MemoryLimitBytes: 1, ProcessLimit: 1}, "environment"},
		{"nul env", Profile{WorkDir: "/w", Env: []string{"K=v\x00x"}, MemoryLimitBytes: 1, ProcessLimit: 1}, "environment"},
		{"zero memory", Profile{WorkDir: "/w", MemoryLimitBytes: 0, ProcessLimit: 1}, "positive"},
		{"zero processes", Profile{WorkDir: "/w", MemoryLimitBytes: 1, ProcessLimit: 0}, "positive"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			err := Validate(tc.profile)
			if err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("want error containing %q, got %v", tc.want, err)
			}
		})
	}
}

func TestCurrentFailsClosedWhenUnavailable(t *testing.T) {
	b := Current()
	if b == nil {
		t.Fatal("Current must return a backend value")
	}
	if b.Available() {
		return
	}
	_, err := b.Wrap(context.Background(), Profile{WorkDir: "/w", MemoryLimitBytes: 1, ProcessLimit: 1}, "/bin/true")
	if !errors.Is(err, ErrUnavailable) {
		t.Fatalf("unavailable backend must return ErrUnavailable, got %v", err)
	}
	if err := b.Prove(context.Background()); !errors.Is(err, ErrUnavailable) {
		t.Fatalf("unavailable backend proof must fail, got %v", err)
	}
}

func TestDefaultMemoryLimitConfigurableAndPageSizeAware(t *testing.T) {
	// 1. Explicit override via NABD_SANDBOX_MEMORY_LIMIT within bounds
	t.Setenv(MemoryLimitEnv, "3221225472") // 3 GiB
	if got := DefaultMemoryLimit(); got != 3221225472 {
		t.Fatalf("DefaultMemoryLimit() = %d, want 3221225472", got)
	}

	// 2. Values below MinMemoryLimitBytes (512 MiB) are clamped to MinMemoryLimitBytes
	t.Setenv(MemoryLimitEnv, "104857600") // 100 MiB
	if got := DefaultMemoryLimit(); got != MinMemoryLimitBytes {
		t.Fatalf("DefaultMemoryLimit() with sub-minimum env = %d, want MinMemoryLimitBytes (%d)", got, MinMemoryLimitBytes)
	}

	// 3. Values above MaxMemoryLimitBytes (64 GiB) are clamped to MaxMemoryLimitBytes
	t.Setenv(MemoryLimitEnv, "137438953472") // 128 GiB
	if got := DefaultMemoryLimit(); got != MaxMemoryLimitBytes {
		t.Fatalf("DefaultMemoryLimit() with excessive env = %d, want MaxMemoryLimitBytes (%d)", got, MaxMemoryLimitBytes)
	}

	// 4. Invalid or non-positive value falls back to default 2 GiB
	t.Setenv(MemoryLimitEnv, "invalid")
	if got := DefaultMemoryLimit(); got != DefaultMemoryLimitBytes {
		t.Fatalf("DefaultMemoryLimit() with invalid env = %d, want %d", got, DefaultMemoryLimitBytes)
	}

	t.Setenv(MemoryLimitEnv, "0")
	if got := DefaultMemoryLimit(); got != DefaultMemoryLimitBytes {
		t.Fatalf("DefaultMemoryLimit() with zero env = %d, want %d", got, DefaultMemoryLimitBytes)
	}

	t.Setenv(MemoryLimitEnv, "-1024")
	if got := DefaultMemoryLimit(); got != DefaultMemoryLimitBytes {
		t.Fatalf("DefaultMemoryLimit() with negative env = %d, want %d", got, DefaultMemoryLimitBytes)
	}

	// 5. Unset env defaults to 2 GiB
	t.Setenv(MemoryLimitEnv, "")
	if got := DefaultMemoryLimit(); got != DefaultMemoryLimitBytes {
		t.Fatalf("DefaultMemoryLimit() with empty env = %d, want %d", got, DefaultMemoryLimitBytes)
	}

	// 6. Test pageSizeFunc injectability
	origPageSize := pageSizeFunc
	t.Cleanup(func() { pageSizeFunc = origPageSize })
	pageSizeFunc = func() int { return 65536 }
	if pageSizeFunc() != 65536 {
		t.Fatalf("pageSizeFunc stubbing failed, got %d", pageSizeFunc())
	}
}
