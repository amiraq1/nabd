package tools

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"go/format"
	"io/fs"
	"os"
	"strings"
)

// ApprovalState binds an approval decision to the file bytes the user saw.
// Hash is the hex-encoded SHA-256 of those bytes and is empty when Exists is false.
type ApprovalState struct {
	Path   string
	Exists bool
	Hash   string
}

// ApprovalPreview is what the interface should render for an approval prompt,
// together with the state that must be passed back to RunApproved.
type ApprovalPreview struct {
	Display string
	State   ApprovalState
}

// PreviewApproval computes the approval payload for a tool call without writing
// anything. edit_file gets a unified diff and a content hash; other tools keep
// their raw arguments.
func (r *Registry) PreviewApproval(name string, rawArgs json.RawMessage) (ApprovalPreview, error) {
	if name != "edit_file" {
		return ApprovalPreview{Display: string(rawArgs)}, nil
	}

	var p struct {
		Path    string `json:"path"`
		OldText string `json:"old_text"`
		NewText string `json:"new_text"`
	}
	if len(rawArgs) > 0 {
		if err := json.Unmarshal(rawArgs, &p); err != nil {
			return ApprovalPreview{}, fmt.Errorf("invalid arguments: %w", err)
		}
	}
	if p.OldText == "" {
		return ApprovalPreview{}, fmt.Errorf("old_text cannot be empty")
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
	}
	content, readErr := readBounded(r.root, safePath, maxEditBytes)
	if readErr != nil {
		if errors.Is(readErr, fs.ErrNotExist) {
			return ApprovalPreview{
				Display: fmt.Sprintf("File %s does not exist at approval time; no diff available (edit_file will fail).", p.Path),
				State:   state,
			}, nil
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
	return ApprovalPreview{Display: unifiedDiff(content, updated, p.Path), State: state}, nil
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
	hasCRLF := bytes.Contains(contentBytes, []byte("\r\n"))
	content := strings.ReplaceAll(string(contentBytes), "\r\n", "\n")
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

	updated := []byte(strings.Replace(content, oldNorm, newNorm, 1))
	if strings.HasSuffix(safePath, ".go") {
		formatted, err := format.Source(updated)
		if err != nil {
			return nil, fmt.Errorf("edit rejected due to Go syntax error:\n%v", err)
		}
		updated = formatted
	}
	if hasCRLF {
		updated = bytes.ReplaceAll(updated, []byte("\n"), []byte("\r\n"))
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
		return nil, "", fmt.Errorf("file changed since approval: path mismatch")
	}
	info, err := r.root.Lstat(safePath)
	if err != nil {
		return nil, "", fmt.Errorf("file changed since approval: %s", p.Path)
	}
	if info.Mode()&os.ModeSymlink != 0 {
		return nil, "", fmt.Errorf("editing symlinks is not supported: %s", p.Path)
	}
	content, err := readBounded(r.root, safePath, maxEditBytes)
	if err != nil {
		return nil, "", fmt.Errorf("file changed since approval: %s: %w", p.Path, err)
	}
	if !state.Exists || hashBytes(content) != state.Hash {
		return nil, "", fmt.Errorf("file changed since approval: %s", p.Path)
	}
	return content, safePath, nil
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
	writeLine := func(prefix, line string) {
		eol := "NONE"
		if strings.HasSuffix(line, "\r\n") {
			line = strings.TrimSuffix(line, "\r\n")
			eol = "CRLF"
		} else if strings.HasSuffix(line, "\n") {
			line = strings.TrimSuffix(line, "\n")
			eol = "LF"
		}
		fmt.Fprintf(&b, "%s%s [EOL=%s]\n", prefix, line, eol)
		if eol == "NONE" {
			b.WriteString("\\ No newline at end of file\n")
		}
	}
	for _, line := range oldLines[startOld:prefix] {
		writeLine(" ", line)
	}
	for _, line := range oldLines[prefix : len(oldLines)-suffix] {
		writeLine("-", line)
	}
	for _, line := range newLines[prefix : len(newLines)-suffix] {
		writeLine("+", line)
	}
	for _, line := range oldLines[len(oldLines)-suffix : endOld] {
		writeLine(" ", line)
	}
	return b.String()
}
