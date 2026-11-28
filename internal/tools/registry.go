package tools

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"
	"unicode/utf8"

	"github.com/amiraq1/nabd/internal/audit"
	"github.com/amiraq1/nabd/internal/memory"
	"github.com/amiraq1/nabd/internal/sandbox"
	shadowstore "github.com/amiraq1/nabd/internal/shadow"
)

const (
	maxOutputBytes       = 4096
	maxReadBytes         = 512 << 10
	maxReadPageBytes     = 3584
	maxEditBytes         = 2 << 20
	maxCreateBytes       = maxEditBytes
	maxCreateDepth       = 15
	maxCreatePathBytes   = 512
	maxPathSegmentBytes  = 255
	maxListEntries       = 1000
	maxListScan          = 4096
	maxVerifyOutputBytes = 64 << 10
)

type ApprovalFunc func(toolName, summary string) bool

type ToolSpec struct {
	Name        string
	Description string
	Parameters  json.RawMessage
	// ReadOnly marks tools that cannot modify the workspace or execute
	// external code. The zero value is false, so every new tool requires
	// approval unless its author explicitly declares it read-only.
	ReadOnly bool
	// MaxOutputBytes overrides the default 4096-byte cap when > 0.
	MaxOutputBytes int
	Execute        func(ctx context.Context, args json.RawMessage) (string, error)
}

type Registry struct {
	workDir                 string
	root                    *os.Root
	shadow                  *shadowstore.Store
	memory                  *memory.Store
	auditLog                *audit.Log
	approve                 ApprovalFunc
	handlers                map[string]ToolSpec
	mu                      sync.Mutex
	writeMu                 sync.Mutex
	pending                 map[string]pendingApproval
	now                     func() time.Time
	mcpConfig               *MCPConfig
	reaConfig               *REAConfig
	webConfig               *WebConfig
	sandbox                 sandbox.Backend
	allowUnsandboxedForTest bool
}

type pendingApproval struct {
	state        *ApprovalState
	preflightErr error
	tool         string
	argsHash     string
	identity     string
	expires      time.Time
}

// Approval is a single-use token proving that the UI obtained an explicit
// approval for this exact tool call.
type Approval struct {
	Token          string
	Tool           string
	ArgsSHA256     string
	IdentitySHA256 string
}

const approvalTTL = 5 * time.Minute

func NewRegistry(workDir string, approvers ...ApprovalFunc) (*Registry, error) {
	var approve ApprovalFunc
	if len(approvers) > 1 {
		return nil, fmt.Errorf("multiple approvers")
	}
	if len(approvers) == 1 {
		approve = approvers[0]
	}
	absRoot, err := filepath.Abs(workDir)
	if err != nil {
		return nil, err
	}
	if resolved, err := filepath.EvalSymlinks(absRoot); err == nil {
		absRoot = resolved
	}
	root, err := os.OpenRoot(absRoot)
	if err != nil {
		return nil, fmt.Errorf("open workspace root: %w", err)
	}
	store, err := shadowstore.Open(root)
	if err != nil {
		_ = root.Close()
		return nil, err
	}
	memStore, err := memory.Open(root)
	if err != nil {
		_ = root.Close()
		return nil, err
	}
	auditLog, err := audit.Open(root)
	if err != nil {
		_ = root.Close()
		return nil, err
	}
	r := &Registry{
		workDir:  absRoot,
		root:     root,
		shadow:   store,
		memory:   memStore,
		auditLog: auditLog,
		approve:  approve,
		handlers: make(map[string]ToolSpec),
		pending:  make(map[string]pendingApproval),
		now:      time.Now,
	}
	retentionInterval := 24 * time.Hour
	if value := os.Getenv("NABD_SHADOW_RETENTION_INTERVAL"); value != "" {
		if parsed, err := time.ParseDuration(value); err == nil && parsed > 0 {
			retentionInterval = parsed
		}
	}
	store.StartRetention(context.Background(), retentionInterval)

	if path := os.Getenv(MCPConfigEnv); strings.TrimSpace(path) != "" {
		cfg, err := LoadMCPConfig(path)
		if err != nil {
			_ = root.Close()
			return nil, fmt.Errorf("load MCP allowlist: %w", err)
		}
		r.mcpConfig = cfg
	}

	// Check opt-in REA MCP configuration (fail-closed if invalid)
	reaCfg, err := LoadREAConfigFromEnv()
	if err != nil {
		_ = root.Close()
		return nil, fmt.Errorf("load REA MCP configuration: %w", err)
	}
	if reaCfg != nil {
		if r.mcpConfig == nil {
			r.mcpConfig = &MCPConfig{servers: make(map[string]MCPServer)}
		}
		validatedServer, err := validateServer(REAServerName, reaCfg.Server)
		if err != nil {
			_ = root.Close()
			return nil, fmt.Errorf("validate REA MCP server: %w", err)
		}
		r.mcpConfig.servers[REAServerName] = validatedServer
		r.reaConfig = reaCfg
	}

	// Check opt-in web search/fetch configuration (fail-closed if invalid)
	webCfg, err := LoadWebConfigFromEnv()
	if err != nil {
		_ = root.Close()
		return nil, fmt.Errorf("load web search configuration: %w", err)
	}
	r.webConfig = webCfg

	r.registerDefaults()
	return r, nil
}

// WorkDir returns the resolved absolute workspace directory bound to this registry.
func (r *Registry) WorkDir() string {
	if r == nil {
		return ""
	}
	return r.workDir
}

// IsREAEnabled reports whether the opt-in REA MCP integration is configured and enabled.
func (r *Registry) IsREAEnabled() bool {
	return r.reaConfig != nil && r.Has("mcp_call")
}

// READetails returns the configured REA binary path and target directory.
func (r *Registry) READetails() (binPath, targetDir string) {
	if r.reaConfig == nil {
		return "", ""
	}
	return r.reaConfig.BinPath, r.reaConfig.TargetDir
}

// EnableExecution opts into external execution only after the isolation
// backend has proven its restrictions. Without an MCP allowlist or a proven
// sandbox it fails closed, leaving both tools disabled.
func (r *Registry) EnableExecution(ctx context.Context) error {
	if r.mcpConfig == nil {
		return fmt.Errorf("%w: no user-configured MCP servers", sandbox.ErrUnavailable)
	}
	b, err := sandbox.Enabled(ctx, os.Getenv("NABD_EXEC_SANDBOX") == "1")
	if err != nil {
		return err
	}
	r.sandbox = b
	_ = r.logEvent(audit.Event{
		Event:  "configured",
		Tool:   "sandbox",
		Detail: fmt.Sprintf("memory_limit=%d", sandbox.DefaultMemoryLimit()),
	})
	return nil
}

// MCPIdentity returns the identity digest that must be bound to an MCP
// approval token. It fails when the server is not user-configured.
func (r *Registry) MCPIdentity(name string) (string, error) {
	return r.mcpConfig.Identity(name)
}

// Close releases the workspace handle. The registry must not be used afterwards.
func (r *Registry) Close() error {
	if r.shadow != nil {
		r.shadow.StopRetention()
	}
	if r.root == nil {
		return nil
	}
	err := r.root.Close()
	r.root = nil
	return err
}

// OpenAITools returns tool definitions sorted alphabetically to preserve KV-cache.
func (r *Registry) OpenAITools() []map[string]any {
	names := make([]string, 0, len(r.handlers))
	for name := range r.handlers {
		names = append(names, name)
	}
	sort.Strings(names)

	out := make([]map[string]any, 0, len(names))
	for _, name := range names {
		spec := r.handlers[name]
		var params any
		if len(spec.Parameters) > 0 {
			_ = json.Unmarshal(spec.Parameters, &params)
		} else {
			params = map[string]any{"type": "object", "properties": map[string]any{}}
		}
		out = append(out, map[string]any{
			"type": "function",
			"function": map[string]any{
				"name":        spec.Name,
				"description": spec.Description,
				"parameters":  params,
			},
		})
	}
	return out
}

// RequiresApproval reports whether a tool must be approved before running.
// Unknown tools and every tool that is not explicitly read-only require
// approval.
func (r *Registry) RequiresApproval(name string) bool {
	spec, ok := r.handlers[name]
	if !ok {
		return true
	}
	return !spec.ReadOnly
}

// manuallyApprovedTools must be approved by a human on every call, including
// when an auto-approval mode would otherwise cover the call. The guarantee is
// scoped to exactly these tools: it says nothing about whether other tools
// start subprocesses under their own policy.
var manuallyApprovedTools = map[string]struct{}{
	"go_verify":   {},
	"mcp_call":    {},
	"web_search":  {},
	"web_fetch":   {},
	"memory_save": {},
}

// RequiresManualApproval reports whether auto-approval is refused for this
// tool. Callers must fall back to an interactive decision instead of guessing.
func (r *Registry) RequiresManualApproval(name string) bool {
	_, ok := manuallyApprovedTools[name]
	return ok
}

// IsReadOnly reports whether a registered tool is marked ReadOnly.
func (r *Registry) IsReadOnly(name string) bool {
	spec, ok := r.handlers[name]
	if !ok {
		return false
	}
	return spec.ReadOnly
}

// Has reports whether a tool is registered.
func (r *Registry) NeedsApproval(name string) bool { return r.RequiresApproval(name) }

func (r *Registry) Has(name string) bool {
	_, ok := r.handlers[name]
	return ok
}

// ShadowDirectorySyncSupported reports whether this platform can fsync the
// shadow store directory. False means the newest backup is not guaranteed to
// survive a sudden power loss; the UI shows this limitation explicitly.
func (r *Registry) ShadowDirectorySyncSupported() bool {
	if r == nil || r.shadow == nil {
		return true
	}
	return r.shadow.DirectorySyncSupported()
}

// ShadowCleanupStatus reports retention-sweep health for the UI and any health
// check: the last failure (nil when the most recent sweep succeeded), when it
// happened, how many sweeps failed in a row, and when the last fully
// successful sweep finished. Messages name store entries, not host paths.
func (r *Registry) ShadowCleanupStatus() (lastAt time.Time, consecutiveFailures int, lastSuccess time.Time, lastErr error) {
	if r == nil || r.shadow == nil {
		return time.Time{}, 0, time.Time{}, nil
	}
	lastAt, lastErr = r.shadow.LastCleanupError()
	consecutiveFailures, lastSuccess = r.shadow.CleanupFailures()
	return lastAt, consecutiveFailures, lastSuccess, lastErr
}

// Disable removes tools from the registry. Disabled tools are neither
// advertised nor executable.
func (r *Registry) Disable(names ...string) {
	for _, name := range names {
		delete(r.handlers, name)
	}
}

