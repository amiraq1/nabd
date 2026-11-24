//go:build linux && !android

// nabd-sandbox-probe runs only inside the sandbox and exits successfully only
// if every promised restriction is actually enforced. It is shipped next to
// the agent and used by sandbox.Prove at startup, so failing to install it
// keeps external execution disabled.
package main

import (
	"errors"
	"fmt"
	"net"
	"os"
	"strconv"
	"time"

	"golang.org/x/sys/unix"
)

func fail(format string, args ...any) {
	fmt.Fprintf(os.Stderr, "sandbox-proof-failed: "+format+"\n", args...)
	os.Exit(1)
}

func main() {
	if _, err := os.Stat("/etc/passwd"); !errors.Is(err, os.ErrNotExist) {
		fail("host /etc/passwd is visible: %v", err)
	}
	if err := os.WriteFile("/workspace/must-not-exist", []byte("x"), 0o600); err == nil {
		fail("workspace was writable in a read-only profile")
	}
	conn, err := net.DialTimeout("tcp", "127.0.0.1:9", 500*time.Millisecond)
	if err == nil {
		_ = conn.Close()
		fail("network namespace did not block loopback connections")
	}
	expectMem, err := strconv.ParseUint(os.Getenv("EXPECT_MEM"), 10, 64)
	if err != nil {
		fail("missing expected memory limit")
	}
	expectProcs, err := strconv.ParseUint(os.Getenv("EXPECT_PROCS"), 10, 64)
	if err != nil {
		fail("missing expected process limit")
	}
	var rl unix.Rlimit
	if err := unix.Getrlimit(unix.RLIMIT_AS, &rl); err != nil || rl.Cur != expectMem {
		fail("RLIMIT_AS is %d, want %d (err=%v)", rl.Cur, expectMem, err)
	}
	if err := unix.Getrlimit(unix.RLIMIT_NPROC, &rl); err != nil || rl.Cur != expectProcs {
		fail("RLIMIT_NPROC is %d, want %d (err=%v)", rl.Cur, expectProcs, err)
	}
	fmt.Println("sandbox-proof-ok")
}
