package tools

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"sync"
	"testing"
	"time"
)

// TestMCPPooledSessionConcurrency verifies that multiple concurrent callers
// accessing the same MCP pooled session are safely serialized via session mutex
// and do not race or deadlock.
func TestMCPPooledSessionConcurrency(t *testing.T) {
	dir := t.TempDir()
	reg, err := NewRegistry(dir)
	if err != nil {
		t.Fatal(err)
	}
	defer reg.Close()

	exe := "mcp-server"
	if runtime.GOOS == "windows" {
		exe = "mcp-server.exe"
	}
	serverBin := filepath.Join(dir, exe)
	cmd := exec.Command("go", "build", "-o", serverBin, "../../cmd/mcp-server")
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("failed to build mcp-server: %v\n%s", err, string(out))
	}

	// Create test file
	_ = os.WriteFile(filepath.Join(dir, "data.txt"), []byte("nabd test content"), 0644)

	const goroutines = 10
	var wg sync.WaitGroup
	errCh := make(chan error, goroutines)

	for i := 0; i < goroutines; i++ {
		wg.Add(1)
		go func(id int) {
			defer wg.Done()
			ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
			defer cancel()

			c, cleanup, err := reg.getOrStartMCPSession(ctx, serverBin)
			if err != nil {
				errCh <- fmt.Errorf("g %d getOrStartMCPSession error: %w", id, err)
				return
			}
			defer cleanup()

			if c == nil {
				errCh <- fmt.Errorf("g %d client is nil", id)
				return
			}
		}(i)
	}

	wg.Wait()
	close(errCh)
	for err := range errCh {
		t.Errorf("concurrency error: %v", err)
	}
}

// TestMCPSessionEviction verifies that when a pooled session encounters a failure
// and is evicted, concurrent callers safely restart or handle the eviction without
// deadlocks or map data races.
func TestMCPSessionEviction(t *testing.T) {
	dir := t.TempDir()
	reg, err := NewRegistry(dir)
	if err != nil {
		t.Fatal(err)
	}
	defer reg.Close()

	exe := "mcp-server"
	if runtime.GOOS == "windows" {
		exe = "mcp-server.exe"
	}
	serverBin := filepath.Join(dir, exe)
	cmd := exec.Command("go", "build", "-o", serverBin, "../../cmd/mcp-server")
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("failed to build mcp-server: %v\n%s", err, string(out))
	}

	const callers = 8
	var wg sync.WaitGroup

	for i := 0; i < callers; i++ {
		wg.Add(1)
		go func(id int) {
			defer wg.Done()
			ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
			defer cancel()

			_, cleanup, err := reg.getOrStartMCPSession(ctx, serverBin)
			if err != nil {
				return
			}
			if id%2 == 0 {
				// Simulate failure and eviction while holding session lock
				reg.mcpSessionsMu.Lock()
				if sess, ok := reg.mcpSessions[serverBin]; ok {
					sess.closeLocked()
					delete(reg.mcpSessions, serverBin)
				}
				reg.mcpSessionsMu.Unlock()
			}
			cleanup()
		}(i)
	}

	wg.Wait()
}

// TestRegistryShutdownDuringActiveSessions verifies that calling Registry.Close()
// while active read tools and MCP sessions are running cleanly cancels operations
// without panics, data races, or process leaks.
func TestRegistryShutdownDuringActiveSessions(t *testing.T) {
	dir := t.TempDir()
	for i := 0; i < 20; i++ {
		_ = os.WriteFile(filepath.Join(dir, fmt.Sprintf("f%d.txt", i)), []byte(fmt.Sprintf("sample line %d", i)), 0644)
	}

	reg, err := NewRegistry(dir)
	if err != nil {
		t.Fatal(err)
	}

	var wg sync.WaitGroup
	const workers = 20

	// Launch read tool workers
	for i := 0; i < workers; i++ {
		wg.Add(1)
		go func(id int) {
			defer wg.Done()
			for j := 0; j < 5; j++ {
				_ = reg.Run(context.Background(), "read_file", json.RawMessage(fmt.Sprintf(`{"path":"f%d.txt"}`, id%20)))
				_ = reg.Run(context.Background(), "search_files", json.RawMessage(`{"pattern":"sample"}`))
				time.Sleep(1 * time.Millisecond)
			}
		}(i)
	}

	// Concurrently close the registry
	time.Sleep(5 * time.Millisecond)
	_ = reg.Close()

	wg.Wait()

	// Calling Close() again must be safe and idempotent
	if err := reg.Close(); err != nil {
		t.Errorf("second Close() returned error: %v", err)
	}
}
