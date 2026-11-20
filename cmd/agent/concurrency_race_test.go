package main

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"

	"github.com/amiraq1/nabd/internal/tools"
)

// TestConcurrentApprovalTokenConsumption verifies that multiple goroutines
// racing to consume the exact same approval token result in strictly ONE success.
// All other attempts must be rejected (single-use guarantee).
func TestConcurrentApprovalTokenConsumption(t *testing.T) {
	dir := t.TempDir()
	reg, err := tools.NewRegistry(dir)
	if err != nil {
		t.Fatal(err)
	}
	defer reg.Close()

	testFile := "target.txt"
	_ = os.WriteFile(filepath.Join(dir, testFile), []byte("hello world"), 0644)

	args := json.RawMessage(fmt.Sprintf(`{"path":%q,"old_text":"hello","new_text":"hi"}`, testFile))
	preview, err := reg.PreviewApproval("edit_file", args)
	if err != nil {
		t.Fatalf("PreviewApproval failed: %v", err)
	}
	approval, err := reg.ApprovePreview("edit_file", args, preview.State)
	if err != nil {
		t.Fatalf("ApprovePreview failed: %v", err)
	}

	const contestants = 20
	var wg sync.WaitGroup
	var successCount int64

	for i := 0; i < contestants; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			res := reg.RunApproved(context.Background(), "edit_file", args, approval)
			if !strings.HasPrefix(res, "Error:") {
				atomic.AddInt64(&successCount, 1)
			}
		}()
	}

	wg.Wait()

	if successCount != 1 {
		t.Fatalf("expected exactly 1 successful execution, got %d", successCount)
	}
}

// TestParallelReadOnlyToolExecution verifies that running parallel read tools
// concurrently returns exact results with no data races or cross-goroutine corruption.
func TestParallelReadOnlyToolExecution(t *testing.T) {
	dir := t.TempDir()
	reg, err := tools.NewRegistry(dir)
	if err != nil {
		t.Fatal(err)
	}
	defer reg.Close()

	for i := 0; i < 10; i++ {
		_ = os.WriteFile(filepath.Join(dir, fmt.Sprintf("read_%d.txt", i)), []byte(fmt.Sprintf("content_%d", i)), 0644)
	}

	calls := make([]ToolCall, 10)
	for i := 0; i < 10; i++ {
		calls[i] = ToolCall{
			ID: fmt.Sprintf("call_%d", i),
			Function: struct {
				Name      string `json:"name"`
				Arguments string `json:"arguments"`
			}{
				Name:      "read_file",
				Arguments: fmt.Sprintf(`{"path":"read_%d.txt"}`, i),
			},
		}
	}

	cmd := parallelToolsCmd(context.Background(), reg, calls)
	msg := cmd().(parallelToolsMsg)

	if len(msg.toolMsgs) != 10 {
		t.Fatalf("expected 10 tool messages, got %d", len(msg.toolMsgs))
	}

	for i, tm := range msg.toolMsgs {
		expectedID := fmt.Sprintf("call_%d", i)
		expectedContent := fmt.Sprintf("content_%d", i)
		if tm.ToolCallID != expectedID {
			t.Errorf("msg %d ID mismatch: got %s, want %s", i, tm.ToolCallID, expectedID)
		}
		if tm.Content != expectedContent {
			t.Errorf("msg %d content mismatch: got %q, want %q", i, tm.Content, expectedContent)
		}
	}
}

// TestCancellationDuringParallelExecution verifies that cancelling the context
// during parallel execution cleanly terminates execution without hanging or leaking goroutines.
func TestCancellationDuringParallelExecution(t *testing.T) {
	dir := t.TempDir()
	reg, err := tools.NewRegistry(dir)
	if err != nil {
		t.Fatal(err)
	}
	defer reg.Close()

	for i := 0; i < 5; i++ {
		_ = os.WriteFile(filepath.Join(dir, fmt.Sprintf("cancel_%d.txt", i)), []byte("sample"), 0644)
	}

	calls := make([]ToolCall, 5)
	for i := 0; i < 5; i++ {
		calls[i] = ToolCall{
			ID: fmt.Sprintf("call_%d", i),
			Function: struct {
				Name      string `json:"name"`
				Arguments string `json:"arguments"`
			}{
				Name:      "read_file",
				Arguments: fmt.Sprintf(`{"path":"cancel_%d.txt"}`, i),
			},
		}
	}

	ctx, cancel := context.WithCancel(context.Background())
	cancel() // Cancel immediately

	cmd := parallelToolsCmd(ctx, reg, calls)
	msg := cmd().(parallelToolsMsg)

	if len(msg.toolMsgs) != 5 {
		t.Fatalf("expected 5 tool messages, got %d", len(msg.toolMsgs))
	}
}