// withProcessLock serializes writes and restores across separate Nabd
// processes using root-confined, symlink-safe descriptor operations.
func (r *Registry) withProcessLock(fn func() (string, error)) (string, error) {
	lock, err := acquireProcessLock(r.root)
	if err != nil {
		return "", err
	}
	defer lock.close()
	return fn()
}

// resolveSafePath validates a workspace-relative path and returns it in the
// form expected by os.Root. File operations use the root handle, so the
// containment check is enforced at open time, not only here.
func (r *Registry) resolveSafePath(relPath string) (string, error) {
	if strings.TrimSpace(relPath) == "" {
		return "", fmt.Errorf("empty path is not allowed")
	}
	if strings.ContainsRune(relPath, 0) {
		return "", fmt.Errorf("invalid path: contains NUL byte")
	}
	cleaned := filepath.Clean(relPath)
	if filepath.IsAbs(cleaned) || filepath.VolumeName(cleaned) != "" {
		return "", fmt.Errorf("absolute paths are not allowed: %s", relPath)
	}
	if cleaned == ".." || strings.HasPrefix(cleaned, ".."+string(filepath.Separator)) {
		return "", fmt.Errorf("path outside workspace not allowed: %s", relPath)
	}
	if isSensitivePath(cleaned) {
		return "", fmt.Errorf("access to sensitive path denied: %s", relPath)
	}

	// Defense in depth: os.Root rejects escaping symlinks at open time, but we
	// still check the resolved path so symlinks to sensitive files are denied.
	abs := filepath.Join(r.workDir, cleaned)
	if resolved, err := filepath.EvalSymlinks(abs); err == nil {
		relResolved, err := filepath.Rel(r.workDir, resolved)
		if err != nil || relResolved == ".." || strings.HasPrefix(relResolved, ".."+string(filepath.Separator)) {
			return "", fmt.Errorf("symlink points outside workspace: %s", relPath)
		}
		if isSensitivePath(relResolved) {
			return "", fmt.Errorf("access to sensitive path denied: %s", relPath)
		}
	}

	return cleaned, nil
}

// isSensitivePath denies credentials, VCS internals and the reserved .nabd
// directory. The check is intentionally case-insensitive so it also holds on
// case-insensitive filesystems such as Android shared storage.
func isSensitivePath(rel string) bool {
	parts := strings.Split(filepath.ToSlash(rel), "/")
	for i, part := range parts {
		lower := strings.ToLower(part)
		switch lower {
		case ".nabd", ".git":
			return true
		}
		if strings.HasPrefix(lower, ".env") && (i != len(parts)-1 || !isEnvTemplate(lower)) {
			return true
		}
	}

	base := strings.ToLower(parts[len(parts)-1])
	if strings.HasPrefix(base, ".nabd-tmp-") {
		return true
	}
	switch {
	case strings.HasPrefix(base, "id_") && !strings.HasSuffix(base, ".pub"):
		return true
	}

	switch strings.ToLower(filepath.Ext(base)) {
	case ".pem", ".key", ".p12", ".pfx", ".jks", ".keystore":
		return true
	}
	return false
}

// Only these exact final names are exempt from .env* filtering.
func isEnvTemplate(name string) bool {
	switch strings.ToLower(name) {
	case ".env.example", ".env.sample", ".env.template", ".env.dist":
		return true
	default:
		return false
	}
}

// FileDigest returns the SHA-256 of a file inside the workspace. The approval
// UI uses it to confirm that the file did not change between the preview and
// the approved edit.
func (r *Registry) FileDigest(relPath string) (string, error) {
	rel, err := r.resolveSafePath(relPath)
	if err != nil {
		return "", err
	}
	f, err := openRegularSingleLink(r.root, rel)
	if err != nil {
		return "", err
	}
	defer f.Close()

	digest, err := digestBounded(f, maxEditBytes)
	if err != nil {
		return "", err
	}
	if err := ensureNoHardLinks(f); err != nil {
		return "", err
	}
	return digest, nil
}

func digestBounded(reader io.Reader, limit int64) (string, error) {
	h := sha256.New()
	n, err := io.Copy(h, io.LimitReader(reader, limit+1))
	if err != nil {
		return "", err
	}
	if n > limit {
		return "", fmt.Errorf("file too large for digest (limit %d bytes)", limit)
	}
	return hex.EncodeToString(h.Sum(nil)), nil
}

func (r *Registry) readFile(rel string) (string, error) {
	f, err := openRegularSingleLink(r.root, rel)
	if err != nil {
		return "", err
	}
	defer f.Close()

	data, err := io.ReadAll(io.LimitReader(f, maxReadBytes+1))
	if err != nil {
		return "", err
	}
	if err := ensureNoHardLinks(f); err != nil {
		return "", err
	}
	truncated := len(data) > maxReadBytes
	if truncated {
		data = data[:maxReadBytes]
	}
	out := string(data)
	if truncated {
		out += fmt.Sprintf("\n...[truncated at %d bytes]", maxReadBytes)
	}
	return out, nil
}

// readFilePage bounds both raw input and encoded output so the continuation
// marker survives the registry's output cap, even with invalid UTF-8 bytes.
func (r *Registry) readFilePage(rel string, offset, limit int64, structured bool) (string, error) {
	if offset < 0 {
		return "", fmt.Errorf("offset must be non-negative")
	}
	if limit < 1 || limit > maxReadPageBytes {
		return "", fmt.Errorf("limit must be between 1 and %d", maxReadPageBytes)
	}
	f, err := openRegularSingleLink(r.root, rel)
	if err != nil {
		return "", err
	}
	defer f.Close()
	if _, err := f.Seek(offset, io.SeekStart); err != nil {
		return "", err
	}
	data, err := io.ReadAll(io.LimitReader(f, limit+utf8.UTFMax))
	if err != nil {
		return "", err
	}
	if err := ensureNoHardLinks(f); err != nil {
		return "", err
	}
	var out strings.Builder
	consumed, encodedBytes := 0, 0
	for consumed < len(data) {
		r, size := utf8.DecodeRune(data[consumed:])
		encodedSize := utf8.RuneLen(r)
		if structured {
			encoded, err := json.Marshal(string(r))
			if err != nil {
				return "", err
			}
			encodedSize = len(encoded) - 2 // exclude the JSON string quotes
		}
		budget := maxReadPageBytes
		if structured {
			budget = maxOutputBytes - 128
		} // reserve object and offset metadata
		if consumed > 0 && (int64(consumed+size) > limit || encodedBytes+encodedSize > budget) {
			break
		}
		out.WriteRune(r)
		encodedBytes += encodedSize
		consumed += size
	}
	more := consumed < len(data)
	if structured {
		page := struct {
			Content    string `json:"content"`
			Truncated  bool   `json:"truncated"`
			NextOffset *int64 `json:"next_offset,omitempty"`
		}{Content: out.String(), Truncated: more}
		if more {
			next := offset + int64(consumed)
			page.NextOffset = &next
		}
		encoded, err := json.Marshal(page)
		if err != nil {
			return "", err
		}
		if len(encoded) > maxOutputBytes {
			return "", fmt.Errorf("structured page exceeds output limit")
		}
		return string(encoded), nil
	}
	if more {
		fmt.Fprintf(&out, "\n...[truncated; continue at byte offset %d]", offset+int64(consumed))
	}
	return out.String(), nil
}

func (r *Registry) listDir(rel string) (string, error) {
	var err error
	rel, err = r.resolveSafePath(rel)
	if err != nil {
		return "", err
	}
	if isEnvTemplate(filepath.Base(rel)) {
		return "", fmt.Errorf("access to sensitive directory denied: %s", rel)
	}
	info, err := r.root.Stat(rel)
	if err != nil {
		return "", err
	}
	if !info.IsDir() {
		return "", fmt.Errorf("not a directory: %s", rel)
	}
	f, err := openWorkspaceFile(r.root, rel)
	if err != nil {
		return "", err
	}
	defer f.Close()
	info, err = f.Stat()
	if err != nil {
		return "", err
	}
	if !info.IsDir() {
		return "", fmt.Errorf("not a directory: %s", rel)
	}

	entries := make([]os.DirEntry, 0, maxListScan)
	more := false
	for len(entries) <= maxListScan {
		batch, readErr := f.ReadDir(min(128, maxListScan+1-len(entries)))
		entries = append(entries, batch...)
		if readErr != nil && readErr != io.EOF {
			return "", readErr
		}
		if readErr == io.EOF {
			break
		}
		if len(batch) == 0 {
			break
		}
	}
	if len(entries) > maxListScan {
		more = true
		entries = entries[:maxListScan]
	}
	sort.Slice(entries, func(i, j int) bool { return entries[i].Name() < entries[j].Name() })

	var b strings.Builder
	shown, hidden := 0, 0
	for _, e := range entries {
		if isSensitivePath(filepath.Join(rel, e.Name())) || (isEnvTemplate(e.Name()) && (e.IsDir() || e.Type()&os.ModeSymlink != 0)) {
			continue
		}
		if shown >= maxListEntries {
			hidden++
			continue
		}
		typ := "F"
		if e.IsDir() {
			typ = "D"
		}
		b.WriteString(fmt.Sprintf("%s  %s\n", typ, e.Name()))
		shown++
	}
	if hidden > 0 {
		b.WriteString(fmt.Sprintf("... [%d more entries in scanned portion]\n", hidden))
	}
	if more {
		b.WriteString("... [directory scan limit reached; more entries exist]\n")
	}
	return b.String(), nil
}

// Undo restores the most recent non-undone shadow entry for relPath (or the
// most recent entry overall when relPath is empty). It refuses if the file
// changed since the edit or if the shadow copy is corrupted.
func (r *Registry) Undo(relPath string) (string, error) {
	return r.withProcessLock(func() (string, error) { return r.undoLocked(relPath) })
}

