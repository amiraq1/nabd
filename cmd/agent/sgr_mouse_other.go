//go:build !unix

package main

func waitFDReadable(_ uintptr, _ int) bool {
	return false
}
