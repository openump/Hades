package gateway

import (
	"bufio"
	"bytes"
	"context"
	"fmt"
	"io"
	"os"
	"os/exec"
	"strings"
	"sync"
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
	Binary, Config, Endpoint string
	Profile                  Profile
	options                  DriverOptions
	session                  *PersistentSession
	guardrail                *EndpointGuardrail
	deadbands                deadbandSet
	cmd                      *exec.Cmd
	cancel                   context.CancelFunc
	updates                  chan driverUpdate
	failures                 chan string
	stderr                   *boundedOutput
	closeOnce                sync.Once
	waitDone                 chan struct{}
	status                   *statusEmitter
}

// DeadbandConfig configures report-by-exception for one signal: readings within
// ±Epsilon of the last reported value are suppressed until MaxSilenceInterval
// has elapsed, at which point the current value is reported as a heartbeat.
type DeadbandConfig struct {
	Epsilon            float64
	MaxSilenceInterval time.Duration
}

type DriverOptions struct {
	PollInterval   time.Duration
	ReadTimeout    time.Duration
	RatePerSecond  float64
	Burst          int
	MaxInFlight    int
	GatewayID      string
	StatusReporter func(ProducerStatusEvent)
	Deadbands      map[string]DeadbandConfig
	Now            func() time.Time
}

type driverUpdate struct {
	values map[string]uint16
	stamp  time.Time
}

func OpenDriver(ctx context.Context, binary, endpoint string) (*Driver, error) {
	return OpenDriverWithOptions(ctx, binary, endpoint, DefaultLabProfile(), DriverOptions{})
}

func OpenDriverForProfile(ctx context.Context, binary, endpoint string, profile Profile) (*Driver, error) {
	return OpenDriverWithOptions(ctx, binary, endpoint, profile, DriverOptions{})
}

func OpenDriverWithOptions(ctx context.Context, binary, endpoint string, profile Profile, options DriverOptions) (*Driver, error) {
	p, err := validateProfile(profile)
	if err != nil {
		return nil, err
	}
	options = normalizeDriverOptions(options)
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
	config, err := DriverConfigForProfileWithInterval(endpoint, p, options.PollInterval)
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
	sidecarCtx, sidecarCancel := context.WithCancel(context.Background())
	cmd = exec.CommandContext(sidecarCtx, binary, "--config", file.Name(), "--input-filter", "modbus", "--output-filter", "file")
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		sidecarCancel()
		os.Remove(file.Name())
		return nil, err
	}
	stderrPipe, err := cmd.StderrPipe()
	if err != nil {
		sidecarCancel()
		os.Remove(file.Name())
		return nil, err
	}
	stderrBuf := &boundedOutput{limit: 4096}
	d := &Driver{
		Binary:    binary,
		Config:    file.Name(),
		Endpoint:  endpoint,
		Profile:   p,
		options:   options,
		session:   NewPersistentSession(endpoint),
		guardrail: NewEndpointGuardrail(endpoint, options.RatePerSecond, options.Burst, options.MaxInFlight),
		deadbands: newDeadbandSet(options.Deadbands),
		updates:   make(chan driverUpdate, 1),
		failures:  make(chan string, 1),
		stderr:    stderrBuf,
		cmd:       cmd,
		cancel:    sidecarCancel,
		waitDone:  make(chan struct{}),
		status:    newStatusEmitter(options),
	}
	if err := cmd.Start(); err != nil {
		sidecarCancel()
		os.Remove(file.Name())
		return nil, err
	}
	go d.consumeStdout(stdout)
	go d.consumeStderr(stderrPipe)
	go d.waitSidecar()
	return d, nil
}

func normalizeDriverOptions(options DriverOptions) DriverOptions {
	if options.PollInterval == 0 {
		options.PollInterval = time.Second
	}
	if options.ReadTimeout == 0 {
		options.ReadTimeout = 2 * time.Second
	}
	if options.RatePerSecond == 0 {
		options.RatePerSecond = 1
	}
	if options.Burst == 0 {
		options.Burst = 1
	}
	if options.MaxInFlight == 0 {
		options.MaxInFlight = 1
	}
	if options.Now == nil {
		options.Now = func() time.Time { return time.Now().UTC() }
	}
	if options.Deadbands == nil {
		options.Deadbands = map[string]DeadbandConfig{}
	}
	return options
}

