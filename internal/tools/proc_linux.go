//go:build linux

package tools

import (
	"bufio"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"time"

	"golang.org/x/sys/unix"
)

const (
	processTreeHelperArg = "--nabd-internal-process-tree-helper"
	processTreeEnvKey    = "NABD_INTERNAL_PROCESS_TREE_CONFIG"
	maxProcessTreeConfig = 256 << 10
	processTreeExitError = 125
)

type processTree struct {
	cmd      *exec.Cmd
	prepared bool
}

type processTreeConfig struct {
	Path string   `json:"path"`
	Args []string `json:"args"`
	Dir  string   `json:"dir"`
	Env  []string `json:"env"`
}

// A linked Nabd executable can be re-executed as an isolated supervisor. The
// hidden mode is reached only by configureProcessTree-created commands; it is
// intentionally in this Linux-only file so non-Linux builds cannot silently
// fall back to ordinary process killing.
func init() {
	if len(os.Args) > 1 && os.Args[1] == processTreeHelperArg {
		os.Exit(runProcessTreeHelper(os.Getenv(processTreeEnvKey)))
	}
}

// configureProcessTree arranges for each command to run beneath its own helper
// process. The helper is a child subreaper, so descendants that call setsid or
// setpgid, or outlive their original leader, remain attributable to that one
// managed command. Prepare must run before Start; Cleanup follows Wait/Close.
func configureProcessTree(cmd *exec.Cmd) *processTree {
	tree := &processTree{cmd: cmd}
	cmd.Cancel = tree.Kill
	return tree
}

// Prepare wraps cmd in a private supervisor. Unlike a process-wide subreaper,
// the helper has no unrelated children to confuse with the managed tree.
func (t *processTree) Prepare() error {
	if t.cmd == nil {
		return errors.New("process-tree supervision: nil command")
	}
	if t.prepared || t.cmd.Process != nil {
		return errors.New("process-tree supervision must be prepared once before Start")
	}
	if t.cmd.SysProcAttr != nil {
		return errors.New("process-tree supervision cannot preserve custom SysProcAttr; refusing to start command")
	}
	if t.cmd.Path == "" || len(t.cmd.Args) == 0 {
		return errors.New("process-tree supervision requires a resolved command path and argv")
	}
	if _, err := readLinuxProcesses(); err != nil {
		return fmt.Errorf("process-tree supervision requires readable /proc state: %w", err)
	}
	if err := probePidfd(os.Getpid()); err != nil {
		return fmt.Errorf("process-tree supervision requires pidfd signaling support: %w", err)
	}

	executable, err := os.Executable()
	if err != nil {
		return fmt.Errorf("locate process-tree supervisor executable: %w", err)
	}
	executable, err = filepath.Abs(executable)
	if err != nil {
		return fmt.Errorf("resolve process-tree supervisor executable: %w", err)
	}

	childEnv := t.cmd.Env
	if childEnv == nil {
		childEnv = os.Environ()
	}
	childEnv = withoutProcessTreeEnv(childEnv)
	config, err := json.Marshal(processTreeConfig{
		Path: t.cmd.Path,
		Args: append([]string(nil), t.cmd.Args...),
		Dir:  t.cmd.Dir,
		Env:  append([]string(nil), childEnv...),
	})
	if err != nil {
		return fmt.Errorf("encode process-tree command: %w", err)
	}
	encoded := base64.RawURLEncoding.EncodeToString(config)
	if len(encoded) > maxProcessTreeConfig {
		return fmt.Errorf("process-tree command configuration exceeds %d bytes", maxProcessTreeConfig)
	}

	// Keep the trusted helper's environment minimal. The intended command's
	// environment is separately serialized and passed only to that child.
	t.cmd.Path = executable
	t.cmd.Args = []string{executable, processTreeHelperArg}
	t.cmd.Env = []string{processTreeEnvKey + "=" + encoded}
	t.cmd.Cancel = t.Kill
	t.prepared = true
	return nil
}

// Kill asks the supervisor to terminate its managed descendants. Sending a
// signal to this direct child cannot target any unrelated process.
func (t *processTree) Kill() error {
	if t.cmd == nil || t.cmd.Process == nil || t.cmd.ProcessState != nil {
		return os.ErrProcessDone
	}
	if err := t.cmd.Process.Signal(syscall.SIGTERM); err != nil && !errors.Is(err, os.ErrProcessDone) {
		return fmt.Errorf("signal process-tree supervisor: %w", err)
	}
	return nil
}

