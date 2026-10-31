// hades-mcp serves a safety-gated Industrial Model Context Protocol endpoint.
// Read tools serve validated plant context without polling any PLC; the
// propose_command tool's only write path is a signed, offline-validated
// proposal to hades-actuator, capped at L1 (operator confirmation).
package main

import (
	"context"
	"crypto/ed25519"
	"encoding/json"
	"flag"
	"fmt"
	"log"
	"net"
	"net/http"
	"net/url"
	"os"
	"os/signal"
	"path/filepath"
	"strings"
	"sync"
	"syscall"
	"time"

	"github.com/eclipse/paho.golang/autopaho"
	"github.com/eclipse/paho.golang/paho"
	sdk "github.com/modelcontextprotocol/go-sdk/mcp"
	"github.com/openump/Hades/internal/actuator"
	"github.com/openump/Hades/internal/contracts"
	"github.com/openump/Hades/internal/infer"
	"github.com/openump/Hades/internal/mcp"
	"github.com/openump/Hades/internal/sample"
	"github.com/openump/Hades/internal/supervisor"
	"github.com/openump/Hades/internal/transport"
)

const version = "1.0.0"

func main() {
	if err := run(); err != nil {
		log.Fatal(err)
	}
}

func run() error {
	root := flag.String("root", ".", "contract root")
	config := flag.String("config", "examples/site/metadata.json", "reviewed metadata snapshot (comma-separated catalog)")
	broker := flag.String("broker", "tls://127.0.0.1:8883", "mTLS broker")
	history := flag.String("history", "", "optional hades-history-proxy base URL (https://...)")
	services := flag.String("services", "", "optional comma-separated supervisor service.json paths for edge health")
	listen := flag.String("listen", "", "optional loopback HTTPS listener for MCP over HTTP (e.g. 127.0.0.1:18090)")
	readonlyClients := flag.String("readonly-clients", "", "comma-separated client certificate CNs granted mcp-readonly over HTTP")
	proposerClients := flag.String("proposer-clients", "", "comma-separated client certificate CNs granted mcp-proposer over HTTP")
	stdioRole := flag.String("stdio-role", string(mcp.RoleReadOnly), "role for the stdio transport: mcp-readonly or mcp-proposer")
	policy := flag.String("policy", "", "reviewed actuation policy; required for propose_command")
	signingKey := flag.String("signing-key", "", "private base64 Ed25519 key of the hades-mcp origin")
	signingKeyID := flag.String("signing-key-id", "hades-mcp-key", "hades-mcp origin key id, allowlisted per asset by the site")
	actuatorKeys := flag.String("actuator-keys", "", "actuator public keyring used to verify receipts")
	proposalTTL := flag.Duration("proposal-ttl", 60*time.Second, "proposal lease; an L1 proposal must be confirmed by an operator within it (max 60s, the command schema limit)")
	rate := flag.Float64("rate", 2, "tool calls per second per caller")
	burst := flag.Int("burst", 10, "tool call burst per caller")
	credentials := transport.Flags()
	var server transport.Paths
	flag.StringVar(&server.CA, "listen-ca", "", "client CA for the HTTPS listener")
	flag.StringVar(&server.Cert, "listen-cert", "", "HTTPS listener certificate")
	flag.StringVar(&server.Key, "listen-key", "", "HTTPS listener key")
	flag.Parse()

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	cfg, err := sample.LoadContext(*root, *config)
	if err != nil {
		return err
	}
	cfg.TLS, err = transport.Client(*credentials)
	if err != nil {
		return err
	}
	if cfg.TLS == nil {
		return fmt.Errorf("hades-mcp requires mTLS broker credentials")
	}
	validator, err := contracts.New(os.DirFS(filepath.Join(*root, "schemas")))
	if err != nil {
		return err
	}
	cache := mcp.NewLatestCache(validator, cfg.Registry)
	prefixes := map[string]string{}
	var topics []paho.SubscribeOptions
	for _, asset := range cfg.Assets() {
		id, _ := asset.Metadata["asset_id"].(string)
		prefixes[id] = asset.Prefix
		// Read-only views: latest telemetry and status, never a PLC poll.
		topics = append(topics, paho.SubscribeOptions{Topic: asset.Prefix + "telemetry", QoS: 1}, paho.SubscribeOptions{Topic: asset.Prefix + "status", QoS: 1})
	}
	receipts := newReceiptBox()
	if *proposalTTL < time.Second || *proposalTTL > 60*time.Second {
		return fmt.Errorf("-proposal-ttl must be 1s..60s")
	}
	proposer, actuatorTrust, err := loadProposer(*policy, *signingKey, *signingKeyID, *actuatorKeys, *proposalTTL, validator, cfg, prefixes)
	if err != nil {
		return err
	}
	if proposer != nil {
		for _, prefix := range prefixes {
			topics = append(topics, paho.SubscribeOptions{Topic: prefix + "command_receipt/+", QoS: 1})
		}
	}
	manager, err := connectBroker(ctx, *broker, cfg, topics, cache, receipts)
	if err != nil {
		return err
	}
	defer manager.Disconnect(context.Background())

	var historyFn func(context.Context, url.Values) (json.RawMessage, error)
	if *history != "" {
		u, err := url.Parse(*history)
		if err != nil || u.Scheme != "https" {
			return fmt.Errorf("-history must be an https URL")
		}
		client := &http.Client{Timeout: 10 * time.Second, Transport: &http.Transport{TLSClientConfig: cfg.TLS, Proxy: nil}}
		historyFn = mcp.HistoryClient(client, strings.TrimSuffix(*history, "/"))
	} else {
		historyFn = func(context.Context, url.Values) (json.RawMessage, error) {
			return nil, fmt.Errorf("history is not configured on this edge node")
		}
	}
	sources := mcp.Sources{Config: cfg, Telemetry: cache, History: historyFn, Health: healthFrom(*services)}
	var submit mcp.Submitter
	if proposer != nil {
		submit = func(ctx context.Context, p *mcp.Proposal) (map[string]any, error) {
			return publishAndAwait(ctx, manager, receipts, p, validator, actuatorTrust)
		}
	}
	limiter := mcp.NewLimiter(*rate, *burst)
	build := func(role mcp.Role, caller string) *sdk.Server {
		opts := mcp.Options{Sources: sources, Limiter: limiter, Caller: caller, Now: time.Now, ServerVer: version}
		if role == mcp.RoleProposer {
			opts.Proposer, opts.Submit = proposer, submit
		}
		return mcp.NewServer(role, opts)
	}

	if *listen != "" {
		roles, err := clientRoles(*readonlyClients, *proposerClients, proposer != nil)
		if err != nil {
			return err
		}
		return serveHTTP(ctx, *listen, server, roles, build)
	}
	role := mcp.Role(*stdioRole)
	if role != mcp.RoleReadOnly && role != mcp.RoleProposer {
		return fmt.Errorf("-stdio-role must be mcp-readonly or mcp-proposer")
	}
	if role == mcp.RoleProposer && proposer == nil {
		return fmt.Errorf("mcp-proposer requires -policy, -signing-key and -actuator-keys")
	}
	fmt.Fprintln(os.Stderr, `{"type":"mcp_ready","transport":"stdio","role":"`+string(role)+`"}`)
	return build(role, "stdio").Run(ctx, &sdk.StdioTransport{})
}

