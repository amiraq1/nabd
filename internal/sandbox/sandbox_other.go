//go:build !linux || android

package sandbox

import (
	"context"
	"os/exec"
)

func current() Backend { return unavailableBackend{} }

func setHelpersForTesting(string, string) {}

type unavailableBackend struct{}

func (unavailableBackend) Name() string                { return "unavailable" }
func (unavailableBackend) Available() bool             { return false }
func (unavailableBackend) Prove(context.Context) error { return ErrUnavailable }
func (unavailableBackend) Wrap(context.Context, Profile, string, ...string) (*exec.Cmd, error) {
	return nil, ErrUnavailable
}
