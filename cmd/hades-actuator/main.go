// hades-actuator is a separate signed write-control service; default mode is L0.
package main

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"github.com/openump/Hades/internal/actuator"
	"github.com/openump/Hades/internal/sample"
	"github.com/openump/Hades/internal/transport"
	"os"
	"os/signal"
	"path/filepath"
	"syscall"
	"time"
)

func main() {
	if err := run(); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}
func run() error {
	action := flag.String("action", "run", "run, health, kill or confirm")
	state := flag.String("state", "runtime/actuator", "private state directory")
	root := flag.String("root", ".", "contract root")
	metadata := flag.String("config", "examples/site/metadata.json", "reviewed metadata snapshot")
	policy := flag.String("policy", "", "reviewed actuation policy")
	keys := flag.String("keys", "", "local actor public keyring")
	key := flag.String("receipt-key", "", "private base64 Ed25519 receipt key")
	keyID := flag.String("receipt-key-id", "actuator-key", "receipt signing identity")
	bindings := flag.String("bindings", "", "reviewed loopback mailbox bindings JSON array")
	broker := flag.String("broker", "tls://127.0.0.1:8883", "mTLS broker")
	enable := flag.Bool("enable-write-pilot", false, "explicitly enable reviewed loopback mailbox writes; default shadow")
	id := flag.String("command-id", "", "exact pending command to confirm")
	digest := flag.String("command-sha256", "", "exact signed-command digest to confirm")
	credentials := transport.Flags()
	flag.Parse()
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	socket := filepath.Join(*state, "control.sock")
	if *action != "run" {
		raw, err := actuator.Control(ctx, socket, *action, *id, *digest)
		if err == nil {
			fmt.Println(string(raw))
		}
		return err
	}
	cfg, err := sample.LoadContext(*root, *metadata)
	if err != nil {
		return err
	}
	cfg.TLS, err = transport.Client(*credentials)
	if err != nil {
		return err
	}
	public, err := actuator.LoadPublicKeys(*keys)
	if err != nil {
		return err
	}
	private, err := actuator.LoadSigningKey(*key)
	if err != nil {
		return err
	}
	policyRaw, err := os.ReadFile(*policy)
	if err != nil {
		return err
	}
	var approved []actuator.Binding
	if *bindings != "" {
		raw, err := os.ReadFile(*bindings)
		if err != nil {
			return err
		}
		if err = json.Unmarshal(raw, &approved); err != nil {
			return err
		}
	}
	engine, err := actuator.Open(actuator.Options{Config: cfg, Policy: policyRaw, Keys: public, SigningKey: private, SigningKeyID: *keyID, Directory: *state, Bindings: approved, PLC: &actuator.ModbusMailbox{}})
	if err != nil {
		return err
	}
	defer engine.Close()
	if !*enable {
		engine.Stop()
	}
	errors := make(chan error, 3)
	go func() { errors <- engine.ServeControl(ctx, socket) }()
	go func() { errors <- engine.RunWatchdog(ctx) }()
	go func() { errors <- engine.RunMQTT(ctx, *broker) }()
	// Supervisor live.sock binds this health stream to the reviewed binary/config.
	ready := false
	ticker := time.NewTicker(time.Second)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			engine.Stop()
			return nil
		case err := <-errors:
			engine.Stop()
			return err
		case <-ticker.C:
			if !ready && engine.Stats()["mqtt_ready"] == true {
				if _, err := os.Stat(socket); err == nil {
					fmt.Println(`{"type":"actuator_ready"}`)
					ready = true
				}
			}
			if err := json.NewEncoder(os.Stdout).Encode(engine.Stats()); err != nil {
				return err
			}
		}
	}
}
