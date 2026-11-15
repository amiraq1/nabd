package tools

import (
	crand "crypto/rand"
	"encoding/json"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

type reviewBrokenRandom struct{}

func (reviewBrokenRandom) Read([]byte) (int, error) {
	return 0, errors.New("synthetic entropy failure")
}
func TestReviewApprovalRandomFailure(t *testing.T) {
	if os.Getenv("NABD_REVIEW_ENTROPY_TEST") == "child" {
		root, r := newTestRegistry(t)
		crand.Reader = reviewBrokenRandom{}
		approval, err := r.Approve("go_verify", json.RawMessage(`{}`))
		if err == nil || approval.Token != "" {
			t.Fatal("entropy failure issued an approval")
		}
		data, err := os.ReadFile(filepath.Join(root, ".nabd", "audit.log"))
		if err != nil {
			t.Fatal(err)
		}
		if strings.Contains(string(data), `"event":"approved"`) {
			t.Fatal("failed approval recorded as approved")
		}
		return
	}
	cmd := exec.Command(os.Args[0], "-test.run=^TestReviewApprovalRandomFailure$")
	cmd.Env = append(os.Environ(), "NABD_REVIEW_ENTROPY_TEST=child")
	if err := cmd.Run(); err != nil {
		t.Fatal("entropy failure did not return safely without a token")
	}
}

type reviewZeroRandom struct{}

func (reviewZeroRandom) Read(b []byte) (int, error) { clear(b); return len(b), nil }
func TestReviewApprovalTokenCollision(t *testing.T) {
	if os.Getenv("NABD_REVIEW_COLLISION_TEST") == "child" {
		_, r := newTestRegistry(t)
		crand.Reader = reviewZeroRandom{}
		raw := json.RawMessage(`{}`)
		first, err := r.Approve("go_verify", raw)
		if err != nil || first.Token == "" {
			t.Fatal("initial approval failed")
		}
		second, err := r.Approve("go_verify", raw)
		if err == nil || second.Token != "" {
			t.Fatal("collision issued a second approval")
		}
		return
	}
	cmd := exec.Command(os.Args[0], "-test.run=^TestReviewApprovalTokenCollision$")
	cmd.Env = append(os.Environ(), "NABD_REVIEW_COLLISION_TEST=child")
	if err := cmd.Run(); err != nil {
		t.Fatal("token collision did not fail closed")
	}
}
