//go:build linux && !android

// nabd-limit is the in-sandbox resource limiter. It is started by the sandbox
// backend after all filesystem and namespace restrictions are in place, sets
// hard rlimits, then replaces itself with the target executable. It accepts no
// user-controlled paths beyond those already bound read-only by the backend.
package main

import (
	"fmt"
	"os"
	"strconv"

	"golang.org/x/sys/unix"
)

func main() {
	if len(os.Args) < 4 {
		fmt.Fprintln(os.Stderr, "nabd-limit: usage: nabd-limit MEMORY PROCESSES TARGET [ARGS...]")
		os.Exit(2)
	}
	mem, err := strconv.ParseUint(os.Args[1], 10, 64)
	if err != nil || mem == 0 {
		fmt.Fprintln(os.Stderr, "nabd-limit: invalid memory limit")
		os.Exit(2)
	}
	procs, err := strconv.ParseUint(os.Args[2], 10, 64)
	if err != nil || procs == 0 {
		fmt.Fprintln(os.Stderr, "nabd-limit: invalid process limit")
		os.Exit(2)
	}
	for _, limit := range []struct {
		res  int
		val  uint64
		name string
	}{
		{unix.RLIMIT_AS, mem, "RLIMIT_AS"},
		{unix.RLIMIT_NPROC, procs, "RLIMIT_NPROC"},
		{unix.RLIMIT_CORE, 0, "RLIMIT_CORE"},
	} {
		if err := unix.Setrlimit(limit.res, &unix.Rlimit{Cur: limit.val, Max: limit.val}); err != nil {
			fmt.Fprintf(os.Stderr, "nabd-limit: setrlimit %s: %v\n", limit.name, err)
			os.Exit(2)
		}
	}
	target := os.Args[3]
	if err := unix.Exec(target, os.Args[3:], os.Environ()); err != nil {
		fmt.Fprintf(os.Stderr, "nabd-limit: exec: %v\n", err)
		os.Exit(2)
	}
}
