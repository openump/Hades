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

type Driver struct {
	Binary, Config string
	Profile        Profile
}

func OpenDriver(ctx context.Context, binary, endpoint string) (*Driver, error) {
	return OpenDriverForProfile(ctx, binary, endpoint, DefaultLabProfile())
}

func OpenDriverForProfile(ctx context.Context, binary, endpoint string, profile Profile) (*Driver, error) {
	p, err := validateProfile(profile)
	if err != nil {
		return nil, err
	}
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
	config, err := DriverConfigForProfile(endpoint, p)
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
	return &Driver{Binary: binary, Config: file.Name(), Profile: p}, nil
}

func (d *Driver) activeProfile() Profile {
	if len(d.Profile.Registers) == 0 {
		return DefaultLabProfile()
	}
	return d.Profile
}

func (d *Driver) ReadMap(ctx context.Context) (map[string]uint16, time.Time, string) {
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
	values, stamp, err := DecodeReadingsForProfile(bytes.TrimSpace(output.Bytes()), d.activeProfile())
	if err != nil {
		return nil, time.Now().UTC(), "invalid-or-missing-reading"
	}
	return values, stamp, ""
}

func (d *Driver) Read(ctx context.Context) (*uint16, time.Time, string) {
	values, stamp, reason := d.ReadMap(ctx)
	if values == nil {
		return nil, stamp, reason
	}
	p := d.activeProfile()
	v := values[p.Registers[0].Name]
	return &v, stamp, ""
}

func (d *Driver) Poll(ctx context.Context) (map[string]Reading, time.Time, string) {
	values, stamp, reason := d.ReadMap(ctx)
	p := d.activeProfile()
	out := make(map[string]Reading, len(p.Registers))
	for _, reg := range p.Registers {
		if v, ok := values[reg.Name]; ok && values != nil {
			out[reg.Name] = Reading{Value: v, Unit: reg.Unit, Quality: "good"}
		} else {
			out[reg.Name] = Reading{Value: nil, Unit: reg.Unit, Quality: "bad", Reason: reason}
		}
	}
	return out, stamp, reason
}

func (d *Driver) Close() error { return os.Remove(d.Config) }