func (r *Registry) undoLocked(relPath string) (string, error) {
	r.writeMu.Lock()
	defer r.writeMu.Unlock()
	var target string
	if strings.TrimSpace(relPath) != "" {
		var err error
		target, err = r.resolveSafePath(relPath)
		if err != nil {
			return "", err
		}
	}

	entry, backup, err := r.shadow.Latest(target)
	if err != nil {
		return "", err
	}

	currentPath, err := r.resolveSafePath(entry.Path)
	if err != nil {
		return "", err
	}
	info, err := r.root.Lstat(currentPath)
	if err != nil {
		return "", err
	}
	detail := sanitizeAuditDetail(entry.Path)
	if info.Mode()&os.ModeSymlink != 0 {
		_ = r.logEvent(audit.Event{Event: "denied", Tool: "undo", Detail: detail, Reason: "symlink"})
		return "", fmt.Errorf("refusing to undo a symlink: %s", entry.Path)
	}

	current, err := readBounded(r.root, currentPath, maxEditBytes)
	if err != nil {
		return "", err
	}
	currentHash := hashBytes(current)
	if currentHash != entry.SHA256After {
		_ = r.logEvent(audit.Event{Event: "denied", Tool: "undo", ArgsSHA256: currentHash, Detail: detail, Reason: "file changed since the edit"})
		return "", fmt.Errorf("refusing undo: %s changed since the edit", entry.Path)
	}

	if err := r.logEvent(audit.Event{Event: "requested", Tool: "undo", ArgsSHA256: currentHash, Detail: detail}); err != nil {
		return "", err
	}
	if err := r.logEvent(audit.Event{Event: "executing", Tool: "undo", ArgsSHA256: currentHash, Detail: detail}); err != nil {
		_ = r.logEvent(audit.Event{Event: "failed", Tool: "undo", ArgsSHA256: currentHash, Detail: detail})
		return "", fmt.Errorf("cannot record undo execution in audit log: %w", err)
	}
	var syncWarning string
	if err := r.atomicWriteChecked(currentPath, backup, os.FileMode(entry.Mode), currentHash); err != nil {
		var uncertain *commitUncertainError
		if !errors.As(err, &uncertain) {
			_ = r.logEvent(audit.Event{Event: "failed", Tool: "undo", ArgsSHA256: currentHash, Detail: detail})
			return "", err
		}
		syncWarning = "; warning: " + err.Error()
	}
	if err := r.shadow.MarkUndone(entry.ID); err != nil {
		return "", fmt.Errorf("restored %s but could not update shadow metadata: %w", entry.Path, err)
	}
	_ = r.logEvent(audit.Event{Event: "applied", Tool: "undo", ArgsSHA256: currentHash, ResultSHA256: entry.SHA256Before, Detail: detail})
	return fmt.Sprintf("Restored %s from shadow %s%s", entry.Path, entry.ID, syncWarning), nil
}

// atomicWrite writes data to a temporary file in the target directory, syncs
// it, and renames it over the destination. All operations stay inside root.
// commitUncertainError means rename succeeded but durability could not be confirmed.
type commitUncertainError struct{ err error }

func (e *commitUncertainError) Error() string {
	return "replacement succeeded but parent directory sync failed: " + e.err.Error()
}
func (e *commitUncertainError) Unwrap() error { return e.err }

// syncParentDirectoryHook is a test-only seam for simulating directory sync
// failures. Production leaves it nil.
var syncParentDirectoryHook func(*os.File) error

// parentRenameHook is a test-only seam for simulating replacement failures.
var parentRenameHook func() error

// publishNewFileHook is a test-only seam for simulating exclusive-create
// primitives on restricted filesystems. Production leaves it nil.
var publishNewFileHook func(parent *os.Root, dir *os.File, tmp, base string) error

type atomicWriteOpts struct {
	mode         os.FileMode
	preserveMode bool
	expectedHash string
	createNew    bool
	parentRoot   *os.Root
}

func (r *Registry) atomicWriteChecked(rel string, data []byte, mode os.FileMode, expectedHash string) error {
	return r.atomicWrite(rel, data, atomicWriteOpts{
		mode:         mode,
		preserveMode: true,
		expectedHash: expectedHash,
	})
}

func (r *Registry) atomicWrite(rel string, data []byte, opts atomicWriteOpts) error {
	// Keep a handle to the same parent for staging, checking, publish and sync.
	parent := opts.parentRoot
	if parent == nil {
		var err error
		parent, err = r.root.OpenRoot(filepath.Dir(rel))
		if err != nil {
			return fmt.Errorf("open parent: %w", err)
		}
		defer parent.Close()
	}
	dir, err := parent.Open(".")
	if err != nil {
		return fmt.Errorf("open parent for sync: %w", err)
	}
	defer dir.Close()

	base := filepath.Base(rel)
	tmp := ".nabd-tmp-" + randomHex(8)
	f, err := parent.OpenFile(tmp, os.O_WRONLY|os.O_CREATE|os.O_EXCL, opts.mode.Perm())
	if err != nil {
		return fmt.Errorf("create temporary file: %w", err)
	}
	_, writeErr := f.Write(data)
	var modeErr error
	if opts.preserveMode {
		modeErr = f.Chmod(opts.mode.Perm())
	}
	syncErr := f.Sync()
	closeErr := f.Close()
	if err := errors.Join(writeErr, modeErr, syncErr, closeErr); err != nil {
		_ = parent.Remove(tmp)
		return fmt.Errorf("write temporary file: %w", err)
	}

	if opts.expectedHash != "" {
		if editBeforeRenameHook != nil {
			editBeforeRenameHook()
		}
		info, statErr := parent.Lstat(base)
		current, readErr := readBounded(parent, base, maxEditBytes)
		if errors.Is(statErr, fs.ErrNotExist) || errors.Is(readErr, fs.ErrNotExist) {
			_ = parent.Remove(tmp)
			return fmt.Errorf("file no longer exists since approval: %s", rel)
		}
		if statErr != nil || readErr != nil || info.Mode()&os.ModeSymlink != 0 || hashBytes(current) != opts.expectedHash {
			_ = parent.Remove(tmp)
			return fmt.Errorf("file changed since approval: %s", rel)
		}
	} else if opts.createNew {
		if editBeforeRenameHook != nil {
			editBeforeRenameHook()
		}
		if _, statErr := parent.Lstat(base); statErr == nil {
			_ = parent.Remove(tmp)
			return fmt.Errorf("file already exists: %s", rel)
		} else if !errors.Is(statErr, fs.ErrNotExist) {
			_ = parent.Remove(tmp)
			return fmt.Errorf("inspect target %s: %w", rel, statErr)
		}
	}

	if parentRenameHook != nil {
		if err := parentRenameHook(); err != nil {
			_ = parent.Remove(tmp)
			return fmt.Errorf("replace %s: %w", rel, err)
		}
	}
	if opts.createNew {
		if err := publishNewFile(parent, dir, tmp, base, rel); err != nil {
			return err
		}
	} else {
		if err := parent.Rename(tmp, base); err != nil {
			_ = parent.Remove(tmp)
			return fmt.Errorf("replace %s: %w", rel, err)
		}
	}
	syncDir := syncParentDirectory
	if syncParentDirectoryHook != nil {
		syncDir = syncParentDirectoryHook
	}
	if err := syncDir(dir); err != nil {
		return &commitUncertainError{err: err}
	}
	return nil
}

func publishNewFile(parent *os.Root, dir *os.File, tmp, base, rel string) error {
	if publishNewFileHook != nil {
		err := publishNewFileHook(parent, dir, tmp, base)
		if err == nil {
			return nil
		}
		if isExistErr(err) {
			_ = parent.Remove(tmp)
			return fmt.Errorf("file already exists: %s", rel)
		}
		if !isUnsupportedExclusiveRename(err) && !isUnsupportedHardLink(err) {
			_ = parent.Remove(tmp)
			return fmt.Errorf("create %s: %w", rel, err)
		}
		return fallbackExclusiveRename(parent, tmp, base, rel)
	}

	if err := tryRenameNoReplace(dir, tmp, base); err == nil {
		return nil
	} else if isExistErr(err) {
		_ = parent.Remove(tmp)
		return fmt.Errorf("file already exists: %s", rel)
	} else if !isUnsupportedExclusiveRename(err) {
		_ = parent.Remove(tmp)
		return fmt.Errorf("create %s: %w", rel, err)
	}

	if err := parent.Link(tmp, base); err == nil {
		if rmErr := parent.Remove(tmp); rmErr != nil && !errors.Is(rmErr, fs.ErrNotExist) {
			return fmt.Errorf("created %s but could not remove temp file: %w", rel, rmErr)
		}
		return nil
	} else if isExistErr(err) {
		_ = parent.Remove(tmp)
		return fmt.Errorf("file already exists: %s", rel)
	} else if !isUnsupportedHardLink(err) {
		_ = parent.Remove(tmp)
		return fmt.Errorf("create %s: %w", rel, err)
	}

	return fallbackExclusiveRename(parent, tmp, base, rel)
}

func fallbackExclusiveRename(parent *os.Root, tmp, base, rel string) error {
	// Filesystems without RENAME_NOREPLACE or hard links (e.g., Android /sdcard FUSE
	// or FAT32) fall back to a final Lstat check before Rename; an external writer
	// racing in this narrow window cannot be blocked atomically by the kernel.
	if _, err := parent.Lstat(base); err == nil {
		_ = parent.Remove(tmp)
		return fmt.Errorf("file already exists: %s", rel)
	} else if !errors.Is(err, fs.ErrNotExist) {
		_ = parent.Remove(tmp)
		return fmt.Errorf("inspect target %s: %w", rel, err)
	}
	if err := parent.Rename(tmp, base); err != nil {
		_ = parent.Remove(tmp)
		return fmt.Errorf("create %s: %w", rel, err)
	}
	return nil
}

func hashBytes(data []byte) string {
	sum := sha256.Sum256(data)
	return hex.EncodeToString(sum[:])
}

func randomHex(n int) string {
	buf := make([]byte, n)
	if _, err := rand.Read(buf); err != nil {
		return fmt.Sprintf("%d", time.Now().UnixNano())
	}
	return hex.EncodeToString(buf)
}

// newApprovalToken fails closed if secure randomness is unavailable.
func newApprovalToken() (string, error) {
	var buf [16]byte
	if _, err := io.ReadFull(rand.Reader, buf[:]); err != nil {
		return "", fmt.Errorf("cannot generate approval token: %w", err)
	}
	return hex.EncodeToString(buf[:]), nil
}