func loadProposer(policyPath, keyPath, keyID, actuatorKeysPath string, ttl time.Duration, validator *contracts.Validator, cfg *sample.Config, prefixes map[string]string) (*mcp.Proposer, map[string]ed25519.PublicKey, error) {
	if policyPath == "" && keyPath == "" && actuatorKeysPath == "" {
		return nil, nil, nil // read-only deployment
	}
	if policyPath == "" || keyPath == "" || actuatorKeysPath == "" {
		return nil, nil, fmt.Errorf("propose_command needs -policy, -signing-key and -actuator-keys together")
	}
	policy, err := os.ReadFile(policyPath)
	if err != nil {
		return nil, nil, err
	}
	if _, err := validator.Validate("actuation-policy", policy); err != nil {
		return nil, nil, fmt.Errorf("actuation policy: %w", err)
	}
	key, err := actuator.LoadSigningKey(keyPath)
	if err != nil {
		return nil, nil, err
	}
	trust, err := actuator.LoadPublicKeys(actuatorKeysPath)
	if err != nil {
		return nil, nil, err
	}
	return &mcp.Proposer{
		Validator: validator, Registry: cfg.Registry, Policy: policy,
		ActorID: "hades-mcp", KeyID: keyID, Key: key, TTL: ttl,
		TopicFor: func(asset, parameter string) (string, error) {
			prefix, ok := prefixes[asset]
			if !ok {
				return "", fmt.Errorf("asset %s is not in the approved catalog", asset)
			}
			return prefix + "command/" + parameter, nil
		},
		Now:   func() time.Time { return time.Now().UTC() },
		NewID: infer.UUIDv7,
	}, trust, nil
}

