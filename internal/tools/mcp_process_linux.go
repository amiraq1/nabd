//go:build linux

package tools

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"sync"
	"syscall"
	"time"

	"golang.org/x/sys/unix"
)

// A waitable leader reserves its PID/PGID until after the group is killed.
// The mutex prevents cancellation or cleanup from signalling after reaping.
type mcpProcess struct {
	cmd        *exec.Cmd
	mu         sync.Mutex
	reaped     bool
	done       chan struct{}
	waitErr    error
	stopCancel func() bool
}

func startMCPProcess(ctx context.Context, guard, dir, target string) (*mcpProcess, *os.File, *os.File, error) {
	cmd := exec.Command(guard, target)
	cmd.Dir = dir
	cmd.Env = mcpEnvironment()
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	inR, inW, err := os.Pipe()
	if err != nil {
		return nil, nil, nil, err
	}
	outR, outW, err := os.Pipe()
	if err != nil {
		inR.Close()
		inW.Close()
		return nil, nil, nil, err
	}
	cmd.Stdin = inR
	cmd.Stdout = outW
	cmd.Stderr = nil
	if err = cmd.Start(); err != nil {
		inR.Close()
		inW.Close()
		outR.Close()
		outW.Close()
		return nil, nil, nil, fmt.Errorf("failed to start MCP guard: %w", err)
	}
	inR.Close()
	outW.Close()
	p := &mcpProcess{cmd: cmd, done: make(chan struct{})}
	p.stopCancel = context.AfterFunc(ctx, p.terminate)
	go p.watch()
	return p, inW, outR, nil
}
func (p *mcpProcess) terminate() {
	p.mu.Lock()
	defer p.mu.Unlock()
	if !p.reaped {
		_ = syscall.Kill(-p.cmd.Process.Pid, syscall.SIGKILL)
	}
}
func (p *mcpProcess) watch() {
	var info unix.Siginfo
	var err error
	for {
		err = unix.Waitid(unix.P_PID, p.cmd.Process.Pid, &info, unix.WEXITED|unix.WNOWAIT, nil)
		if err != unix.EINTR {
			break
		}
	}
	// If waitid is unsupported or fails, fail closed by killing before Wait.
	p.mu.Lock()
	if err != nil {
		p.waitErr = fmt.Errorf("waiting for MCP guard: %w", err)
	}
	_ = syscall.Kill(-p.cmd.Process.Pid, syscall.SIGKILL)
	// No further signalling once reaping can begin. Wait is outside the lock,
	// so a stuck Wait cannot block the bounded shutdown path.
	p.reaped = true
	p.mu.Unlock()
	_ = p.cmd.Wait()
	close(p.done)
}
func (p *mcpProcess) closeAndWait() error {
	p.terminate()
	p.stopCancel()
	select {
	case <-p.done:
		p.mu.Lock()
		defer p.mu.Unlock()
		return p.waitErr
	case <-time.After(4 * time.Second):
		return fmt.Errorf("timed out waiting for MCP process cleanup")
	}
}