// Run executes a tool. Tools that are not read-only are refused unless an
// approver is configured and grants the request, so callers without an
// interactive approval path fail closed. Every action is appended to the
// audit log.
func (r *Registry) Run(ctx context.Context, name string, rawArgs json.RawMessage) string {
	spec, ok := r.handlers[name]
	if !ok {
		return fmt.Sprintf("Error: unknown tool %q", name)
	}
	hash := hashBytes(rawArgs)
	detail := r.toolDetail(name, rawArgs)

	if spec.ReadOnly {
		_ = r.logEvent(audit.Event{Event: "requested", Tool: name, ArgsSHA256: hash, Detail: detail})
		return r.execute(ctx, spec, name, rawArgs, hash)
	}

	if err := r.logEvent(audit.Event{Event: "requested", Tool: name, ArgsSHA256: hash, Detail: detail}); err != nil {
		return fmt.Sprintf("Error: %v", err)
	}
	if name == "mcp_call" {
		_ = r.logEvent(audit.Event{Event: "denied", Tool: name, ArgsSHA256: hash, Reason: "RunApproved required"})
		return fmt.Sprintf("Error: tool %q requires approval; use RunApproved", name)
	}
	if r.approve == nil {
		_ = r.logEvent(audit.Event{Event: "denied", Tool: name, ArgsSHA256: hash, Reason: "no approver configured"})
		return fmt.Sprintf("Error: tool %q requires approval, but no approver is configured", name)
	}
	var previewState *ApprovalState
	if isFileApprovalTool(name) {
		preview, err := r.PreviewApproval(name, rawArgs)
		if err != nil {
			return r.failedResult(name, hash, err)
		}
		previewState = &preview.State
	}
	if !r.approve(name, string(rawArgs)) {
		_ = r.logEvent(audit.Event{Event: "denied", Tool: name, ArgsSHA256: hash, Reason: "denied by approver"})
		return "Action rejected by user."
	}
	if err := r.logEvent(audit.Event{Event: "approved", Tool: name, ArgsSHA256: hash}); err != nil {
		return fmt.Sprintf("Error: %v", err)
	}
	if previewState != nil {
		var err error
		ctx, err = r.verifyAndBindFileApproval(ctx, name, rawArgs, *previewState)
		if err != nil {
			return r.failedResult(name, hash, err)
		}
	}
	return r.execute(ctx, spec, name, rawArgs, hash)
}

func isFileApprovalTool(name string) bool {
	return name == "edit_file" || name == "create_file"
}

func (r *Registry) prepareApprovalState(name string, rawArgs json.RawMessage) (*ApprovalState, error) {
	if !isFileApprovalTool(name) {
		return nil, nil
	}
	preview, err := r.PreviewApproval(name, rawArgs)
	if err != nil {
		return nil, err
	}
	return &preview.State, nil
}

func (r *Registry) verifyAndBindFileApproval(ctx context.Context, name string, rawArgs json.RawMessage, state ApprovalState) (context.Context, error) {
	switch name {
	case "edit_file":
		p, err := parseEditParams(rawArgs)
		if err != nil {
			return ctx, err
		}
		if _, _, err := r.verifyEditState(p, state); err != nil {
			return ctx, err
		}
		return context.WithValue(ctx, editApprovalKey{}, state), nil
	case "create_file":
		p, err := parseCreateParams(rawArgs)
		if err != nil {
			return ctx, err
		}
		if _, _, err := r.verifyCreateState(p, state); err != nil {
			return ctx, err
		}
		return context.WithValue(ctx, createApprovalKey{}, state), nil
	default:
		return ctx, nil
	}
}

// RecordDenied records a user rejection without storing raw arguments.
// A logging error never turns a rejection into an approval.
func (r *Registry) RecordDenied(name string, rawArgs json.RawMessage) error {
	if !r.Has(name) || !r.RequiresApproval(name) {
		return fmt.Errorf("tool %q is not a registered approval tool", name)
	}
	hash := hashBytes(rawArgs)
	if err := r.logEvent(audit.Event{Event: "requested", Tool: name, ArgsSHA256: hash}); err != nil {
		return err
	}
	return r.logEvent(audit.Event{Event: "denied", Tool: name, ArgsSHA256: hash, Reason: "user rejected"})
}

// Approve records an explicit approval and returns a single-use token bound to
// this exact tool call. The audit write is mandatory: if it fails, no token is
// issued and nothing can execute.
func (r *Registry) Approve(name string, rawArgs json.RawMessage) (Approval, error) {
	state, previewErr := r.prepareApprovalState(name, rawArgs)
	return r.approveWithState(name, rawArgs, state, previewErr, "", "")
}

// ApproveAuto records an explicit approval with an auto-approval mode tag in the audit log.
func (r *Registry) ApproveAuto(name string, rawArgs json.RawMessage, mode string) (Approval, error) {
	state, previewErr := r.prepareApprovalState(name, rawArgs)
	return r.approveWithState(name, rawArgs, state, previewErr, "", mode)
}

// ApproveWithIdentity binds a token to a server identity digest in addition to
// the tool name and argument hash. It is used for MCP calls so the approval
// refers to the exact configured executable, guard and environment.
func (r *Registry) ApproveWithIdentity(name string, rawArgs json.RawMessage, identity string) (Approval, error) {
	if identity == "" {
		return Approval{}, fmt.Errorf("identity is required for approval")
	}
	return r.approveWithState(name, rawArgs, nil, nil, identity, "")
}

// ApproveWithIdentityAuto binds a token with server identity and an auto-approval mode tag.
func (r *Registry) ApproveWithIdentityAuto(name string, rawArgs json.RawMessage, identity string, mode string) (Approval, error) {
	if identity == "" {
		return Approval{}, fmt.Errorf("identity is required for approval")
	}
	return r.approveWithState(name, rawArgs, nil, nil, identity, mode)
}

// ApprovePreview binds a single-use token to the file state shown in the UI.
func (r *Registry) ApprovePreview(name string, rawArgs json.RawMessage, state ApprovalState) (Approval, error) {
	if !isFileApprovalTool(name) {
		return Approval{}, fmt.Errorf("preview state is only valid for edit_file or create_file")
	}
	return r.approveWithState(name, rawArgs, &state, nil, "", "")
}

// ApprovePreviewAuto binds a single-use token to preview state with an auto-approval mode tag.
func (r *Registry) ApprovePreviewAuto(name string, rawArgs json.RawMessage, state ApprovalState, mode string) (Approval, error) {
	if !isFileApprovalTool(name) {
		return Approval{}, fmt.Errorf("preview state is only valid for edit_file or create_file")
	}
	return r.approveWithState(name, rawArgs, &state, nil, "", mode)
}

// LogDisclosure records a tool output disclosure event in the audit log.
func (r *Registry) LogDisclosure(tool string, contentHash string, autoMode string, approved bool) error {
	event := "disclosure_approved"
	reason := ""
	if !approved {
		event = "disclosure_denied"
		reason = "user rejected"
	}
	detail := ""
	if autoMode != "" {
		detail = "[auto_approved:" + autoMode + "]"
	}
	return r.logEvent(audit.Event{
		Event:        event,
		Tool:         tool,
		ResultSHA256: contentHash,
		Detail:       detail,
		Reason:       reason,
	})
}

func (r *Registry) approveWithState(name string, rawArgs json.RawMessage, state *ApprovalState, preflightErr error, identity string, autoTag string) (Approval, error) {
	spec, ok := r.handlers[name]
	if !ok {
		return Approval{}, fmt.Errorf("unknown tool %q", name)
	}
	if spec.ReadOnly {
		return Approval{}, fmt.Errorf("tool %q does not require approval", name)
	}

	if name == "web_fetch" {
		_, _, canonicalID, err := CanonicalWebFetchURL(rawArgs)
		if err != nil {
			return Approval{}, err
		}
		if identity != "" && identity != canonicalID {
			return Approval{}, fmt.Errorf("web_fetch canonical URL identity mismatch")
		}
		identity = canonicalID
	}
	if name == "web_search" {
		_, _, canonicalID, err := r.CanonicalWebSearch(rawArgs)
		if err != nil {
			return Approval{}, err
		}
		if identity != "" && identity != canonicalID {
			return Approval{}, fmt.Errorf("web_search canonical request identity mismatch")
		}
		identity = canonicalID
	}
	if name == "memory_save" {
		_, canonicalID, err := r.CanonicalMemorySave(rawArgs)
		if err != nil {
			return Approval{}, err
		}
		if identity != "" && identity != canonicalID {
			return Approval{}, fmt.Errorf("memory_save canonical request identity mismatch")
		}
		identity = canonicalID
	}

	hash := hashBytes(rawArgs)
	if err := r.logEvent(audit.Event{Event: "requested", Tool: name, ArgsSHA256: hash, Detail: r.toolDetail(name, rawArgs)}); err != nil {
		return Approval{}, err
	}
	// Single enforcement point for every auto-approval entry point: a tool that
	// needs a human decision cannot be authorized by a mode string.
	if autoTag != "" && r.RequiresManualApproval(name) {
		_ = r.logEvent(audit.Event{Event: "denied", Tool: name, ArgsSHA256: hash, Reason: "manual approval required"})
		return Approval{}, fmt.Errorf("tool %q always requires a manual approval; auto-approval (%q) is refused", name, autoTag)
	}
	token, err := newApprovalToken()
	if err != nil {
		return Approval{}, err
	}
	now := r.now()
	r.mu.Lock()
	defer r.mu.Unlock()
	r.pruneApprovalsLocked(now)
	if _, exists := r.pending[token]; exists {
		return Approval{}, fmt.Errorf("approval token collision")
	}
	detail := ""
	if autoTag != "" {
		detail = "[auto_approved:" + autoTag + "]"
	}
	if err := r.logEvent(audit.Event{Event: "approved", Tool: name, ArgsSHA256: hash, Detail: detail}); err != nil {
		return Approval{}, err
	}
	r.pending[token] = pendingApproval{state: state, preflightErr: preflightErr, tool: name, argsHash: hash, identity: identity, expires: now.Add(approvalTTL)}

	return Approval{Token: token, Tool: name, ArgsSHA256: hash, IdentitySHA256: identity}, nil
}

// RunApproved executes a tool with a token issued by Approve. Tokens are bound
// to the tool and argument hash, expire, and can be used once.
func (r *Registry) RunApproved(ctx context.Context, name string, rawArgs json.RawMessage, approval Approval) string {
	spec, ok := r.handlers[name]
	if !ok {
		return fmt.Sprintf("Error: unknown tool %q", name)
	}
	hash := hashBytes(rawArgs)

	if spec.ReadOnly {
		_ = r.logEvent(audit.Event{Event: "requested", Tool: name, ArgsSHA256: hash, Detail: r.toolDetail(name, rawArgs)})
		return r.execute(ctx, spec, name, rawArgs, hash)
	}

	expectedIdentity, err := r.expectedApprovalIdentity(name, rawArgs)
	if err != nil {
		return r.failedResult(name, hash, err)
	}
	pending, ok := r.consumeApproval(name, hash, approval, expectedIdentity)
	if !ok {
		_ = r.logEvent(audit.Event{Event: "denied", Tool: name, ArgsSHA256: hash, Reason: "invalid or expired approval token"})
		return "Error: approval token is invalid or expired"
	}

	if isFileApprovalTool(name) {
		if pending.preflightErr != nil {
			return r.failedResult(name, hash, pending.preflightErr)
		}
		if pending.state == nil {
			return r.failedResult(name, hash, fmt.Errorf("missing approval state"))
		}
		var bindErr error
		ctx, bindErr = r.verifyAndBindFileApproval(ctx, name, rawArgs, *pending.state)
		if bindErr != nil {
			return r.failedResult(name, hash, bindErr)
		}
	}
	return r.execute(ctx, spec, name, rawArgs, hash)
}

