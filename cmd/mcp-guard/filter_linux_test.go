//go:build linux && (amd64 || arm64)

package main

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"syscall"
	"testing"
	"unsafe"

	"golang.org/x/sys/unix"
)

func TestGuardHelperProcess(t *testing.T) {
	if os.Getenv("GO_WANT_GUARD_HELPER_PROCESS") != "1" {
		return
	}
	mode := os.Getenv("GUARD_HELPER_MODE")
	runtime.LockOSThread()

	switch mode {
	case "allowed_ops":
		if err := installFilter(); err != nil {
			fmt.Fprintf(os.Stderr, "installFilter failed: %v\n", err)
			os.Exit(1)
		}
		// 1. stdio write
		if _, err := fmt.Fprintln(os.Stdout, "START_ALLOWED"); err != nil {
			fmt.Fprintf(os.Stderr, "stdout write failed: %v\n", err)
			os.Exit(1)
		}
		// 2. memory allocation and verification
		buf := make([]byte, 1024*1024)
		for i := range buf {
			buf[i] = byte(i % 256)
		}
		if buf[1024] != 0 {
			fmt.Fprintf(os.Stderr, "memory verification failed\n")
			os.Exit(1)
		}
		// 3. process & working directory
		if os.Getpid() <= 0 {
			fmt.Fprintf(os.Stderr, "invalid pid: %d\n", os.Getpid())
			os.Exit(1)
		}
		if _, err := os.Getwd(); err != nil {
			fmt.Fprintf(os.Stderr, "getwd failed: %v\n", err)
			os.Exit(1)
		}
		// 4. network socket (demonstrates mcp-guard filter does not block sockets)
		fd, err := unix.Socket(unix.AF_INET, unix.SOCK_STREAM, 0)
		if err != nil {
			fmt.Fprintf(os.Stderr, "socket creation failed: %v\n", err)
			os.Exit(1)
		}
		_ = unix.Close(fd)

		fd6, err := unix.Socket(unix.AF_INET6, unix.SOCK_STREAM, 0)
		if err == nil {
			_ = unix.Close(fd6)
		} else if err != unix.EAFNOSUPPORT {
			fmt.Fprintf(os.Stderr, "socket(AF_INET6) unexpected error: %v\n", err)
			os.Exit(1)
		}

		fmt.Println("ALLOWED_OPS_CONFIRMED")
		os.Exit(0)

	case "blocked_setsid":
		if err := installFilter(); err != nil {
			fmt.Fprintf(os.Stderr, "installFilter failed: %v\n", err)
			os.Exit(1)
		}
		_, _, errno := syscall.RawSyscall(syscall.SYS_SETSID, 0, 0, 0)
		if errno == syscall.EPERM {
			fmt.Println("BLOCKED_SETSID_EPERM")
			os.Exit(0)
		}
		if errno == 0 {
			fmt.Fprintf(os.Stderr, "setsid unexpectedly succeeded!\n")
			os.Exit(2)
		}
		fmt.Fprintf(os.Stderr, "unexpected errno for setsid: %v\n", errno)
		os.Exit(3)

	case "blocked_setpgid":
		if err := installFilter(); err != nil {
			fmt.Fprintf(os.Stderr, "installFilter failed: %v\n", err)
			os.Exit(1)
		}
		errno := unix.Setpgid(0, 0)
		if errno == unix.EPERM {
			fmt.Println("BLOCKED_SETPGID_EPERM")
			os.Exit(0)
		}
		if errno == nil {
			fmt.Fprintf(os.Stderr, "setpgid unexpectedly succeeded!\n")
			os.Exit(2)
		}
		fmt.Fprintf(os.Stderr, "unexpected errno for setpgid: %v\n", errno)
		os.Exit(3)

	case "blocked_bit30":
		if err := installFilter(); err != nil {
			fmt.Fprintf(os.Stderr, "installFilter failed: %v\n", err)
			os.Exit(1)
		}
		// Syscall with bit 30 set must trigger SECCOMP_RET_KILL_PROCESS
		syscall.RawSyscall(0x40000000|uintptr(unix.SYS_GETPID), 0, 0, 0)
		fmt.Fprintf(os.Stderr, "process survived bit 30 syscall!\n")
		os.Exit(2)

	case "arch_mismatch":
		filter := seccompFilter()
		// Invert/modify K on the architecture comparison instruction
		filter[1].K = guardAuditArch ^ 0xffff

		if err := unix.Prctl(unix.PR_SET_NO_NEW_PRIVS, 1, 0, 0, 0); err != nil {
			fmt.Fprintf(os.Stderr, "prctl no_new_privs failed: %v\n", err)
			os.Exit(1)
		}
		prog := unix.SockFprog{Len: uint16(len(filter)), Filter: &filter[0]}
		if err := unix.Prctl(unix.PR_SET_SECCOMP, unix.SECCOMP_MODE_FILTER, uintptr(unsafe.Pointer(&prog)), 0, 0); err != nil {
			fmt.Fprintf(os.Stderr, "prctl seccomp failed: %v\n", err)
			os.Exit(1)
		}
		// Any syscall issued under mismatched arch must trigger SECCOMP_RET_KILL_PROCESS
		_ = unix.Getpid()
		fmt.Fprintf(os.Stderr, "process survived architecture mismatch!\n")
		os.Exit(2)

	case "blocked_dangerous_syscalls":
		if err := installFilter(); err != nil {
			fmt.Fprintf(os.Stderr, "installFilter failed: %v\n", err)
			os.Exit(1)
		}
		checks := []struct {
			name string
			nr   uintptr
			a1   uintptr
		}{
			{"personality", uintptr(unix.SYS_PERSONALITY), 0xffffffff},
			{"ptrace", uintptr(unix.SYS_PTRACE), 0},
			{"unshare", uintptr(unix.SYS_UNSHARE), 0},
			{"bpf", uintptr(unix.SYS_BPF), 0},
			{"setpgid", uintptr(unix.SYS_SETPGID), 0},
			{"setsid", uintptr(unix.SYS_SETSID), 0},
		}
		for _, c := range checks {
			_, _, errno := syscall.RawSyscall(c.nr, c.a1, 0, 0)
			if errno != syscall.EPERM {
				fmt.Fprintf(os.Stderr, "%s returned errno=%v, want EPERM\n", c.name, errno)
				os.Exit(2)
			}
		}
		fmt.Println("BLOCKED_DANGEROUS_EPERM")
		os.Exit(0)

	default:
		fmt.Fprintf(os.Stderr, "unknown helper mode: %s\n", mode)
		os.Exit(1)
	}
}

