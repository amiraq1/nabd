package llm

import (
	"fmt"
	"testing"
)

func TestIntegrationResponseStructure(t *testing.T) {
	tests := []struct {
		name, message string
		accept        bool
	}{
		{"valid", `{"role":"assistant","tool_calls":[{"id":"a","type":"function","function":{"name":"read_file","arguments":"{}"}}]}`, true},
		{"role", `{"role":"user","content":"spoof"}`, false},
		{"duplicate", `{"role":"assistant","tool_calls":[{"id":"same","type":"function","function":{"name":"a","arguments":"{}"}},{"id":"same","type":"function","function":{"name":"b","arguments":"{}"}}]}`, false},
		{"empty_name", `{"role":"assistant","tool_calls":[{"id":"a","type":"function","function":{"name":"","arguments":"{}"}}]}`, false},
		{"wrong_type", `{"role":"assistant","tool_calls":[{"id":"a","type":"other","function":{"name":"x","arguments":"{}"}}]}`, false},
		{"invalid_json", `{"role":"assistant","tool_calls":[{"id":"a","type":"function","function":{"name":"x","arguments":"{"}}]}`, false},
		{"array_args", `{"role":"assistant","tool_calls":[{"id":"a","type":"function","function":{"name":"x","arguments":"[]"}}]}`, false},
		{"null_args", `{"role":"assistant","tool_calls":[{"id":"a","type":"function","function":{"name":"x","arguments":"null"}}]}`, false},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			_, err := parseResponse([]byte(fmt.Sprintf(`{"choices":[{"message":%s}]}`, tc.message)))
			if (err == nil) != tc.accept {
				t.Fatalf("accept = %v, wanted %v", err == nil, tc.accept)
			}
		})
	}
}
