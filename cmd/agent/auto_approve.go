package main

import (
	"fmt"
	"os"
	"strings"
)

const AutoApproveEnv = "NABD_AUTO_APPROVE"

type AutoApproveMode string

const (
	AutoApproveOff      AutoApproveMode = "off"
	AutoApproveReadOnly AutoApproveMode = "readonly"
	AutoApproveAll      AutoApproveMode = "all"
)

// ParseAutoApproveMode parses and validates a mode string case-insensitively.
// Empty or "off" returns AutoApproveOff.
// "readonly" returns AutoApproveReadOnly.
// "all" returns AutoApproveAll.
// Any other non-empty value returns an error.
func ParseAutoApproveMode(val string) (AutoApproveMode, error) {
	trimmed := strings.TrimSpace(val)
	if trimmed == "" {
		return AutoApproveOff, nil
	}
	lower := strings.ToLower(trimmed)
	switch lower {
	case "off":
		return AutoApproveOff, nil
	case "readonly":
		return AutoApproveReadOnly, nil
	case "all":
		return AutoApproveAll, nil
	default:
		return "", fmt.Errorf("invalid %s value %q: must be off, readonly, or all", AutoApproveEnv, val)
	}
}

// LoadAutoApproveFromEnv reads and parses NABD_AUTO_APPROVE from the environment.
func LoadAutoApproveFromEnv() (AutoApproveMode, error) {
	val, ok := os.LookupEnv(AutoApproveEnv)
	if !ok {
		return AutoApproveOff, nil
	}
	return ParseAutoApproveMode(val)
}
