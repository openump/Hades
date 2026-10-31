package mcp

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"sort"
	"sync"
	"time"

	sdk "github.com/modelcontextprotocol/go-sdk/mcp"
	"github.com/openump/Hades/internal/contracts"
	"github.com/openump/Hades/internal/sample"
)

// Role scopes what an MCP caller may do.
type Role string

const (
	RoleReadOnly Role = "mcp-readonly"
	RoleProposer Role = "mcp-proposer"
)

// Sources are the read-only plant views hades-mcp serves. None of them polls a
// PLC: telemetry and status come from broker subscriptions, history from the
// guarded historian, health from supervisor status.
type Sources struct {
	Config    *sample.Config
	Telemetry *LatestCache
	History   func(ctx context.Context, values url.Values) (json.RawMessage, error)
	Health    func(ctx context.Context) (map[string]any, error)
}

// Submitter sends a built proposal to hades-actuator and returns its verified
// receipt.
type Submitter func(ctx context.Context, p *Proposal) (map[string]any, error)

// Options configures one role's server.
type Options struct {
	Sources   Sources
	Limiter   *Limiter
	Proposer  *Proposer
	Submit    Submitter
	Caller    string
	Now       func() time.Time
	ServerVer string
}

// NewServer builds the MCP server for one role. A read-only server does not
// register propose_command at all, so a read-only caller can neither call nor
// list it.
func NewServer(role Role, opts Options) *sdk.Server {
	server := sdk.NewServer(&sdk.Implementation{Name: "hades-mcp", Version: opts.ServerVer}, nil)
	t := &tools{opts: opts}
	sdk.AddTool(server, &sdk.Tool{Name: "list_assets", Description: "List approved assets with their ISA-95 hierarchy."}, limited(t, t.listAssets))
	sdk.AddTool(server, &sdk.Tool{Name: "get_asset_context", Description: "Get an asset's validated hierarchy, measurements, units and ranges."}, limited(t, t.assetContext))
	sdk.AddTool(server, &sdk.Tool{Name: "get_latest_telemetry", Description: "Get the latest validated observation for an asset."}, limited(t, t.latestTelemetry))
	sdk.AddTool(server, &sdk.Tool{Name: "get_producer_status", Description: "Get the latest gateway and service status for an asset."}, limited(t, t.producerStatus))
	sdk.AddTool(server, &sdk.Tool{Name: "query_metric_history", Description: "Bounded history for one measurement: window <= 24h, <= 1440 points."}, limited(t, t.history))
	sdk.AddTool(server, &sdk.Tool{Name: "get_edge_health", Description: "Get supervised edge service status."}, limited(t, t.edgeHealth))
	if role == RoleProposer && opts.Proposer != nil && opts.Submit != nil {
		sdk.AddTool(server, &sdk.Tool{Name: "propose_command", Description: "Propose a setpoint change. dry_run is required: true evaluates it in shadow (L0); false queues it for explicit operator confirmation (L1). Never writes to a PLC directly."}, limited(t, t.propose))
	}
	return server
}

type tools struct{ opts Options }

var errRateLimited = errors.New("rate limit exceeded; retry later")

// limited wraps a handler with the caller's token bucket.
func limited[In, Out any](t *tools, h sdk.ToolHandlerFor[In, Out]) sdk.ToolHandlerFor[In, Out] {
	return func(ctx context.Context, req *sdk.CallToolRequest, in In) (*sdk.CallToolResult, Out, error) {
		var zero Out
		if !t.opts.Limiter.Allow(t.opts.Caller) {
			return nil, zero, errRateLimited
		}
		return h(ctx, req, in)
	}
}

type empty struct{}

type assetRef struct {
	Asset string `json:"asset" jsonschema:"asset URN, e.g. urn:openump:acme:berlin:press-04"`
}

type assetSummary struct {
	Asset     string            `json:"asset"`
	Hierarchy map[string]string `json:"hierarchy"`
	Name      string            `json:"display_name"`
}

type assetList struct {
	Assets []assetSummary `json:"assets"`
}

