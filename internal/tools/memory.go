package tools

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"

	"github.com/amiraq1/nabd/internal/audit"
	"github.com/amiraq1/nabd/internal/memory"
)

type memorySearchArgs struct {
	Query      string `json:"query"`
	Kind       string `json:"kind"`
	MaxResults *int   `json:"max_results"`
}

type memorySaveResult struct {
	ID        string `json:"id"`
	Kind      string `json:"kind"`
	CreatedAt string `json:"created_at"`
	Warning   string `json:"warning,omitempty"`
}

// AuditWarningError reports that a primary write succeeded while recording the
// post-write audit event failed.
type AuditWarningError struct {
	Err error
}

func (e *AuditWarningError) Error() string {
	return fmt.Sprintf("audit log write failed: %v", e.Err)
}

func (e *AuditWarningError) Unwrap() error { return e.Err }

func (r *Registry) registerMemoryTools() {
	r.handlers["memory_search"] = ToolSpec{
		Name:           "memory_search",
		Description:    "Search local workspace memories stored in .nabd/memory/entries.jsonl using Okapi BM25 ranking. Read-only and bounded to 8 KiB JSON output tagged as untrusted workspace memory.",
		ReadOnly:       true,
		MaxOutputBytes: memory.MaxSearchOutputBytes,
		Parameters:     json.RawMessage(`{"type":"object","properties":{"query":{"type":"string","description":"Search query (required, max 256 bytes UTF-8)"},"kind":{"type":"string","enum":["decision","bugfix","handoff","note"],"description":"Optional memory kind filter"},"max_results":{"type":"integer","minimum":1,"maximum":10,"description":"Maximum results to return (default 5, max 10)"}},"required":["query"]}`),
		Execute: func(ctx context.Context, args json.RawMessage) (string, error) {
			return r.executeMemorySearch(ctx, args)
		},
	}

	r.handlers["memory_save"] = ToolSpec{
		Name:        "memory_save",
		Description: "Save a bounded project memory entry (decision, bugfix, handoff, or note) to .nabd/memory/entries.jsonl. Always requires explicit human approval and runs advisory secret heuristics before saving.",
		ReadOnly:    false,
		Parameters:  json.RawMessage(`{"type":"object","properties":{"kind":{"type":"string","enum":["decision","bugfix","handoff","note"],"description":"Entry category (default note)"},"title":{"type":"string","description":"Required single-line summary (max 120 bytes UTF-8)"},"body":{"type":"string","description":"Required memory text (max 2048 bytes UTF-8)"},"tags":{"type":"array","items":{"type":"string"},"maxItems":8,"description":"Optional up to 8 lowercase tags matching ^[a-z0-9][a-z0-9_-]{0,31}$"}},"required":["title","body"]}`),
		Execute: func(ctx context.Context, args json.RawMessage) (string, error) {
			return r.executeMemorySave(ctx, args)
		},
	}
}

// CanonicalMemorySave validates, normalizes, secret-checks, and hashes
// memory_save arguments so the preview, approval token, and execution all bind
// to the exact same normalized payload.
func (r *Registry) CanonicalMemorySave(rawArgs json.RawMessage) (memory.SaveInput, string, error) {
	var in memory.SaveInput
	if err := memory.DecodeStrictJSONObject(rawArgs, &in); err != nil {
		return memory.SaveInput{}, "", err
	}
	return memory.CanonicalizeSaveInput(in)
}

func (r *Registry) executeMemorySearch(ctx context.Context, rawArgs json.RawMessage) (string, error) {
	if r.memory == nil {
		return "", errors.New("memory store is not initialized")
	}
	var p memorySearchArgs
	if err := memory.DecodeStrictJSONObject(rawArgs, &p); err != nil {
		return "", err
	}
	_, encoded, err := r.memory.Search(ctx, p.Query, p.Kind, p.MaxResults)
	if err != nil {
		return "", err
	}
	return string(encoded), nil
}