// Cleanup is normally a no-op after Cmd.Wait or transport Close has completed.
// If a caller returns early with the helper still running, request its
// ownership-scoped cleanup rather than killing the helper abruptly.
func (t *processTree) Cleanup() error {
	if !t.prepared || t.cmd == nil || t.cmd.Process == nil || t.cmd.ProcessState != nil {
		return nil
	}
	return t.Kill()
}

func withoutProcessTreeEnv(env []string) []string {
	out := make([]string, 0, len(env))
	for _, item := range env {
		key, _, _ := strings.Cut(item, "=")
		if key != processTreeEnvKey {
			out = append(out, item)
		}
	}
	return out
}

func runProcessTreeHelper(encoded string) int {
	fail := func(format string, args ...any) int {
		_, _ = fmt.Fprintf(os.Stderr, "nabd process-tree helper: "+format+"\n", args...)
		return processTreeExitError
	}
	if encoded == "" || len(encoded) > maxProcessTreeConfig {
		return fail("missing or oversized command configuration")
	}
	payload, err := base64.RawURLEncoding.DecodeString(encoded)
	if err != nil {
		return fail("invalid command configuration: %v", err)
	}
	var config processTreeConfig
	if err := json.Unmarshal(payload, &config); err != nil || config.Path == "" || len(config.Args) == 0 {
		return fail("invalid command configuration")
	}
	if len(withoutProcessTreeEnv(config.Env)) != len(config.Env) {
		return fail("reserved helper environment variable in target command")
	}

	signals := make(chan os.Signal, 4)
	signal.Notify(signals, syscall.SIGTERM, syscall.SIGINT, syscall.SIGHUP)
	defer signal.Stop(signals)

	if err := unix.Prctl(unix.PR_SET_CHILD_SUBREAPER, 1, 0, 0, 0); err != nil {
		return fail("enable child subreaper: %v", err)
	}
	if err := probePidfd(os.Getpid()); err != nil {
		return fail("pidfd signaling unavailable: %v", err)
	}

	child := &exec.Cmd{
		Path:   config.Path,
		Args:   config.Args,
		Dir:    config.Dir,
		Env:    config.Env,
		Stdin:  os.Stdin,
		Stdout: os.Stdout,
		Stderr: os.Stderr,
	}
	if err := child.Start(); err != nil {
		return fail("start managed command: %v", err)
	}
	waitCh := make(chan error, 1)
	go func() { waitCh <- child.Wait() }()

	var childErr error
	cancelled := false
	select {
	case childErr = <-waitCh:
	case <-signals:
		cancelled = true
		if err := terminateOwnedChildren(os.Getpid(), child.Process.Pid); err != nil {
			return fail("terminate managed descendants: %v", err)
		}
		childErr = <-waitCh
	}

	// A successful command leader may have left descendants behind. They are
	// now children of this helper because it is the nearest subreaper.
	if err := terminateOwnedChildren(os.Getpid(), child.Process.Pid); err != nil {
		return fail("clean up managed descendants: %v", err)
	}
	if cancelled {
		return 128 + int(syscall.SIGTERM)
	}
	return commandExitCode(childErr)
}

func commandExitCode(err error) int {
	if err == nil {
		return 0
	}
	var exitErr *exec.ExitError
	if !errors.As(err, &exitErr) {
		return processTreeExitError
	}
	status, ok := exitErr.Sys().(syscall.WaitStatus)
	if !ok {
		return processTreeExitError
	}
	if status.Exited() {
		return status.ExitStatus()
	}
	if status.Signaled() {
		return 128 + int(status.Signal())
	}
	return processTreeExitError
}

func probePidfd(pid int) error {
	fd, err := unix.PidfdOpen(pid, 0)
	if err != nil {
		return err
	}
	defer unix.Close(fd)
	// Signal 0 performs permission/existence validation without delivering a
	// signal; it also proves the kernel supports the operation used at cleanup.
	return unix.PidfdSendSignal(fd, 0, nil, 0)
}

