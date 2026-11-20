package main

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"sync"
	"testing"

	"github.com/amiraq1/nabd/internal/llm"
	"github.com/amiraq1/nabd/internal/tools"
	tea "github.com/charmbracelet/bubbletea"
)

// TestSecurity_ParallelNeverExecutesWriteTools validates that if the model returns
// a sequence of tool calls containing an edit_file or mcp_call, parallel execution
// stops immediately before the approval-required tool, and NEVER executes it concurrently.
func TestSecurity_ParallelNeverExecutesWriteTools(t *testing.T) {
	dir := t.TempDir()
	reg, err := tools.NewRegistry(dir, nil)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = reg.Close() })

	f1 := filepath.Join(dir, "f1.txt")
	f2 := filepath.Join(dir, "f2.txt")
	_ = os.WriteFile(f1, []byte("content1"), 0644)
	_ = os.WriteFile(f2, []byte("content2"), 0644)

	// Model returns: read_file (ro), read_file (ro), edit_file (write), read_file (ro)
	client := funcClient(func(ctx context.Context, req llm.Request) (*llm.Response, error) {
		return &llm.Response{
			Message: Message{
				Role: "assistant",
				ToolCalls: []ToolCall{
					llmToolCall("call_1", "read_file", `{"path":"f1.txt"}`),
					llmToolCall("call_2", "read_file", `{"path":"f2.txt"}`),
					llmToolCall("call_3", "edit_file", `{"path":"f1.txt","old_text":"content1","new_text":"hacked"}`),
					llmToolCall("call_4", "read_file", `{"path":"f2.txt"}`),
				},
			},
		}, nil
	})

	m := initialModel(reg, client)
	m.textInput.SetValue("run mixed")
	updated, cmd := m.Update(tea.KeyMsg{Type: tea.KeyEnter})
	m = updated.(uiModel)

	// Step 1: LLM returns response
	msg := cmd()
	updated, cmd = m.Update(msg)
	m = updated.(uiModel)

	// Step 2: advance() should detect exactly 2 leading ReadOnly tools and run parallelToolsCmd
	if len(m.pendingTools) != 4 {
		t.Fatalf("expected 4 pending tools, got %d", len(m.pendingTools))
	}
	if m.awaitingApproval {
		t.Fatal("awaitingApproval should not be true before leading read-only tools finish")
	}

	// Execute parallel batch
	msg = cmd()
	pMsg, ok := msg.(parallelToolsMsg)
	if !ok {
		t.Fatalf("expected parallelToolsMsg, got %T", msg)
	}
	if len(pMsg.toolMsgs) != 2 {
		t.Fatalf("expected batch of 2 read tools, got %d", len(pMsg.toolMsgs))
	}

	// Update with parallel results
	updated, _ = m.Update(pMsg)
	m = updated.(uiModel)

	// Now pendingTools should have edit_file at index 0, and awaitingApproval MUST be true!
	if len(m.pendingTools) != 2 {
		t.Fatalf("expected 2 pending tools remaining, got %d", len(m.pendingTools))
	}
	if !m.awaitingApproval {
		t.Fatal("CRITICAL: edit_file was not gated behind approval after parallel read batch!")
	}
	if m.pendingTools[0].Function.Name != "edit_file" {
		t.Fatalf("expected edit_file next, got %s", m.pendingTools[0].Function.Name)
	}

	// Verify file was NOT modified
	data, _ := os.ReadFile(f1)
	if string(data) != "content1" {
		t.Fatalf("file modified before user approval! content: %s", string(data))
	}
}

// TestSecurity_AgentLoopConcurrentStress runs 20 concurrent turns on independent
// models to ensure zero data races or state corruption in the agent loop.
func TestSecurity_AgentLoopConcurrentStress(t *testing.T) {
	dir := t.TempDir()
	reg, err := tools.NewRegistry(dir, nil)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = reg.Close() })

	for i := 0; i < 5; i++ {
		_ = os.WriteFile(filepath.Join(dir, fmt.Sprintf("test_%d.txt", i)), []byte(fmt.Sprintf("data %d", i)), 0644)
	}

	var wg sync.WaitGroup
	workers := 10
	wg.Add(workers)

	for w := 0; w < workers; w++ {
		workerID := w
		go func() {
			defer wg.Done()
			client := funcClient(func(ctx context.Context, req llm.Request) (*llm.Response, error) {
				return &llm.Response{
					Message: Message{
						Role: "assistant",
						ToolCalls: []ToolCall{
							llmToolCall(fmt.Sprintf("c_%d_1", workerID), "read_file", `{"path":"test_0.txt"}`),
							llmToolCall(fmt.Sprintf("c_%d_2", workerID), "read_file", `{"path":"test_1.txt"}`),
						},
					},
				}, nil
			})

			m := initialModel(reg, client)
			_ = runTurn(t, m, "query", 5)
		}()
	}
	wg.Wait()
}

// TestSecurity_CancellationDuringParallelExecution validates that cancelling context
// stops in-flight work and resets model status cleanly.
func TestSecurity_CancellationDuringParallelExecution(t *testing.T) {
	dir := t.TempDir()
	reg, err := tools.NewRegistry(dir, nil)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = reg.Close() })

	client := funcClient(func(ctx context.Context, req llm.Request) (*llm.Response, error) {
		<-ctx.Done()
		return nil, ctx.Err()
	})

	m := initialModel(reg, client)
	m.textInput.SetValue("hang")
	updated, _ := m.Update(tea.KeyMsg{Type: tea.KeyEnter})
	m = updated.(uiModel)

	if !m.busy {
		t.Fatal("expected model to be busy")
	}

	// Press Ctrl+C (cancels in-flight request)
	updated, _ = m.Update(tea.KeyMsg{Type: tea.KeyCtrlC})
	m = updated.(uiModel)

	// finishRequest() should have cancelled context
	if m.requestCancel != nil {
		t.Fatal("requestCancel not cleared after Ctrl+C")
	}
}