func runHelperSubprocess(t *testing.T, mode string) (*exec.Cmd, []byte, error) {
	t.Helper()
	exe, err := os.Executable()
	if err != nil {
		exe = os.Args[0]
	}
	cmd := exec.Command(exe, "-test.run=^TestGuardHelperProcess$", "--")
	cmd.Env = append(os.Environ(),
		"GO_WANT_GUARD_HELPER_PROCESS=1",
		"GUARD_HELPER_MODE="+mode,
	)
	out, err := cmd.CombinedOutput()
	return cmd, out, err
}

func TestSeccompArchitectureParity(t *testing.T) {
	// 1. Architecture Audit Constant Verification
	switch runtime.GOARCH {
	case "amd64":
		if guardAuditArch != unix.AUDIT_ARCH_X86_64 {
			t.Fatalf("amd64 guardAuditArch = 0x%x, want AUDIT_ARCH_X86_64 (0x%x)", guardAuditArch, unix.AUDIT_ARCH_X86_64)
		}
		if unix.SYS_SETSID != 112 {
			t.Fatalf("amd64 SYS_SETSID = %d, want 112", unix.SYS_SETSID)
		}
		if unix.SYS_SETPGID != 109 {
			t.Fatalf("amd64 SYS_SETPGID = %d, want 109", unix.SYS_SETPGID)
		}
	case "arm64":
		if guardAuditArch != unix.AUDIT_ARCH_AARCH64 {
			t.Fatalf("arm64 guardAuditArch = 0x%x, want AUDIT_ARCH_AARCH64 (0x%x)", guardAuditArch, unix.AUDIT_ARCH_AARCH64)
		}
		if unix.SYS_SETSID != 157 {
			t.Fatalf("arm64 SYS_SETSID = %d, want 157", unix.SYS_SETSID)
		}
		if unix.SYS_SETPGID != 154 {
			t.Fatalf("arm64 SYS_SETPGID = %d, want 154", unix.SYS_SETPGID)
		}
	default:
		t.Fatalf("unsupported architecture under test: %s", runtime.GOARCH)
	}

	// 2. Program Structure and Instruction Parity
	denied := deniedSyscalls()
	expectedLen := 6 + len(denied) + 2
	filter := seccompFilter()
	if len(filter) != expectedLen {
		t.Fatalf("expected filter length %d on %s, got %d", expectedLen, runtime.GOARCH, len(filter))
	}

	// Instruction 0: load arch at offset 4
	if filter[0].Code != (unix.BPF_LD|unix.BPF_W|unix.BPF_ABS) || filter[0].K != 4 {
		t.Errorf("inst 0: want load arch (offset 4), got code=0x%x K=%d", filter[0].Code, filter[0].K)
	}

	// Instruction 1: branch on guardAuditArch
	if filter[1].Code != (unix.BPF_JMP|unix.BPF_JEQ|unix.BPF_K) || filter[1].K != guardAuditArch || filter[1].Jt != 1 || filter[1].Jf != 0 {
		t.Errorf("inst 1: want JEQ guardAuditArch (0x%x) Jt=1 Jf=0, got K=0x%x Jt=%d Jf=%d", guardAuditArch, filter[1].K, filter[1].Jt, filter[1].Jf)
	}

	// Instruction 2: KILL on arch mismatch (fail-closed)
	if filter[2].Code != (unix.BPF_RET|unix.BPF_K) || filter[2].K != unix.SECCOMP_RET_KILL_PROCESS {
		t.Errorf("inst 2: want SECCOMP_RET_KILL_PROCESS, got code=0x%x K=0x%x", filter[2].Code, filter[2].K)
	}

	// Instruction 3: load syscall nr at offset 0
	if filter[3].Code != (unix.BPF_LD|unix.BPF_W|unix.BPF_ABS) || filter[3].K != 0 {
		t.Errorf("inst 3: want load nr (offset 0), got code=0x%x K=%d", filter[3].Code, filter[3].K)
	}

	// Instruction 4: branch on bit 30 (0x40000000)
	if filter[4].Code != (unix.BPF_JMP|unix.BPF_JSET|unix.BPF_K) || filter[4].K != 0x40000000 || filter[4].Jt != 0 || filter[4].Jf != 1 {
		t.Errorf("inst 4: want JSET 0x40000000 Jt=0 Jf=1, got K=0x%x Jt=%d Jf=%d", filter[4].K, filter[4].Jt, filter[4].Jf)
	}

	// Instruction 5: KILL on bit 30
	if filter[5].Code != (unix.BPF_RET|unix.BPF_K) || filter[5].K != unix.SECCOMP_RET_KILL_PROCESS {
		t.Errorf("inst 5: want SECCOMP_RET_KILL_PROCESS, got code=0x%x K=0x%x", filter[5].Code, filter[5].K)
	}

	allowIdx := 6 + len(denied)
	epermIdx := allowIdx + 1
	for i, nr := range denied {
		inst := filter[6+i]
		wantJt := uint8(len(denied) - i)
		if inst.Code != (unix.BPF_JMP|unix.BPF_JEQ|unix.BPF_K) || inst.K != nr || inst.Jt != wantJt || inst.Jf != 0 {
			t.Errorf("inst %d: want JEQ %d Jt=%d Jf=0, got code=0x%x K=%d Jt=%d Jf=%d", 6+i, nr, wantJt, inst.Code, inst.K, inst.Jt, inst.Jf)
		}
	}

	// Default ALLOW
	if filter[allowIdx].Code != (unix.BPF_RET|unix.BPF_K) || filter[allowIdx].K != unix.SECCOMP_RET_ALLOW {
		t.Errorf("inst %d: want SECCOMP_RET_ALLOW, got code=0x%x K=0x%x", allowIdx, filter[allowIdx].Code, filter[allowIdx].K)
	}

	// Reject denied syscalls with EPERM
	wantEperm := unix.SECCOMP_RET_ERRNO | uint32(unix.EPERM)
	if filter[epermIdx].Code != (unix.BPF_RET|unix.BPF_K) || filter[epermIdx].K != wantEperm {
		t.Errorf("inst %d: want SECCOMP_RET_ERRNO | EPERM (0x%x), got code=0x%x K=0x%x", epermIdx, wantEperm, filter[epermIdx].Code, filter[epermIdx].K)
	}
}