func connectBroker(ctx context.Context, broker string, cfg *sample.Config, topics []paho.SubscribeOptions, cache *mcp.LatestCache, receipts *receiptBox) (*autopaho.ConnectionManager, error) {
	u, err := transport.BrokerURL(broker, cfg.TLS)
	if err != nil {
		return nil, err
	}
	ready := make(chan error, 1)
	conf := autopaho.ClientConfig{ServerUrls: []*url.URL{u}, TlsCfg: cfg.TLS, KeepAlive: 10, ConnectTimeout: 3 * time.Second, ClientConfig: paho.ClientConfig{ClientID: "hades-mcp-" + infer.UUIDv7(time.Now())[24:], PacketTimeout: 3 * time.Second}}
	conf.OnConnectionUp = func(m *autopaho.ConnectionManager, _ *paho.Connack) {
		go func() {
			c, done := context.WithTimeout(ctx, 3*time.Second)
			defer done()
			ack, err := m.Subscribe(c, &paho.Subscribe{Subscriptions: topics})
			if err == nil {
				for _, reason := range ack.Reasons {
					if reason > 1 {
						err = fmt.Errorf("hades-mcp subscription denied")
					}
				}
			}
			select {
			case ready <- err:
			default:
				// Startup already completed: a failed resubscribe after a reconnect
				// would leave the cache and receipts silently stale, so report it.
				if err != nil {
					log.Printf(`{"type":"mcp_degraded","reason":"resubscribe failed: %v"}`, err)
				}
			}
		}()
	}
	conf.OnPublishReceived = []func(paho.PublishReceived) (bool, error){func(e paho.PublishReceived) (bool, error) {
		p := e.Packet
		if len(p.Payload) > contracts.MaxPayloadBytes {
			return true, nil
		}
		switch {
		case strings.HasSuffix(p.Topic, "/telemetry"):
			_ = cache.Observe(p.Topic, p.Payload, "telemetry")
		case strings.HasSuffix(p.Topic, "/status"):
			_ = cache.Observe(p.Topic, p.Payload, "status")
		case strings.Contains(p.Topic, "/command_receipt/"):
			receipts.deliver(p.Payload)
		}
		return true, nil
	}}
	manager, err := autopaho.NewConnection(ctx, conf)
	if err != nil {
		return nil, err
	}
	select {
	case err := <-ready:
		if err != nil {
			return nil, err
		}
	case <-time.After(10 * time.Second):
		return nil, fmt.Errorf("broker subscription timed out")
	}
	return manager, nil
}

// receiptBox routes command receipts to the proposal waiting for them.
type receiptBox struct {
	mu      sync.Mutex
	waiters map[string]chan []byte
}

func newReceiptBox() *receiptBox { return &receiptBox{waiters: map[string]chan []byte{}} }

func (b *receiptBox) wait(commandID string) (chan []byte, func()) {
	ch := make(chan []byte, 4)
	b.mu.Lock()
	b.waiters[commandID] = ch
	b.mu.Unlock()
	return ch, func() {
		b.mu.Lock()
		delete(b.waiters, commandID)
		b.mu.Unlock()
	}
}

