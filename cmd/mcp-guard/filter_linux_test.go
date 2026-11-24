//go:build linux && (amd64 || arm64)

package main

import (
	"fmt"
	"os"
	"os/exec"
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
	filter := seccompFilter()
	if len(filter) == 0 {
		t.Fatal("seccompFilter returned empty program")
	}
	if len(filter) > 4096 {
		t.Fatalf("filter length %d exceeds kernel BPF_MAXINSNS (4096)", len(filter))
	}
	if len(filter) != 10 {
		t.Errorf("expected filter length 10, got %d", len(filter))
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
	filter := seccompFilter()
	if len(filter) != 10 {
		t.Fatalf("expected 10 instructions, got %d", len(filter))
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

	// Instruction 6: check SYS_SETSID
	if filter[6].Code != (unix.BPF_JMP|unix.BPF_JEQ|unix.BPF_K) || filter[6].K != uint32(unix.SYS_SETSID) {
		t.Errorf("instruction 6 must check SYS_SETSID (%d): got code=0x%x K=%d", unix.SYS_SETSID, filter[6].Code, filter[6].K)
	}
	if filter[6].Jt != 2 || filter[6].Jf != 0 {
		t.Errorf("instruction 6 jump targets invalid: Jt=%d (expected 2 to errno ret), Jf=%d (expected 0)", filter[6].Jt, filter[6].Jf)
	}

	// Instruction 7: check SYS_SETPGID
	if filter[7].Code != (unix.BPF_JMP|unix.BPF_JEQ|unix.BPF_K) || filter[7].K != uint32(unix.SYS_SETPGID) {
		t.Errorf("instruction 7 must check SYS_SETPGID (%d): got code=0x%x K=%d", unix.SYS_SETPGID, filter[7].Code, filter[7].K)
	}
	if filter[7].Jt != 1 || filter[7].Jf != 0 {
		t.Errorf("instruction 7 jump targets invalid: Jt=%d (expected 1 to errno ret), Jf=%d (expected 0)", filter[7].Jt, filter[7].Jf)
	}

	// Instruction 8: default ALLOW
	if filter[8].Code != (unix.BPF_RET|unix.BPF_K) || filter[8].K != unix.SECCOMP_RET_ALLOW {
		t.Errorf("instruction 8 must be SECCOMP_RET_ALLOW: got code=0x%x K=0x%x", filter[8].Code, filter[8].K)
	}

	// Instruction 9: reject with EPERM
	expectedErrnoRet := unix.SECCOMP_RET_ERRNO | uint32(unix.EPERM)
	if filter[9].Code != (unix.BPF_RET|unix.BPF_K) || filter[9].K != expectedErrnoRet {
		t.Errorf("instruction 9 must be SECCOMP_RET_ERRNO | EPERM (0x%x): got code=0x%x K=0x%x", expectedErrnoRet, filter[9].Code, filter[9].K)
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

func TestCLISuccessfulExecution(t *testing.T) {
	echoPath, err := exec.LookPath("echo")
	if err != nil {
		t.Fatalf("echo not found in PATH on Linux: %v", err)
	}
	guard := getGuardBinary(t)
	cmd := exec.Command(guard, echoPath, "mcp-guard-test-pass")
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("mcp-guard execution failed: %v\nOutput: %s", err, string(out))
	}
	if !strings.Contains(string(out), "mcp-guard-test-pass") {
		t.Fatalf("expected 'mcp-guard-test-pass' in output, got: %q", string(out))
	}
}

func TestCLIExitCodePropagation(t *testing.T) {
	shPath, err := exec.LookPath("sh")
	if err != nil {
		t.Fatalf("sh not found in PATH on Linux: %v", err)
	}
	guard := getGuardBinary(t)
	cmd := exec.Command(guard, shPath, "-c", "exit 42")
	err = cmd.Run()
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
