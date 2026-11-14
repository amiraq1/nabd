//go:build linux && (amd64 || arm64)

package tools

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"
	"unicode/utf8"
)

const f24Helper = `package main
import ("bufio";"encoding/json";"fmt";"os";"os/exec";"path/filepath";"sort";"strings";"syscall";"time")
func main(){
 if len(os.Args)>1 { for {time.Sleep(time.Hour)} }
 os.Chdir(filepath.Dir(os.Args[0])); mode:=filepath.Base(os.Args[0])
 if strings.Contains(mode,"x32probe") { syscall.RawSyscall(0x40000070,0,0,0); os.WriteFile("x32-after",[]byte("executed"),0600);return }
 names:=[]string{}; for _,v:=range os.Environ(){ k,_,_:=strings.Cut(v,"="); names=append(names,k) }; sort.Strings(names); os.WriteFile("names",[]byte(strings.Join(names,"\n")),0600)
 if strings.Contains(mode,"earlyexit") {
  c:=exec.Command(os.Args[0],"child"); c.Stdout=os.Stdout
  if c.Start()==nil { os.WriteFile("pids",[]byte(fmt.Sprintf("%d %d",os.Getpid(),c.Process.Pid)),0600) }
  return
 }
 if strings.Contains(mode,"hang")||strings.Contains(mode,"error") {
  c:=exec.Command(os.Args[0],"child")
  if c.Start()==nil { os.WriteFile("pids",[]byte(fmt.Sprintf("%d %d",os.Getpid(),c.Process.Pid)),0600) }
 }
 if strings.Contains(mode,"detach") {
  c:=exec.Command(os.Args[0],"child")
  if strings.Contains(mode,"setsid") { c.SysProcAttr=&syscall.SysProcAttr{Setsid:true} }
  if strings.Contains(mode,"setpgid") { c.SysProcAttr=&syscall.SysProcAttr{Setpgid:true} }
  if err:=c.Start(); err!=nil { os.WriteFile("detach",[]byte("err:"+err.Error()),0600) } else { os.WriteFile("detach",[]byte("ok"),0600); os.WriteFile("pids",[]byte(fmt.Sprintf("%d %d",os.Getpid(),c.Process.Pid)),0600) }
 }
 sc:=bufio.NewScanner(os.Stdin)
 for sc.Scan() {
  var x struct{ ID json.RawMessage ` + "`json:\"id\"`" + `; Method string ` + "`json:\"method\"`" + ` }
  if json.Unmarshal(sc.Bytes(),&x)!=nil || len(x.ID)==0 { continue }
  id:=string(x.ID)
  switch x.Method {
  case "initialize": fmt.Printf("{\"jsonrpc\":\"2.0\",\"id\":%s,\"result\":{\"protocolVersion\":\"2025-11-25\",\"capabilities\":{},\"serverInfo\":{\"name\":\"probe\",\"version\":\"1\"}}}\n",id)
  case "tools/call":
   switch {
   case strings.Contains(mode,"hang"): for {time.Sleep(time.Hour)}
   case strings.Contains(mode,"error"): fmt.Printf("{\"jsonrpc\":\"2.0\",\"id\":%s,\"error\":{\"code\":-32603,\"message\":\"probe failure\"}}\n",id)
   case strings.Contains(mode,"large"): text,_:=json.Marshal(strings.Repeat("X",2<<20)); fmt.Printf("{\"jsonrpc\":\"2.0\",\"id\":%s,\"result\":{\"content\":[{\"type\":\"text\",\"text\":%s}]}}\n",id,text)
   case strings.Contains(mode,"medium"): text,_:=json.Marshal(strings.Repeat("X",20<<10)); fmt.Printf("{\"jsonrpc\":\"2.0\",\"id\":%s,\"result\":{\"content\":[{\"type\":\"text\",\"text\":%s}]}}\n",id,text)
   default: text,_:=json.Marshal(strings.Join(names,"\n")); fmt.Printf("{\"jsonrpc\":\"2.0\",\"id\":%s,\"result\":{\"content\":[{\"type\":\"text\",\"text\":%s}]}}\n",id,text)
   }
  }
 }
}`

func f24Probe(t *testing.T, mode string) (string, string) {
	t.Helper()
	dir := t.TempDir()
	src := filepath.Join(dir, "probe.go")
	if err := os.WriteFile(src, []byte(f24Helper), 0600); err != nil {
		t.Fatal(err)
	}
	bin := filepath.Join(dir, mode+" ; literal")
	cmd := exec.Command("go", "build", "-o", bin, src)
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("build probe: %v\n%s", err, out)
	}
	return dir, bin
}

