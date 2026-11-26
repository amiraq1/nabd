package tools

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"go/format"
	"io"
	"io/fs"
	"os"
	"strings"
)

// ApprovalState binds an approval decision to the file bytes the user saw.
// Hash is the hex-encoded SHA-256 of those bytes and is empty when Exists is false.
type ApprovalState struct {
	Path       string
	Exists     bool
	Hash       string
	OutputHash string
}

// ApprovalPreview is what the interface should render for an approval prompt,
// together with the state that must be passed back to RunApproved.
type ApprovalPreview struct {
	Display string
	State   ApprovalState
}

// PreviewApproval computes the approval payload for a tool call without writing
// anything. edit_file gets a unified diff and content hash, create_file gets a
// full new-file preview and output hash, and other tools keep raw arguments.
func (r *Registry) PreviewApproval(name string, rawArgs json.RawMessage) (ApprovalPreview, error) {
	switch name {
	case "edit_file":
		return r.previewEditApproval(rawArgs)
	case "create_file":
		return r.previewCreateApproval(rawArgs)
	default:
		return ApprovalPreview{Display: string(rawArgs)}, nil
	}
}

func (r *Registry) previewEditApproval(rawArgs json.RawMessage) (ApprovalPreview, error) {
	p, err := parseEditParams(rawArgs)
	if err != nil {
		return ApprovalPreview{}, err
	}

	safePath, err := r.resolveSafePath(p.Path)
	if err != nil {
		return ApprovalPreview{}, err
	}

	state := ApprovalState{Path: safePath}
	info, statErr := r.root.Lstat(safePath)
	if statErr == nil {
		if info.Mode()&os.ModeSymlink != 0 {
			return ApprovalPreview{}, fmt.Errorf("editing symlinks is not supported: %s", p.Path)
		}
		if info.Size() > maxEditBytes {
			return ApprovalPreview{}, fmt.Errorf("file is too large to edit (%d bytes; limit %d)", info.Size(), maxEditBytes)
		}
	} else if errors.Is(statErr, fs.ErrNotExist) {
		return ApprovalPreview{}, fmt.Errorf("file does not exist: %s; use create_file", p.Path)
	}
	content, readErr := readBounded(r.root, safePath, maxEditBytes)
	if readErr != nil {
		if errors.Is(readErr, fs.ErrNotExist) {
			return ApprovalPreview{}, fmt.Errorf("file does not exist: %s; use create_file", p.Path)
		}
		return ApprovalPreview{}, readErr
	}

	state.Exists = true
	sum := sha256.Sum256(content)
	state.Hash = hex.EncodeToString(sum[:])

	updated, err := applyEdit(content, p.Path, safePath, p.OldText, p.NewText)
	if err != nil {
		return ApprovalPreview{}, err
	}
	state.OutputHash = hashBytes(updated)
	return ApprovalPreview{Display: unifiedDiff(content, updated, p.Path), State: state}, nil
}

func (r *Registry) previewCreateApproval(rawArgs json.RawMessage) (ApprovalPreview, error) {
	p, err := parseCreateParams(rawArgs)
	if err != nil {
		return ApprovalPreview{}, err
	}

	safePath, err := r.resolveSafePath(p.Path)
	if err != nil {
		return ApprovalPreview{}, err
	}
	parts, err := validateCreatePathBounds(safePath)
	if err != nil {
		return ApprovalPreview{}, err
	}
	if err := r.inspectCreateTarget(parts, p.Path); err != nil {
		return ApprovalPreview{}, err
	}

	contentBytes := []byte(*p.Content)
	if err := validateGoSyntaxIfNeeded(safePath, contentBytes, "create"); err != nil {
		return ApprovalPreview{}, err
	}

	state := ApprovalState{
		Path:       safePath,
		Exists:     false,
		OutputHash: hashBytes(contentBytes),
	}
	return ApprovalPreview{Display: newFilePreview(contentBytes, p.Path), State: state}, nil
}

type editParams struct {
	Path    string `json:"path"`
	OldText string `json:"old_text"`
	NewText string `json:"new_text"`
}

func parseEditParams(rawArgs json.RawMessage) (editParams, error) {
	var p editParams
	if len(rawArgs) > 0 {
		if err := json.Unmarshal(rawArgs, &p); err != nil {
			return p, fmt.Errorf("invalid arguments: %w", err)
		}
	}
	if p.OldText == "" {
		return p, fmt.Errorf("old_text cannot be empty")
	}
	return p, nil
}

type createParams struct {
	Path    string  `json:"path"`
	Content *string `json:"content"`
}

