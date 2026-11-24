//go:build !linux || android

package main

import "os"

func main() {
	os.Stderr.WriteString("nabd-sandbox-probe is only available on Linux\n")
	os.Exit(2)
}
