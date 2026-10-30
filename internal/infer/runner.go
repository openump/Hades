package infer

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"time"
)

// Input is one inference call: the good samples of each bound measurement,
// oldest first, plus each window's good-sample ratio.
type Input struct {
	Windows    map[string][]float64 `json:"windows"`
	GoodRatios map[string]float64   `json:"good_ratios"`
}

// Result is a runner's output for one window. Score and Confidence are 0..1;
// Contributions maps each measurement to its 0..1 share of the score;
// SoftSensors maps soft-sensor output measurements to derived values.
type Result struct {
	Score         float64            `json:"score"`
	Confidence    float64            `json:"confidence"`
	Contributions map[string]float64 `json:"contributions"`
	SoftSensors   map[string]float64 `json:"soft_sensors"`
}

// Runner executes one verified model. Implementations must be safe to call
// from a single goroutine and must honor ctx cancellation.
type Runner interface {
	Infer(ctx context.Context, in Input) (Result, error)
	Close() error
}

// NewRunner builds the runner for a verified bundle. statistical_v1 runs in
// process; onnx_v1 and external_runner_v1 run in a separate process reached
// over a local Unix socket, so hardware-accelerated runtimes need no cgo here.
func NewRunner(bundle *Bundle, externalSocket string) (Runner, error) {
	switch bundle.Manifest.RuntimeType {
	case "statistical_v1":
		model, err := ParseStatisticalModel(bundle.Artifact)
		if err != nil {
			return nil, err
		}
		return &statisticalRunner{model: model}, nil
	case "onnx_v1", "external_runner_v1":
		if externalSocket == "" {
			return nil, fmt.Errorf("%s needs an external runner socket", bundle.Manifest.RuntimeType)
		}
		return &externalRunner{socket: externalSocket, model: bundle.Manifest.ModelID + "@" + bundle.Manifest.ModelVersion}, nil
	default:
		return nil, fmt.Errorf("unsupported runtime %q", bundle.Manifest.RuntimeType)
	}
}

const maxRunnerMessage = 1 << 20

// externalRunner sends one newline-delimited JSON request per window to a
// local runner process and reads one JSON response. Each call has its own
// connection and the context deadline, and both directions are size-bounded,
// so a stuck or noisy runner cannot block or exhaust the sidecar.
type externalRunner struct {
	socket string
	model  string
}

type externalRequest struct {
	Model string `json:"model"`
	Input
}

type externalResponse struct {
	Result
	Error string `json:"error,omitempty"`
}

func (r *externalRunner) Infer(ctx context.Context, in Input) (Result, error) {
	var dialer net.Dialer
	conn, err := dialer.DialContext(ctx, "unix", r.socket)
	if err != nil {
		return Result{}, err
	}
	defer conn.Close()
	if deadline, ok := ctx.Deadline(); ok {
		conn.SetDeadline(deadline)
	} else {
		conn.SetDeadline(time.Now().Add(10 * time.Second))
	}
	request, err := json.Marshal(externalRequest{Model: r.model, Input: in})
	if err != nil {
		return Result{}, err
	}
	if len(request) > maxRunnerMessage {
		return Result{}, fmt.Errorf("runner request exceeds %d bytes", maxRunnerMessage)
	}
	if _, err := conn.Write(append(request, '\n')); err != nil {
		return Result{}, err
	}
	scanner := bufio.NewScanner(io.LimitReader(conn, maxRunnerMessage+1))
	scanner.Buffer(make([]byte, 0, 64<<10), maxRunnerMessage)
	if !scanner.Scan() {
		if err := scanner.Err(); err != nil {
			return Result{}, fmt.Errorf("runner response: %w", err)
		}
		return Result{}, fmt.Errorf("runner closed without a response")
	}
	var response externalResponse
	if err := json.Unmarshal(scanner.Bytes(), &response); err != nil {
		return Result{}, fmt.Errorf("runner response: %w", err)
	}
	if response.Error != "" {
		return Result{}, fmt.Errorf("runner: %s", response.Error)
	}
	if !(response.Score >= 0 && response.Score <= 1) || !(response.Confidence >= 0 && response.Confidence <= 1) {
		return Result{}, fmt.Errorf("runner returned out-of-range score or confidence")
	}
	return response.Result, nil
}

func (r *externalRunner) Close() error { return nil }