func (b *receiptBox) deliver(raw []byte) {
	var probe struct {
		CommandID string `json:"command_id"`
	}
	if json.Unmarshal(raw, &probe) != nil {
		return
	}
	b.mu.Lock()
	ch := b.waiters[probe.CommandID]
	b.mu.Unlock()
	if ch != nil {
		select {
		case ch <- append([]byte(nil), raw...):
		default:
		}
	}
}

// publishAndAwait sends a proposal to the actuator and returns its verified
// receipt. Untrusted or mismatched receipts are ignored, not returned.
func publishAndAwait(ctx context.Context, manager *autopaho.ConnectionManager, receipts *receiptBox, p *mcp.Proposal, validator *contracts.Validator, trust map[string]ed25519.PublicKey) (map[string]any, error) {
	ch, done := receipts.wait(p.CommandID)
	defer done()
	c, cancel := context.WithTimeout(ctx, 3*time.Second)
	ack, err := manager.Publish(c, &paho.Publish{Topic: p.Topic, Payload: p.Command, QoS: 1})
	cancel()
	if err != nil {
		return nil, fmt.Errorf("proposal not delivered: %w", err)
	}
	if ack != nil && ack.ReasonCode >= 0x80 {
		return nil, fmt.Errorf("broker refused the proposal (reason 0x%02x)", ack.ReasonCode)
	}
	deadline := time.NewTimer(10 * time.Second)
	defer deadline.Stop()
	for {
		select {
		case raw := <-ch:
			if receipt, err := mcp.VerifyReceipt(raw, p, validator, trust); err == nil {
				return receipt, nil
			}
		case <-deadline.C:
			return nil, fmt.Errorf("no verified receipt for %s; the outcome is unknown, not executed", p.CommandID)
		case <-ctx.Done():
			return nil, ctx.Err()
		}
	}
}

func healthFrom(paths string) func(context.Context) (map[string]any, error) {
	return func(ctx context.Context) (map[string]any, error) {
		out := map[string]any{}
		for _, path := range strings.Split(paths, ",") {
			if path = strings.TrimSpace(path); path == "" {
				continue
			}
			cfg, err := supervisor.Load(path)
			if err != nil {
				out[path] = map[string]any{"error": err.Error()}
				continue
			}
			status, err := supervisor.Probe(ctx, cfg)
			if err != nil {
				out[cfg.Revision] = map[string]any{"ready": false, "error": err.Error()}
				continue
			}
			out[cfg.Revision] = map[string]any{"ready": status.Ready, "started_at": status.Started, "last_output_at": status.LastOutput}
		}
		return out, nil
	}
}

// clientRoles maps verified client certificate CNs to roles. Unlisted clients
// get no server at all.
func clientRoles(readonly, proposer string, canPropose bool) (map[string]mcp.Role, error) {
	roles := map[string]mcp.Role{}
	for _, cn := range transport.ParseCSVIdentities(readonly) {
		roles[cn] = mcp.RoleReadOnly
	}
	for _, cn := range transport.ParseCSVIdentities(proposer) {
		if !canPropose {
			return nil, fmt.Errorf("-proposer-clients needs -policy, -signing-key and -actuator-keys")
		}
		if roles[cn] != "" {
			return nil, fmt.Errorf("client %q is listed as both read-only and proposer", cn)
		}
		roles[cn] = mcp.RoleProposer
	}
	if len(roles) == 0 {
		return nil, fmt.Errorf("HTTP transport needs -readonly-clients and/or -proposer-clients")
	}
	return roles, nil
}