func TestStaticBPFArchitectureValidation(t *testing.T) {
	switch runtime.GOARCH {
	case "amd64":
		if guardAuditArch != unix.AUDIT_ARCH_X86_64 {
			t.Fatalf("expected guardAuditArch to be AUDIT_ARCH_X86_64 (0x%x), got 0x%x", unix.AUDIT_ARCH_X86_64, guardAuditArch)
		}
	case "arm64":
		if guardAuditArch != unix.AUDIT_ARCH_AARCH64 {
			t.Fatalf("expected guardAuditArch to be AUDIT_ARCH_AARCH64 (0x%x), got 0x%x", unix.AUDIT_ARCH_AARCH64, guardAuditArch)
		}
	default:
		t.Fatalf("unexpected GOARCH for filter_linux_test: %s", runtime.GOARCH)
	}

	filter := seccompFilter()
	if len(filter) < 3 {
		t.Fatalf("filter too short to contain architecture validation: %d instructions", len(filter))
	}

	// Instruction 0: load arch (offset 4)
	if filter[0].Code != (unix.BPF_LD|unix.BPF_W|unix.BPF_ABS) || filter[0].K != 4 {
		t.Errorf("instruction 0 must load seccomp_data.arch at offset 4: got code=0x%x K=%d", filter[0].Code, filter[0].K)
	}

	// Instruction 1: jump if arch == guardAuditArch (Jt: skip kill, Jf: fall into kill)
	if filter[1].Code != (unix.BPF_JMP|unix.BPF_JEQ|unix.BPF_K) || filter[1].K != guardAuditArch {
		t.Errorf("instruction 1 must compare arch with guardAuditArch (0x%x): got code=0x%x K=0x%x", guardAuditArch, filter[1].Code, filter[1].K)
	}
	if filter[1].Jt != 1 || filter[1].Jf != 0 {
		t.Errorf("instruction 1 jump targets invalid: Jt=%d (expected 1), Jf=%d (expected 0)", filter[1].Jt, filter[1].Jf)
	}

	// Instruction 2: kill process on architecture mismatch
	if filter[2].Code != (unix.BPF_RET|unix.BPF_K) || filter[2].K != unix.SECCOMP_RET_KILL_PROCESS {
		t.Errorf("instruction 2 must be SECCOMP_RET_KILL_PROCESS: got code=0x%x K=0x%x", filter[2].Code, filter[2].K)
	}
}

