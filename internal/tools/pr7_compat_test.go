package tools

import (
	"context"
	"encoding/json"
	"fmt"
	"testing"
)

// Preserve the old regression scenarios while exercising the single-use token gate.
func legacyApproved(t *testing.T, r *Registry, ctx context.Context, name string, args json.RawMessage, state ApprovalState) string {
	t.Helper()
	var token Approval
	var err error
	if name == "edit_file" {
		token, err = r.ApprovePreview(name, args, state)
	} else {
		token, err = r.Approve(name, args)
	}
	if err != nil {
		return fmt.Sprintf("Error: %v", err)
	}
	return r.RunApproved(ctx, name, args, token)
}
