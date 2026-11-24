//go:build !linux || android

package sandbox

import (
	"context"
	"testing"
)

func TestSandboxFailsClosedOnNonLinuxOrAndroid(t *testing.T) {
	b := Current()
	if b == nil {
		t.Fatal("Current() returned nil backend")
	}
	if b.Available() {
		t.Fatalf("sandbox backend %q must not be available on !linux || android", b.Name())
	}
	if err := b.Prove(context.Background()); err != ErrUnavailable {
		t.Fatalf("Prove() must return ErrUnavailable on !linux || android, got: %v", err)
	}
	if _, err := b.Wrap(context.Background(), Profile{}, "/bin/true"); err != ErrUnavailable {
		t.Fatalf("Wrap() must return ErrUnavailable on !linux || android, got: %v", err)
	}
	binds, err := LoaderBinds("/bin/true")
	if err != nil {
		t.Fatalf("LoaderBinds returned unexpected error: %v", err)
	}
	if binds != nil {
		t.Fatalf("LoaderBinds must return nil on !linux || android, got: %v", binds)
	}
}