func TestStaticBPFProgramIntegrity(t *testing.T) {
	denied := deniedSyscalls()
	expectedLen := 6 + len(denied) + 2
	filter := seccompFilter()
	if len(filter) == 0 {
		t.Fatal("seccompFilter returned empty program")
	}
	if len(filter) > 4096 {
		t.Fatalf("filter length %d exceeds kernel BPF_MAXINSNS (4096)", len(filter))
	}
	if len(filter) != expectedLen {
		t.Errorf("expected filter length %d, got %d", expectedLen, len(filter))
	}

	// Verify all jump offsets stay within filter boundaries
	for i, inst := range filter {
		cls := inst.Code & 0x07
		if cls == unix.BPF_JMP {
			op := inst.Code & 0xf0
			if op == unix.BPF_JA {
				target := i + 1 + int(inst.K)
				if target >= len(filter) {
					t.Errorf("instruction %d: JA jump target %d out of bounds (len=%d)", i, target, len(filter))
				}
			} else {
				targetTrue := i + 1 + int(inst.Jt)
				targetFalse := i + 1 + int(inst.Jf)
				if targetTrue >= len(filter) {
					t.Errorf("instruction %d: true jump target %d out of bounds (len=%d)", i, targetTrue, len(filter))
				}
				if targetFalse >= len(filter) {
					t.Errorf("instruction %d: false jump target %d out of bounds (len=%d)", i, targetFalse, len(filter))
				}
			}
		}
	}

	// Last instruction must be a valid return
	last := filter[len(filter)-1]
	if (last.Code & unix.BPF_RET) == 0 {
		t.Errorf("filter terminal instruction is not a BPF_RET: code=0x%x", last.Code)
	}
}

