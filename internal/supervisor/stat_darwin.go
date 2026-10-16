//go:build darwin

package supervisor

import (
	"syscall"
	"time"
)

func changeTime(st *syscall.Stat_t) time.Time { return time.Unix(st.Ctimespec.Sec, st.Ctimespec.Nsec) }
