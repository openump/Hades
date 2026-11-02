//go:build darwin || freebsd || netbsd || openbsd

package tui

import "golang.org/x/sys/unix"

const ioctlGet, ioctlSet = unix.TIOCGETA, unix.TIOCSETA
