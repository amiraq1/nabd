//go:build !linux || android

package main

import "os"

func main() {
	os.Stderr.WriteString("nabd-limit is only available on Linux\n")
	os.Exit(2)
}
