package audit

import (
	"bufio"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestOpenDoesNotWrite(t *testing.T) {
	dirRoot := t.TempDir()
	root, err := os.OpenRoot(dirRoot)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = root.Close() })

	if _, err := Open(root); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(dirRoot, ".nabd")); !os.IsNotExist(err) {
		t.Fatalf("Open created .nabd eagerly (err=%v)", err)
	}
}

func TestWriteAppendsJSONLines(t *testing.T) {
	dirRoot := t.TempDir()
	root, err := os.OpenRoot(dirRoot)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = root.Close() })

	log, err := Open(root)
	if err != nil {
		t.Fatal(err)
	}
	now := time.Date(2026, 1, 1, 12, 0, 0, 0, time.UTC)
	log.now = func() time.Time { return now }

	events := []Event{
		{Event: "requested", Tool: "edit_file", ArgsSHA256: "a", Detail: "main.go"},
		{Event: "approved", Tool: "edit_file", ArgsSHA256: "a"},
		{Event: "applied", Tool: "edit_file", ArgsSHA256: "a", ResultSHA256: "b"},
	}
	for _, event := range events {
		if err := log.Write(event); err != nil {
			t.Fatal(err)
		}
	}

	file, err := os.Open(filepath.Join(dirRoot, ".nabd", "audit.log"))
	if err != nil {
		t.Fatal(err)
	}
	defer file.Close()

	var got []Event
	scanner := bufio.NewScanner(file)
	for scanner.Scan() {
		var event Event
		if err := json.Unmarshal(scanner.Bytes(), &event); err != nil {
			t.Fatalf("invalid JSON line %q: %v", scanner.Text(), err)
		}
		got = append(got, event)
	}
	if err := scanner.Err(); err != nil {
		t.Fatal(err)
	}
	if len(got) != len(events) {
		t.Fatalf("got %d events, want %d", len(got), len(events))
	}
	for i, event := range got {
		if event.Event != events[i].Event || event.Tool != events[i].Tool {
			t.Errorf("event %d = %+v, want %+v", i, event, events[i])
		}
		if !event.Time.Equal(now) {
			t.Errorf("event %d time = %v, want %v", i, event.Time, now)
		}
	}
}

func TestAppliedEventSurvivesLogFailureAndReplaysOnce(t *testing.T) {
	rootDir := t.TempDir()
	root, err := os.OpenRoot(rootDir)
	if err != nil {
		t.Fatal(err)
	}
	defer root.Close()

	log, err := Open(root)
	if err != nil {
		t.Fatal(err)
	}
	if err := log.Write(Event{Event: "requested", Tool: "edit_file", ArgsSHA256: "a"}); err != nil {
		t.Fatal(err)
	}
	logPath := filepath.Join(rootDir, fileName)
	savedLogPath := logPath + ".saved"
	if err := os.Rename(logPath, savedLogPath); err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(logPath, 0o700); err != nil {
		t.Fatal(err)
	}

	err = log.RecordApplied(Event{Event: "applied", Tool: "edit_file", ArgsSHA256: "a", ResultSHA256: "b"})
	if err == nil || !strings.Contains(err.Error(), "durable in pending journal") {
		t.Fatalf("RecordApplied error = %v, want durable pending notice", err)
	}
	entries, err := os.ReadDir(filepath.Join(rootDir, pendingDir))
	if err != nil || len(entries) != 1 {
		t.Fatalf("pending entries = %v, err=%v", entries, err)
	}
	pendingData, err := os.ReadFile(filepath.Join(rootDir, pendingDir, entries[0].Name()))
	if err != nil {
		t.Fatal(err)
	}
	var pending Event
	if err := json.Unmarshal(pendingData, &pending); err != nil {
		t.Fatal(err)
	}
	if pending.Event != "applied" || pending.ID == "" || pending.ResultSHA256 != "b" {
		t.Fatalf("pending event = %+v", pending)
	}

	if err := os.Remove(logPath); err != nil {
		t.Fatal(err)
	}
	if err := os.Rename(savedLogPath, logPath); err != nil {
		t.Fatal(err)
	}
	recoveredLog, err := Open(root)
	if err != nil {
		t.Fatalf("Open with pending journal: %v", err)
	}
	if err := recoveredLog.RecoverPending(); err != nil {
		t.Fatalf("second RecoverPending: %v", err)
	}
	data, err := os.ReadFile(logPath)
	if err != nil {
		t.Fatal(err)
	}
	var events []Event
	scanner := bufio.NewScanner(strings.NewReader(string(data)))
	for scanner.Scan() {
		var event Event
		if err := json.Unmarshal(scanner.Bytes(), &event); err != nil {
			t.Fatal(err)
		}
		events = append(events, event)
	}
	if err := scanner.Err(); err != nil {
		t.Fatal(err)
	}
	if len(events) != 2 || events[1].Event != "applied" || events[1].ID != pending.ID {
		t.Fatalf("recovered events = %+v", events)
	}
}