func TestF24IntegrationRealMCPAndEnvironment(t *testing.T) {
	dir, bin := f24Probe(t, "env")
	t.Setenv("NABD_API_KEY", "synthetic-only")
	t.Setenv("F24_PRIVATE_TOKEN", "synthetic-only")
	r := mustRegistry(t, dir)
	args, _ := json.Marshal(map[string]any{"command": bin, "tool": "probe"})
	approval, err := r.Approve("mcp_call", args)
	if err != nil {
		t.Fatal(err)
	}
	out := r.RunApproved(context.Background(), "mcp_call", args, approval)
	if strings.Contains(out, "Error:") {
		t.Fatalf("real MCP request failed: %s", out)
	}
	if !strings.Contains(out, "PATH") {
		t.Fatal("probe did not return environment names")
	}
	names, err := os.ReadFile(filepath.Join(dir, "names"))
	if err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"NABD_API_KEY", "F24_PRIVATE_TOKEN"} {
		if strings.Contains(string(names), name) {
			t.Errorf("child inherited private environment name: %s", name)
		}
	}
}

func f24Run(t *testing.T, dir, bin string, ctx context.Context) string {
	t.Helper()
	r := mustRegistry(t, dir)
	args, _ := json.Marshal(map[string]any{"command": bin, "tool": "probe"})
	a, err := r.Approve("mcp_call", args)
	if err != nil {
		t.Fatal(err)
	}
	return r.RunApproved(ctx, "mcp_call", args, a)
}
func f24PIDs(t *testing.T, dir string) []int {
	t.Helper()
	b, err := os.ReadFile(filepath.Join(dir, "pids"))
	if err != nil {
		t.Fatalf("probe did not record pids: %v", err)
	}
	var pids []int
	for _, v := range strings.Fields(string(b)) {
		pid, err := strconv.Atoi(v)
		if err != nil {
			t.Fatalf("bad pid %q", v)
		}
		pids = append(pids, pid)
	}
	return pids
}

func f24Live(pid int) bool {
	b, err := os.ReadFile(fmt.Sprintf("/proc/%d/stat", pid))
	if err != nil {
		return syscall.Kill(pid, 0) == nil
	}
	parts := strings.Fields(string(b))
	return len(parts) < 3 || parts[2] != "Z"
}

func f24WaitDead(t *testing.T, pids []int) {
	t.Helper()
	for _, pid := range pids {
		pid := pid
		t.Cleanup(func() { _ = syscall.Kill(pid, syscall.SIGKILL) })
		deadline := time.Now().Add(2 * time.Second)
		for f24Live(pid) && time.Now().Before(deadline) {
			time.Sleep(20 * time.Millisecond)
		}
		if f24Live(pid) {
			t.Errorf("process %d survived", pid)
		}
	}
}

func f24CleanupPIDs(t *testing.T, dir string) {
	t.Helper()
	b, err := os.ReadFile(filepath.Join(dir, "pids"))
	if err != nil {
		return
	}
	for _, v := range strings.Fields(string(b)) {
		pid, err := strconv.Atoi(v)
		if err != nil {
			continue
		}
		t.Cleanup(func() { _ = syscall.Kill(pid, syscall.SIGKILL) })
	}
}

func TestF24RealMCPCallAndEnvironment(t *testing.T) {
	dir, bin := f24Probe(t, "env")
	t.Setenv("NABD_API_KEY", "f24-secret")
	t.Setenv("F24_PRIVATE_TOKEN", "f24-secret")

	out := f24Run(t, dir, bin, context.Background())
	if strings.Contains(out, "Error:") {
		t.Fatalf("real MCP call failed: %s", out)
	}
	if !strings.Contains(out, "PATH") {
		t.Fatalf("call did not return the probe environment: %q", out)
	}
	names, err := os.ReadFile(filepath.Join(dir, "names"))
	if err != nil {
		t.Fatal(err)
	}
	for _, private := range []string{"NABD_API_KEY", "F24_PRIVATE_TOKEN"} {
		if strings.Contains(string(names), private) {
			t.Errorf("private environment name inherited: %s", private)
		}
	}
}

