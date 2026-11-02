package tui

import (
	"os"
	"strconv"
	"strings"
	"time"

	"github.com/openump/Hades/internal/inspect"
)

// clockTicks is USER_HZ, 100 on every Linux platform Hades targets.
const clockTicks = 100

// ReadResources samples RSS and CPU of each PID from /proc (Linux). CPU is
// the share of one core since the previous sample. PIDs that cannot be read
// (another OS, or the process is gone) are omitted.
func ReadResources(procRoot string, pids []int, previous map[int]Resource, now time.Time) map[int]Resource {
	out := map[int]Resource{}
	for _, pid := range pids {
		dir := procRoot + "/" + strconv.Itoa(pid)
		statm, err := os.ReadFile(dir + "/statm")
		if err != nil {
			continue
		}
		stat, err := os.ReadFile(dir + "/stat")
		if err != nil {
			continue
		}
		fields := strings.Fields(string(statm))
		if len(fields) < 2 {
			continue
		}
		pages, _ := strconv.ParseInt(fields[1], 10, 64)
		// The command name may contain spaces; fields resume after the last ')'.
		rest := strings.Fields(string(stat[strings.LastIndexByte(string(stat), ')')+1:]))
		if len(rest) < 13 {
			continue
		}
		utime, _ := strconv.ParseUint(rest[11], 10, 64)
		stime, _ := strconv.ParseUint(rest[12], 10, 64)
		r := Resource{RSSBytes: pages * int64(os.Getpagesize()), cpuTicks: utime + stime, at: now}
		if prev, ok := previous[pid]; ok && now.After(prev.at) && r.cpuTicks >= prev.cpuTicks {
			r.CPUPercent = float64(r.cpuTicks-prev.cpuTicks) / clockTicks / now.Sub(prev.at).Seconds() * 100
			r.CPUKnown = true
		}
		out[pid] = r
	}
	return out
}

// PIDs lists the supervised child PIDs in a snapshot's services.
func PIDs(snap *inspect.Snapshot) []int {
	if snap == nil {
		return nil
	}
	var pids []int
	for _, v := range snap.Services {
		e, _ := v.(map[string]any)
		if pid, _ := e["child_pid"].(float64); pid > 0 {
			pids = append(pids, int(pid))
		}
	}
	return pids
}