func (r *Registry) executeMemorySave(ctx context.Context, rawArgs json.RawMessage) (string, error) {
	if r.memory == nil {
		return "", errors.New("memory store is not initialized")
	}
	canon, _, err := r.CanonicalMemorySave(rawArgs)
	if err != nil {
		return "", err
	}
	entry, err := r.memory.Save(ctx, canon)
	var warning string
	if err != nil {
		var uncertain *memory.CommitUncertainError
		if !errors.As(err, &uncertain) {
			return "", err
		}
		warning = err.Error()
	}
	res := memorySaveResult{
		ID:        entry.ID,
		Kind:      string(entry.Kind),
		CreatedAt: entry.CreatedAt,
		Warning:   warning,
	}
	encoded, err := json.Marshal(res)
	if err != nil {
		return "", err
	}
	return string(encoded), nil
}

// RememberMemory validates, secret-checks, audits, and saves a direct user
// /remember request without logging raw body text or secrets.
func (r *Registry) RememberMemory(ctx context.Context, input memory.SaveInput) (memory.Entry, error) {
	if r == nil || r.memory == nil {
		return memory.Entry{}, errors.New("memory store is not initialized")
	}
	canon, identity, err := memory.CanonicalizeSaveInput(input)
	kindMeta := sanitizeMemoryKindForAudit(input.Kind)
	if err != nil {
		reason := "validation rejected"
		if errors.Is(err, memory.ErrSecretDetected) {
			reason = "secret heuristic rejected"
		}
		_ = r.logEvent(audit.Event{
			Event:  "denied",
			Tool:   "remember",
			Detail: "kind=" + kindMeta,
			Reason: reason,
		})
		return memory.Entry{}, err
	}

	argsHash := hashBytes([]byte(identity))
	detail := "kind=" + canon.Kind
	if err := r.logEvent(audit.Event{Event: "requested", Tool: "remember", ArgsSHA256: argsHash, Detail: detail}); err != nil {
		return memory.Entry{}, err
	}
	if err := r.logEvent(audit.Event{Event: "executing", Tool: "remember", ArgsSHA256: argsHash, Detail: detail}); err != nil {
		_ = r.logEvent(audit.Event{Event: "failed", Tool: "remember", ArgsSHA256: argsHash, Detail: detail, Reason: "audit executing write failed"})
		return memory.Entry{}, fmt.Errorf("cannot record execution in audit log: %w", err)
	}

	entry, saveErr := r.memory.Save(ctx, canon)
	var uncertain *memory.CommitUncertainError
	if saveErr != nil && !errors.As(saveErr, &uncertain) {
		_ = r.logEvent(audit.Event{Event: "failed", Tool: "remember", ArgsSHA256: argsHash, Detail: detail})
		return memory.Entry{}, saveErr
	}

	appliedDetail := fmt.Sprintf("id=%s kind=%s", entry.ID, entry.Kind)
	reason := ""
	if uncertain != nil {
		reason = "directory sync uncertain"
	}
	auditErr := r.logEvent(audit.Event{
		Event:        "applied",
		Tool:         "remember",
		ArgsSHA256:   argsHash,
		ResultSHA256: hashBytes([]byte(entry.ID)),
		Detail:       appliedDetail,
		Reason:       reason,
	})
	if saveErr != nil && auditErr != nil {
		return entry, errors.Join(saveErr, &AuditWarningError{Err: auditErr})
	}
	if saveErr != nil {
		return entry, saveErr
	}
	if auditErr != nil {
		return entry, &AuditWarningError{Err: auditErr}
	}
	return entry, nil
}

// ListMemories lists recent active entries from the workspace memory store.
func (r *Registry) ListMemories(ctx context.Context, limit int) ([]memory.Entry, int, error) {
	if r == nil || r.memory == nil {
		return nil, 0, errors.New("memory store is not initialized")
	}
	return r.memory.List(ctx, limit)
}

// SearchMemories executes a local BM25 memory search for the TUI /memory command.
func (r *Registry) SearchMemories(ctx context.Context, query, kind string, limit int) (memory.SearchResponse, error) {
	if r == nil || r.memory == nil {
		return memory.SearchResponse{}, errors.New("memory store is not initialized")
	}
	resp, _, err := r.memory.Search(ctx, query, kind, &limit)
	return resp, err
}

// GetMemory fetches a single entry by ID.
func (r *Registry) GetMemory(ctx context.Context, id string) (memory.Entry, error) {
	if r == nil || r.memory == nil {
		return memory.Entry{}, errors.New("memory store is not initialized")
	}
	return r.memory.Get(ctx, id)
}

