package main

import (
	"fmt"
	"os"
	"runtime"
)

func main() {
	runtime.LockOSThread()
	if len(os.Args) < 2 {
		fmt.Fprintln(os.Stderr, "usage: mcp-guard <command>")
		os.Exit(2)
	}
	target := os.Args[1]
	if err := installFilter(); err != nil {
		fmt.Fprintf(os.Stderr, "mcp-guard: %v\n", err)
		os.Exit(1)
	}
	if err := execTarget(target); err != nil {
		fmt.Fprintf(os.Stderr, "mcp-guard: %v\n", err)
		os.Exit(1)
	}
}