func TestStaticBPFPolicyRules(t *testing.T) {
	denied := deniedSyscalls()
	expectedLen := 6 + len(denied) + 2
	filter := seccompFilter()
	if len(filter) != expectedLen {
		t.Fatalf("expected %d instructions, got %d", expectedLen, len(filter))
	}

	// Instruction 3: load syscall nr at offset 0
	if filter[3].Code != (unix.BPF_LD|unix.BPF_W|unix.BPF_ABS) || filter[3].K != 0 {
		t.Errorf("instruction 3 must load seccomp_data.nr at offset 0: got code=0x%x K=%d", filter[3].Code, filter[3].K)
	}

	// Instruction 4: test bit 30 (x32 ABI)
	if filter[4].Code != (unix.BPF_JMP|unix.BPF_JSET|unix.BPF_K) || filter[4].K != 0x40000000 {
		t.Errorf("instruction 4 must test bit 30 (0x40000000): got code=0x%x K=0x%x", filter[4].Code, filter[4].K)
	}
	if filter[4].Jt != 0 || filter[4].Jf != 1 {
		t.Errorf("instruction 4 jump targets invalid: Jt=%d (expected 0), Jf=%d (expected 1)", filter[4].Jt, filter[4].Jf)
	}

	// Instruction 5: kill process if bit 30 is set
	if filter[5].Code != (unix.BPF_RET|unix.BPF_K) || filter[5].K != unix.SECCOMP_RET_KILL_PROCESS {
		t.Errorf("instruction 5 must be SECCOMP_RET_KILL_PROCESS: got code=0x%x K=0x%x", filter[5].Code, filter[5].K)
	}

	seen := make(map[uint32]bool, len(denied))
	epermIdx := len(filter) - 1
	allowIdx := len(filter) - 2
	for i, inst := range filter[6:allowIdx] {
		if inst.Code != (unix.BPF_JMP | unix.BPF_JEQ | unix.BPF_K) {
			t.Fatalf("instruction %d must be BPF_JEQ: got code=0x%x", 6+i, inst.Code)
		}
		targetTrue := (6 + i) + 1 + int(inst.Jt)
		if targetTrue != epermIdx || inst.Jf != 0 {
			t.Fatalf("instruction %d jump targets invalid: targetTrue=%d (want %d), Jf=%d", 6+i, targetTrue, epermIdx, inst.Jf)
		}
		seen[inst.K] = true
	}

	requiredBlocked := []uint32{
		uint32(unix.SYS_SETSID),
		uint32(unix.SYS_SETPGID),
		uint32(unix.SYS_PTRACE),
		uint32(unix.SYS_PROCESS_VM_READV),
		uint32(unix.SYS_PROCESS_VM_WRITEV),
		uint32(unix.SYS_UNSHARE),
		uint32(unix.SYS_SETNS),
		uint32(unix.SYS_MOUNT),
		uint32(unix.SYS_BPF),
		uint32(unix.SYS_IO_URING_SETUP),
		uint32(unix.SYS_IO_URING_ENTER),
		uint32(unix.SYS_IO_URING_REGISTER),
		uint32(unix.SYS_USERFAULTFD),
		uint32(unix.SYS_KEYCTL),
		uint32(unix.SYS_KEXEC_LOAD),
		uint32(unix.SYS_INIT_MODULE),
		uint32(unix.SYS_PERSONALITY),
	}
	for _, nr := range requiredBlocked {
		if !seen[nr] {
			t.Errorf("required dangerous syscall %d missing from seccomp deny list", nr)
		}
	}
	if seen[uint32(unix.SYS_EXECVEAT)] {
		t.Error("SYS_EXECVEAT must not be in the deny list")
	}

	// Default ALLOW
	if filter[allowIdx].Code != (unix.BPF_RET|unix.BPF_K) || filter[allowIdx].K != unix.SECCOMP_RET_ALLOW {
		t.Errorf("instruction %d must be SECCOMP_RET_ALLOW: got code=0x%x K=0x%x", allowIdx, filter[allowIdx].Code, filter[allowIdx].K)
	}

	// Reject with EPERM
	expectedErrnoRet := unix.SECCOMP_RET_ERRNO | uint32(unix.EPERM)
	if filter[epermIdx].Code != (unix.BPF_RET|unix.BPF_K) || filter[epermIdx].K != expectedErrnoRet {
		t.Errorf("instruction %d must be SECCOMP_RET_ERRNO | EPERM (0x%x): got code=0x%x K=0x%x", epermIdx, expectedErrnoRet, filter[epermIdx].Code, filter[epermIdx].K)
	}
}

