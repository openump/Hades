package gateway

import (
	"bufio"
	"bytes"
	"context"
	"fmt"
	"io"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
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

// tailBuffer keeps the most recent limit bytes written to it and never fails,
// so a chatty sidecar cannot block on, or lose, its stderr pipe across restarts.
type tailBuffer struct {
	mu    sync.Mutex
	limit int
	data  []byte
}

func (b *tailBuffer) Write(p []byte) (int, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.data = append(b.data, p...)
	if extra := len(b.data) - b.limit; extra > 0 {
		b.data = append(b.data[:0], b.data[extra:]...)
	}
	return len(p), nil
}

func (b *tailBuffer) String() string {
	b.mu.Lock()
	defer b.mu.Unlock()
	return string(b.data)
}

// Driver is the Tier 2 Telegraf sidecar. One long-lived Telegraf daemon polls
// the controller and streams JSON batches into the gateway over a private Unix
// domain socket (outputs.socket_writer); a supervisor restarts the daemon with
// exponential backoff if it exits.
type Driver struct {
	Binary, Config, Endpoint string
	// Socket is the Unix domain socket the sidecar writes batches to.
	Socket    string
	Profile   Profile
	options   DriverOptions
	session   *PersistentSession
	guardrail *EndpointGuardrail
	deadbands deadbandSet
	dir       string
	listener  net.Listener
	ctx       context.Context
	cancel    context.CancelFunc
	updates   chan driverUpdate
	failures  chan string
	stderr    *tailBuffer
	closeOnce sync.Once
	done      sync.WaitGroup
	status    *statusEmitter
	starts    atomic.Int64
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
	// Subscribe makes the OPC UA driver use MonitoredItem subscriptions instead
	// of polled batch reads.
	Subscribe bool
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
	versionctx, cancel := context.WithTimeout(ctx, sidecarVersionTimeout)
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
	dir, err := os.MkdirTemp("", "hades-telegraf-")
	if err != nil {
		return nil, err
	}
	socket := filepath.Join(dir, "sidecar.sock")
	config, err := DriverConfigForSocket(endpoint, p, options.PollInterval, socket)
	if err != nil {
		os.RemoveAll(dir)
		return nil, err
	}
	configPath := filepath.Join(dir, "telegraf.conf")
	if err = os.WriteFile(configPath, []byte(config), 0o600); err != nil {
		os.RemoveAll(dir)
		return nil, err
	}
	listener, err := net.Listen("unix", socket)
	if err != nil {
		os.RemoveAll(dir)
		return nil, err
	}
	supervisorCtx, supervisorCancel := context.WithCancel(context.Background())
	d := &Driver{
		Binary:    binary,
		Config:    configPath,
		Endpoint:  endpoint,
		Socket:    socket,
		Profile:   p,
		options:   options,
		session:   NewPersistentSession(endpoint),
		guardrail: NewEndpointGuardrail(endpoint, options.RatePerSecond, options.Burst, options.MaxInFlight),
		deadbands: newDeadbandSet(options.Deadbands),
		dir:       dir,
		listener:  listener,
		ctx:       supervisorCtx,
		cancel:    supervisorCancel,
		updates:   make(chan driverUpdate, 1),
		failures:  make(chan string, 1),
		stderr:    &tailBuffer{limit: 4096},
		status:    newStatusEmitter(options),
	}
	d.done.Add(2)
	go d.acceptBatches()
	go d.supervise(supervisorCtx)
	return d, nil
}

// SidecarStarts reports how many times the supervisor has launched Telegraf.
func (d *Driver) SidecarStarts() int64 {
	return d.starts.Load()
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
		d.cancel()
		d.listener.Close()
		d.done.Wait()
		err = os.RemoveAll(d.dir)
	})
	return err
}

const (
	// sidecarVersionTimeout bounds the pinned-version probe; a cold start on a
	// loaded edge host can take several seconds.
	sidecarVersionTimeout = 10 * time.Second
	sidecarBaseBackoff    = 250 * time.Millisecond
	sidecarMaxBackoff     = 30 * time.Second
	// sidecarStableRun resets the restart backoff once a daemon has stayed up.
	sidecarStableRun = 30 * time.Second
)

// supervise keeps one Telegraf daemon running until ctx is cancelled,
// restarting it with exponential backoff after each exit.
func (d *Driver) supervise(ctx context.Context) {
	defer d.done.Done()
	backoff := sidecarBaseBackoff
	for {
		started := time.Now()
		d.runSidecar(ctx)
		if ctx.Err() != nil {
			return
		}
		d.pushFailure("read-error")
		if time.Since(started) >= sidecarStableRun {
			backoff = sidecarBaseBackoff
		}
		timer := time.NewTimer(backoff)
		select {
		case <-ctx.Done():
			timer.Stop()
			return
		case <-timer.C:
		}
		backoff = min(backoff*2, sidecarMaxBackoff)
	}
}

func (d *Driver) runSidecar(ctx context.Context) {
	cmd := exec.CommandContext(ctx, d.Binary, "--config", d.Config, "--input-filter", "modbus", "--output-filter", "socket_writer")
	cmd.Stdout = io.Discard
	cmd.Stderr = d.stderr
	cmd.WaitDelay = time.Second
	if err := cmd.Start(); err != nil {
		return
	}
	d.starts.Add(1)
	_ = cmd.Wait()
}

// acceptBatches serves sidecar connections on the private socket. Telegraf
// reconnects after a restart, so connections are accepted until Close.
func (d *Driver) acceptBatches() {
	defer d.done.Done()
	var conns sync.WaitGroup
	defer conns.Wait()
	for {
		conn, err := d.listener.Accept()
		if err != nil {
			return
		}
		conns.Add(1)
		go func() {
			defer conns.Done()
			d.consumeBatches(conn)
		}()
	}
}

func (d *Driver) consumeBatches(conn net.Conn) {
	defer conn.Close()
	stop := context.AfterFunc(d.ctx, func() { conn.Close() })
	defer stop()
	scanner := bufio.NewScanner(conn)
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
	if scanner.Err() != nil && d.ctx.Err() == nil {
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
