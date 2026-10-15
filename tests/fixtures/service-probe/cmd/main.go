// Disposable Linux resource-policy probe. Never a deployed Hades service.
package main

import (
	"flag"
	"fmt"
	"os"
	"runtime"
	"strings"
	"time"
)

func main() {
	mode := flag.String("mode", "", "cpu or memory")
	flag.Parse()
	if *mode != "cpu" && *mode != "memory" {
		os.Exit(2)
	}
	fmt.Println("probe_ready")
	go func() {
		ticker := time.NewTicker(time.Millisecond)
		defer ticker.Stop()
		for range ticker.C {
			fmt.Println(strings.Repeat("health ", 150))
		}
	}()
	if *mode == "cpu" {
		for {
		}
	}
	var retained [][]byte
	for {
		raw := make([]byte, 32*1024*1024)
		for i := 0; i < len(raw); i += 4096 {
			raw[i] = 1
		}
		retained = append(retained, raw)
		if len(retained) > 40 {
			os.Exit(3)
		} // Fail safely if a missing cgroup limit permits >1.25 GiB.
		time.Sleep(50 * time.Millisecond)
		runtime.KeepAlive(retained)
	}
}