func (r *Registry) failedResult(name, hash string, err error) string {
	result := fmt.Sprintf("Error: %v", err)
	_ = r.logEvent(audit.Event{Event: "failed", Tool: name, ArgsSHA256: hash, ResultSHA256: hashBytes([]byte(result))})
	return result
}

type editApprovalKey struct{}
type createApprovalKey struct{}

func (r *Registry) execute(ctx context.Context, spec ToolSpec, name string, rawArgs json.RawMessage, hash string) string {
	// Record the decision to execute before side effects happen. If the final
	// result record fails, the log still shows that the operation was started.
	if !spec.ReadOnly {
		if err := r.logEvent(audit.Event{Event: "executing", Tool: name, ArgsSHA256: hash, Detail: r.toolDetail(name, rawArgs)}); err != nil {
			result := fmt.Sprintf("Error: cannot record execution in audit log: %v", err)
			_ = r.logEvent(audit.Event{Event: "failed", Tool: name, ArgsSHA256: hash, ResultSHA256: hashBytes([]byte(result))})
			return result
		}
	}
	outCap := maxOutputBytes
	if spec.MaxOutputBytes > 0 {
		outCap = spec.MaxOutputBytes
	}
	out, err := spec.Execute(ctx, rawArgs)
	if err != nil {
		result := fmt.Sprintf("Error: %v\nOutput:\n%s", err, truncateOutputWithCap(out, outCap))
		_ = r.logEvent(audit.Event{Event: "failed", Tool: name, ArgsSHA256: hash, ResultSHA256: hashBytes([]byte(result))})
		return result
	}
	result := truncateOutputWithCap(out, outCap)
	appliedDetail := ""
	if name == "memory_save" {
		appliedDetail = memorySaveAppliedAuditDetail(out)
	}
	if err := r.logEvent(audit.Event{Event: "applied", Tool: name, ArgsSHA256: hash, ResultSHA256: hashBytes([]byte(result)), Detail: appliedDetail}); err != nil && !spec.ReadOnly {
		result += "\nWarning: audit log write failed"
	}
	return result
}

func (r *Registry) expectedApprovalIdentity(name string, rawArgs json.RawMessage) (string, error) {
	switch name {
	case "mcp_call":
		var p struct {
			Command string `json:"command"`
		}
		if err := json.Unmarshal(rawArgs, &p); err != nil {
			return "", err
		}
		identity, err := r.MCPIdentity(p.Command)
		if err != nil {
			return "", err
		}
		return identity, nil
	case "web_fetch":
		_, _, identity, err := CanonicalWebFetchURL(rawArgs)
		if err != nil {
			return "", err
		}
		return identity, nil
	case "web_search":
		_, _, identity, err := r.CanonicalWebSearch(rawArgs)
		if err != nil {
			return "", err
		}
		return identity, nil
	case "memory_save":
		_, identity, err := r.CanonicalMemorySave(rawArgs)
		if err != nil {
			return "", err
		}
		return identity, nil
	default:
		return "", nil
	}
}

