package sample

import (
	"context"
	"encoding/json"
	"fmt"
	"github.com/eclipse/paho.golang/autopaho"
	"github.com/eclipse/paho.golang/paho"
	"net/url"
	"os"
	"sync"
	"testing"
	"time"
)

func TestRecoveryBlockedBrokerConsumer(t *testing.T) {
	address := os.Getenv("HADES_RECOVERY_BLOCKED_BROKER")
	if address == "" {
		t.Skip("isolated two-record broker required")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	u, err := url.Parse(address)
	if err != nil {
		t.Fatal(err)
	}
	topic := "hades/recovery/" + UUID()
	clientID := "blocked-" + UUID()
	var mu sync.Mutex
	ids := []string{}
	connect := func(id string, consumer, manual bool) (*autopaho.ConnectionManager, context.CancelFunc) {
		owner, stop := context.WithCancel(ctx)
		ready := make(chan error, 1)
		options := autopaho.ClientConfig{ServerUrls: []*url.URL{u}, KeepAlive: 5, SessionExpiryInterval: 60, ConnectTimeout: time.Second, ClientConfig: paho.ClientConfig{ClientID: id, PacketTimeout: 2 * time.Second, EnableManualAcknowledgment: manual}}
		options.ConnectPacketBuilder = func(p *paho.Connect, _ *url.URL) (*paho.Connect, error) {
			if p.Properties == nil {
				p.Properties = &paho.ConnectProperties{}
			}
			maximum := uint16(1)
			p.Properties.ReceiveMaximum = &maximum
			return p, nil
		}
		options.OnConnectionUp = func(m *autopaho.ConnectionManager, _ *paho.Connack) {
			go func() {
				var err error
				if consumer {
					ack, e := m.Subscribe(owner, &paho.Subscribe{Subscriptions: []paho.SubscribeOptions{{Topic: topic, QoS: 1}}})
					err = e
					if e == nil && ack.Reasons[0] > 1 {
						err = fmt.Errorf("subscribe refused")
					}
				}
				ready <- err
			}()
		}
		if consumer {
			options.OnPublishReceived = []func(paho.PublishReceived) (bool, error){func(e paho.PublishReceived) (bool, error) {
				mu.Lock()
				ids = append(ids, string(e.Packet.Payload))
				mu.Unlock()
				return true, nil
			}}
		}
		m, err := autopaho.NewConnection(owner, options)
		if err != nil {
			stop()
			t.Fatal(err)
		}
		select {
		case err = <-ready:
			if err != nil {
				stop()
				t.Fatal(err)
			}
		case <-ctx.Done():
			stop()
			t.Fatal(ctx.Err())
		}
		return m, stop
	}
	sub, subcancel := connect(clientID, true, true)
	pub, pubcancel := connect("producer-"+UUID(), false, false)
	defer pubcancel()
	defer pub.Disconnect(ctx)
	publish := func(id string) {
		response, err := pub.Publish(ctx, &paho.Publish{Topic: topic, QoS: 1, Payload: []byte(id)})
		if err != nil || response.ReasonCode >= 128 {
			t.Fatal(err, response)
		}
	}
	publish("blocked-0")
	deadline := time.Now().Add(3 * time.Second)
	for {
		mu.Lock()
		n := len(ids)
		mu.Unlock()
		if n == 1 {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("first unacknowledged read missing")
		}
		time.Sleep(10 * time.Millisecond)
	}
	sub.Disconnect(ctx)
	subcancel()
	<-sub.Done()
	produced := []string{"blocked-0"}
	for i := 1; i <= 10; i++ {
		id := fmt.Sprintf("blocked-%d", i)
		produced = append(produced, id)
		publish(id)
	}
	marker := os.Getenv("HADES_RECOVERY_CRASH_MARKER")
	if marker != "" {
		ledger, _ := json.Marshal(produced)
		if err := os.WriteFile(marker, ledger, 0600); err != nil {
			t.Fatal(err)
		}
		deadline := time.Now().Add(10 * time.Second)
		for {
			if _, err := os.Stat(marker + ".restarted"); err == nil {
				break
			}
			if time.Now().After(deadline) {
				t.Fatal("isolated broker restart timeout")
			}
			time.Sleep(10 * time.Millisecond)
		}
	}
	mu.Lock()
	ids = nil
	mu.Unlock()
	resumed, resumecancel := connect(clientID, true, false)
	defer resumecancel()
	defer resumed.Disconnect(ctx)
	time.Sleep(time.Second)
	mu.Lock()
	recovered := append([]string{}, ids...)
	mu.Unlock()
	if (len(recovered) == 0 && marker == "") || len(recovered) > 3 {
		t.Fatal("queue limit not observable", recovered)
	}
	seen := map[string]bool{}
	for _, id := range recovered {
		seen[id] = true
	}
	lost := []string{}
	for _, id := range produced {
		if !seen[id] {
			lost = append(lost, id)
		}
	}
	if len(lost) == 0 {
		t.Fatal("overflow not exercised")
	}
	scenario := "broker-blocked-consumer-overflow"
	if marker != "" {
		scenario = "broker-sigkill-before-autosave"
	}
	report, _ := json.Marshal(map[string]any{"scenario": scenario, "produced": produced, "recovered": recovered, "lost": lost, "queue_limit": 2, "unacked_receive_maximum": 1})
	t.Log("RECOVERY " + string(report))
}
