//go:build linux && (arm64 || amd64)

package main

import (
	"fmt"
	"os"
	"os/exec"
	"runtime"
	"strings"
	"syscall"
	"unsafe"

	"github.com/amiraq1/nabd/internal/safefs"
	"golang.org/x/sys/unix"
)

// commonDeniedSyscalls lists dangerous kernel, debugging, namespace, mount,
// keyring, eBPF, and io_uring syscalls shared by Linux amd64 and arm64.
// An expanded deny-list (rather than a brittle static syscall allowlist) avoids
// breaking arbitrary Go/glibc/Python/Node MCP servers (such as REA) on newer
// Linux kernels while blocking process detachment, cross-process memory access,
// container/namespace escapes, and kernel attack surfaces.
var commonDeniedSyscalls = []uint32{
	// Session and process-group detachment
	unix.SYS_SETSID,
	unix.SYS_SETPGID,
	// Cross-process inspection, debugging, and FD theft
	unix.SYS_PTRACE,
	unix.SYS_PROCESS_VM_READV,
	unix.SYS_PROCESS_VM_WRITEV,
	unix.SYS_PROCESS_MADVISE,
	unix.SYS_KCMP,
	unix.SYS_PIDFD_GETFD,
	// Namespace and mount escapes
	unix.SYS_UNSHARE,
	unix.SYS_SETNS,
	unix.SYS_MOUNT,
	unix.SYS_UMOUNT2,
	unix.SYS_PIVOT_ROOT,
	unix.SYS_CHROOT,
	unix.SYS_OPEN_TREE,
	unix.SYS_MOVE_MOUNT,
	unix.SYS_FSOPEN,
	unix.SYS_FSCONFIG,
	unix.SYS_FSMOUNT,
	unix.SYS_FSPICK,
	unix.SYS_MOUNT_SETATTR,
	unix.SYS_OPEN_BY_HANDLE_AT,
	unix.SYS_NAME_TO_HANDLE_AT,
	// Kernel attack surface & syscall-audit bypasses
	unix.SYS_BPF,
	unix.SYS_IO_URING_SETUP,
	unix.SYS_IO_URING_ENTER,
	unix.SYS_IO_URING_REGISTER,
	unix.SYS_USERFAULTFD,
	unix.SYS_PERF_EVENT_OPEN,
	// Kernel modules, kexec, reboot, swap, keyring, clock, host identity
	unix.SYS_INIT_MODULE,
	unix.SYS_FINIT_MODULE,
	unix.SYS_DELETE_MODULE,
	unix.SYS_KEXEC_LOAD,
	unix.SYS_KEXEC_FILE_LOAD,
	unix.SYS_REBOOT,
	unix.SYS_SWAPON,
	unix.SYS_SWAPOFF,
	unix.SYS_ADD_KEY,
	unix.SYS_REQUEST_KEY,
	unix.SYS_KEYCTL,
	unix.SYS_PERSONALITY,
	unix.SYS_SYSLOG,
	unix.SYS_LOOKUP_DCOOKIE,
	unix.SYS_QUOTACTL,
	unix.SYS_ACCT,
	unix.SYS_VHANGUP,
	unix.SYS_SETHOSTNAME,
	unix.SYS_SETDOMAINNAME,
	unix.SYS_ADJTIMEX,
	unix.SYS_CLOCK_ADJTIME,
	unix.SYS_CLOCK_SETTIME,
	unix.SYS_SETTIMEOFDAY,
}

func deniedSyscalls() []uint32 {
	out := make([]uint32, 0, len(commonDeniedSyscalls)+len(archDeniedSyscalls))
	out = append(out, commonDeniedSyscalls...)
	out = append(out, archDeniedSyscalls...)
	return out
}

