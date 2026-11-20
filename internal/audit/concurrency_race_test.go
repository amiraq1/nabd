package audit

import (
	"fmt"
	"os"
	"sync"
	"testing"
	"time"
)

// TestConcurrentAuditLogAccess verifies that concurrent calls to Write (durable fsync)
// and WriteBestEffort (async buffered channel) do not race or corrupt the JSON-lines audit file.
func TestConcurrentAuditLogAccess(t *testing.T) {
	dir := t.TempDir()
	root, err := os.OpenRoot(dir)
	if err != nil {
		t.Fatal(err)
	}
	defer root.Close()

	auditLog, err := Open(root)
	if err != nil {
		t.Fatal(err)
	}
	defer auditLog.Close()

	const numDurable = 15
	const numBestEffort = 25
	var wg sync.WaitGroup

	// Durable writes (fsync)
	for i := 0; i < numDurable; i++ {
		wg.Add(1)
		go func(id int) {
			defer wg.Done()
			err := auditLog.Write(Event{
				Time:   time.Now(),
				Event:  "durable_event",
				Tool:   "edit_file",
				Detail: fmt.Sprintf("path_%d.go", id),
			})
			if err != nil {
				t.Errorf("Write error: %v", err)
			}
		}(i)
	}

	// Best-effort writes (buffered channel)
	for i := 0; i < numBestEffort; i++ {
		wg.Add(1)
		go func(id int) {
			defer wg.Done()
			auditLog.WriteBestEffort(Event{
				Time:   time.Now(),
				Event:  "best_effort_event",
				Tool:   "read_file",
				Detail: fmt.Sprintf("read_%d.go", id),
			})
		}(i)
	}

	wg.Wait()
}