func TestF24SeccompBlocksSetsid(t *testing.T) {
	dir, bin := f24Probe(t, "detach-setsid")
	out := f24Run(t, dir, bin, context.Background())
	f24CleanupPIDs(t, dir)
	data, err := os.ReadFile(filepath.Join(dir, "detach"))
	if err != nil {
		t.Fatalf("detach probe did not run: %v (out=%q)", err, out)
	}
	if strings.HasPrefix(string(data), "ok") {
		t.Fatal("setsid was not blocked by seccomp")
	}
	if !strings.HasPrefix(string(data), "err") || !strings.Contains(string(data), "operation not permitted") {
		t.Fatalf("unexpected detach result: %q", data)
	}
}

func TestF24SeccompBlocksSetpgid(t *testing.T) {
	dir, bin := f24Probe(t, "detach-setpgid")
	out := f24Run(t, dir, bin, context.Background())
	f24CleanupPIDs(t, dir)
	data, err := os.ReadFile(filepath.Join(dir, "detach"))
	if err != nil {
		t.Fatalf("detach probe did not run: %v (out=%q)", err, out)
	}
	if strings.HasPrefix(string(data), "ok") {
		t.Fatal("setpgid was not blocked by seccomp")
	}
	if !strings.HasPrefix(string(data), "err") || !strings.Contains(string(data), "operation not permitted") {
		t.Fatalf("unexpected detach result: %q", data)
	}
}

func TestF24TimeoutKillsTree(t *testing.T) {
	dir, bin := f24Probe(t, "hang")
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	out := f24Run(t, dir, bin, ctx)
	if !strings.Contains(out, "Error:") {
		t.Fatalf("expected a timeout error, got %q", out)
	}
	f24WaitDead(t, f24PIDs(t, dir))
}

func TestF24ClientErrorKillsTree(t *testing.T) {
	dir, bin := f24Probe(t, "error")
	out := f24Run(t, dir, bin, context.Background())
	if !strings.Contains(out, "Error:") {
		t.Fatalf("expected a client error, got %q", out)
	}
	f24WaitDead(t, f24PIDs(t, dir))
}

func TestF24WireLimit(t *testing.T) {
	dir, bin := f24Probe(t, "large")
	out := f24Run(t, dir, bin, context.Background())
	if !strings.Contains(out, "1 MiB") && !strings.Contains(out, "wire limit") {
		t.Fatalf("oversized response was not rejected clearly: %q", out)
	}
}

func TestF24TextCap(t *testing.T) {
	dir, bin := f24Probe(t, "medium")
	out, err := callExternalMCP(context.Background(), dir, bin, "probe", nil)
	if err != nil {
		t.Fatalf("call failed: %v", err)
	}
	if len(out) > maxMCPResponseBytes+64 {
		t.Fatalf("output length %d exceeds the text cap", len(out))
	}
	if !strings.Contains(out, "mcp output truncated") {
		t.Fatal("missing text truncation marker")
	}
	if !utf8.ValidString(out) {
		t.Fatal("truncated output is not valid UTF-8")
	}
}

func TestF24GuardUnavailable(t *testing.T) {
	dir, bin := f24Probe(t, "env")
	old := mcpGuardOverride
	mcpGuardOverride = filepath.Join(t.TempDir(), "missing-guard")
	t.Cleanup(func() { mcpGuardOverride = old })
	_, err := callExternalMCP(context.Background(), dir, bin, "probe", nil)
	if err == nil || !strings.Contains(err.Error(), "unavailable") {
		t.Fatalf("missing guard did not fail closed: %v", err)
	}
	if _, statErr := os.Stat(filepath.Join(dir, "names")); statErr == nil {
		t.Fatal("target ran without the guard")
	}
}

func TestF24GuardFailure(t *testing.T) {
	dir, bin := f24Probe(t, "env")
	guard := filepath.Join(t.TempDir(), "guard.sh")
	if err := os.WriteFile(guard, []byte("#!/bin/sh\nexit 1\n"), 0755); err != nil {
		t.Fatal(err)
	}
	old := mcpGuardOverride
	mcpGuardOverride = guard
	t.Cleanup(func() { mcpGuardOverride = old })
	_, err := callExternalMCP(context.Background(), dir, bin, "probe", nil)
	if err == nil {
		t.Fatal("failing guard did not refuse the call")
	}
	if _, statErr := os.Stat(filepath.Join(dir, "names")); statErr == nil {
		t.Fatal("target ran after the guard failed")
	}
}

func TestF24TimeoutConstant(t *testing.T) {
	if mcpCallTimeout != 60*time.Second {
		t.Fatalf("mcpCallTimeout = %v, want 60s", mcpCallTimeout)
	}
}