func (r *Registry) consumeApproval(name, hash string, approval Approval, expectedIdentity string) (pendingApproval, bool) {
	if approval.Token == "" || approval.Tool != name || approval.ArgsSHA256 != hash {
		return pendingApproval{}, false
	}
	if approval.IdentitySHA256 != expectedIdentity {
		return pendingApproval{}, false
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	pending, ok := r.pending[approval.Token]
	if !ok {
		return pendingApproval{}, false
	}
	delete(r.pending, approval.Token)
	return pending, pending.tool == name && pending.argsHash == hash &&
		pending.identity == expectedIdentity && !r.now().After(pending.expires)
}

func (r *Registry) pruneApprovalsLocked(now time.Time) {
	for token, pending := range r.pending {
		if now.After(pending.expires) {
			delete(r.pending, token)
		}
	}
}

func (r *Registry) logEvent(event audit.Event) error {
	return r.auditLog.Write(event)
}

func (r *Registry) toolDetail(name string, rawArgs json.RawMessage) string {
	var p struct {
		Path    string  `json:"path"`
		Content *string `json:"content"`
		Pkg     string  `json:"pkg"`
		Command string  `json:"command"`
		Query   string  `json:"query"`
		URL     string  `json:"url"`
		Kind    string  `json:"kind"`
	}
	_ = json.Unmarshal(rawArgs, &p)

	var detail string
	switch name {
	case "create_file":
		pathPart := sanitizeAuditDetail(p.Path)
		if p.Content != nil {
			contentBytes := []byte(*p.Content)
			suffix := fmt.Sprintf(" size=%d sha256=%s", len(contentBytes), hashBytes(contentBytes))
			maxPath := 200 - len(suffix)
			if maxPath < 20 {
				maxPath = 20
			}
			return clipUTF8(pathPart, maxPath) + suffix
		}
		detail = p.Path
	case "edit_file", "read_file", "list_dir":
		detail = p.Path
	case "go_verify":
		detail = p.Pkg
		if detail == "" {
			detail = "./..."
		}
	case "mcp_call":
		detail = p.Command
	case "web_search":
		// Record provider and query length only; never record raw query text in audit logs.
		detail = fmt.Sprintf("provider=%s query_len=%d", r.WebSearchProvider(), len(strings.TrimSpace(p.Query)))
	case "web_fetch":
		// Record destination hostname only; never record query strings or sensitive URL components.
		host := "(invalid-url)"
		if _, parsed, _, err := CanonicalWebFetchURL(rawArgs); err == nil && parsed.Hostname() != "" {
			host = parsed.Hostname()
		} else if u, err := url.Parse(p.URL); err == nil && u.Hostname() != "" {
			host = u.Hostname()
		}
		detail = "host=" + host
	case "memory_save":
		// Record kind only; never record title, body, tags, or secret-bearing arguments in audit logs.
		detail = "kind=" + sanitizeMemoryKindForAudit(p.Kind)
	case "memory_search":
		// Record query length and optional kind only; never record raw query text in audit logs.
		detail = fmt.Sprintf("query_len=%d", len(strings.TrimSpace(p.Query)))
		if strings.TrimSpace(p.Kind) != "" {
			detail += " kind=" + sanitizeMemoryKindForAudit(p.Kind)
		}
	}
	if name == "list_dir" && detail == "" {
		detail = "."
	}
	if name == "mcp_call" && len(detail) > 80 {
		detail = clipUTF8(detail, 80)
	}
	return clipUTF8(sanitizeAuditDetail(detail), 200)
}

func clipUTF8(s string, maxBytes int) string {
	if maxBytes <= 0 {
		return ""
	}
	if len(s) <= maxBytes {
		return s
	}
	cut := maxBytes
	for cut > 0 && !utf8.RuneStart(s[cut]) {
		cut--
	}
	return s[:cut]
}

// sanitizeAuditDetail makes control and bidirectional characters visible so
// a logged path cannot repaint a terminal or spoof a reviewer.
func sanitizeAuditDetail(s string) string {
	s = strings.ToValidUTF8(s, "\uFFFD")
	var b strings.Builder
	for _, r := range s {
		if r < 0x20 || r == 0x7f || (r >= 0x80 && r <= 0x9f) || r == '\u061c' || r == '\u200e' || r == '\u200f' ||
			(r >= '\u202a' && r <= '\u202e') || (r >= '\u2066' && r <= '\u206f') {
			fmt.Fprintf(&b, "\\u%04X", r)
			continue
		}
		b.WriteRune(r)
	}
	return b.String()
}

func (r *Registry) registerDefaults() {
	r.registerCreateTool()
	r.registerEditTool()
	r.registerOtherTools()
	r.registerWebTools()
	r.registerMemoryTools()
}

func (r *Registry) registerCreateTool() {
	r.handlers["create_file"] = ToolSpec{
		Name:        "create_file",
		Description: "Create a new file inside the workspace, creating missing parent directories safely. Fails closed if the file already exists (use edit_file for existing files). Validates Go syntax before saving. Keep content under 4000 bytes (the approval limit); for larger files, create a minimal skeleton and extend it with edit_file.",
		ReadOnly:    false,
		Parameters:  json.RawMessage(`{"type":"object","properties":{"path":{"type":"string","description":"Relative file path within workspace"},"content":{"type":"string","description":"Full content of the new file"}},"required":["path","content"]}`),
		Execute: func(ctx context.Context, args json.RawMessage) (string, error) {
			return r.withProcessLock(func() (string, error) { return r.executeCreate(ctx, args) })
		},
	}
}

func (r *Registry) registerEditTool() {
	r.handlers["edit_file"] = ToolSpec{
		Name:        "edit_file",
		Description: "Replace an exact block of text in an existing file. Validates Go syntax before saving without reformatting unrelated bytes. Keep edits under 4000 bytes (the approval limit). Provide 2-4 lines of unique surrounding context in old_text to avoid ambiguous matches. For large changes, apply multiple smaller syntax-valid edits. For new files, use create_file.",
		ReadOnly:    false,
		Parameters:  json.RawMessage(`{"type":"object","properties":{"path":{"type":"string","description":"Relative file path within workspace"},"old_text":{"type":"string","description":"Exact existing block to replace, including 2-4 lines of surrounding context for uniqueness"},"new_text":{"type":"string","description":"Replacement block"}},"required":["path","old_text","new_text"]}`),
		Execute: func(ctx context.Context, args json.RawMessage) (string, error) {
			return r.withProcessLock(func() (string, error) { return r.executeEdit(ctx, args) })
		},
	}
}

// PathAbsent reports whether relPath does not currently exist (including as
// a dangling symlink) and has no symlink or non-directory parent components.
func (r *Registry) PathAbsent(relPath string) (bool, error) {
	rel, err := r.resolveSafePath(relPath)
	if err != nil {
		return false, err
	}
	parts, err := validateCreatePathBounds(rel)
	if err != nil {
		return false, err
	}
	if err := r.inspectCreateTarget(parts, relPath); err != nil {
		return false, nil
	}
	return true, nil
}

func validateCreatePathBounds(rel string) ([]string, error) {
	if rel == "." {
		return nil, fmt.Errorf("file path cannot be workspace root")
	}
	if len(rel) > maxCreatePathBytes {
		return nil, fmt.Errorf("path is too long (%d bytes; limit %d)", len(rel), maxCreatePathBytes)
	}
	parts := strings.Split(filepath.ToSlash(rel), "/")
	if len(parts)-1 > maxCreateDepth {
		return nil, fmt.Errorf("path nesting too deep (%d parent directories; limit %d)", len(parts)-1, maxCreateDepth)
	}
	for _, part := range parts {
		if part == "" || part == "." || part == ".." {
			return nil, fmt.Errorf("invalid path component %q", part)
		}
		if len(part) > maxPathSegmentBytes {
			return nil, fmt.Errorf("path component too long (%d bytes; limit %d)", len(part), maxPathSegmentBytes)
		}
	}
	return parts, nil
}

func (r *Registry) inspectCreateTarget(parts []string, displayPath string) error {
	parentRoot, _, err := r.walkCreateParents(parts, displayPath, false)
	if err != nil {
		return err
	}
	if parentRoot == nil {
		return nil
	}
	defer parentRoot.Close()
	return ensureCreateTargetAbsent(parentRoot, parts[len(parts)-1], displayPath)
}

func ensureCreateTargetAbsent(parentRoot *os.Root, base, displayPath string) error {
	info, err := parentRoot.Lstat(base)
	if err == nil {
		if info.Mode()&os.ModeSymlink != 0 {
			return fmt.Errorf("creating over symlinks is not supported: %s", displayPath)
		}
		if info.IsDir() {
			return fmt.Errorf("path is a directory: %s", displayPath)
		}
		return fmt.Errorf("file already exists: %s; use edit_file", displayPath)
	}
	if !errors.Is(err, fs.ErrNotExist) {
		return fmt.Errorf("inspect %s: %w", displayPath, err)
	}
	return nil
}

func (r *Registry) walkCreateParents(parts []string, _ string, createDirs bool) (parentRoot *os.Root, created []string, err error) {
	curRoot, err := r.root.OpenRoot(".")
	if err != nil {
		return nil, nil, fmt.Errorf("open workspace root: %w", err)
	}
	closeOnErr := true
	defer func() {
		if closeOnErr && curRoot != nil {
			_ = curRoot.Close()
		}
	}()

	curRel := ""
	for _, part := range parts[:len(parts)-1] {
		if curRel == "" {
			curRel = part
		} else {
			curRel += "/" + part
		}
		info, lerr := curRoot.Lstat(part)
		if lerr == nil {
			if info.Mode()&os.ModeSymlink != 0 {
				return nil, created, fmt.Errorf("symlink path components are not allowed: %s", curRel)
			}
			if !info.IsDir() {
				return nil, created, fmt.Errorf("parent path is not a directory: %s", curRel)
			}
		} else if errors.Is(lerr, fs.ErrNotExist) {
			if !createDirs {
				return nil, nil, nil
			}
			if mkErr := curRoot.Mkdir(part, 0o755); mkErr == nil {
				created = append(created, curRel)
			} else if errors.Is(mkErr, fs.ErrExist) {
				info, lerr = curRoot.Lstat(part)
				if lerr != nil {
					return nil, created, fmt.Errorf("inspect parent %s: %w", curRel, lerr)
				}
				if info.Mode()&os.ModeSymlink != 0 {
					return nil, created, fmt.Errorf("symlink path components are not allowed: %s", curRel)
				}
				if !info.IsDir() {
					return nil, created, fmt.Errorf("parent path is not a directory: %s", curRel)
				}
			} else {
				return nil, created, fmt.Errorf("create parent directory %s: %w", curRel, mkErr)
			}
		} else {
			return nil, created, fmt.Errorf("inspect parent %s: %w", curRel, lerr)
		}

		nextRoot, oerr := curRoot.OpenRoot(part)
		if oerr != nil {
			return nil, created, fmt.Errorf("open parent directory %s: %w", curRel, oerr)
		}
		if verr := verifyOpenedDirNoFollow(curRoot, part, nextRoot); verr != nil {
			_ = nextRoot.Close()
			return nil, created, verr
		}
		_ = curRoot.Close()
		curRoot = nextRoot
	}

	closeOnErr = false
	return curRoot, created, nil
}

func (r *Registry) rollbackCreatedDirs(created []string) {
	for i := len(created) - 1; i >= 0; i-- {
		_ = r.root.Remove(created[i])
	}
}

// executeCreate creates a brand-new file while holding the in-process write
// mutex. Cross-process exclusion is applied by the caller.
func (r *Registry) executeCreate(ctx context.Context, args json.RawMessage) (string, error) {
	r.writeMu.Lock()
	defer r.writeMu.Unlock()

	p, err := parseCreateParams(args)
	if err != nil {
		return "", err
	}
	rel, err := r.resolveSafePath(p.Path)
	if err != nil {
		return "", err
	}
	parts, err := validateCreatePathBounds(rel)
	if err != nil {
		return "", err
	}
	contentBytes := []byte(*p.Content)
	if err := validateGoSyntaxIfNeeded(rel, contentBytes, "create"); err != nil {
		return "", err
	}

	if state, ok := ctx.Value(createApprovalKey{}).(ApprovalState); ok {
		if state.Path != rel || state.Exists || state.Hash != "" {
			return "", fmt.Errorf("approval path mismatch")
		}
		if state.OutputHash == "" || hashBytes(contentBytes) != state.OutputHash {
			return "", fmt.Errorf("output differs from approved preview")
		}
	}
	if err := ctx.Err(); err != nil {
		return "", err
	}

	parentRoot, createdDirs, err := r.walkCreateParents(parts, p.Path, true)
	if err != nil {
		r.rollbackCreatedDirs(createdDirs)
		return "", err
	}
	defer parentRoot.Close()

	if err := ensureCreateTargetAbsent(parentRoot, parts[len(parts)-1], p.Path); err != nil {
		r.rollbackCreatedDirs(createdDirs)
		return "", err
	}
	if err := ctx.Err(); err != nil {
		r.rollbackCreatedDirs(createdDirs)
		return "", err
	}

	err = r.atomicWrite(rel, contentBytes, atomicWriteOpts{
		mode:         0o644,
		preserveMode: false,
		createNew:    true,
		parentRoot:   parentRoot,
	})
	if err != nil {
		var uncertain *commitUncertainError
		if errors.As(err, &uncertain) {
			return fmt.Sprintf("Successfully created %s; warning: %s", p.Path, err), nil
		}
		r.rollbackCreatedDirs(createdDirs)
		return "", err
	}
	return fmt.Sprintf("Successfully created %s", p.Path), nil
}

// executeEdit performs the edit while holding the in-process write mutex.
// Cross-process exclusion is applied by the caller.
func (r *Registry) executeEdit(ctx context.Context, args json.RawMessage) (string, error) {
	r.writeMu.Lock()
	defer r.writeMu.Unlock()
	var p struct {
		Path    string `json:"path"`
		OldText string `json:"old_text"`
		NewText string `json:"new_text"`
	}
	if err := json.Unmarshal(args, &p); err != nil {
		return "", err
	}
	if p.OldText == "" {
		return "", fmt.Errorf("old_text cannot be empty")
	}
	if p.OldText == p.NewText {
		return "", fmt.Errorf("old_text and new_text are identical")
	}

	rel, err := r.resolveSafePath(p.Path)
	if err != nil {
		return "", err
	}
	info, err := r.root.Lstat(rel)
	if err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			return "", fmt.Errorf("file does not exist: %s; use create_file", p.Path)
		}
		return "", err
	}
	if info.IsDir() {
		return "", fmt.Errorf("%s is a directory", p.Path)
	}
	if info.Mode()&os.ModeSymlink != 0 {
		return "", fmt.Errorf("editing symlinks is not supported: %s", p.Path)
	}
	if info.Size() > maxEditBytes {
		return "", fmt.Errorf("file is too large to edit (%d bytes; limit %d)", info.Size(), maxEditBytes)
	}

	contentBytes, err := readBounded(r.root, rel, maxEditBytes)
	if err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			return "", fmt.Errorf("file does not exist: %s; use create_file", p.Path)
		}
		return "", err
	}
	if state, ok := ctx.Value(editApprovalKey{}).(ApprovalState); ok {
		if !state.Exists {
			return "", fmt.Errorf("file does not exist: %s; use create_file", p.Path)
		}
		if state.Path != rel || hashBytes(contentBytes) != state.Hash {
			return "", fmt.Errorf("file changed since approval: %s", p.Path)
		}
	}

	updatedBytes, err := applyEdit(contentBytes, p.Path, rel, p.OldText, p.NewText)
	if err != nil {
		return "", err
	}
	if state, ok := ctx.Value(editApprovalKey{}).(ApprovalState); ok && (state.OutputHash == "" || hashBytes(updatedBytes) != state.OutputHash) {
		return "", fmt.Errorf("output differs from approved preview")
	}
	if err := ctx.Err(); err != nil {
		return "", err
	}

	entry, err := r.shadow.Backup(rel, contentBytes, info.Mode(), updatedBytes)
	if err != nil {
		return "", err
	}
	if err := ctx.Err(); err != nil {
		_ = r.shadow.MarkUndone(entry.ID)
		return "", err
	}
	if err := r.atomicWriteChecked(rel, updatedBytes, info.Mode(), hashBytes(contentBytes)); err != nil {
		var uncertain *commitUncertainError
		if errors.As(err, &uncertain) {
			return fmt.Sprintf("Successfully updated %s (shadow %s); warning: %s", p.Path, entry.ID, err), nil
		}
		_ = r.shadow.MarkUndone(entry.ID)
		return "", err
	}
	return fmt.Sprintf("Successfully updated %s (shadow %s)", p.Path, entry.ID), nil
}

