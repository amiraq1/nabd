//go:build unix

package main

import (
	"errors"

	"golang.org/x/sys/unix"
)

func waitFDReadable(fd uintptr, timeoutMs int) bool {
	fds := []unix.PollFd{{Fd: int32(fd), Events: unix.POLLIN}}
	for {
		n, err := unix.Poll(fds, timeoutMs)
		if errors.Is(err, unix.EINTR) {
			continue
		}
		if err != nil || n <= 0 {
			return false
		}
		return fds[0].Revents&(unix.POLLIN|unix.POLLHUP|unix.POLLERR) != 0
	}
}
