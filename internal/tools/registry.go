package tools

import (
	"bytes"
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"go/format"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"
	"unicode/utf8"

	"github.com/amiraq1/nabd/internal/audit"
	shadowstore "github.com/amiraq1/nabd/internal/shadow"
	"github.com/mark3labs/mcp-go/client"
	"github.com/mark3labs/mcp-go/client/transport"
	"github.com/mark3labs/mcp-go/mcp"
)

const (
	maxOutputBytes       = 4096
	maxReadBytes         = 3584
	maxEditBytes         = 2 << 20
	maxListEntries       = 1000
	maxVerifyOutputBytes = 1 << 20
)

// fileMutationMu serializes edit/undo transactions across Registry instances.
// It is process-local; external writers are detected by inode and digest checks,
// but cannot be locked out atomically on all supported platforms.
var fileMutationMu sync.Mutex

type ApprovalFunc func(toolName, summary string) bool

type ToolSpec struct {
	Name        string
	Description string
	Parameters  json.RawMessage
	// ReadOnly marks tools that cannot modify the workspace or execute
	// external code. The zero value is false, so every new tool requires
	// approval unless its author explicitly declares it read-only.
	ReadOnly bool
	Execute  func(ctx context.Context, args json.RawMessage) (string, error)
}

type Registry struct {
	workDir  string
	root     *os.Root
	shadow   *shadowstore.Store
	auditLog *audit.Log
	approve  ApprovalFunc
	handlers map[string]ToolSpec
	mu       sync.Mutex
	// beforeAtomicReplace is an internal test seam for deterministic race tests.
	beforeAtomicReplace func()
	pending             map[string]pendingApproval
	now                 func() time.Time
}

type pendingApproval struct {
	tool       string
	argsHash   string
	basePath   string
	baseSHA256 string
	baseFile   os.FileInfo
	baseFD     *os.File
	expires    time.Time
}

// Approval is a single-use token proving that the UI obtained an explicit
// approval for this exact tool call.
type Approval struct {
	Token      string
	Tool       string
	ArgsSHA256 string
	BasePath   string
	BaseSHA256 string
}

const approvalTTL = 5 * time.Minute

func NewRegistry(workDir string, approve ApprovalFunc) (*Registry, error) {
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
	auditLog, err := audit.Open(root)
	if err != nil {
		_ = root.Close()
		return nil, err
	}
	r := &Registry{
		workDir:  absRoot,
		root:     root,
		shadow:   store,
		auditLog: auditLog,
		approve:  approve,
		handlers: make(map[string]ToolSpec),
		pending:  make(map[string]pendingApproval),
		now:      time.Now,
	}
	r.registerDefaults()
	return r, nil
}

