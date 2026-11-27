package llm

import (
	"bytes"
	"encoding/json"
	"testing"
)

func stabilityTools() []map[string]any {
	return []map[string]any{
		{
			"type": "function",
			"function": map[string]any{
				"name":        "read_file",
				"description": "Read a workspace file.",
				"parameters": map[string]any{
					"type": "object",
					"properties": map[string]any{
						"path":   map[string]any{"type": "string"},
						"format": map[string]any{"type": "string", "enum": []any{"text", "json"}},
					},
					"required": []any{"path", "format"},
				},
			},
		},
		{
			"type": "function",
			"function": map[string]any{
				"name":        "edit_file",
				"description": "Replace an exact block.",
				"parameters": map[string]any{
					"type": "object",
					"properties": map[string]any{
						"path":     map[string]any{"type": "string"},
						"old_text": map[string]any{"type": "string"},
						"new_text": map[string]any{"type": "string"},
					},
					"required": []any{"path", "old_text", "new_text"},
				},
			},
		},
	}
}

func stabilityMessages() []Message {
	return []Message{
		{Role: "system", Content: "You are a coding agent."},
		{Role: "user", Content: "Inspect the repository."},
		{
			Role: "assistant",
			ToolCalls: []ToolCall{
				{
					ID:   "call_1",
					Type: "function",
					Function: struct {
						Name      string `json:"name"`
						Arguments string `json:"arguments"`
					}{Name: "read_file", Arguments: `{"path":"main.go","format":"json"}`},
				},
			},
		},
		{Role: "tool", ToolCallID: "call_1", Content: `{"content":"package main"}`},
	}
}

type openAIBody struct {
	Model       string            `json:"model"`
	Messages    []json.RawMessage `json:"messages"`
	Tools       json.RawMessage   `json:"tools"`
	Temperature float64           `json:"temperature"`
	MaxTokens   int               `json:"max_tokens"`
}

func TestOpenAIRequestPrefixIsDeterministicAndAppendOnly(t *testing.T) {
	client := NewOpenAIClient(Config{BaseURL: "https://example.com/v1", Model: "test-model"})
	req := Request{Messages: stabilityMessages(), Tools: stabilityTools()}

	first, err := client.encode(req)
	if err != nil {
		t.Fatal(err)
	}
	var allowed map[string]json.RawMessage
	if err := json.Unmarshal(first, &allowed); err != nil {
		t.Fatal(err)
	}
	for key := range allowed {
		switch key {
		case "model", "messages", "temperature", "max_tokens", "tools":
		default:
			t.Fatalf("OpenAI payload contains a non-standard field %q", key)
		}
	}
	for i := 0; i < 20; i++ {
		next, err := client.encode(req)
		if err != nil {
			t.Fatal(err)
		}
		if !bytes.Equal(first, next) {
			t.Fatalf("build %d changed the OpenAI payload", i)
		}
	}

	var base openAIBody
	if err := json.Unmarshal(first, &base); err != nil {
		t.Fatal(err)
	}
	nextReq := req
	nextReq.Messages = append(append([]Message{}, req.Messages...), Message{Role: "user", Content: "Continue."})
	next, err := client.encode(nextReq)
	if err != nil {
		t.Fatal(err)
	}
	var extended openAIBody
	if err := json.Unmarshal(next, &extended); err != nil {
		t.Fatal(err)
	}
	if len(extended.Messages) != len(base.Messages)+1 {
		t.Fatalf("messages = %d, want %d", len(extended.Messages), len(base.Messages)+1)
	}
	for i := range base.Messages {
		if !bytes.Equal(base.Messages[i], extended.Messages[i]) {
			t.Fatalf("message %d changed when a later message was appended", i)
		}
	}
	if !bytes.Equal(base.Tools, extended.Tools) {
		t.Fatal("tool definitions changed when a message was appended")
	}

	// Changing the system prompt or the tools must change the prefix.
	changedSystem := req
	changedSystem.Messages = append([]Message{}, req.Messages...)
	changedSystem.Messages[0].Content += " Extra."
	changedBody, err := client.encode(changedSystem)
	if err != nil {
		t.Fatal(err)
	}
	var changed openAIBody
	if err := json.Unmarshal(changedBody, &changed); err != nil {
		t.Fatal(err)
	}
	if bytes.Equal(changed.Messages[0], base.Messages[0]) {
		t.Fatal("changing the system prompt did not change its serialized form")
	}
	if !bytes.Equal(changed.Tools, base.Tools) {
		t.Fatal("changing only the system prompt changed the tools")
	}

	changedTools := req
	changedTools.Tools = stabilityTools()
	changedTools.Tools[0]["function"].(map[string]any)["description"] = "Changed."
	changedToolsBody, err := client.encode(changedTools)
	if err != nil {
		t.Fatal(err)
	}
	var toolsBody openAIBody
	if err := json.Unmarshal(changedToolsBody, &toolsBody); err != nil {
		t.Fatal(err)
	}
	if bytes.Equal(toolsBody.Tools, base.Tools) {
		t.Fatal("changing a tool description did not change the tools")
	}
}

