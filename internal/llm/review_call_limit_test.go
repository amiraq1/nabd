package llm

import (
	"encoding/json"
	"fmt"
	"strings"
	"testing"
)

func TestReviewToolCallCountLimit(t *testing.T) {
	for _, n := range []int{64, 65} {
		t.Run(fmt.Sprint(n), func(t *testing.T) {
			calls := make([]ToolCall, n)
			for i := range calls {
				calls[i].ID = fmt.Sprintf("call-%d", i)
				calls[i].Type = "function"
				calls[i].Function.Name = "read_file"
				calls[i].Function.Arguments = `{}`
			}
			b, err := json.Marshal(map[string]any{"choices": []any{map[string]any{"message": Message{Role: "assistant", ToolCalls: calls}}}})
			if err != nil {
				t.Fatal(err)
			}
			_, err = parseResponse(b)
			if n == 64 && err != nil {
				t.Fatalf("limit rejected: %v", err)
			}
			if n == 65 && (err == nil || !strings.Contains(err.Error(), "too many tool calls")) {
				t.Fatal("excess tool calls accepted")
			}
		})
	}
}
