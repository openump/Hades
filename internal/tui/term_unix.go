//go:build linux || darwin || freebsd || netbsd || openbsd

package tui

import "golang.org/x/sys/unix"

// makeRaw puts fd in raw mode (no echo, no line buffering, no signal keys;
// Ctrl+C arrives as a key) and returns the function that restores it.
func makeRaw(fd int) (func() error, error) {
	old, err := unix.IoctlGetTermios(fd, ioctlGet)
	if err != nil {
		return nil, err
	}
	raw := *old
	raw.Iflag &^= unix.IGNBRK | unix.BRKINT | unix.PARMRK | unix.ISTRIP | unix.INLCR | unix.IGNCR | unix.ICRNL | unix.IXON
	raw.Oflag &^= unix.OPOST
	raw.Lflag &^= unix.ECHO | unix.ECHONL | unix.ICANON | unix.ISIG | unix.IEXTEN
	raw.Cflag &^= unix.CSIZE | unix.PARENB
	raw.Cflag |= unix.CS8
	raw.Cc[unix.VMIN], raw.Cc[unix.VTIME] = 1, 0
	if err := unix.IoctlSetTermios(fd, ioctlSet, &raw); err != nil {
		return nil, err
	}
	return func() error { return unix.IoctlSetTermios(fd, ioctlSet, old) }, nil
}

func size(fd int) (int, int, error) {
	ws, err := unix.IoctlGetWinsize(fd, unix.TIOCGWINSZ)
	if err != nil {
		return 0, 0, err
	}
	return int(ws.Col), int(ws.Row), nil
}

// IsTerminal reports whether fd is a terminal.
func IsTerminal(fd int) bool {
	_, err := unix.IoctlGetTermios(fd, ioctlGet)
	return err == nil
}