func TestKernelEnforcedAllowedOperations(t *testing.T) {
	_, out, err := runHelperSubprocess(t, "allowed_ops")
	if err != nil {
		t.Fatalf("allowed_ops subprocess failed: %v\nOutput: %s", err, string(out))
	}
	if !strings.Contains(string(out), "ALLOWED_OPS_CONFIRMED") {
		t.Fatalf("expected ALLOWED_OPS_CONFIRMED in output, got: %s", string(out))
	}
}

func TestKernelEnforcedBlockedSetsid(t *testing.T) {
	_, out, err := runHelperSubprocess(t, "blocked_setsid")
	if err != nil {
		t.Fatalf("blocked_setsid subprocess failed: %v\nOutput: %s", err, string(out))
	}
	if !strings.Contains(string(out), "BLOCKED_SETSID_EPERM") {
		t.Fatalf("expected BLOCKED_SETSID_EPERM in output, got: %s", string(out))
	}
}

func TestKernelEnforcedBlockedSetpgid(t *testing.T) {
	_, out, err := runHelperSubprocess(t, "blocked_setpgid")
	if err != nil {
		t.Fatalf("blocked_setpgid subprocess failed: %v\nOutput: %s", err, string(out))
	}
	if !strings.Contains(string(out), "BLOCKED_SETPGID_EPERM") {
		t.Fatalf("expected BLOCKED_SETPGID_EPERM in output, got: %s", string(out))
	}
}

func TestKernelEnforcedBlockedBit30(t *testing.T) {
	_, out, err := runHelperSubprocess(t, "blocked_bit30")
	if err == nil {
		t.Fatalf("expected bit30 subprocess to be killed, but exited cleanly: %s", string(out))
	}
	exitErr, ok := err.(*exec.ExitError)
	if !ok {
		t.Fatalf("expected *exec.ExitError, got %T: %v", err, err)
	}
	status, ok := exitErr.Sys().(syscall.WaitStatus)
	if !ok {
		t.Fatalf("expected syscall.WaitStatus, got %T", exitErr.Sys())
	}
	if !status.Signaled() {
		t.Fatalf("expected process to be terminated by signal, exited with code %d", exitErr.ExitCode())
	}
	if status.Signal() != syscall.SIGSYS {
		t.Fatalf("expected process to be killed by SIGSYS (%d), got signal %d", syscall.SIGSYS, status.Signal())
	}
}

