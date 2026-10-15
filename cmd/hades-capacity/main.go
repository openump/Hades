// hades-capacity measures the synced transfer-intake slice, not full pilot capacity.
package main

import (
	"encoding/json"
	"flag"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"runtime/debug"
	"sort"
	"strings"
	"syscall"
	"time"

	"github.com/openump/Hades/internal/sample"
	"github.com/openump/Hades/internal/transfer"
)

func main() {
	if err := run(); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}
func percentile(values []float64, fraction float64) float64 {
	if len(values) == 0 {
		return 0
	}
	copy := append([]float64(nil), values...)
	sort.Float64s(copy)
	return copy[int(float64(len(copy)-1)*fraction)]
}
func directoryBytes(root string) (int64, error) {
	var total int64
	err := filepath.WalkDir(root, func(path string, d os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if !d.IsDir() {
			info, err := d.Info()
			if err != nil {
				return err
			}
			total += info.Size()
		}
		return nil
	})
	return total, err
}
func run() error {
	count := flag.Int("count", 200, "observations, 1..10000")
	rate := flag.Int("rate", 50, "scheduled observations/sec; 0 is unpaced burst")
	host := flag.String("host-label", "", "operator supplied machine label")
	root := flag.String("root", ".", "repository root")
	flag.Parse()
	if *host == "" || *count < 1 || *count > 10000 || *rate < 0 || *rate > 10000 {
		return fmt.Errorf("host-label required; count 1..10000, rate 0..10000")
	}
	cfg, err := sample.Load(*root, filepath.Join(*root, "examples/site/metadata.json"))
	if err != nil {
		return err
	}
	scratch, err := os.MkdirTemp("", "hades-capacity-")
	if err != nil {
		return err
	}
	defer os.RemoveAll(scratch)
	q, err := transfer.Open(cfg, scratch, 100000, 256*1024*1024)
	if err != nil {
		return err
	}
	defer q.Close()
	var before, after syscall.Rusage
	if err = syscall.Getrusage(syscall.RUSAGE_SELF, &before); err != nil {
		return err
	}
	var memory runtime.MemStats
	runtime.ReadMemStats(&memory)
	startHeap := memory.HeapAlloc
	peakHeap := startHeap
	start := time.Now()
	latencies := make([]float64, 0, *count)
	var payloadBytes int64
	boot := sample.UUID()
	refused := 0
	lag := time.Duration(0)
	for i := 0; i < *count; i++ {
		if *rate > 0 {
			scheduled := start.Add(time.Duration(i) * time.Second / time.Duration(*rate))
			if wait := time.Until(scheduled); wait > 0 {
				time.Sleep(wait)
			} else if -wait > lag {
				lag = -wait
			}
		}
		raw, err := cfg.Observation("normal", i+1, boot, time.Now())
		if err != nil {
			return err
		}
		payloadBytes += int64(len(raw))
		call := time.Now()
		err = q.Intake(cfg.Prefix+"telemetry", raw)
		latencies = append(latencies, float64(time.Since(call).Microseconds())/1000)
		if err != nil {
			refused++
			return fmt.Errorf("workload refused at %d: %w", i, err)
		}
		runtime.ReadMemStats(&memory)
		if memory.HeapAlloc > peakHeap {
			peakHeap = memory.HeapAlloc
		}
	}
	elapsed := time.Since(start)
	if err = syscall.Getrusage(syscall.RUSAGE_SELF, &after); err != nil {
		return err
	}
	disk, err := directoryBytes(scratch)
	if err != nil {
		return err
	}
	cpu := float64(after.Utime.Sec-before.Utime.Sec+after.Stime.Sec-before.Stime.Sec) + float64(after.Utime.Usec-before.Utime.Usec+after.Stime.Usec-before.Stime.Usec)/1e6
	rss := after.Maxrss
	rssUnit := "KiB"
	if runtime.GOOS == "darwin" {
		rssUnit = "bytes"
	}
	revision, _ := exec.Command("git", "-C", *root, "rev-parse", "HEAD").Output()
	machine, _ := exec.Command("uname", "-m").Output()
	build, _ := debug.ReadBuildInfo()
	report := map[string]any{"build_info": build, "instrumentation": "includes per-record runtime.ReadMemStats overhead; no warmup", "format": "openump-capacity/1", "host_label": *host, "architecture": strings.TrimSpace(string(machine)), "os": runtime.GOOS, "goarch": runtime.GOARCH, "logical_cpus": runtime.NumCPU(), "go_version": runtime.Version(), "source_revision": strings.TrimSpace(string(revision)), "scope": "single_process_transfer_intake_only", "pilot_qualified": false, "assets": 1, "measurements_per_asset": 1, "numeric_label_sets": 1, "queue_identity_cardinality": *count, "observations": *count, "scheduled_rate_per_second": *rate, "elapsed_seconds": elapsed.Seconds(), "achieved_per_second": float64(*count) / elapsed.Seconds(), "schedule_max_lag_ms": float64(lag.Microseconds()) / 1000, "payload_bytes_mean": float64(payloadBytes) / float64(*count), "intake_ms_p50": percentile(latencies, .50), "intake_ms_p95": percentile(latencies, .95), "intake_ms_p99": percentile(latencies, .99), "process_cpu_seconds": cpu, "process_cpu_core_equivalents": cpu / elapsed.Seconds(), "process_maxrss": rss, "process_maxrss_unit": rssUnit, "go_heap_start_bytes": startHeap, "go_heap_peak_sampled_bytes": peakHeap, "queue_file_bytes": disk, "queue_file_bytes_per_record": float64(disk) / float64(*count), "queue": q.Stats(), "refused": refused, "not_measured": []string{"whole_stack_cpu_ram", "historian_disk_per_sample", "physical_disk_write_amplification", "storage_endurance", "multi_asset_cardinality", "PLC_poll_load", "pilot_hardware"}}
	return json.NewEncoder(os.Stdout).Encode(report)
}
