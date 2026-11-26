//go:build linux && amd64

package main

import "golang.org/x/sys/unix"

const guardAuditArch uint32 = unix.AUDIT_ARCH_X86_64

// archDeniedSyscalls blocks legacy x86-64-only kernel/privilege interfaces
// that have no legitimate use in an MCP server process.
var archDeniedSyscalls = []uint32{
	unix.SYS_USELIB,
	unix.SYS_USTAT,
	unix.SYS_SYSFS,
	unix.SYS_MODIFY_LDT,
	unix.SYS_IOPL,
	unix.SYS_IOPERM,
	unix.SYS_CREATE_MODULE,
	unix.SYS_GET_KERNEL_SYMS,
	unix.SYS_QUERY_MODULE,
}
