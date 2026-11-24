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