func seccompFilter() []unix.SockFilter {
	denied := deniedSyscalls()
	if len(denied) > 250 {
		panic("seccomp deny list exceeds single-byte BPF jump range")
	}

	filter := make([]unix.SockFilter, 0, 6+len(denied)+2)
	filter = append(filter,
		unix.SockFilter{Code: unix.BPF_LD | unix.BPF_W | unix.BPF_ABS, K: 4},
		unix.SockFilter{Code: unix.BPF_JMP | unix.BPF_JEQ | unix.BPF_K, Jt: 1, Jf: 0, K: guardAuditArch},
		unix.SockFilter{Code: unix.BPF_RET | unix.BPF_K, K: unix.SECCOMP_RET_KILL_PROCESS},
		unix.SockFilter{Code: unix.BPF_LD | unix.BPF_W | unix.BPF_ABS, K: 0},
		// x86-64 x32 calls share AUDIT_ARCH_X86_64 but have bit 30 set.
		// Reject the alternate ABI rather than allowing a filter bypass.
		unix.SockFilter{Code: unix.BPF_JMP | unix.BPF_JSET | unix.BPF_K, Jt: 0, Jf: 1, K: 0x40000000},
		unix.SockFilter{Code: unix.BPF_RET | unix.BPF_K, K: unix.SECCOMP_RET_KILL_PROCESS},
	)

	n := len(denied)
	for i, nr := range denied {
		// Jump to the terminal SECCOMP_RET_ERRNO|EPERM at index 6+n+1.
		jt := uint8(n - i)
		filter = append(filter, unix.SockFilter{
			Code: unix.BPF_JMP | unix.BPF_JEQ | unix.BPF_K,
			Jt:   jt,
			Jf:   0,
			K:    nr,
		})
	}

	filter = append(filter,
		unix.SockFilter{Code: unix.BPF_RET | unix.BPF_K, K: unix.SECCOMP_RET_ALLOW},
		unix.SockFilter{Code: unix.BPF_RET | unix.BPF_K, K: unix.SECCOMP_RET_ERRNO | uint32(unix.EPERM)},
	)
	return filter
}

func installFilter() error {
	if err := unix.Prctl(unix.PR_SET_NO_NEW_PRIVS, 1, 0, 0, 0); err != nil {
		return fmt.Errorf("no_new_privs: %w", err)
	}
	filter := seccompFilter()
	prog := unix.SockFprog{Len: uint16(len(filter)), Filter: &filter[0]}
	if err := unix.Prctl(unix.PR_SET_SECCOMP, unix.SECCOMP_MODE_FILTER, uintptr(unsafe.Pointer(&prog)), 0, 0); err != nil {
		return fmt.Errorf("seccomp: %w", err)
	}
	return nil
}

func execTarget(target string, targetArgs []string) error {
	path := target
	if !strings.ContainsRune(target, '/') {
		var err error
		path, err = exec.LookPath(target)
		if err != nil {
			return err
		}
	}
	f, _, err := safefs.OpenFileNoFollow(path, safefs.OpenPolicy{
		RejectGroupWrite:  true,
		RequireExecutable: true,
	})
	if err != nil {
		return fmt.Errorf("refusing target %s: %w", target, err)
	}
	defer f.Close()

	argv := append([]string{target}, targetArgs...)
	return execFileFD(int(f.Fd()), argv, os.Environ())
}

func execFileFD(fd int, argv, envv []string) error {
	empty, err := unix.BytePtrFromString("")
	if err != nil {
		return err
	}
	argvp, err := syscall.SlicePtrFromStrings(argv)
	if err != nil {
		return err
	}
	envpp, err := syscall.SlicePtrFromStrings(envv)
	if err != nil {
		return err
	}
	_, _, errno := unix.Syscall6(
		unix.SYS_EXECVEAT,
		uintptr(fd),
		uintptr(unsafe.Pointer(empty)),
		uintptr(unsafe.Pointer(&argvp[0])),
		uintptr(unsafe.Pointer(&envpp[0])),
		unix.AT_EMPTY_PATH,
		0,
	)
	runtime.KeepAlive(empty)
	runtime.KeepAlive(argvp)
	runtime.KeepAlive(envpp)
	if errno != 0 {
		return errno
	}
	return fmt.Errorf("execveat returned unexpectedly")
}