func TestKernelEnforcedAuditArchMismatch(t *testing.T) {
	_, out, err := runHelperSubprocess(t, "arch_mismatch")
	if err == nil {
		t.Fatalf("expected arch_mismatch subprocess to be killed, but exited cleanly: %s", string(out))
	}
	exitErr, ok := err.(*exec.ExitError)
	if !ok {
		t.Fatalf("expected *exec.ExitError, got %T: %v", err, err)
	}
	status, ok := exitErr.Sys().(syscall.WaitStatus)
	if !ok {
		t.Fatalf("expected syscall.WaitStatus, got %T", exitErr.Sys())
	}
	if !status.Signaled() {
		t.Fatalf("expected process to be terminated by signal, exited with code %d", exitErr.ExitCode())
	}
	if status.Signal() != syscall.SIGSYS {
		t.Fatalf("expected process to be killed by SIGSYS (%d), got signal %d", syscall.SIGSYS, status.Signal())
	}
}

func TestKernelEnforcedBlockedDangerousSyscalls(t *testing.T) {
	_, out, err := runHelperSubprocess(t, "blocked_dangerous_syscalls")
	if err != nil {
		t.Fatalf("blocked_dangerous_syscalls subprocess failed: %v\nOutput: %s", err, string(out))
	}
	if !strings.Contains(string(out), "BLOCKED_DANGEROUS_EPERM") {
		t.Fatalf("expected BLOCKED_DANGEROUS_EPERM in output, got: %s", string(out))
	}
}

func TestCLISuccessfulExecution(t *testing.T) {
	target := getTestTargetBinary(t)
	guard := getGuardBinary(t)
	cmd := exec.Command(guard, target, "mcp-guard-test-pass")
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("mcp-guard execution failed: %v\nOutput: %s", err, string(out))
	}
	if !strings.Contains(string(out), "mcp-guard-test-pass") {
		t.Fatalf("expected 'mcp-guard-test-pass' in output, got: %q", string(out))
	}
}

func TestCLIExitCodePropagation(t *testing.T) {
	target := getTestTargetBinary(t)
	guard := getGuardBinary(t)
	cmd := exec.Command(guard, target, "--exit", "42")
	err := cmd.Run()
	if err == nil {
		t.Fatal("expected exit code 42, got exit 0")
	}
	exitErr, ok := err.(*exec.ExitError)
	if !ok {
		t.Fatalf("expected *exec.ExitError, got %T: %v", err, err)
	}
	if exitErr.ExitCode() != 42 {
		t.Fatalf("expected exit code 42, got %d", exitErr.ExitCode())
	}
}

func TestCLISymlinkAndWritableTargetRejected(t *testing.T) {
	guard := getGuardBinary(t)
	target := getTestTargetBinary(t)

	// 1. Direct symlink target must be rejected fail-closed.
	symDir := t.TempDir()
	symTarget := filepath.Join(symDir, "sym-target")
	if err := os.Symlink(target, symTarget); err != nil {
		t.Skipf("symlink creation unsupported: %v", err)
	}
	if out, err := exec.Command(guard, symTarget, "should-not-run").CombinedOutput(); err == nil {
		t.Fatalf("expected symlink target to be rejected, output: %s", string(out))
	}

	// 2. Intermediate symlinked parent directory must be rejected fail-closed.
	symParent := filepath.Join(symDir, "sym-parent")
	if err := os.Symlink(filepath.Dir(target), symParent); err != nil {
		t.Skipf("parent symlink creation unsupported: %v", err)
	}
	viaParent := filepath.Join(symParent, filepath.Base(target))
	if out, err := exec.Command(guard, viaParent, "should-not-run").CombinedOutput(); err == nil {
		t.Fatalf("expected symlink parent dir target to be rejected, output: %s", string(out))
	}

	// 3. Group/world-writable target executable must be rejected fail-closed.
	data, err := os.ReadFile(target)
	if err != nil {
		t.Fatal(err)
	}
	writableTarget := filepath.Join(symDir, "writable-target")
	if err := os.WriteFile(writableTarget, data, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(writableTarget, 0o775); err != nil {
		t.Fatal(err)
	}
	if out, err := exec.Command(guard, writableTarget, "should-not-run").CombinedOutput(); err == nil {
		t.Fatalf("expected group-writable target to be rejected, output: %s", string(out))
	}
}