type geminiBody struct {
	Contents          []json.RawMessage `json:"contents"`
	Tools             json.RawMessage   `json:"tools"`
	SystemInstruction json.RawMessage   `json:"systemInstruction"`
	GenerationConfig  json.RawMessage   `json:"generationConfig"`
}

type geminiContent struct {
	Role  string            `json:"role"`
	Parts []json.RawMessage `json:"parts"`
}

func flattenGeminiParts(t *testing.T, contents []json.RawMessage) []json.RawMessage {
	t.Helper()
	var parts []json.RawMessage
	for _, raw := range contents {
		var content geminiContent
		if err := json.Unmarshal(raw, &content); err != nil {
			t.Fatal(err)
		}
		parts = append(parts, content.Parts...)
	}
	return parts
}

func TestGeminiRequestPrefixIsDeterministicAndAppendOnly(t *testing.T) {
	client := NewGeminiClient(Config{
		Provider: "gemini",
		BaseURL:  "https://example.com",
		Model:    "gemini-2.5-flash",
	})
	req := Request{Messages: stabilityMessages(), Tools: stabilityTools()}

	first, err := client.encode(req)
	if err != nil {
		t.Fatal(err)
	}
	var allowed map[string]json.RawMessage
	if err := json.Unmarshal(first, &allowed); err != nil {
		t.Fatal(err)
	}
	for key := range allowed {
		switch key {
		case "contents", "generationConfig", "systemInstruction", "tools":
		default:
			t.Fatalf("Gemini payload contains a non-standard field %q", key)
		}
	}
	for i := 0; i < 20; i++ {
		next, err := client.encode(req)
		if err != nil {
			t.Fatal(err)
		}
		if !bytes.Equal(first, next) {
			t.Fatalf("build %d changed the Gemini payload", i)
		}
	}

	var base geminiBody
	if err := json.Unmarshal(first, &base); err != nil {
		t.Fatal(err)
	}
	baseParts := flattenGeminiParts(t, base.Contents)

	nextReq := req
	nextReq.Messages = append(append([]Message{}, req.Messages...), Message{Role: "user", Content: "Continue."})
	next, err := client.encode(nextReq)
	if err != nil {
		t.Fatal(err)
	}
	var extended geminiBody
	if err := json.Unmarshal(next, &extended); err != nil {
		t.Fatal(err)
	}
	extendedParts := flattenGeminiParts(t, extended.Contents)
	if len(extendedParts) <= len(baseParts) {
		t.Fatalf("contents did not grow: %d -> %d parts", len(baseParts), len(extendedParts))
	}
	for i := range baseParts {
		if !bytes.Equal(baseParts[i], extendedParts[i]) {
			t.Fatalf("part %d changed when a later message was appended", i)
		}
	}
	if !bytes.Equal(base.Tools, extended.Tools) {
		t.Fatal("Gemini tools changed when a message was appended")
	}
	if !bytes.Equal(base.SystemInstruction, extended.SystemInstruction) {
		t.Fatal("Gemini systemInstruction changed when a message was appended")
	}

	changedSystem := req
	changedSystem.Messages = append([]Message{}, req.Messages...)
	changedSystem.Messages[1].Content += " Extra."
	changedBody, err := client.encode(changedSystem)
	if err != nil {
		t.Fatal(err)
	}
	var changed geminiBody
	if err := json.Unmarshal(changedBody, &changed); err != nil {
		t.Fatal(err)
	}
	changedParts := flattenGeminiParts(t, changed.Contents)
	if bytes.Equal(changedParts[0], baseParts[0]) {
		t.Fatal("changing the first user message did not change its serialized form")
	}
}