func (r *Registry) registerOtherTools() {
	r.handlers["go_verify"] = ToolSpec{
		Name:        "go_verify",
		Description: "Run go test and go vet with Termux-safe memory limits.",
		ReadOnly:    false,
		Parameters:  json.RawMessage(`{"type":"object","properties":{"pkg":{"type":"string","description":"Package pattern, default ./..."}}}`),
		Execute: func(ctx context.Context, args json.RawMessage) (string, error) {
			var p struct {
				Pkg string `json:"pkg"`
			}
			if err := json.Unmarshal(args, &p); err != nil {
				return "", err
			}
			pkg, err := r.validatePkgPattern(p.Pkg)
			if err != nil {
				return "", err
			}
			return r.runGoVerify(ctx, pkg, 90*time.Second)
		},
	}

	r.handlers["read_file"] = ToolSpec{
		Name:        "read_file",
		Description: "Read a bounded UTF-8-safe byte range inside the workspace. Each page is capped at 3584 bytes. Use format: \"json\" to receive structured pagination (content, truncated flag, and next_offset). When truncated is true, issue another read_file call with offset set to next_offset to read the remainder.",
		ReadOnly:    true,
		Parameters:  json.RawMessage(`{"type":"object","properties":{"path":{"type":"string","description":"Relative file path"},"offset":{"type":"integer","minimum":0,"description":"Zero-based byte offset, default 0"},"limit":{"type":"integer","minimum":1,"maximum":3584,"description":"Maximum input bytes per page, default 3584; a first UTF-8 rune may extend this by up to 3 bytes"},"format":{"type":"string","enum":["text","json"],"description":"text by default; json returns content, truncated, and next_offset"}},"required":["path"]}`),
		Execute: func(ctx context.Context, args json.RawMessage) (string, error) {
			var p struct {
				Path   string `json:"path"`
				Offset int64  `json:"offset"`
				Limit  *int64 `json:"limit"`
				Format string `json:"format"`
			}
			if err := json.Unmarshal(args, &p); err != nil {
				return "", err
			}
			rel, err := r.resolveSafePath(p.Path)
			if err != nil {
				return "", err
			}
			limit := int64(maxReadPageBytes)
			if p.Limit != nil {
				limit = *p.Limit
			}
			if p.Format != "" && p.Format != "text" && p.Format != "json" {
				return "", fmt.Errorf("format must be text or json")
			}
			return r.readFilePage(rel, p.Offset, limit, p.Format == "json")
		},
	}

	r.handlers["list_dir"] = ToolSpec{
		Name:        "list_dir",
		Description: "List directory contents inside the workspace. Displays up to 1000 entries and scans up to 4096 directory entries. Returns F for files and D for directories.",
		ReadOnly:    true,
		Parameters:  json.RawMessage(`{"type":"object","properties":{"path":{"type":"string","description":"Relative directory path (default .)"}}}`),
		Execute: func(ctx context.Context, args json.RawMessage) (string, error) {
			var p struct {
				Path string `json:"path"`
			}
			_ = json.Unmarshal(args, &p)
			if strings.TrimSpace(p.Path) == "" {
				p.Path = "."
			}
			rel, err := r.resolveSafePath(p.Path)
			if err != nil {
				return "", err
			}
			return r.listDir(rel)
		},
	}

	r.handlers["repo_map"] = ToolSpec{
		Name:        "repo_map",
		Description: "Generate a compact, deterministic structural map of Go packages, types, methods, and functions in the workspace using Go AST. Read-only and resource-bounded (12 KiB cap).",
		ReadOnly:    true,
		Parameters:  json.RawMessage(`{"type":"object","properties":{"path":{"type":"string","description":"Relative directory or file path to map (default .)"},"include_unexported":{"type":"boolean","description":"Include unexported symbols (default false)"},"max_files":{"type":"integer","minimum":1,"maximum":400,"description":"Maximum number of non-test Go files to analyze (default 100, max 400)"}}}`),
		Execute: func(ctx context.Context, args json.RawMessage) (string, error) {
			return r.repoMap(ctx, args)
		},
	}

	r.handlers["grep_search"] = ToolSpec{
		Name:           "grep_search",
		Description:    "Search workspace text with a literal or RE2 regex query. Read-only and resource-bounded: replies are JSON with matches, total_matches, files_scanned, and truncated; skips binary files, symlinks, sensitive paths, and .git/.nabd/node_modules/vendor; budget 2000 files and 16 MiB of reads.",
		ReadOnly:       true,
		MaxOutputBytes: maxGrepOutputBytes,
		Parameters:     json.RawMessage(`{"type":"object","properties":{"query":{"type":"string","description":"Text to find (required, max 256 bytes); treated literally unless is_regex is true"},"path":{"type":"string","description":"Relative file or directory to search (default .)"},"is_regex":{"type":"boolean","description":"Interpret query as an RE2 regular expression (default false)"},"case_sensitive":{"type":"boolean","description":"Case-sensitive matching (default true)"},"file_glob":{"type":"string","description":"Optional glob filter on file name or relative path, e.g. *.go"},"max_results":{"type":"integer","minimum":1,"maximum":100,"description":"Maximum matches returned (default 50, max 100)"}},"required":["query"]}`),
		Execute: func(ctx context.Context, args json.RawMessage) (string, error) {
			return r.grepSearch(ctx, args)
		},
	}

	r.handlers["mcp_call"] = ToolSpec{
		Name:        "mcp_call",
		Description: "Call a tool on an external MCP server via stdio. Starts the server process, calls the tool, then closes it.",
		ReadOnly:    false,
		Parameters:  json.RawMessage(`{"type":"object","properties":{"command":{"type":"string","description":"Command to start the MCP server (e.g. nabd-mcp)"},"tool":{"type":"string","description":"Name of the tool to call on the MCP server"},"arguments":{"type":"object","description":"Arguments to pass to the tool"}},"required":["command","tool"]}`),
		Execute: func(ctx context.Context, args json.RawMessage) (string, error) {
			var p struct {
				Command   string                 `json:"command"`
				Tool      string                 `json:"tool"`
				Arguments map[string]interface{} `json:"arguments"`
			}
			if err := json.Unmarshal(args, &p); err != nil {
				return "", err
			}
			if p.Command == "" || p.Tool == "" {
				return "", fmt.Errorf("command and tool are required")
			}
			if r.sandbox == nil {
				return "", fmt.Errorf("mcp_call is disabled: no proven execution sandbox is active")
			}
			server, err := r.mcpConfig.Resolve(p.Command, "")
			if err != nil {
				return "", err
			}
			return callExternalMCPServer(ctx, r.workDir, r.sandbox, server, p.Tool, p.Arguments)
		},
	}
}

// validatePkgPattern accepts only package patterns that stay inside the
// workspace. Module paths ("std", "github.com/...") are rejected so go_verify
// cannot be pointed at packages outside the project.
func (r *Registry) validatePkgPattern(pattern string) (string, error) {
	pattern = strings.TrimSpace(pattern)
	if pattern == "" {
		return "./...", nil
	}
	if strings.HasPrefix(pattern, "-") {
		return "", fmt.Errorf("invalid package pattern %q: flags are not allowed", pattern)
	}
	switch pattern {
	case ".", "./":
		return ".", nil
	case "./...":
		return pattern, nil
	}
	if !strings.HasPrefix(pattern, "./") {
		return "", fmt.Errorf("package pattern must be workspace-relative (for example ./... or ./pkg)")
	}

	rel := strings.TrimSuffix(pattern, "/...")
	rel = strings.TrimPrefix(rel, "./")
	if rel == "" {
		return "", fmt.Errorf("invalid package pattern %q", pattern)
	}
	if _, err := r.resolveSafePath(rel); err != nil {
		return "", err
	}
	return pattern, nil
}

// runGoVerify executes go test with a hard timeout and a structured summary.
// Test failures are normal results, not tool errors.
func (r *Registry) runGoVerify(ctx context.Context, pkg string, timeout time.Duration) (string, error) {
	if r.sandbox == nil && !r.allowUnsandboxedForTest {
		return "", fmt.Errorf("go_verify is disabled: no proven execution sandbox is active")
	}
	tCtx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()

	cmd, err := r.goVerifyCommand(tCtx, pkg)
	if err != nil {
		return "", err
	}

	out := newBoundedOutput(maxVerifyOutputBytes)
	cmd.Stdout = out
	cmd.Stderr = out
	err = cmd.Run()

	status := "PASS"
	if err != nil {
		status = "FAIL"
	}
	if tCtx.Err() == context.DeadlineExceeded {
		status = "FAIL (timeout)"
	} else if tCtx.Err() == context.Canceled {
		status = "CANCELLED (context canceled)"
	}
	if err != nil && !isGoTestFailure(tCtx, err) {
		return "", err
	}
	head, omitted, tail := out.segments()
	return summarizeGoTest(status, string(head), omitted, string(tail), err), nil
}

// goVerifyCommand builds the verifier command. Without a proven sandbox it
// fails closed unless allowUnsandboxedForTest is enabled by internal unit
// tests; with a proven sandbox, the Go toolchain is bound read-only and project
// code runs in an isolated namespace with a writable tmpfs cache.
func (r *Registry) goVerifyCommand(ctx context.Context, pkg string) (*exec.Cmd, error) {
	if r.sandbox == nil && !r.allowUnsandboxedForTest {
		return nil, fmt.Errorf("go_verify is disabled: no proven execution sandbox is active")
	}
	goTool, err := exec.LookPath("go")
	if err != nil {
		return nil, err
	}
	if resolved, err := filepath.EvalSymlinks(goTool); err == nil {
		goTool = resolved
	}
	env := buildVerifyEnv()
	if r.sandbox == nil {
		cmd := exec.CommandContext(ctx, goTool, "test", "-p", "2", "-vet=all", pkg)
		cmd.Dir = r.workDir
		cmd.Env = env
		cmd.WaitDelay = 2 * time.Second
		configureProcessTree(cmd)
		return cmd, nil
	}
	goroot, err := exec.Command(goTool, "env", "GOROOT").Output()
	if err != nil {
		return nil, fmt.Errorf("locate GOROOT: %w", err)
	}
	gorootPath := strings.TrimSpace(string(goroot))
	if gorootPath == "" {
		return nil, fmt.Errorf("empty GOROOT")
	}
	binds := []string{gorootPath}
	// A dynamically linked Go toolchain needs its ELF interpreter and shared
	// libraries; only those individual files are bound, never /lib or /usr/lib.
	loaderBinds, err := sandbox.LoaderBinds(goTool)
	if err != nil {
		return nil, fmt.Errorf("resolve dynamic loader for the Go toolchain: %w", err)
	}
	binds = appendUniquePaths(binds, loaderBinds...)
	sandboxEnv := []string{
		"HOME=/tmp", "TMPDIR=/tmp", "PATH=/usr/bin:/bin",
		"GOROOT=" + gorootPath, "GOCACHE=/tmp/gocache", "GOPATH=/tmp/gopath",
		"GOMODCACHE=/tmp/gopath/pkg/mod", "GOTOOLCHAIN=local",
		"GOPROXY=off", "GOFLAGS=-mod=readonly", "CGO_ENABLED=0",
		"GOMEMLIMIT=512MiB", "GOGC=50",
	}
	if modcache := os.Getenv("GOMODCACHE"); modcache != "" {
		// The module cache is read-only; the build cache remains on tmpfs.
		binds = appendUniquePaths(binds, modcache)
		sandboxEnv = append(sandboxEnv, "GOMODCACHE="+modcache)
	}
	maskedDirs, maskedFiles, err := collectSensitiveWorkspaceMasks(r.workDir)
	if err != nil {
		return nil, err
	}
	profile := sandbox.Profile{
		WorkDir:            r.workDir,
		WritableWorkspace:  true,
		ExtraReadonlyBinds: binds,
		MaskedDirs:         maskedDirs,
		MaskedFiles:        maskedFiles,
		Env:                sandboxEnv,
		MemoryLimitBytes:   sandbox.DefaultMemoryLimit(),
		ProcessLimit:       64,
	}
	cmd, err := r.sandbox.Wrap(ctx, profile, goTool, "test", "-p", "2", "-vet=all", pkg)
	if err != nil {
		return nil, err
	}
	configureProcessTree(cmd)
	return cmd, nil
}