// RecordMemoryDeleteDenied records a user rejection/cancellation of /memory delete.
func (r *Registry) RecordMemoryDeleteDenied(id string) error {
	if r == nil || r.auditLog == nil {
		return nil
	}
	detail := "id=invalid"
	if memory.ValidateID(id) == nil {
		detail = "id=" + id
	}
	hash := hashBytes([]byte(detail))
	if err := r.logEvent(audit.Event{Event: "requested", Tool: "memory_delete", ArgsSHA256: hash, Detail: detail}); err != nil {
		return err
	}
	return r.logEvent(audit.Event{Event: "denied", Tool: "memory_delete", ArgsSHA256: hash, Detail: detail, Reason: "user rejected"})
}

// DeleteMemory deletes a workspace memory entry after explicit user confirmation.
func (r *Registry) DeleteMemory(ctx context.Context, id string) (memory.Entry, error) {
	if r == nil || r.memory == nil {
		return memory.Entry{}, errors.New("memory store is not initialized")
	}
	if err := memory.ValidateID(id); err != nil {
		return memory.Entry{}, err
	}
	detail := "id=" + id
	hash := hashBytes([]byte(detail))

	if err := r.logEvent(audit.Event{Event: "requested", Tool: "memory_delete", ArgsSHA256: hash, Detail: detail}); err != nil {
		return memory.Entry{}, err
	}
	if err := r.logEvent(audit.Event{Event: "approved", Tool: "memory_delete", ArgsSHA256: hash, Detail: detail}); err != nil {
		return memory.Entry{}, err
	}
	if err := r.logEvent(audit.Event{Event: "executing", Tool: "memory_delete", ArgsSHA256: hash, Detail: detail}); err != nil {
		_ = r.logEvent(audit.Event{Event: "failed", Tool: "memory_delete", ArgsSHA256: hash, Detail: detail, Reason: "audit executing write failed"})
		return memory.Entry{}, fmt.Errorf("cannot record execution in audit log: %w", err)
	}

	removed, delErr := r.memory.Delete(ctx, id)
	var uncertain *memory.CommitUncertainError
	if delErr != nil && !errors.As(delErr, &uncertain) {
		_ = r.logEvent(audit.Event{Event: "failed", Tool: "memory_delete", ArgsSHA256: hash, Detail: detail})
		return memory.Entry{}, delErr
	}

	appliedDetail := fmt.Sprintf("id=%s kind=%s", removed.ID, removed.Kind)
	reason := ""
	if uncertain != nil {
		reason = "directory sync uncertain"
	}
	auditErr := r.logEvent(audit.Event{
		Event:        "applied",
		Tool:         "memory_delete",
		ArgsSHA256:   hash,
		ResultSHA256: hashBytes([]byte(removed.ID)),
		Detail:       appliedDetail,
		Reason:       reason,
	})
	if delErr != nil && auditErr != nil {
		return removed, errors.Join(delErr, &AuditWarningError{Err: auditErr})
	}
	if delErr != nil {
		return removed, delErr
	}
	if auditErr != nil {
		return removed, &AuditWarningError{Err: auditErr}
	}
	return removed, nil
}

// MemoryDirectorySyncSupported reports whether the underlying memory store can
// fsync the parent directory on this platform.
func (r *Registry) MemoryDirectorySyncSupported() bool {
	if r == nil || r.memory == nil {
		return true
	}
	return r.memory.DirectorySyncSupported()
}

func sanitizeMemoryKindForAudit(raw string) string {
	trimmed := strings.ToLower(strings.TrimSpace(raw))
	if trimmed == "" {
		return string(memory.KindNote)
	}
	if k, err := memory.ParseKind(trimmed, false); err == nil {
		return string(k)
	}
	return "invalid"
}

func memorySaveAppliedAuditDetail(out string) string {
	var res memorySaveResult
	if err := json.Unmarshal([]byte(out), &res); err == nil && res.ID != "" && res.Kind != "" {
		return fmt.Sprintf("id=%s kind=%s", sanitizeAuditDetail(res.ID), sanitizeAuditDetail(res.Kind))
	}
	return ""
}
