package main

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/amiraq1/nabd/internal/tools"
)

func setupBenchRegistry(b *testing.B, numFiles int) (*tools.Registry, []ToolCall) {
	b.Helper()
	dir := b.TempDir()
	reg, err := tools.NewRegistry(dir, nil)
	if err != nil {
		b.Fatal(err)
	}
	b.Cleanup(func() { _ = reg.Close() })

	calls := make([]ToolCall, numFiles)
	for i := 0; i < numFiles; i++ {
		fname := fmt.Sprintf("file_%d.txt", i)
		content := fmt.Sprintf("line 1 in file %d\nline 2 in file %d\nline 3 in file %d\n", i, i, i)
		if err := os.WriteFile(filepath.Join(dir, fname), []byte(content), 0644); err != nil {
			b.Fatal(err)
		}
		calls[i] = ToolCall{
			ID:   fmt.Sprintf("call_%d", i),
			Type: "function",
			Function: struct {
				Name      string `json:"name"`
				Arguments string `json:"arguments"`
			}{
				Name:      "read_file",
				Arguments: fmt.Sprintf(`{"path":"%s"}`, fname),
			},
		}
	}
	return reg, calls
}

func runSequential(reg *tools.Registry, batch []ToolCall) []string {
	results := make([]string, len(batch))
	for i, tc := range batch {
		results[i] = reg.Run(context.Background(), tc.Function.Name, json.RawMessage(tc.Function.Arguments))
	}
	return results
}

func runParallel(reg *tools.Registry, batch []ToolCall) []string {
	cmd := parallelToolsCmd(context.Background(), reg, batch)
	msg := cmd().(parallelToolsMsg)
	results := make([]string, len(msg.toolMsgs))
	for i, m := range msg.toolMsgs {
		results[i] = m.Content
	}
	return results
}

func BenchmarkReadOnlyTools_Sequential_2(b *testing.B) {
	reg, calls := setupBenchRegistry(b, 2)
	b.ResetTimer()
	b.ReportAllocs()
	for i := 0; i < b.N; i++ {
		_ = runSequential(reg, calls)
	}
}

func BenchmarkReadOnlyTools_Parallel_2(b *testing.B) {
	reg, calls := setupBenchRegistry(b, 2)
	b.ResetTimer()
	b.ReportAllocs()
	for i := 0; i < b.N; i++ {
		_ = runParallel(reg, calls)
	}
}

func BenchmarkReadOnlyTools_Sequential_5(b *testing.B) {
	reg, calls := setupBenchRegistry(b, 5)
	b.ResetTimer()
	b.ReportAllocs()
	for i := 0; i < b.N; i++ {
		_ = runSequential(reg, calls)
	}
}

func BenchmarkReadOnlyTools_Parallel_5(b *testing.B) {
	reg, calls := setupBenchRegistry(b, 5)
	b.ResetTimer()
	b.ReportAllocs()
	for i := 0; i < b.N; i++ {
		_ = runParallel(reg, calls)
	}
}

func BenchmarkReadOnlyTools_Sequential_10(b *testing.B) {
	reg, calls := setupBenchRegistry(b, 10)
	b.ResetTimer()
	b.ReportAllocs()
	for i := 0; i < b.N; i++ {
		_ = runSequential(reg, calls)
	}
}

func BenchmarkReadOnlyTools_Parallel_10(b *testing.B) {
	reg, calls := setupBenchRegistry(b, 10)
	b.ResetTimer()
	b.ReportAllocs()
	for i := 0; i < b.N; i++ {
		_ = runParallel(reg, calls)
	}
}

// BenchmarkReadOnlyTools_MixedLatency simulates real workloads where each tool call
// has varied I/O latency (e.g. 1ms to 3ms).
func BenchmarkReadOnlyTools_MixedLatency(b *testing.B) {
	dir := b.TempDir()
	reg, err := tools.NewRegistry(dir, nil)
	if err != nil {
		b.Fatal(err)
	}
	b.Cleanup(func() { _ = reg.Close() })

	// Register simulated slow read tools
	for i := 0; i < 4; i++ {
		toolName := fmt.Sprintf("slow_read_%d", i)
		latency := time.Duration(i+1) * time.Millisecond
		reg.Register(tools.ToolSpec{
			Name:        toolName,
			Description: "simulated read",
			ReadOnly:    true,
			Execute: func(ctx context.Context, args json.RawMessage) (string, error) {
				time.Sleep(latency)
				return "ok", nil
			},
		})
	}

	calls := make([]ToolCall, 4)
	for i := 0; i < 4; i++ {
		calls[i] = ToolCall{
			ID:   fmt.Sprintf("call_%d", i),
			Type: "function",
			Function: struct {
				Name      string `json:"name"`
				Arguments string `json:"arguments"`
			}{
				Name: fmt.Sprintf("slow_read_%d", i),
			},
		}
	}

	b.Run("Sequential", func(b *testing.B) {
		b.ResetTimer()
		for i := 0; i < b.N; i++ {
			_ = runSequential(reg, calls)
		}
	})

	b.Run("Parallel", func(b *testing.B) {
		b.ResetTimer()
		for i := 0; i < b.N; i++ {
			_ = runParallel(reg, calls)
		}
	})
}