func parseCreateParams(rawArgs json.RawMessage) (createParams, error) {
	var p createParams
	if len(bytes.TrimSpace(rawArgs)) == 0 {
		return p, fmt.Errorf("invalid arguments: empty")
	}
	dec := json.NewDecoder(bytes.NewReader(rawArgs))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&p); err != nil {
		return p, fmt.Errorf("invalid arguments: %w", err)
	}
	if err := dec.Decode(&struct{}{}); err != io.EOF {
		return p, fmt.Errorf("invalid arguments: unexpected trailing data")
	}
	if p.Content == nil {
		return p, fmt.Errorf("content is required")
	}
	if len(*p.Content) > maxCreateBytes {
		return p, fmt.Errorf("file content is too large (%d bytes; limit %d)", len(*p.Content), maxCreateBytes)
	}
	return p, nil
}

func validateGoSyntaxIfNeeded(safePath string, content []byte, op string) error {
	if !strings.HasSuffix(strings.ToLower(safePath), ".go") {
		return nil
	}
	// Parse and validate without gofmt: automatic formatting could alter
	// unrelated bytes outside what the user reviewed and approved.
	if _, err := format.Source(content); err != nil {
		return fmt.Errorf("%s rejected due to Go syntax error:\n%v", op, err)
	}
	return nil
}

// editBeforeRenameHook is a test-only seam; production leaves it nil.
var editBeforeRenameHook func()

// applyEdit performs the in-memory transformation used by both the preview and
// the write path.
func applyEdit(contentBytes []byte, displayPath, safePath, oldText, newText string) ([]byte, error) {
	if oldText == "" {
		return nil, fmt.Errorf("old_text cannot be empty")
	}
	if oldText == newText {
		return nil, fmt.Errorf("old_text and new_text are identical")
	}
	// Normalize only for matching; map normalized positions back to original
	// offsets so bytes outside the requested edit are never rewritten.
	var normalized bytes.Buffer
	offsets := make([]int, 0, len(contentBytes)+1)
	for i := 0; i < len(contentBytes); i++ {
		offsets = append(offsets, i)
		if contentBytes[i] == '\r' && i+1 < len(contentBytes) && contentBytes[i+1] == '\n' {
			i++
			normalized.WriteByte('\n')
		} else {
			normalized.WriteByte(contentBytes[i])
		}
	}
	offsets = append(offsets, len(contentBytes))
	content := normalized.String()
	oldNorm := strings.ReplaceAll(oldText, "\r\n", "\n")
	newNorm := strings.ReplaceAll(newText, "\r\n", "\n")
	if oldNorm == newNorm {
		return nil, fmt.Errorf("old_text and new_text are identical after line-ending normalization")
	}
	count := strings.Count(content, oldNorm)
	if count == 0 {
		return nil, fmt.Errorf("old_text not found in %s", displayPath)
	}
	if count != 1 {
		return nil, fmt.Errorf("old_text appears %d times; include more surrounding lines to make it unique", count)
	}
	pos := strings.Index(content, oldNorm)
	start, end := offsets[pos], offsets[pos+len(oldNorm)]
	replacement := []byte(newNorm)
	// Preserve the line-ending convention for uniformly CRLF files; mixed
	// files retain all existing bytes outside the match.
	if bytes.Contains(contentBytes, []byte("\r\n")) && !bytes.Contains(bytes.ReplaceAll(contentBytes, []byte("\r\n"), nil), []byte("\n")) {
		replacement = bytes.ReplaceAll(replacement, []byte("\n"), []byte("\r\n"))
	}
	updated := make([]byte, 0, len(contentBytes)-(end-start)+len(replacement))
	updated = append(updated, contentBytes[:start]...)
	updated = append(updated, replacement...)
	updated = append(updated, contentBytes[end:]...)
	if err := validateGoSyntaxIfNeeded(safePath, updated, "edit"); err != nil {
		return nil, err
	}
	if len(updated) > maxEditBytes {
		return nil, fmt.Errorf("edited file too large (limit %d bytes)", maxEditBytes)
	}
	if bytes.Equal(updated, contentBytes) {
		return nil, fmt.Errorf("edit would not change %s", displayPath)
	}
	return updated, nil
}

// verifyEditState compares the approved path and bytes against the workspace root.
func (r *Registry) verifyEditState(p editParams, state ApprovalState) ([]byte, string, error) {
	safePath, err := r.resolveSafePath(p.Path)
	if err != nil {
		return nil, "", err
	}
	if state.Path == "" {
		return nil, "", fmt.Errorf("missing approval state")
	}
	if state.Path != safePath {
		return nil, "", fmt.Errorf("approval path mismatch")
	}
	if !state.Exists {
		return nil, "", fmt.Errorf("file does not exist: %s; use create_file", p.Path)
	}
	info, err := r.root.Lstat(safePath)
	if err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			return nil, "", fmt.Errorf("file no longer exists since approval: %s", p.Path)
		}
		return nil, "", fmt.Errorf("file changed since approval: %s", p.Path)
	}
	if info.Mode()&os.ModeSymlink != 0 {
		return nil, "", fmt.Errorf("editing symlinks is not supported: %s", p.Path)
	}
	content, err := readBounded(r.root, safePath, maxEditBytes)
	if err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			return nil, "", fmt.Errorf("file no longer exists since approval: %s", p.Path)
		}
		return nil, "", fmt.Errorf("file changed since approval: %s: %w", p.Path, err)
	}
	if hashBytes(content) != state.Hash {
		return nil, "", fmt.Errorf("file changed since approval: %s", p.Path)
	}
	return content, safePath, nil
}

