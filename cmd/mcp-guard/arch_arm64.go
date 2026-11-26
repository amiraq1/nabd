//go:build linux && arm64

package main

import "golang.org/x/sys/unix"

const guardAuditArch uint32 = unix.AUDIT_ARCH_AARCH64

var archDeniedSyscalls []uint32