// appendUniquePaths appends only paths that are not already bound, so
// bubblewrap never receives two binds for the same target.
func appendUniquePaths(binds []string, extra ...string) []string {
	seen := make(map[string]struct{}, len(binds))
	for _, bind := range binds {
		seen[bind] = struct{}{}
	}
	for _, path := range extra {
		if _, dup := seen[path]; dup {
			continue
		}
		seen[path] = struct{}{}
		binds = append(binds, path)
	}
	return binds
}

func isGoTestFailure(ctx context.Context, err error) bool {
	if ctx.Err() != nil {
		return true
	}
	var exitErr *exec.ExitError
	return errors.As(err, &exitErr)
}

const (
	maxSummarySignals = 5
	maxSummaryTail    = 12
	maxSummaryLine    = 180
)

// isGoTestSignal reports whether a trimmed output line indicates a test failure,
// a build failure or a panic. The summary keeps these even when the output is
// far larger than the retained buffer.
func isGoTestSignal(trimmed string) bool {
	// "FAIL" as a whole line is the package verdict, "FAIL\t<pkg>" its summary
	// line. A plain "go: downloading ..." line is routine noise and must not
	// consume a signal slot, so only toolchain errors are matched under "go: ".
	if trimmed == "FAIL" ||
		strings.HasPrefix(trimmed, "FAIL\t") ||
		strings.HasPrefix(trimmed, "--- FAIL:") ||
		strings.HasPrefix(trimmed, "panic:") ||
		strings.HasPrefix(trimmed, "--- build failed") ||
		strings.HasPrefix(trimmed, "go: build") ||
		strings.HasPrefix(trimmed, "go: error") ||
		strings.HasPrefix(trimmed, "go: cannot") {
		return true
	}
	return strings.Contains(trimmed, "build failed") ||
		strings.Contains(trimmed, "cannot find") ||
		strings.Contains(trimmed, "undefined:") ||
		strings.Contains(trimmed, "panic:")
}

// summarizeGoTest returns a small, structured result instead of raw logs: a
// status line, the leading output lines with the first failure signals, and the
// tail of the output.
//
// head, omitted and tail come from boundedOutput.segments, so the beginning and
// the end of the stream are analysed separately. Building the summary from the
// flattened string instead would lose both: the inline omission marker sits
// between the two retained halves and can itself be truncated away, and signal
// lines that were dropped from the middle of a huge log stay invisible.
func summarizeGoTest(status, head string, omitted int64, tail string, err error) string {
	headLines := splitSummaryLines(head)
	tailLines := splitSummaryLines(tail)
	// Head and tail describe the same output when nothing was dropped; then only
	// the head is printed once.
	showTail := omitted > 0

	var b strings.Builder
	b.WriteString(status)
	if err != nil {
		b.WriteString(" (")
		b.WriteString(clipSummary(err.Error(), maxSummaryLine))
		b.WriteString(")")
	}
	b.WriteString("\n")

	// Signals are collected from the beginning first, so a failing build or the
	// first subtest failure is never hidden behind output that was dropped. The
	// tail keeps a reserved share so a late failure is still reported when the
	// head alone would use up the budget.
	headBudget := maxSummarySignals
	if showTail {
		headBudget = maxSummarySignals / 2
	}
	signals := 0
	seen := make(map[string]bool, maxSummarySignals)
	emit := func(line string) {
		clipped := clipSummary(strings.TrimSpace(line), maxSummaryLine)
		if seen[clipped] {
			return
		}
		seen[clipped] = true
		b.WriteString("  ")
		b.WriteString(clipped)
		b.WriteString("\n")
		signals++
	}
	for _, line := range signalLines(head, headBudget) {
		emit(line)
	}
	if showTail {
		for _, line := range signalLines(tail, maxSummarySignals-signals) {
			emit(line)
		}
	}

	if len(headLines) == 0 {
		headLines = tailLines
		showTail = false
	}
	b.WriteString("--- head ---\n")
	if len(headLines) == 0 {
		b.WriteString("(no output)\n")
		return b.String()
	}
	for _, line := range headLines {
		b.WriteString(clipSummary(line, maxSummaryLine))
		b.WriteString("\n")
	}

	if !showTail {
		return b.String()
	}
	b.WriteString(fmt.Sprintf("... [%d bytes omitted] ...\n", omitted))
	b.WriteString("--- tail ---\n")
	for _, line := range lastLines(tail, maxSummarySegmentLines) {
		b.WriteString(clipSummary(line, maxSummaryLine))
		b.WriteString("\n")
	}
	return b.String()
}

// lastLines returns the final want lines of segment, without collapsing.
func lastLines(segment string, want int) []string {
	segment = strings.ReplaceAll(segment, "\r\n", "\n")
	lines := strings.Split(segment, "\n")
	if len(lines) > 0 && lines[len(lines)-1] == "" {
		lines = lines[:len(lines)-1]
	}
	if want <= 0 || len(lines) <= want {
		return lines
	}
	return lines[len(lines)-want:]
}

// maxSummarySegmentLines bounds each printed segment (head and tail) to half of
// maxSummaryTail, so head and tail together never exceed the budget the tests
// and the docs promise.
const maxSummarySegmentLines = maxSummaryTail / 2

// signalLines returns the lines of a segment that look like failure markers.
// It scans the whole segment rather than only the printed window: the "--- FAIL:"
// line of a test normally sits a few lines above the trailing "FAIL"/"exit
// status" lines, outside any fixed-size slice taken from the end.
func signalLines(segment string, limit int) []string {
	if limit <= 0 {
		return nil
	}
	var kept []string
	for _, raw := range strings.Split(strings.ReplaceAll(segment, "\r\n", "\n"), "\n") {
		trimmed := strings.TrimSpace(raw)
		if trimmed == "" || !isGoTestSignal(trimmed) {
			continue
		}
		kept = append(kept, trimmed)
		if len(kept) >= limit {
			break
		}
	}
	return kept
}

// splitSummaryLines reduces a segment to a printable window of at most
// maxSummarySegmentLines lines: the first half, an omission marker, and the last
// half. Keeping the window fixed is what bounds the summary; the retained
// buffer itself is 64 KiB.
func splitSummaryLines(segment string) []string {
	segment = strings.ReplaceAll(segment, "\r\n", "\n")
	lines := strings.Split(segment, "\n")
	if len(lines) > 0 && lines[len(lines)-1] == "" {
		lines = lines[:len(lines)-1]
	}
	for len(lines) > 0 && lines[0] == "" {
		lines = lines[1:]
	}
	if len(lines) <= maxSummarySegmentLines {
		return lines
	}
	edge := maxSummarySegmentLines / 2
	trimmed := make([]string, 0, maxSummarySegmentLines+1)
	trimmed = append(trimmed, lines[:edge]...)
	trimmed = append(trimmed, "...")
	trimmed = append(trimmed, lines[len(lines)-edge:]...)
	return trimmed
}

func clipSummary(s string, n int) string {
	if len(s) <= n {
		return s
	}
	cut := n
	for cut > 0 && !utf8.RuneStart(s[cut]) {
		cut--
	}
	return s[:cut] + "..."
}

// verifyEnvAllowlist is the only set of parent variables forwarded to project
// test code. Secrets such as API keys and proxy credentials are intentionally
// omitted.
var verifyEnvAllowlist = []string{
	"PATH", "HOME", "USER", "LOGNAME", "SHELL", "LANG", "LC_ALL", "LC_CTYPE", "TZ",
	"TMPDIR", "TMP", "TEMP",
	"GOROOT", "GOPATH", "GOCACHE", "GOMODCACHE", "GOTOOLCHAIN",
	"GOOS", "GOARCH", "GOARM", "GOAMD64", "GOEXPERIMENT",
	"CGO_ENABLED", "CC", "CXX", "PKG_CONFIG",
	"PREFIX", "ANDROID_ROOT", "ANDROID_DATA", "ANDROID_STORAGE",
	"SSL_CERT_FILE", "SSL_CERT_DIR",
	"SYSTEMROOT", "WINDIR", "COMSPEC", "PATHEXT",
	"USERPROFILE", "HOMEDRIVE", "HOMEPATH", "APPDATA", "LOCALAPPDATA",
}

func buildVerifyEnv() []string {
	env := make([]string, 0, len(verifyEnvAllowlist)+4)
	for _, key := range verifyEnvAllowlist {
		if value, ok := os.LookupEnv(key); ok {
			env = append(env, key+"="+value)
		}
	}
	env = append(env,
		"GOMEMLIMIT=512MiB",
		"GOGC=50",
		"GOFLAGS=-mod=readonly",
		"GOPROXY=off",
		"GOTOOLCHAIN=local",
	)
	if os.Getenv("TMPDIR") == "" {
		if prefix := os.Getenv("PREFIX"); prefix != "" {
			env = append(env, "TMPDIR="+filepath.Join(prefix, "tmp"))
		}
	}
	return env
}

func truncateOutput(s string) string {
	return truncateOutputWithCap(s, maxOutputBytes)
}

func truncateOutputWithCap(s string, maxBytes int) string {
	if maxBytes <= 0 {
		maxBytes = maxOutputBytes
	}
	if len(s) <= maxBytes {
		return s
	}
	half := maxBytes / 2

	head := s[:half]
	for len(head) > 0 && !utf8.ValidString(head) {
		head = head[:len(head)-1]
	}

	tail := s[len(s)-half:]
	for len(tail) > 0 && !utf8.ValidString(tail) {
		tail = tail[1:]
	}

	return head + "\n...[output truncated]...\n" + tail
}