func (t *tools) listAssets(context.Context, *sdk.CallToolRequest, empty) (*sdk.CallToolResult, assetList, error) {
	var out assetList
	for _, asset := range t.opts.Sources.Config.Assets() {
		id, _ := asset.Metadata["asset_id"].(string)
		name, _ := asset.Metadata["display_name"].(string)
		hierarchy := map[string]string{}
		if h, ok := asset.Metadata["hierarchy"].(map[string]any); ok {
			for k, v := range h {
				hierarchy[k], _ = v.(string)
			}
		}
		out.Assets = append(out.Assets, assetSummary{Asset: id, Hierarchy: hierarchy, Name: name})
	}
	sort.Slice(out.Assets, func(i, j int) bool { return out.Assets[i].Asset < out.Assets[j].Asset })
	return nil, out, nil
}

type assetContext struct {
	Asset        string         `json:"asset"`
	Revision     any            `json:"metadata_revision"`
	Hierarchy    map[string]any `json:"hierarchy"`
	Measurements map[string]any `json:"measurements"`
}

func (t *tools) assetContext(_ context.Context, _ *sdk.CallToolRequest, in assetRef) (*sdk.CallToolResult, assetContext, error) {
	asset, err := t.opts.Sources.Config.ResolveAsset(in.Asset, nil)
	if err != nil {
		return nil, assetContext{}, err
	}
	m := asset.Metadata
	h, _ := m["hierarchy"].(map[string]any)
	ms, _ := m["measurements"].(map[string]any)
	return nil, assetContext{Asset: in.Asset, Revision: m["metadata_revision"], Hierarchy: h, Measurements: ms}, nil
}

type observation struct {
	Asset       string          `json:"asset"`
	Observation json.RawMessage `json:"observation,omitempty"`
	Available   bool            `json:"available"`
}

func (t *tools) latestTelemetry(_ context.Context, _ *sdk.CallToolRequest, in assetRef) (*sdk.CallToolResult, observation, error) {
	if _, err := t.opts.Sources.Config.ResolveAsset(in.Asset, nil); err != nil {
		return nil, observation{}, err
	}
	raw, ok := t.opts.Sources.Telemetry.Get(in.Asset, "telemetry")
	return nil, observation{Asset: in.Asset, Observation: raw, Available: ok}, nil
}

func (t *tools) producerStatus(_ context.Context, _ *sdk.CallToolRequest, in assetRef) (*sdk.CallToolResult, observation, error) {
	if _, err := t.opts.Sources.Config.ResolveAsset(in.Asset, nil); err != nil {
		return nil, observation{}, err
	}
	raw, ok := t.opts.Sources.Telemetry.Get(in.Asset, "status")
	return nil, observation{Asset: in.Asset, Observation: raw, Available: ok}, nil
}

type historyIn struct {
	Asset       string `json:"asset"`
	Measurement string `json:"measurement"`
	StartUnix   int64  `json:"start_unix" jsonschema:"window start, Unix seconds"`
	EndUnix     int64  `json:"end_unix" jsonschema:"window end, Unix seconds"`
	StepSeconds int64  `json:"step_seconds" jsonschema:"resolution, >= 1"`
}

type historyOut struct {
	Result map[string]any `json:"result"`
}