// verifyCreateState verifies that a create_file target remains absent, path-safe,
// and bound to the exact approved output hash.
func (r *Registry) verifyCreateState(p createParams, state ApprovalState) ([]byte, string, error) {
	safePath, err := r.resolveSafePath(p.Path)
	if err != nil {
		return nil, "", err
	}
	parts, err := validateCreatePathBounds(safePath)
	if err != nil {
		return nil, "", err
	}
	if state.Path == "" {
		return nil, "", fmt.Errorf("missing approval state")
	}
	if state.Path != safePath {
		return nil, "", fmt.Errorf("approval path mismatch")
	}
	if state.Exists || state.Hash != "" {
		return nil, "", fmt.Errorf("invalid approval state for new file: %s", p.Path)
	}
	if err := r.inspectCreateTarget(parts, p.Path); err != nil {
		return nil, "", err
	}
	contentBytes := []byte(*p.Content)
	if err := validateGoSyntaxIfNeeded(safePath, contentBytes, "create"); err != nil {
		return nil, "", err
	}
	if state.OutputHash == "" || hashBytes(contentBytes) != state.OutputHash {
		return nil, "", fmt.Errorf("output differs from approved preview")
	}
	return contentBytes, safePath, nil
}

func splitDiffLines(s string) []string {
	if s == "" {
		return nil
	}
	lines := strings.SplitAfter(s, "\n")
	if len(lines) > 0 && lines[len(lines)-1] == "" {
		lines = lines[:len(lines)-1]
	}
	return lines
}

func writePreviewLine(b *strings.Builder, prefix, line string) {
	eol := "NONE"
	if strings.HasSuffix(line, "\r\n") {
		line = strings.TrimSuffix(line, "\r\n")
		eol = "CRLF"
	} else if strings.HasSuffix(line, "\n") {
		line = strings.TrimSuffix(line, "\n")
		eol = "LF"
	}
	fmt.Fprintf(b, "%s%s [EOL=%s]\n", prefix, line, eol)
	if eol == "NONE" {
		b.WriteString("\\ No newline at end of file\n")
	}
}

func newFilePreview(content []byte, path string) string {
	var b strings.Builder
	fmt.Fprintf(&b, "--- /dev/null\n+++ b/%s\n", path)
	if len(content) == 0 {
		b.WriteString("(empty file, 0 bytes)\n")
		return b.String()
	}
	lines := splitDiffLines(string(content))
	fmt.Fprintf(&b, "@@ -0,0 +1,%d @@\n", len(lines))
	for _, line := range lines {
		writePreviewLine(&b, "+", line)
	}
	return b.String()
}

// unifiedDiff renders a human-reviewable diff. It uses a common prefix/suffix
// cut, so separated changes appear as one replaced block with 3 lines of context.
func unifiedDiff(oldContent, newContent []byte, path string) string {
	if bytes.Equal(oldContent, newContent) {
		return fmt.Sprintf("No changes to %s\n", path)
	}

	oldLines := splitDiffLines(string(oldContent))
	newLines := splitDiffLines(string(newContent))

	prefix := 0
	for prefix < len(oldLines) && prefix < len(newLines) && oldLines[prefix] == newLines[prefix] {
		prefix++
	}
	suffix := 0
	for suffix < len(oldLines)-prefix && suffix < len(newLines)-prefix &&
		oldLines[len(oldLines)-1-suffix] == newLines[len(newLines)-1-suffix] {
		suffix++
	}

	const contextLines = 3
	startOld := prefix - contextLines
	if startOld < 0 {
		startOld = 0
	}
	endOld := len(oldLines) - suffix + contextLines
	if endOld > len(oldLines) {
		endOld = len(oldLines)
	}
	startNew := prefix - contextLines
	if startNew < 0 {
		startNew = 0
	}
	endNew := len(newLines) - suffix + contextLines
	if endNew > len(newLines) {
		endNew = len(newLines)
	}

	var b strings.Builder
	fmt.Fprintf(&b, "--- a/%s\n+++ b/%s\n", path, path)
	fmt.Fprintf(&b, "@@ -%d,%d +%d,%d @@\n", startOld+1, endOld-startOld, startNew+1, endNew-startNew)
	for _, line := range oldLines[startOld:prefix] {
		writePreviewLine(&b, " ", line)
	}
	for _, line := range oldLines[prefix : len(oldLines)-suffix] {
		writePreviewLine(&b, "-", line)
	}
	for _, line := range newLines[prefix : len(newLines)-suffix] {
		writePreviewLine(&b, "+", line)
	}
	for _, line := range oldLines[len(oldLines)-suffix : endOld] {
		writePreviewLine(&b, " ", line)
	}
	return b.String()
}