func serveHTTP(ctx context.Context, listen string, paths transport.Paths, roles map[string]mcp.Role, build func(mcp.Role, string) *sdk.Server) error {
	host, _, err := net.SplitHostPort(listen)
	if ip := net.ParseIP(host); err != nil || ip == nil || !ip.IsLoopback() {
		return fmt.Errorf("-listen must bind a loopback IP")
	}
	tlsConfig, err := transport.Server(paths)
	if err != nil {
		return err
	}
	if tlsConfig == nil {
		return fmt.Errorf("HTTP transport requires -listen-ca, -listen-cert and -listen-key (mTLS)")
	}
	handler := newHTTPHandler(roles, build)
	srv := &http.Server{Addr: listen, Handler: handler, TLSConfig: tlsConfig, ReadHeaderTimeout: 5 * time.Second}
	errs := make(chan error, 1)
	go func() { errs <- srv.ListenAndServeTLS("", "") }()
	fmt.Fprintln(os.Stderr, `{"type":"mcp_ready","transport":"https","listen":"`+listen+`"}`)
	select {
	case <-ctx.Done():
		shutdown, cancel := context.WithTimeout(context.Background(), 3*time.Second)
		defer cancel()
		return srv.Shutdown(shutdown)
	case err := <-errs:
		return err
	}
}

// sessionOwners records which certificate identity created each MCP session.
type sessionOwners struct {
	mu   sync.Mutex
	byID map[string]string
}

// ownedBy reports whether session id belongs to cn. An unknown id is refused,
// so a client cannot use a session id this server never issued to it.
func (o *sessionOwners) ownedBy(id, cn string) bool {
	o.mu.Lock()
	defer o.mu.Unlock()
	owner, ok := o.byID[id]
	return ok && owner == cn
}

func (o *sessionOwners) record(id, cn string) {
	o.mu.Lock()
	defer o.mu.Unlock()
	if _, exists := o.byID[id]; !exists {
		o.byID[id] = cn
	}
}

// sessionRecorder binds a session id issued in a response to the caller.
type sessionRecorder struct {
	http.ResponseWriter
	cn     string
	owners *sessionOwners
	bound  bool
}

func (r *sessionRecorder) bind() {
	if !r.bound {
		r.bound = true
		if id := r.Header().Get("Mcp-Session-Id"); id != "" {
			r.owners.record(id, r.cn)
		}
	}
}

func (r *sessionRecorder) WriteHeader(code int) { r.bind(); r.ResponseWriter.WriteHeader(code) }
func (r *sessionRecorder) Write(b []byte) (int, error) {
	r.bind()
	return r.ResponseWriter.Write(b)
}

// Flush keeps server-sent events streaming through the recorder.
func (r *sessionRecorder) Flush() {
	r.bind()
	if f, ok := r.ResponseWriter.(http.Flusher); ok {
		f.Flush()
	}
}

// newHTTPHandler serves MCP over HTTP: one server per verified certificate
// identity, unlisted identities refused, and every session bound to its creator.
func newHTTPHandler(roles map[string]mcp.Role, build func(mcp.Role, string) *sdk.Server) http.Handler {
	// One server per caller identity (its role and rate-limit bucket), built once.
	var mu sync.Mutex
	servers := map[string]*sdk.Server{}
	mcpHandler := sdk.NewStreamableHTTPHandler(func(r *http.Request) *sdk.Server {
		cn := transport.Peer(r)
		role, ok := roles[cn]
		if !ok {
			return nil
		}
		mu.Lock()
		defer mu.Unlock()
		if servers[cn] == nil {
			servers[cn] = build(role, cn)
		}
		return servers[cn]
	}, nil)
	// Unverified or unlisted clients are refused before the MCP layer, and every
	// MCP session is bound to the certificate identity that created it. The SDK
	// routes a request with an existing Mcp-Session-Id straight to that session,
	// so without this binding a read-only client holding a proposer's session ID
	// would reach the proposer server.
	owners := &sessionOwners{byID: map[string]string{}}
	handler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		cn := transport.Peer(r)
		if _, ok := roles[cn]; !ok {
			http.Error(w, "forbidden", http.StatusForbidden)
			return
		}
		if id := r.Header.Get("Mcp-Session-Id"); id != "" && !owners.ownedBy(id, cn) {
			http.Error(w, "session belongs to another identity", http.StatusForbidden)
			return
		}
		mcpHandler.ServeHTTP(&sessionRecorder{ResponseWriter: w, cn: cn, owners: owners}, r)
	})
	return handler
}