func (d *Driver) activeProfile() Profile {
	if len(d.Profile.Registers) == 0 {
		return DefaultLabProfile()
	}
	return d.Profile
}

// ReadMap returns the latest raw sidecar batch. Deadbands are applied by Poll,
// which can omit suppressed readings; ReadMap and Read always return raw values.
func (d *Driver) ReadMap(ctx context.Context) (map[string]uint16, time.Time, string) {
	attempt, done := context.WithTimeout(ctx, d.options.ReadTimeout)
	defer done()
	if err := d.guardrail.Enter(); err != nil {
		return nil, d.options.Now(), guardReason(err)
	}
	defer d.guardrail.Leave()
	select {
	case update := <-d.updates:
		d.session.RecordPoll(CoalesceAdjacentReads(d.activeProfile().Registers), "")
		recovery := d.guardrail.RecordSuccess()
		d.status.emit(recovery)
		return update.values, update.stamp, ""
	case reason := <-d.failures:
		event := d.guardrail.RecordFailure(reason)
		d.status.emit(event)
		return nil, d.options.Now(), reason
	case <-attempt.Done():
		reason := "read-timeout"
		event := d.guardrail.RecordFailure(reason)
		d.status.emit(event)
		return nil, d.options.Now(), reason
	}
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

// Poll reads the latest sidecar batch. Readings suppressed by a configured
// deadband are omitted from the result.
func (d *Driver) Poll(ctx context.Context) (map[string]Reading, time.Time, string) {
	values, stamp, reason := d.ReadMap(ctx)
	p := d.activeProfile()
	out := make(map[string]Reading, len(p.Registers))
	for _, reg := range p.Registers {
		if v, ok := values[reg.Name]; ok {
			out[reg.Name] = Reading{Value: v, Unit: reg.Unit, Quality: "good", ObservedAt: stamp}
		} else {
			out[reg.Name] = Reading{Value: nil, Unit: reg.Unit, Quality: "bad", Reason: reason, ObservedAt: stamp}
		}
	}
	return d.deadbands.filter(out, d.options.Now()), stamp, reason
}

func (d *Driver) Close() error {
	var err error
	d.closeOnce.Do(func() {
		if d.cancel != nil {
			d.cancel()
		}
		if d.cmd != nil && d.cmd.Process != nil {
			_ = d.cmd.Process.Kill()
		}
		if d.waitDone != nil {
			<-d.waitDone
		}
		if d.Config != "" {
			err = os.Remove(d.Config)
		}
	})
	return err
}

func (d *Driver) consumeStdout(stdout io.Reader) {
	scanner := bufio.NewScanner(stdout)
	buffer := make([]byte, 0, 64*1024)
	scanner.Buffer(buffer, 128*1024)
	for scanner.Scan() {
		line := bytes.TrimSpace(scanner.Bytes())
		if len(line) == 0 {
			continue
		}
		values, stamp, err := DecodeReadingsForProfile(line, d.activeProfile())
		if err != nil {
			d.pushFailure("invalid-or-missing-reading")
			continue
		}
		d.pushUpdate(driverUpdate{values: values, stamp: stamp})
	}
	if scanner.Err() != nil {
		d.pushFailure("read-error")
	}
}

func (d *Driver) consumeStderr(stderr io.Reader) {
	_, _ = io.Copy(d.stderr, stderr)
}

func (d *Driver) waitSidecar() {
	defer close(d.waitDone)
	if d.cmd == nil {
		return
	}
	if err := d.cmd.Wait(); err != nil && !strings.Contains(err.Error(), "killed") {
		d.pushFailure("read-error")
	}
}

func (d *Driver) pushUpdate(update driverUpdate) {
	select {
	case d.updates <- update:
	default:
		select {
		case <-d.updates:
		default:
		}
		d.updates <- update
	}
}

func (d *Driver) pushFailure(reason string) {
	select {
	case d.failures <- reason:
	default:
	}
}