// Close releases the workspace handle. The registry must not be used afterwards.
func (r *Registry) Close() error {
	if r.root == nil {
		return nil
	}
	r.mu.Lock()
	for token, pending := range r.pending {
		if pending.baseFD != nil {
			_ = pending.baseFD.Close()
		}
		delete(r.pending, token)
	}
	r.mu.Unlock()
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

// Has reports whether a tool is registered.
func (r *Registry) Has(name string) bool {
	_, ok := r.handlers[name]
	return ok
}

// Disable removes tools from the registry. Disabled tools are neither
// advertised nor executable.
func (r *Registry) Disable(names ...string) {
	for _, name := range names {
		delete(r.handlers, name)
	}
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
	for _, part := range parts {
		switch strings.ToLower(part) {
		case ".nabd", ".git":
			return true
		}
	}

	base := strings.ToLower(parts[len(parts)-1])
	if strings.HasPrefix(base, ".nabd-tmp-") {
		return true
	}
	switch {
	case base == ".env":
		return true
	case strings.HasPrefix(base, ".env."):
		switch base {
		case ".env.example", ".env.sample", ".env.template", ".env.dist":
			return false
		}
		return true
	case strings.HasPrefix(base, "id_") && !strings.HasSuffix(base, ".pub"):
		return true
	}

	switch strings.ToLower(filepath.Ext(base)) {
	case ".pem", ".key", ".p12", ".pfx", ".jks", ".keystore":
		return true
	}
	return false
}

func (r *Registry) openSingleLinkFile(rel string) (*os.File, error) {
	preInfo, err := r.root.Stat(rel)
	if err != nil {
		return nil, err
	}
	if !preInfo.Mode().IsRegular() {
		return nil, fmt.Errorf("refusing non-regular file %s", rel)
	}
	f, err := openWorkspaceFile(r.root, rel)
	if err != nil {
		return nil, err
	}
	info, err := f.Stat()
	if err != nil {
		_ = f.Close()
		return nil, err
	}
	if !info.Mode().IsRegular() {
		_ = f.Close()
		return nil, fmt.Errorf("refusing non-regular file %s", rel)
	}
	if err := ensureNoHardLinks(f); err != nil {
		_ = f.Close()
		return nil, err
	}
	return f, nil
}

// FileDigest returns the SHA-256 of a file inside the workspace. The approval
// UI uses it to confirm that the file did not change between the preview and
// the approved edit.
func (r *Registry) FileDigest(relPath string) (string, error) {
	rel, err := r.resolveSafePath(relPath)
	if err != nil {
		return "", err
	}
	f, err := r.openSingleLinkFile(rel)
	if err != nil {
		return "", err
	}
	defer f.Close()
	info, err := f.Stat()
	if err != nil {
		return "", err
	}
	if !info.Mode().IsRegular() {
		return "", fmt.Errorf("refusing non-regular file %s", rel)
	}
	if info.Size() > maxEditBytes {
		return "", fmt.Errorf("file is too large to fingerprint (%d bytes; limit %d)", info.Size(), maxEditBytes)
	}

	h := sha256.New()
	n, err := io.Copy(h, io.LimitReader(f, maxEditBytes+1))
	if err != nil {
		return "", err
	}
	if n > maxEditBytes {
		return "", fmt.Errorf("file is too large to fingerprint (limit %d bytes)", maxEditBytes)
	}
	if err := ensureNoHardLinks(f); err != nil {
		return "", err
	}
	return hex.EncodeToString(h.Sum(nil)), nil
}

// PreviewEdit returns the exact base bytes and final bytes that edit_file
// would use, including Go formatting and preserved CRLF line endings.
func (r *Registry) PreviewEdit(path, oldText, newText string) (string, []byte, []byte, error) {
	rel, err := r.resolveSafePath(path)
	if err != nil {
		return "", nil, nil, err
	}
	content, _, err := r.readEditableFile(rel)
	if err != nil {
		return "", nil, nil, err
	}
	updated, err := prepareEdit(rel, content, oldText, newText)
	if err != nil {
		return "", nil, nil, err
	}
	return hashBytes(content), content, updated, nil
}

func (r *Registry) readEditableFile(rel string) ([]byte, os.FileMode, error) {
	f, content, mode, err := r.openEditableFile(rel)
	if err != nil {
		return nil, 0, err
	}
	if err := f.Close(); err != nil {
		return nil, 0, err
	}
	return content, mode, nil
}

// openEditableFile keeps the approved inode open through the eventual rename.
// The caller must close the returned descriptor after its final identity and
// digest checks.
func (r *Registry) openEditableFile(rel string) (*os.File, []byte, os.FileMode, error) {
	info, err := r.root.Lstat(rel)
	if err != nil {
		return nil, nil, 0, err
	}
	if info.Mode()&os.ModeSymlink != 0 {
		return nil, nil, 0, fmt.Errorf("editing symlinks is not supported: %s", rel)
	}
	if !info.Mode().IsRegular() {
		return nil, nil, 0, fmt.Errorf("refusing non-regular file %s", rel)
	}
	if info.Size() > maxEditBytes {
		return nil, nil, 0, fmt.Errorf("file is too large to edit (%d bytes; limit %d)", info.Size(), maxEditBytes)
	}

	f, err := r.openSingleLinkFile(rel)
	if err != nil {
		return nil, nil, 0, err
	}
	openedInfo, statErr := f.Stat()
	var content []byte
	var readErr error
	if statErr == nil {
		if !openedInfo.Mode().IsRegular() {
			statErr = fmt.Errorf("refusing non-regular file %s", rel)
		} else if !os.SameFile(info, openedInfo) {
			statErr = fmt.Errorf("file changed while opening %s; re-read it and retry", rel)
		} else if openedInfo.Size() > maxEditBytes {
			statErr = fmt.Errorf("file is too large to edit (%d bytes; limit %d)", openedInfo.Size(), maxEditBytes)
		} else {
			content, readErr = io.ReadAll(io.LimitReader(f, maxEditBytes+1))
			if readErr == nil && int64(len(content)) > maxEditBytes {
				readErr = fmt.Errorf("file is too large to edit (limit %d bytes)", maxEditBytes)
			}
		}
	}
	linkErr := ensureNoHardLinks(f)
	identityErr := r.checkOpenFilePath(rel, f)
	if err := errors.Join(statErr, readErr, linkErr, identityErr); err != nil {
		_ = f.Close()
		return nil, nil, 0, err
	}
	return f, content, openedInfo.Mode(), nil
}

func (r *Registry) checkOpenFilePath(rel string, f *os.File) error {
	openedInfo, err := f.Stat()
	if err != nil {
		return err
	}
	pathInfo, err := r.root.Lstat(rel)
	if err != nil {
		return fmt.Errorf("file changed after approval; re-read it and retry: %w", err)
	}
	if pathInfo.Mode()&os.ModeSymlink != 0 || !pathInfo.Mode().IsRegular() || !os.SameFile(openedInfo, pathInfo) {
		return fmt.Errorf("file changed after approval; re-read it and retry")
	}
	if err := ensureNoHardLinks(f); err != nil {
		return err
	}
	return nil
}

func (r *Registry) verifyApprovedFile(rel string, f *os.File, expectedSHA string) error {
	if err := r.checkOpenFilePath(rel, f); err != nil {
		return err
	}
	digest, err := digestOpenFile(f)
	if err != nil {
		return err
	}
	if digest != expectedSHA {
		return fmt.Errorf("file changed after approval; re-read it and retry")
	}
	return nil
}

func digestOpenFile(f *os.File) (string, error) {
	info, err := f.Stat()
	if err != nil {
		return "", err
	}
	if !info.Mode().IsRegular() || info.Size() > maxEditBytes {
		return "", fmt.Errorf("refusing to fingerprint a non-regular or oversized file")
	}
	h := sha256.New()
	n, err := io.Copy(h, io.NewSectionReader(f, 0, info.Size()))
	if err != nil {
		return "", err
	}
	if n != info.Size() {
		return "", fmt.Errorf("file changed while fingerprinting")
	}
	return hex.EncodeToString(h.Sum(nil)), nil
}

func (r *Registry) editableSnapshot(rel string) (*os.File, string, os.FileInfo, error) {
	f, content, _, err := r.openEditableFile(rel)
	if err != nil {
		return nil, "", nil, err
	}
	digest := hashBytes(content)
	if err := r.verifyApprovedFile(rel, f, digest); err != nil {
		_ = f.Close()
		return nil, "", nil, err
	}
	info, err := f.Stat()
	if err != nil {
		_ = f.Close()
		return nil, "", nil, err
	}
	return f, digest, info, nil
}

func prepareEdit(rel string, contentBytes []byte, oldText, newText string) ([]byte, error) {
	if oldText == "" {
		return nil, fmt.Errorf("old_text cannot be empty")
	}
	if oldText == newText {
		return nil, fmt.Errorf("old_text and new_text are identical")
	}

	hasCRLF := bytes.Contains(contentBytes, []byte("\r\n"))
	content := strings.ReplaceAll(string(contentBytes), "\r\n", "\n")
	oldNorm := strings.ReplaceAll(oldText, "\r\n", "\n")
	newNorm := strings.ReplaceAll(newText, "\r\n", "\n")
	if oldNorm == newNorm {
		return nil, fmt.Errorf("old_text and new_text are identical after line-ending normalization")
	}

	count := strings.Count(content, oldNorm)
	if count == 0 {
		return nil, fmt.Errorf("old_text not found in %s", rel)
	}
	if count != 1 {
		return nil, fmt.Errorf("old_text appears %d times; include more surrounding lines to make it unique", count)
	}

	updatedBytes := []byte(strings.Replace(content, oldNorm, newNorm, 1))
	if strings.HasSuffix(rel, ".go") {
		formatted, err := format.Source(updatedBytes)
		if err != nil {
			return nil, fmt.Errorf("edit rejected due to Go syntax error:\n%v", err)
		}
		updatedBytes = formatted
	}
	if hasCRLF {
		updatedBytes = bytes.ReplaceAll(updatedBytes, []byte("\n"), []byte("\r\n"))
	}
	if bytes.Equal(updatedBytes, contentBytes) {
		return nil, fmt.Errorf("edit would not change %s", rel)
	}
	return updatedBytes, nil
}

func (r *Registry) readFile(rel string, offset, limit int64) (string, error) {
	if offset < 0 || limit < 0 {
		return "", fmt.Errorf("offset and limit must be non-negative")
	}
	if limit == 0 || limit > maxReadBytes {
		limit = maxReadBytes
	}
	f, err := r.openSingleLinkFile(rel)
	if err != nil {
		return "", err
	}
	defer f.Close()
	info, err := f.Stat()
	if err != nil {
		return "", err
	}
	if info.IsDir() {
		return "", fmt.Errorf("%s is a directory", rel)
	}
	if _, err := f.Seek(offset, io.SeekStart); err != nil {
		return "", err
	}

	data, err := io.ReadAll(io.LimitReader(f, limit+utf8.UTFMax))
	if err != nil {
		return "", err
	}
	data, truncated := completeUTF8Page(data, limit)
	if err := ensureNoHardLinks(f); err != nil {
		return "", err
	}
	out := strings.ToValidUTF8(string(data), "\uFFFD")
	if truncated {
		out += fmt.Sprintf("\n...[truncated; continue at byte offset %d]", offset+int64(len(data)))
	}
	return out, nil
}

func completeUTF8Page(data []byte, limit int64) ([]byte, bool) {
	end := int(limit)
	if end > len(data) {
		end = len(data)
	}
	if end < len(data) && end > 0 && !utf8.RuneStart(data[end]) {
		start := end
		for start > 0 && !utf8.RuneStart(data[start]) {
			start--
		}
		if start == 0 && utf8.FullRune(data) {
			_, runeSize := utf8.DecodeRune(data)
			if runeSize > end {
				end = runeSize
			} else {
				end = start
			}
		} else {
			end = start
		}
	}
	truncated := len(data) > end
	return data[:end], truncated
}

func (r *Registry) listDir(rel string) (string, error) {
	f, err := r.root.Open(rel)
	if err != nil {
		return "", err
	}
	defer f.Close()

	entries, err := f.ReadDir(-1)
	if err != nil {
		return "", err
	}
	sort.Slice(entries, func(i, j int) bool { return entries[i].Name() < entries[j].Name() })

	var b strings.Builder
	shown, hidden := 0, 0
	for _, e := range entries {
		if isSensitivePath(filepath.Join(rel, e.Name())) {
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
		b.WriteString(fmt.Sprintf("... [%d more entries]\n", hidden))
	}
	return b.String(), nil
}

// Undo restores the most recent non-undone shadow entry for relPath (or the
// most recent entry overall when relPath is empty). It refuses if the file
// changed since the edit or if the shadow copy is corrupted.
func (r *Registry) Undo(relPath string) (string, error) {
	fileMutationMu.Lock()
	defer fileMutationMu.Unlock()

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
	if info.Mode()&os.ModeSymlink != 0 {
		_ = r.logEvent(audit.Event{Event: "denied", Tool: "undo", Detail: entry.Path, Reason: "symlink"})
		return "", fmt.Errorf("refusing to undo a symlink: %s", entry.Path)
	}

	f, err := r.openSingleLinkFile(currentPath)
	if err != nil {
		return "", err
	}
	defer f.Close()
	if err := r.checkOpenFilePath(currentPath, f); err != nil {
		return "", err
	}
	current, readErr := io.ReadAll(io.LimitReader(f, maxEditBytes+1))
	if readErr != nil {
		return "", readErr
	}
	if int64(len(current)) > maxEditBytes {
		return "", fmt.Errorf("refusing undo: %s is too large to restore safely", entry.Path)
	}
	currentHash := hashBytes(current)
	if currentHash != entry.SHA256After {
		_ = r.logEvent(audit.Event{Event: "denied", Tool: "undo", ArgsSHA256: currentHash, Detail: entry.Path, Reason: "file changed since the edit"})
		return "", fmt.Errorf("refusing undo: %s changed since the edit", entry.Path)
	}

	if err := r.logEvent(audit.Event{Event: "requested", Tool: "undo", ArgsSHA256: currentHash, Detail: entry.Path}); err != nil {
		return "", err
	}
	if err := r.atomicWriteExpected(currentPath, backup, os.FileMode(entry.Mode), f, currentHash, nil, nil); err != nil {
		_ = r.logEvent(audit.Event{Event: "failed", Tool: "undo", ArgsSHA256: currentHash, Detail: entry.Path})
		return "", err
	}
	resultHash := entry.SHA256Before
	auditErr := r.auditLog.RecordApplied(audit.Event{Event: "applied", Tool: "undo", ArgsSHA256: currentHash, ResultSHA256: resultHash, Detail: entry.Path})
	if err := r.shadow.MarkUndone(entry.ID); err != nil {
		return "", fmt.Errorf("restored %s but could not update shadow metadata: %w%s", entry.Path, err, auditWarning(auditErr))
	}
	return fmt.Sprintf("Restored %s from shadow %s%s", entry.Path, entry.ID, auditWarning(auditErr)), nil
}

// atomicWrite writes data to a temporary file in the target directory, syncs
// it, and renames it over the destination. All operations stay inside root.
func (r *Registry) atomicWrite(rel string, data []byte, mode os.FileMode) error {
	tmp := filepath.Join(filepath.Dir(rel), ".nabd-tmp-"+randomHex(8))
	f, err := r.root.OpenFile(tmp, os.O_WRONLY|os.O_CREATE|os.O_EXCL, mode.Perm())
	if err != nil {
		return fmt.Errorf("create temporary file: %w", err)
	}

	_, writeErr := f.Write(data)
	syncErr := f.Sync()
	closeErr := f.Close()
	if err := errors.Join(writeErr, syncErr, closeErr); err != nil {
		_ = r.root.Remove(tmp)
		return fmt.Errorf("write temporary file: %w", err)
	}

	if err := r.root.Rename(tmp, rel); err != nil {
		_ = r.root.Remove(tmp)
		return fmt.Errorf("replace %s: %w", rel, err)
	}
	return nil
}

// atomicWriteExpected stages and syncs the replacement, then verifies that
// rel still names the same open inode with the approved bytes immediately
// before rename. The optional callback creates a shadow backup between two
// checks so failed validation cannot leave an active backup for the wrong file.
func (r *Registry) atomicWriteExpected(rel string, data []byte, mode os.FileMode, source *os.File, expectedSHA string, beforeReplace func(), beforeRename func() error) error {
	tmp := filepath.Join(filepath.Dir(rel), ".nabd-tmp-"+randomHex(8))
	f, err := r.root.OpenFile(tmp, os.O_WRONLY|os.O_CREATE|os.O_EXCL, mode.Perm())
	if err != nil {
		return fmt.Errorf("create temporary file: %w", err)
	}
	defer func() { _ = r.root.Remove(tmp) }()

	_, writeErr := f.Write(data)
	syncErr := f.Sync()
	closeErr := f.Close()
	if err := errors.Join(writeErr, syncErr, closeErr); err != nil {
		return fmt.Errorf("write temporary file: %w", err)
	}
	if beforeReplace != nil {
		beforeReplace()
	}
	if err := r.verifyApprovedFile(rel, source, expectedSHA); err != nil {
		return err
	}
	if beforeRename != nil {
		if err := beforeRename(); err != nil {
			return err
		}
	}
	if err := r.verifyApprovedFile(rel, source, expectedSHA); err != nil {
		return err
	}
	if err := r.root.Rename(tmp, rel); err != nil {
		return fmt.Errorf("replace %s: %w", rel, err)
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
	detail := toolDetail(name, rawArgs)

	if spec.ReadOnly {
		_ = r.logEvent(audit.Event{Event: "requested", Tool: name, ArgsSHA256: hash, Detail: detail})
		return r.execute(ctx, spec, name, rawArgs, hash)
	}

	if err := r.logEvent(audit.Event{Event: "requested", Tool: name, ArgsSHA256: hash, Detail: detail}); err != nil {
		return fmt.Sprintf("Error: %v", err)
	}
	if r.approve == nil {
		_ = r.logEvent(audit.Event{Event: "denied", Tool: name, ArgsSHA256: hash, Reason: "no approver configured"})
		return fmt.Sprintf("Error: tool %q requires approval, but no approver is configured", name)
	}
	var guard approvedBase
	if name == "edit_file" {
		var p struct {
			Path string `json:"path"`
		}
		if err := json.Unmarshal(rawArgs, &p); err != nil {
			return fmt.Sprintf("Error: %v", err)
		}
		rel, err := r.resolveSafePath(p.Path)
		if err != nil {
			return fmt.Sprintf("Error: %v", err)
		}
		baseFD, baseSHA, baseFile, err := r.editableSnapshot(rel)
		if err != nil {
			return fmt.Sprintf("Error: cannot obtain edit base fingerprint: %v", err)
		}
		defer baseFD.Close()
		guard = approvedBase{path: rel, sha256: baseSHA, file: baseFile}
	}
	if !r.approve(name, string(rawArgs)) {
		_ = r.logEvent(audit.Event{Event: "denied", Tool: name, ArgsSHA256: hash, Reason: "denied by approver"})
		return "Action rejected by user."
	}
	if err := r.logEvent(audit.Event{Event: "approved", Tool: name, ArgsSHA256: hash}); err != nil {
		return fmt.Sprintf("Error: %v", err)
	}
	if guard.sha256 != "" {
		currentFD, current, currentFile, err := r.editableSnapshot(guard.path)
		if currentFD != nil {
			defer currentFD.Close()
		}
		if err != nil || current != guard.sha256 || !os.SameFile(guard.file, currentFile) {
			_ = r.logEvent(audit.Event{Event: "denied", Tool: name, ArgsSHA256: hash, Reason: "file changed during approval"})
			return "Error: file changed during approval; re-read it and retry"
		}
		ctx = context.WithValue(ctx, approvalBaseContextKey{}, guard)
	}
	return r.execute(ctx, spec, name, rawArgs, hash)
}

// Approve records an explicit approval and returns a single-use token bound to
// this exact tool call. The audit write is mandatory: if it fails, no token is
// issued and nothing can execute.
func (r *Registry) Approve(name string, rawArgs json.RawMessage) (Approval, error) {
	spec, ok := r.handlers[name]
	if !ok {
		return Approval{}, fmt.Errorf("unknown tool %q", name)
	}
	if spec.ReadOnly {
		return Approval{}, fmt.Errorf("tool %q does not require approval", name)
	}
	if name == "edit_file" {
		var p struct {
			Path string `json:"path"`
		}
		if err := json.Unmarshal(rawArgs, &p); err != nil {
			return Approval{}, err
		}
		baseSHA, err := r.FileDigest(p.Path)
		if err != nil {
			return Approval{}, fmt.Errorf("cannot approve edit without a base fingerprint: %w", err)
		}
		return r.ApproveWithBaseSHA256(name, rawArgs, baseSHA)
	}
	return r.approveWithGuard(name, rawArgs, "", "", nil, nil)
}

// ApproveWithBaseSHA256 records an edit approval only if the current file still
// matches the exact fingerprint shown in the approval preview.
func (r *Registry) ApproveWithBaseSHA256(name string, rawArgs json.RawMessage, expectedBaseSHA256 string) (Approval, error) {
	if name != "edit_file" {
		return Approval{}, fmt.Errorf("base fingerprint is only supported for edit_file")
	}
	if expectedBaseSHA256 == "" {
		return Approval{}, fmt.Errorf("base fingerprint is required for edit_file approval")
	}
	var p struct {
		Path string `json:"path"`
	}
	if err := json.Unmarshal(rawArgs, &p); err != nil {
		return Approval{}, err
	}
	rel, err := r.resolveSafePath(p.Path)
	if err != nil {
		return Approval{}, err
	}
	baseFD, current, baseFile, err := r.editableSnapshot(rel)
	if err != nil {
		return Approval{}, fmt.Errorf("cannot verify edit base fingerprint: %w", err)
	}
	if current != expectedBaseSHA256 {
		_ = baseFD.Close()
		return Approval{}, fmt.Errorf("file changed after the approval preview; re-read it and retry")
	}
	approval, err := r.approveWithGuard(name, rawArgs, rel, expectedBaseSHA256, baseFile, baseFD)
	if err != nil {
		_ = baseFD.Close()
	}
	return approval, err
}

func (r *Registry) approveWithGuard(name string, rawArgs json.RawMessage, basePath, baseSHA256 string, baseFile os.FileInfo, baseFD *os.File) (Approval, error) {
	spec, ok := r.handlers[name]
	if !ok {
		return Approval{}, fmt.Errorf("unknown tool %q", name)
	}
	if spec.ReadOnly {
		return Approval{}, fmt.Errorf("tool %q does not require approval", name)
	}

	hash := hashBytes(rawArgs)
	if err := r.logEvent(audit.Event{Event: "requested", Tool: name, ArgsSHA256: hash, Detail: toolDetail(name, rawArgs)}); err != nil {
		return Approval{}, err
	}
	if err := r.logEvent(audit.Event{Event: "approved", Tool: name, ArgsSHA256: hash}); err != nil {
		return Approval{}, err
	}

	now := r.now()
	token := randomHex(16)
	r.mu.Lock()
	r.pruneApprovalsLocked(now)
	r.pending[token] = pendingApproval{tool: name, argsHash: hash, basePath: basePath, baseSHA256: baseSHA256, baseFile: baseFile, baseFD: baseFD, expires: now.Add(approvalTTL)}
	r.mu.Unlock()

	return Approval{Token: token, Tool: name, ArgsSHA256: hash, BasePath: basePath, BaseSHA256: baseSHA256}, nil
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
		_ = r.logEvent(audit.Event{Event: "requested", Tool: name, ArgsSHA256: hash, Detail: toolDetail(name, rawArgs)})
		return r.execute(ctx, spec, name, rawArgs, hash)
	}

	pending, ok := r.consumeApproval(name, hash, approval)
	if !ok {
		if pending.baseFD != nil {
			_ = pending.baseFD.Close()
		}
		_ = r.logEvent(audit.Event{Event: "denied", Tool: name, ArgsSHA256: hash, Reason: "invalid or expired approval token"})
		return "Error: approval token is invalid or expired"
	}
	if pending.baseFD != nil {
		defer pending.baseFD.Close()
	}
	if pending.baseSHA256 != "" {
		ctx = context.WithValue(ctx, approvalBaseContextKey{}, approvedBase{path: pending.basePath, sha256: pending.baseSHA256, file: pending.baseFile})
	}
	return r.execute(ctx, spec, name, rawArgs, hash)
}

func (r *Registry) execute(ctx context.Context, spec ToolSpec, name string, rawArgs json.RawMessage, hash string) string {
	out, err := spec.Execute(ctx, rawArgs)
	if err != nil {
		result := fmt.Sprintf("Error: %v\nOutput:\n%s", err, truncateOutput(out))
		_ = r.logEvent(audit.Event{Event: "failed", Tool: name, ArgsSHA256: hash, ResultSHA256: hashBytes([]byte(result))})
		return result
	}
	result := truncateOutput(out)
	event := audit.Event{Event: "applied", Tool: name, ArgsSHA256: hash, ResultSHA256: hashBytes([]byte(result))}
	if spec.ReadOnly {
		_ = r.logEvent(event)
	} else if err := r.auditLog.RecordApplied(event); err != nil {
		result += auditWarning(err)
	}
	return result
}

func auditWarning(err error) string {
	if err == nil {
		return ""
	}
	return "\nWarning: operation applied; audit event needs recovery (do not rerun): " + err.Error()
}

type approvalBaseContextKey struct{}

type approvedBase struct {
	path   string
	sha256 string
	file   os.FileInfo
}

func (r *Registry) consumeApproval(name, hash string, approval Approval) (pendingApproval, bool) {
	if approval.Token == "" || approval.Tool != name || approval.ArgsSHA256 != hash {
		return pendingApproval{}, false
	}
	r.mu.Lock()
	defer r.mu.Unlock()

	pending, ok := r.pending[approval.Token]
	if !ok {
		return pendingApproval{}, false
	}
	delete(r.pending, approval.Token)
	valid := pending.tool == name && pending.argsHash == hash && pending.basePath == approval.BasePath && pending.baseSHA256 == approval.BaseSHA256 && !r.now().After(pending.expires)
	return pending, valid
}

func (r *Registry) pruneApprovalsLocked(now time.Time) {
	for token, pending := range r.pending {
		if now.After(pending.expires) {
			if pending.baseFD != nil {
				_ = pending.baseFD.Close()
			}
			delete(r.pending, token)
		}
	}
}

func (r *Registry) logEvent(event audit.Event) error {
	return r.auditLog.Write(event)
}

func toolDetail(name string, rawArgs json.RawMessage) string {
	var p struct {
		Path    string `json:"path"`
		Pkg     string `json:"pkg"`
		Command string `json:"command"`
	}
	_ = json.Unmarshal(rawArgs, &p)

	var detail string
	switch name {
	case "edit_file", "read_file", "list_dir":
		detail = p.Path
	case "go_verify":
		detail = p.Pkg
		if detail == "" {
			detail = "./..."
		}
	case "mcp_call":
		detail = p.Command
	}
	if name == "list_dir" && detail == "" {
		detail = "."
	}
	if len(detail) > 200 {
		detail = detail[:200]
	}
	return strings.ToValidUTF8(detail, "\uFFFD")
}

func (r *Registry) registerDefaults() {
	r.handlers["edit_file"] = ToolSpec{
		Name:        "edit_file",
		Description: "Replace an exact block of text in a file. Validates and formats Go code before saving.",
		ReadOnly:    false,
		Parameters:  json.RawMessage(`{"type":"object","properties":{"path":{"type":"string","description":"Relative file path"},"old_text":{"type":"string","description":"Exact existing block to replace"},"new_text":{"type":"string","description":"Replacement block"}},"required":["path","old_text","new_text"]}`),
		Execute: func(ctx context.Context, args json.RawMessage) (string, error) {
			var p struct {
				Path    string `json:"path"`
				OldText string `json:"old_text"`
				NewText string `json:"new_text"`
			}
			if err := json.Unmarshal(args, &p); err != nil {
				return "", err
			}
			rel, err := r.resolveSafePath(p.Path)
			if err != nil {
				return "", err
			}
			fileMutationMu.Lock()
			defer fileMutationMu.Unlock()

			base, ok := ctx.Value(approvalBaseContextKey{}).(approvedBase)
			if !ok || base.path != rel || base.sha256 == "" {
				return "", fmt.Errorf("edit_file requires an approval bound to the file's base fingerprint")
			}
			source, contentBytes, mode, err := r.openEditableFile(rel)
			if err != nil {
				return "", err
			}
			defer source.Close()
			approvedInfo, err := source.Stat()
			if err != nil {
				return "", err
			}
			if base.file == nil || !os.SameFile(base.file, approvedInfo) {
				return "", fmt.Errorf("file changed after approval; re-read it and retry")
			}
			if hashBytes(contentBytes) != base.sha256 {
				return "", fmt.Errorf("file changed after approval; re-read it and retry")
			}
			if err := r.verifyApprovedFile(rel, source, base.sha256); err != nil {
				return "", err
			}
			updatedBytes, err := prepareEdit(rel, contentBytes, p.OldText, p.NewText)
			if err != nil {
				return "", err
			}

			var entry shadowstore.Entry
			err = r.atomicWriteExpected(rel, updatedBytes, mode, source, base.sha256, r.beforeAtomicReplace, func() error {
				var backupErr error
				entry, backupErr = r.shadow.Backup(rel, contentBytes, mode, updatedBytes)
				return backupErr
			})
			if err != nil {
				if entry.ID != "" {
					_ = r.shadow.MarkUndone(entry.ID)
				}
				return "", err
			}
			return fmt.Sprintf("Successfully updated %s (shadow %s)", p.Path, entry.ID), nil
		},
	}

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
		Description: "Read a bounded UTF-8-safe byte range from a file inside the workspace. offset is zero-based; a page may exceed its limit by up to 3 bytes to keep a complete rune intact.",
		ReadOnly:    true,
		Parameters:  json.RawMessage(`{"type":"object","properties":{"path":{"type":"string","description":"Relative file path"},"offset":{"type":"integer","minimum":0,"description":"Zero-based byte offset (default 0)"},"limit":{"type":"integer","minimum":1,"maximum":3584,"description":"Maximum bytes to read (default 3584); may extend by up to 3 bytes for UTF-8"}},"required":["path"]}`),
		Execute: func(ctx context.Context, args json.RawMessage) (string, error) {
			var p struct {
				Path   string `json:"path"`
				Offset int64  `json:"offset"`
				Limit  int64  `json:"limit"`
			}
			if err := json.Unmarshal(args, &p); err != nil {
				return "", err
			}
			rel, err := r.resolveSafePath(p.Path)
			if err != nil {
				return "", err
			}
			return r.readFile(rel, p.Offset, p.Limit)
		},
	}

	r.handlers["list_dir"] = ToolSpec{
		Name:        "list_dir",
		Description: "List directory contents inside the workspace.",
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

	r.handlers["mcp_call"] = ToolSpec{
		Name:        "mcp_call",
		Description: "Call a tool on an external MCP server via stdio. Starts the server process, calls the tool, then closes it.",
		ReadOnly:    false,
		Parameters:  json.RawMessage(`{"type":"object","properties":{"command":{"type":"string","description":"Command to start the MCP server (e.g. nabd-mcp)"},"tool":{"type":"string","description":"Name of the tool to call on the MCP server"},"arguments":{"type":"object","description":"Arguments to pass to the tool"}},"required":["command","tool"]}`),
		Execute: func(ctx context.Context, args json.RawMessage) (output string, retErr error) {
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

			tCtx, cancel := context.WithTimeout(ctx, 60*time.Second)
			defer cancel()

			var tree *processTree
			stdioTransport := transport.NewStdioWithOptions(p.Command, nil, nil, transport.WithCommandFunc(
				func(commandCtx context.Context, command string, _ []string, argv []string) (*exec.Cmd, error) {
					cmd := exec.CommandContext(commandCtx, command, argv...)
					cmd.Env = buildMCPEnv()
					tree = configureProcessTree(cmd)
					if err := tree.Prepare(); err != nil {
						return nil, err
					}
					return cmd, nil
				},
			))
			c := client.NewClient(stdioTransport)
			defer func() {
				_ = c.Close()
				if tree != nil {
					if cleanupErr := tree.Cleanup(); cleanupErr != nil {
						retErr = errors.Join(retErr, cleanupErr)
					}
				}
			}()
			if err := c.Start(tCtx); err != nil {
				return "", fmt.Errorf("failed to start MCP server: %w", err)
			}

			if _, err := c.Initialize(tCtx, mcp.InitializeRequest{
				Params: mcp.InitializeParams{
					ProtocolVersion: mcp.LATEST_PROTOCOL_VERSION,
					ClientInfo:      mcp.Implementation{Name: "NabdCoder", Version: "1.0.0"},
				},
			}); err != nil {
				return "", fmt.Errorf("failed to initialize MCP server: %w", err)
			}

			result, err := c.CallTool(tCtx, mcp.CallToolRequest{
				Params: mcp.CallToolParams{
					Name:      p.Tool,
					Arguments: p.Arguments,
				},
			})
			if err != nil {
				return "", fmt.Errorf("mcp call failed: %w", err)
			}

			var out strings.Builder
			for _, content := range result.Content {
				if tc, ok := content.(mcp.TextContent); ok {
					out.WriteString(tc.Text)
					out.WriteString("\n")
				}
			}
			if out.Len() == 0 {
				return "(no text content returned)", nil
			}
			return out.String(), nil
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
	tCtx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()

	goBinary, err := exec.LookPath("go")
	if err != nil {
		return "", fmt.Errorf("locate go executable: %w", err)
	}
	goBinary, err = filepath.Abs(goBinary)
	if err != nil {
		return "", fmt.Errorf("resolve go executable: %w", err)
	}
	cmd := exec.CommandContext(tCtx, goBinary, "test", "-p", "2", "-vet=all", pkg)
	cmd.Dir = r.workDir
	cmd.Env = buildVerifyEnv()
	// The helper may spend up to two seconds killing/reaping owned descendants;
	// allow that cleanup to finish before exec falls back to killing the helper.
	cmd.WaitDelay = 5 * time.Second
	tree := configureProcessTree(cmd)
	if err := tree.Prepare(); err != nil {
		return "", err
	}

	var out cappedBuffer
	cmd.Stdout = &out
	cmd.Stderr = &out
	err = cmd.Run()
	cleanupErr := tree.Cleanup()
	if cleanupErr != nil {
		return "", cleanupErr
	}

	status := "PASS"
	if err != nil {
		status = "FAIL"
	}
	if tCtx.Err() == context.DeadlineExceeded {
		status = "FAIL (timeout)"
	}
	if err != nil && !isGoTestFailure(tCtx, err) {
		return "", err
	}
	return summarizeGoTest(status, out.String(), err), nil
}

func isGoTestFailure(ctx context.Context, err error) bool {
	if ctx.Err() != nil {
		return true
	}
	var exitErr *exec.ExitError
	return errors.As(err, &exitErr)
}

// cappedBuffer preserves only the first maxVerifyOutputBytes while allowing
// the child to finish normally; noisy tests cannot exhaust memory via output.
type cappedBuffer struct {
	buf       bytes.Buffer
	truncated bool
}

func (b *cappedBuffer) Write(p []byte) (int, error) {
	written := len(p)
	remaining := maxVerifyOutputBytes - b.buf.Len()
	if remaining > 0 {
		if len(p) > remaining {
			_, _ = b.buf.Write(p[:remaining])
			b.truncated = true
		} else {
			_, _ = b.buf.Write(p)
		}
	} else if len(p) > 0 {
		b.truncated = true
	}
	return written, nil
}

func (b *cappedBuffer) String() string {
	out := b.buf.String()
	if b.truncated {
		out += fmt.Sprintf("\n...[go test output capped at %d bytes]", maxVerifyOutputBytes)
	}
	return out
}

const (
	maxSummarySignals = 5
	maxSummaryTail    = 12
	maxSummaryLine    = 180
)

// summarizeGoTest returns a small, structured result instead of raw logs: a
// status line, the first failure signals, and the tail of the output.
func summarizeGoTest(status, output string, err error) string {
	lines := strings.Split(strings.TrimRight(output, "\n"), "\n")

	var b strings.Builder
	b.WriteString(status)
	if err != nil {
		b.WriteString(" (")
		b.WriteString(clipSummary(err.Error(), maxSummaryLine))
		b.WriteString(")")
	}
	b.WriteString("\n")

	signals := 0
	for _, line := range lines {
		trimmed := strings.TrimSpace(line)
		if trimmed == "" {
			continue
		}
		if strings.HasPrefix(trimmed, "--- FAIL:") ||
			strings.Contains(trimmed, "build failed") ||
			strings.Contains(trimmed, "cannot find") ||
			strings.Contains(trimmed, "undefined:") ||
			strings.Contains(trimmed, "panic:") {
			if signals >= maxSummarySignals {
				break
			}
			b.WriteString("  ")
			b.WriteString(clipSummary(trimmed, maxSummaryLine))
			b.WriteString("\n")
			signals++
		}
	}

	b.WriteString("--- tail ---\n")
	if len(lines) == 1 && lines[0] == "" {
		b.WriteString("(no output)\n")
		return b.String()
	}
	start := len(lines) - maxSummaryTail
	if start < 0 {
		start = 0
	}
	for _, line := range lines[start:] {
		b.WriteString(clipSummary(line, maxSummaryLine))
		b.WriteString("\n")
	}
	return b.String()
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

func buildMCPEnv() []string {
	allow := []string{
		"PATH", "HOME", "USER", "LOGNAME", "LANG", "LC_ALL", "LC_CTYPE", "TZ",
		"TMPDIR", "TMP", "TEMP", "SYSTEMROOT", "WINDIR", "COMSPEC", "PATHEXT",
		"USERPROFILE", "HOMEDRIVE", "HOMEPATH", "APPDATA", "LOCALAPPDATA",
		"PREFIX", "ANDROID_ROOT", "ANDROID_DATA", "ANDROID_STORAGE",
	}
	values := make(map[string]string, len(allow))
	for _, key := range allow {
		if value, ok := os.LookupEnv(key); ok {
			values[key] = value
		}
	}
	keys := make([]string, 0, len(values))
	for key := range values {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	env := make([]string, 0, len(keys))
	for _, key := range keys {
		env = append(env, key+"="+values[key])
	}
	return env
}

func buildVerifyEnv() []string {
	// Forward only process identity, locale, temporary-directory, and OS
	// essentials. In particular, do not pass PATH, Go overrides, CGO settings,
	// or compiler/package-config variables to project code.
	allow := []string{
		"HOME", "USER", "LOGNAME", "LANG", "LC_ALL", "LC_CTYPE", "TZ",
		"TMPDIR", "TMP", "TEMP", "SYSTEMROOT", "WINDIR", "COMSPEC", "PATHEXT",
		"USERPROFILE", "HOMEDRIVE", "HOMEPATH", "APPDATA", "LOCALAPPDATA",
		"PREFIX", "ANDROID_ROOT", "ANDROID_DATA", "ANDROID_STORAGE",
	}
	values := make(map[string]string, len(allow)+8)
	for _, key := range allow {
		if value, ok := os.LookupEnv(key); ok {
			values[key] = value
		}
	}
	if values["TMPDIR"] == "" && values["PREFIX"] != "" {
		values["TMPDIR"] = filepath.Join(values["PREFIX"], "tmp")
	}
	values["GOMEMLIMIT"] = "512MiB"
	values["GOGC"] = "50"
	values["GOFLAGS"] = "-mod=readonly"
	values["GOPROXY"] = "off"
	values["GOTOOLCHAIN"] = "local"
	values["GOSUMDB"] = "off"
	values["GOENV"] = "off"
	values["CGO_ENABLED"] = "0"
	keys := make([]string, 0, len(values))
	for key := range values {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	env := make([]string, 0, len(keys))
	for _, key := range keys {
		env = append(env, key+"="+values[key])
	}
	return env
}

func truncateOutput(s string) string {
	if len(s) <= maxOutputBytes {
		return s
	}
	half := maxOutputBytes / 2

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