func (t *tools) history(ctx context.Context, _ *sdk.CallToolRequest, in historyIn) (*sdk.CallToolResult, historyOut, error) {
	asset, err := t.opts.Sources.Config.ResolveAsset(in.Asset, nil)
	if err != nil {
		return nil, historyOut{}, err
	}
	definition, declared := asset.Metadata["measurements"].(map[string]any)[in.Measurement].(map[string]any)
	if !declared {
		return nil, historyOut{}, fmt.Errorf("measurement %q is not declared for %s", in.Measurement, in.Asset)
	}
	unit, _ := definition["unit"].(string)
	q := HistoryQuery{Asset: in.Asset, Measurement: in.Measurement, Unit: unit, Revision: fmt.Sprint(asset.Metadata["metadata_revision"]),
		Start: time.Unix(in.StartUnix, 0), End: time.Unix(in.EndUnix, 0), Step: time.Duration(in.StepSeconds) * time.Second}
	// The bounds are enforced here, at the tool boundary, so every history
	// source is covered. The validated parameters are passed on, so the bounds
	// are computed once with one clock.
	values, err := q.Values(t.opts.Now())
	if err != nil {
		return nil, historyOut{}, err
	}
	raw, err := t.opts.Sources.History(ctx, values)
	if err != nil {
		return nil, historyOut{}, err
	}
	var result map[string]any
	if err := json.Unmarshal(raw, &result); err != nil {
		return nil, historyOut{}, fmt.Errorf("history response: %w", err)
	}
	return nil, historyOut{Result: result}, nil
}

type healthOut struct {
	Services map[string]any `json:"services"`
}

func (t *tools) edgeHealth(ctx context.Context, _ *sdk.CallToolRequest, _ empty) (*sdk.CallToolResult, healthOut, error) {
	services, err := t.opts.Sources.Health(ctx)
	return nil, healthOut{Services: services}, err
}

type proposeOut struct {
	CommandID string         `json:"command_id"`
	Autonomy  string         `json:"autonomy_level"`
	Receipt   map[string]any `json:"receipt"`
}

func (t *tools) propose(ctx context.Context, _ *sdk.CallToolRequest, in ProposalRequest) (*sdk.CallToolResult, proposeOut, error) {
	proposal, err := t.opts.Proposer.Build(in)
	if err != nil {
		return nil, proposeOut{}, err
	}
	receipt, err := t.opts.Submit(ctx, proposal)
	if err != nil {
		return nil, proposeOut{}, err
	}
	return nil, proposeOut{CommandID: proposal.CommandID, Autonomy: proposal.Autonomy, Receipt: receipt}, nil
}

// LatestCache keeps the newest contract-valid telemetry and status payload per
// asset, fed by read-only broker subscriptions.
type LatestCache struct {
	mu        sync.Mutex
	values    map[string]json.RawMessage
	validator *contracts.Validator
	registry  *contracts.Registry
}

func NewLatestCache(validator *contracts.Validator, registry *contracts.Registry) *LatestCache {
	return &LatestCache{values: map[string]json.RawMessage{}, validator: validator, registry: registry}
}

// Observe stores a payload received on an asset's telemetry or status topic
// after validating it and binding its asset to the topic.
func (c *LatestCache) Observe(topic string, raw []byte, kind string) error {
	payload, err := c.validator.Validate(kind, raw)
	if err != nil {
		return err
	}
	asset, _ := payload["asset_id"].(string)
	if err := c.registry.ValidateTopic(topic, asset); err != nil {
		return err
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	c.values[asset+"\x00"+kind] = append(json.RawMessage(nil), raw...)
	return nil
}

func (c *LatestCache) Get(asset, kind string) (json.RawMessage, bool) {
	c.mu.Lock()
	defer c.mu.Unlock()
	raw, ok := c.values[asset+"\x00"+kind]
	return raw, ok
}

// HistoryClient queries the guarded historian with already-bounded,
// server-built query parameters.
func HistoryClient(client *http.Client, base string) func(context.Context, url.Values) (json.RawMessage, error) {
	return func(ctx context.Context, values url.Values) (json.RawMessage, error) {
		req, err := http.NewRequestWithContext(ctx, http.MethodGet, base+"/api/v1/query_range?"+values.Encode(), nil)
		if err != nil {
			return nil, err
		}
		resp, err := client.Do(req)
		if err != nil {
			return nil, err
		}
		defer resp.Body.Close()
		body, err := io.ReadAll(io.LimitReader(resp.Body, 4<<20))
		if err != nil {
			return nil, err
		}
		if resp.StatusCode != http.StatusOK {
			return nil, fmt.Errorf("history query: HTTP %d", resp.StatusCode)
		}
		return body, nil
	}
}
