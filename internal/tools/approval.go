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
	"path/filepath"
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
	content, readErr := os.ReadFile(safePath)
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

func (r *Registry) writeEdit(safePath string, p editParams, content []byte) (string, error) {
	updatedBytes, err := applyEdit(content, p.Path, safePath, p.OldText, p.NewText)
	if err != nil {
		return "", err
	}
	expected := sha256.Sum256(content)
	expectedHash := hex.EncodeToString(expected[:])

	realPath, err := filepath.EvalSymlinks(safePath)
	if err != nil {
		return "", err
	}
	info, err := os.Stat(realPath)
	if err != nil {
		return "", err
	}
	tmp, err := os.CreateTemp(filepath.Dir(realPath), "."+filepath.Base(realPath)+".tmp-*")
	if err != nil {
		return "", err
	}
	tmpName := tmp.Name()
	if _, err := tmp.Write(updatedBytes); err != nil {
		tmp.Close()
		os.Remove(tmpName)
		return "", err
	}
	if err := tmp.Chmod(info.Mode().Perm()); err != nil {
		tmp.Close()
		os.Remove(tmpName)
		return "", err
	}
	if err := tmp.Close(); err != nil {
		os.Remove(tmpName)
		return "", err
	}

	if editBeforeRenameHook != nil {
		editBeforeRenameHook()
	}

	current, readErr := os.ReadFile(realPath)
	if readErr != nil {
		os.Remove(tmpName)
		return "", fmt.Errorf("file changed since approval: %s", p.Path)
	}
	sum := sha256.Sum256(current)
	if hex.EncodeToString(sum[:]) != expectedHash {
		os.Remove(tmpName)
		return "", fmt.Errorf("file changed since approval: %s", p.Path)
	}

	if err := os.Rename(tmpName, realPath); err != nil {
		os.Remove(tmpName)
		return "", err
	}
	return fmt.Sprintf("Successfully updated %s", p.Path), nil
}

// applyEdit performs the in-memory transformation used by both the preview and
// the write path.
func applyEdit(contentBytes []byte, displayPath, safePath, oldText, newText string) ([]byte, error) {
	hasCRLF := bytes.Contains(contentBytes, []byte("\r\n"))
	content := strings.ReplaceAll(string(contentBytes), "\r\n", "\n")
	oldNorm := strings.ReplaceAll(oldText, "\r\n", "\n")
	newNorm := strings.ReplaceAll(newText, "\r\n", "\n")

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
	return updated, nil
}

// verifyEditState re-reads the target once and returns the bytes it hashed.
// The write path must apply the edit to those bytes instead of reading again.
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

	content, readErr := os.ReadFile(safePath)
	exists := readErr == nil
	if readErr != nil && !errors.Is(readErr, fs.ErrNotExist) {
		return nil, "", readErr
	}
	if exists != state.Exists {
		return nil, "", fmt.Errorf("file changed since approval: %s", p.Path)
	}
	if !exists {
		return nil, safePath, nil
	}
	sum := sha256.Sum256(content)
	if hex.EncodeToString(sum[:]) != state.Hash {
		return nil, "", fmt.Errorf("file changed since approval: %s", p.Path)
	}
	return content, safePath, nil
}

func splitDiffLines(s string) []string {
	if s == "" {
		return nil
	}
	lines := strings.Split(s, "\n")
	if len(lines) > 0 && lines[len(lines)-1] == "" {
		lines = lines[:len(lines)-1]
	}
	return lines
}

// unifiedDiff renders a human-reviewable diff. It uses a common prefix/suffix
// cut, so separated changes appear as one replaced block with 3 lines of context.
func unifiedDiff(oldContent, newContent []byte, path string) string {
	oldNorm := strings.ReplaceAll(string(oldContent), "\r\n", "\n")
	newNorm := strings.ReplaceAll(string(newContent), "\r\n", "\n")
	if oldNorm == newNorm {
		return fmt.Sprintf("No changes to %s\n", path)
	}

	oldLines := splitDiffLines(oldNorm)
	newLines := splitDiffLines(newNorm)

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
		b.WriteString(" " + line + "\n")
	}
	for _, line := range oldLines[prefix : len(oldLines)-suffix] {
		b.WriteString("-" + line + "\n")
	}
	for _, line := range newLines[prefix : len(newLines)-suffix] {
		b.WriteString("+" + line + "\n")
	}
	for _, line := range oldLines[len(oldLines)-suffix : endOld] {
		b.WriteString(" " + line + "\n")
	}
	return b.String()
}