// terminateOwnedChildren kills only descendants of this private helper. Every
// target is opened with a pidfd and checked against its /proc start time before
// SIGKILL, preventing a recycled PID from turning an old ancestry snapshot into
// a signal to an unrelated process.
func terminateOwnedChildren(helperPID, rootPID int) error {
	const rounds = 200
	for round := 0; round < rounds; round++ {
		procs, err := readLinuxProcesses()
		if err != nil {
			return fmt.Errorf("inspect helper descendants: %w", err)
		}
		targets := descendants(procs, helperPID)
		alive := false
		for pid := range targets {
			info := procs[pid]
			if info.state == 'Z' || info.state == 'X' {
				continue
			}
			alive = true
			if err := signalProcessIdentity(pid, info.startTime, unix.SIGKILL); err != nil {
				return fmt.Errorf("kill owned descendant %d: %w", pid, err)
			}
		}

		// Reap adopted grandchildren. The command root is reaped by exec.Cmd's
		// Wait goroutine and must not be waited here.
		for pid := range directChildren(procs, helperPID) {
			if pid == rootPID {
				continue
			}
			info := procs[pid]
			if info.state == 'Z' || info.state == 'X' {
				var status syscall.WaitStatus
				_, _ = syscall.Wait4(pid, &status, syscall.WNOHANG, nil)
			}
		}
		if !alive {
			return nil
		}
		time.Sleep(10 * time.Millisecond)
	}
	return errors.New("owned descendants did not exit after forced termination")
}

func signalProcessIdentity(pid int, startTime uint64, sig unix.Signal) error {
	fd, err := unix.PidfdOpen(pid, 0)
	if errors.Is(err, unix.ESRCH) {
		return nil
	}
	if err != nil {
		return err
	}
	defer unix.Close(fd)

	current, ok, err := readLinuxProcess(pid)
	if err != nil {
		return err
	}
	if !ok || current.startTime != startTime {
		return nil
	}
	if err := unix.PidfdSendSignal(fd, sig, nil, 0); errors.Is(err, unix.ESRCH) {
		return nil
	} else if err != nil {
		return err
	}
	return nil
}

type linuxProcess struct {
	ppid      int
	state     byte
	startTime uint64
}

func readLinuxProcesses() (map[int]linuxProcess, error) {
	paths, err := filepath.Glob("/proc/[0-9]*/stat")
	if err != nil {
		return nil, err
	}
	procs := make(map[int]linuxProcess, len(paths))
	for _, path := range paths {
		pidText := filepath.Base(filepath.Dir(path))
		pid, err := strconv.Atoi(pidText)
		if err != nil {
			continue
		}
		info, ok, err := readLinuxProcess(pid)
		if err != nil {
			return nil, err
		}
		if ok {
			procs[pid] = info
		}
	}
	if len(procs) == 0 {
		return nil, errors.New("/proc did not report process state")
	}
	return procs, nil
}

func readLinuxProcess(pid int) (linuxProcess, bool, error) {
	path := filepath.Join("/proc", strconv.Itoa(pid), "stat")
	file, err := os.Open(path)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) || errors.Is(err, syscall.ESRCH) {
			return linuxProcess{}, false, nil
		}
		return linuxProcess{}, false, err
	}
	defer file.Close()

	line, err := bufio.NewReader(io.LimitReader(file, 64<<10)).ReadString('\n')
	if err != nil && !errors.Is(err, io.EOF) {
		return linuxProcess{}, false, err
	}
	closeAt := strings.LastIndexByte(line, ')')
	if closeAt <= 0 {
		return linuxProcess{}, false, fmt.Errorf("parse /proc/%d/stat: missing command terminator", pid)
	}
	fields := strings.Fields(line[closeAt+1:])
	if len(fields) <= 19 || len(fields[0]) != 1 {
		return linuxProcess{}, false, fmt.Errorf("parse /proc/%d/stat: incomplete process state", pid)
	}
	ppid, err := strconv.Atoi(fields[1])
	if err != nil {
		return linuxProcess{}, false, fmt.Errorf("parse /proc/%d/stat parent: %w", pid, err)
	}
	startTime, err := strconv.ParseUint(fields[19], 10, 64)
	if err != nil {
		return linuxProcess{}, false, fmt.Errorf("parse /proc/%d/stat start time: %w", pid, err)
	}
	return linuxProcess{ppid: ppid, state: fields[0][0], startTime: startTime}, true, nil
}

func descendants(procs map[int]linuxProcess, root int) map[int]struct{} {
	children := make(map[int][]int)
	for pid, info := range procs {
		children[info.ppid] = append(children[info.ppid], pid)
	}
	found := make(map[int]struct{})
	queue := []int{root}
	for len(queue) > 0 {
		parent := queue[0]
		queue = queue[1:]
		for _, child := range children[parent] {
			if _, seen := found[child]; seen {
				continue
			}
			found[child] = struct{}{}
			queue = append(queue, child)
		}
	}
	return found
}

func directChildren(procs map[int]linuxProcess, parent int) map[int]struct{} {
	children := make(map[int]struct{})
	for pid, info := range procs {
		if info.ppid == parent {
			children[pid] = struct{}{}
		}
	}
	return children
}
