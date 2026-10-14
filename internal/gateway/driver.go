package gateway

import (
	"bytes"
	"context"
	"fmt"
	"os"
	"os/exec"
	"strings"
	"time"
)

type boundedOutput struct {
	bytes.Buffer
	limit int
}

func (b *boundedOutput) Write(p []byte) (int, error) {
	if b.Len()+len(p) > b.limit {
		return 0, fmt.Errorf("driver output bound exceeded")
	}
	return b.Buffer.Write(p)
}

type Driver struct{ Binary, Config string }

func OpenDriver(ctx context.Context, binary, endpoint string) (*Driver, error) {
	if binary == "" {
		return nil, fmt.Errorf("explicit Telegraf binary required")
	}
	versionctx, cancel := context.WithTimeout(ctx, 3*time.Second)
	defer cancel()
	cmd := exec.CommandContext(versionctx, binary, "--version")
	output := &boundedOutput{limit: 4096}
	cmd.Stdout = output
	cmd.Stderr = output
	if err := cmd.Run(); err != nil {
		return nil, err
	}
	if !strings.HasPrefix(strings.TrimSpace(output.String()), "Telegraf 1.40.2 ") {
		return nil, fmt.Errorf("driver must be pinned Telegraf 1.40.2")
	}
	config, err := DriverConfig(endpoint)
	if err != nil {
		return nil, err
	}
	file, err := os.CreateTemp("", "hades-lab-telegraf-*.conf")
	if err != nil {
		return nil, err
	}
	if _, err = file.WriteString(config); err != nil {
		file.Close()
		os.Remove(file.Name())
		return nil, err
	}
	if err = file.Close(); err != nil {
		os.Remove(file.Name())
		return nil, err
	}
	return &Driver{binary, file.Name()}, nil
}
func (d *Driver) Read(ctx context.Context) (*uint16, time.Time, string) {
	attempt, done := context.WithTimeout(ctx, 2*time.Second)
	defer done()
	cmd := exec.CommandContext(attempt, d.Binary, "--config", d.Config, "--once", "--input-filter", "modbus", "--output-filter", "file")
	output := &boundedOutput{limit: 65536}
	diagnostic := &boundedOutput{limit: 4096}
	cmd.Stdout = output
	cmd.Stderr = diagnostic
	if err := cmd.Run(); err != nil {
		reason := "read-error"
		if attempt.Err() != nil {
			reason = "read-timeout"
		}
		return nil, time.Now().UTC(), reason
	}
	n, stamp, err := DecodeReading(bytes.TrimSpace(output.Bytes()))
	if err != nil {
		return nil, time.Now().UTC(), "invalid-or-missing-reading"
	}
	return &n, stamp, ""
}
func (d *Driver) Close() error { return os.Remove(d.Config) }