func TestF24EarlyExitKillsChildHoldingStdout(t *testing.T) {
	dir, bin := f24Probe(t, "earlyexit")
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	start := time.Now()
	out := f24Run(t, dir, bin, ctx)
	f24CleanupPIDs(t, dir)
	if !strings.Contains(out, "Error:") {
		t.Fatal("early server exit was not reported")
	}
	if time.Since(start) > 2*time.Second {
		t.Fatal("early server exit stalled cleanup")
	}
	f24WaitDead(t, f24PIDs(t, dir))
}
func TestF24CanceledContextKillsTree(t *testing.T) {
	dir, bin := f24Probe(t, "hang")
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	r := mustRegistry(t, dir)
	args, _ := json.Marshal(map[string]any{"command": bin, "tool": "probe"})
	approval, err := r.Approve("mcp_call", args)
	if err != nil {
		t.Fatal(err)
	}
	done := make(chan string, 1)
	go func() { done <- r.RunApproved(ctx, "mcp_call", args, approval) }()
	deadline := time.Now().Add(5 * time.Second)
	for {
		if _, err := os.Stat(filepath.Join(dir, "pids")); err == nil {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("probe did not start")
		}
		time.Sleep(10 * time.Millisecond)
	}
	cancel()
	select {
	case out := <-done:
		if !strings.Contains(out, "canceled") {
			t.Fatal("cancellation was not distinguished")
		}
	case <-time.After(6 * time.Second):
		t.Fatal("cancel did not finish cleanup")
	}
	f24WaitDead(t, f24PIDs(t, dir))
}
func TestF24EnvironmentCannotOverrideGuard(t *testing.T) {
	old := mcpGuardOverride
	mcpGuardOverride = ""
	t.Cleanup(func() { mcpGuardOverride = old })
	path := filepath.Join(t.TempDir(), "alternate-guard")
	if err := os.WriteFile(path, []byte("#!/bin/sh\nexit 0\n"), 0755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("NABD_MCP_GUARD", path)
	found, err := mcpGuardPath()
	if err == nil && found == path {
		t.Fatal("environment substituted the trusted guard")
	}
}
func TestF24RealSixtySecondTimeout(t *testing.T) {
	if testing.Short() {
		t.Skip("actual 60-second timeout")
	}
	dir, bin := f24Probe(t, "hang")
	start := time.Now()
	out := f24Run(t, dir, bin, context.Background())
	elapsed := time.Since(start)
	if !strings.Contains(out, "Error:") {
		t.Fatal("expected timeout")
	}
	if elapsed < 58*time.Second || elapsed > 68*time.Second {
		t.Fatalf("actual timeout outside tolerance: %v", elapsed)
	}
	f24WaitDead(t, f24PIDs(t, dir))
}

// Simulates an ID reused by an unrelated process after the guard is reaped.
// Cleanup must not signal a group once it has handed the child to Wait.
func TestF24CleanupNeverSignalsAfterReap(t *testing.T) {
	cmd := exec.Command("sleep", "30")
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = syscall.Kill(-cmd.Process.Pid, syscall.SIGKILL); _ = cmd.Wait() })
	p := &mcpProcess{cmd: cmd, reaped: true}
	p.terminate()
	time.Sleep(60 * time.Millisecond)
	if !f24Live(cmd.Process.Pid) {
		t.Fatal("cleanup killed an unrelated process group after reap")
	}
}
func TestF24AlreadyCancelledStartsNothing(t *testing.T) {
	dir, bin := f24Probe(t, "env")
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	_, err := callExternalMCP(ctx, dir, bin, "probe", nil)
	if err == nil {
		t.Fatal("already canceled call succeeded")
	}
	if _, err := os.Stat(filepath.Join(dir, "names")); !os.IsNotExist(err) {
		t.Fatal("server ran after cancellation")
	}
}

func TestF24GuardRejectsX32ABI(t *testing.T) {
	if runtime.GOARCH != "amd64" {
		t.Skip("x32 is specific to amd64")
	}
	dir, bin := f24Probe(t, "x32probe")
	// Some hosts already prohibit x32. Without a working baseline, do not claim
	// that this filter caused the rejection.
	if err := exec.Command(bin).Run(); err != nil {
		t.Skip("host already blocks the x32 probe")
	}
	marker := filepath.Join(dir, "x32-after")
	if _, err := os.Stat(marker); err != nil {
		t.Skip("host did not complete the x32 probe")
	}
	if err := os.Remove(marker); err != nil {
		t.Fatal(err)
	}
	_ = f24Run(t, dir, bin, context.Background())
	if _, err := os.Stat(marker); !os.IsNotExist(err) {
		t.Fatal("x32 ABI bypassed the guard")
	}
}
