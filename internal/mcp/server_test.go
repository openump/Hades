package mcp

import (
	"context"
	"encoding/json"
	"net/url"
	"os"
	"strings"
	"testing"
	"time"

	sdk "github.com/modelcontextprotocol/go-sdk/mcp"
	"github.com/openump/Hades/internal/contracts"
	"github.com/openump/Hades/internal/sample"
)

func connect(t *testing.T, server *sdk.Server) *sdk.ClientSession {
	t.Helper()
	serverT, clientT := sdk.NewInMemoryTransports()
	ctx := context.Background()
	if _, err := server.Connect(ctx, serverT, nil); err != nil {
		t.Fatal(err)
	}
	session, err := sdk.NewClient(&sdk.Implementation{Name: "test-agent", Version: "0"}, nil).Connect(ctx, clientT, nil)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { session.Close() })
	return session
}

func testOptions(t *testing.T, limiter *Limiter, submitted *[]*Proposal) Options {
	t.Helper()
	cfg, err := sample.LoadContext("../..", "../../examples/site/metadata.json")
	if err != nil {
		t.Fatal(err)
	}
	v, _ := contracts.New(os.DirFS("../../schemas"))
	p := testProposer(t, policyWith(t, true, "L2"))
	return Options{
		Sources: Sources{
			Config:    cfg,
			Telemetry: NewLatestCache(v, cfg.Registry),
			History: func(_ context.Context, _ url.Values) (json.RawMessage, error) {
				return json.RawMessage(`{"status":"success","data":{"resultType":"matrix","result":[]}}`), nil
			},
			Health: func(context.Context) (map[string]any, error) {
				return map[string]any{"hades-infer": map[string]any{"ready": true}}, nil
			},
		},
		Limiter:  limiter,
		Proposer: p,
		Submit: func(_ context.Context, proposal *Proposal) (map[string]any, error) {
			*submitted = append(*submitted, proposal)
			return map[string]any{"state": "AWAITING_OPERATOR_CONFIRMATION", "command_id": proposal.CommandID}, nil
		},
		Caller: "agent", Now: func() time.Time { return time.Unix(1_800_000_000, 0) }, ServerVer: "test",
	}
}

func toolNames(t *testing.T, s *sdk.ClientSession) map[string]bool {
	t.Helper()
	list, err := s.ListTools(context.Background(), nil)
	if err != nil {
		t.Fatal(err)
	}
	names := map[string]bool{}
	for _, tool := range list.Tools {
		names[tool.Name] = true
	}
	return names
}

func call(s *sdk.ClientSession, name string, args any) (*sdk.CallToolResult, error) {
	return s.CallTool(context.Background(), &sdk.CallToolParams{Name: name, Arguments: args})
}

// TestReadOnlyCallerCannotListOrCallProposeCommand is the role boundary: the
// read-only server never registers propose_command.
func TestReadOnlyCallerCannotListOrCallProposeCommand(t *testing.T) {
	var submitted []*Proposal
	readonly := connect(t, NewServer(RoleReadOnly, testOptions(t, NewLimiter(100, 100), &submitted)))
	names := toolNames(t, readonly)
	if names["propose_command"] {
		t.Fatal("a read-only caller must not even see propose_command")
	}
	for _, want := range []string{"list_assets", "get_asset_context", "get_latest_telemetry", "get_producer_status", "query_metric_history", "get_edge_health"} {
		if !names[want] {
			t.Fatalf("read-only server is missing %s", want)
		}
	}
	res, err := call(readonly, "propose_command", map[string]any{"asset": "urn:openump:acme:berlin:press-04", "parameter": "pressure_setpoint", "value": 5, "dry_run": false})
	if err == nil && (res == nil || !res.IsError) {
		t.Fatal("a read-only caller must be denied propose_command")
	}
	if len(submitted) != 0 {
		t.Fatal("nothing may reach the actuator from a read-only caller")
	}

	proposer := connect(t, NewServer(RoleProposer, testOptions(t, NewLimiter(100, 100), &submitted)))
	if !toolNames(t, proposer)["propose_command"] {
		t.Fatal("a proposer must see propose_command")
	}
	res, err = call(proposer, "propose_command", map[string]any{"asset": "urn:openump:acme:berlin:press-04", "parameter": "pressure_setpoint", "value": 5, "dry_run": false})
	if err != nil || res.IsError || len(submitted) != 1 || submitted[0].Autonomy != "L1" {
		t.Fatal("a proposer's valid proposal must reach the actuator as L1", res, err, submitted)
	}
	// An out-of-envelope proposal is refused before anything is submitted.
	res, err = call(proposer, "propose_command", map[string]any{"asset": "urn:openump:acme:berlin:press-04", "parameter": "pressure_setpoint", "value": 99, "dry_run": false})
	if (err == nil && !res.IsError) || len(submitted) != 1 {
		t.Fatal("an out-of-envelope proposal must be refused offline", res, err)
	}
}

func TestReadToolsReturnCatalogDataAndRefuseUnbounded(t *testing.T) {
	var submitted []*Proposal
	s := connect(t, NewServer(RoleReadOnly, testOptions(t, NewLimiter(100, 100), &submitted)))
	res, err := call(s, "list_assets", map[string]any{})
	if err != nil || res.IsError || !strings.Contains(structured(t, res), "urn:openump:acme:berlin:press-04") {
		t.Fatal(res, err)
	}
	res, err = call(s, "get_asset_context", map[string]any{"asset": "urn:openump:acme:berlin:press-04"})
	if err != nil || res.IsError || !strings.Contains(structured(t, res), "motor_temperature") {
		t.Fatal(res, err)
	}
	if res, err := call(s, "get_asset_context", map[string]any{"asset": "urn:openump:acme:berlin:press-99"}); err == nil && !res.IsError {
		t.Fatal("an unknown asset must be refused")
	}
	end := int64(1_800_000_000)
	ok := map[string]any{"asset": "urn:openump:acme:berlin:press-04", "measurement": "motor_temperature", "start_unix": end - 3600, "end_unix": end, "step_seconds": 60}
	if res, err := call(s, "query_metric_history", ok); err != nil || res.IsError {
		t.Fatal("a bounded history query must succeed", res, err)
	}
	for name, args := range map[string]map[string]any{
		"undeclared measurement": {"asset": "urn:openump:acme:berlin:press-04", "measurement": "secret", "start_unix": end - 3600, "end_unix": end, "step_seconds": 60},
		"unbounded window":       {"asset": "urn:openump:acme:berlin:press-04", "measurement": "motor_temperature", "start_unix": end - 30*86400, "end_unix": end, "step_seconds": 3600},
	} {
		if res, err := call(s, "query_metric_history", args); err == nil && !res.IsError {
			t.Fatalf("%s must be refused", name)
		}
	}
}

func TestToolCallsAreRateLimited(t *testing.T) {
	var submitted []*Proposal
	s := connect(t, NewServer(RoleReadOnly, testOptions(t, NewLimiter(0.001, 2), &submitted)))
	for i := 0; i < 2; i++ {
		if res, err := call(s, "list_assets", map[string]any{}); err != nil || res.IsError {
			t.Fatal("within burst", res, err)
		}
	}
	res, err := call(s, "list_assets", map[string]any{})
	if err == nil && !res.IsError {
		t.Fatal("calls beyond the token bucket must be refused")
	}
}

func structured(t *testing.T, res *sdk.CallToolResult) string {
	t.Helper()
	raw, _ := json.Marshal(res.StructuredContent)
	return string(raw)
}
