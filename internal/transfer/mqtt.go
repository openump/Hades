package transfer

import (
	"context"
	"crypto/sha256"
	"errors"
	"fmt"
	"github.com/openump/Hades/internal/storage"
	"log"
	"net/url"
	"sync"
	"time"

	"github.com/eclipse/paho.golang/autopaho"
	"github.com/eclipse/paho.golang/paho"
	"github.com/openump/Hades/internal/delivery"
	"github.com/openump/Hades/internal/transport"
)

type Subscription struct {
	once    sync.Once
	manager *autopaho.ConnectionManager
	Errors  chan error
	cancel  context.CancelFunc
}

// Subscribe establishes stable MQTT ownership. Manual PUBACK follows synced
// intake or a synced diagnostic. Failure leaves the packet unacknowledged.
func (a *Queue) Subscribe(ctx context.Context, address string, diagnostic func(string, []byte, error) error) (*Subscription, error) {
	u, err := transport.BrokerURL(address, a.cfg.TLS)
	if err != nil {
		return nil, err
	}
	owner, cancel := context.WithCancel(ctx)
	s := &Subscription{Errors: make(chan error, 1), cancel: cancel}
	ready := make(chan error, 1)
	notify := func(err error) {
		select {
		case ready <- err:
		default:
		}
	}
	fail := func(err error) {
		select {
		case s.Errors <- err:
		default:
		}
	}
	topic := a.cfg.Prefix + "telemetry"
	topics := []string{topic, a.cfg.Prefix + "metadata", a.cfg.Prefix + "status"}
	hash := sha256.Sum256([]byte(topic))
	id := fmt.Sprintf("hades-transfer-%x", hash[:12])
	options := autopaho.ClientConfig{TlsCfg: a.cfg.TLS, ServerUrls: []*url.URL{u}, KeepAlive: 10, SessionExpiryInterval: 86400, CleanStartOnInitialConnection: false, ConnectTimeout: 5 * time.Second, ReconnectBackoff: func(attempt int) time.Duration {
		return delivery.Delay(attempt)
	}, ClientConfig: paho.ClientConfig{ClientID: id, PacketTimeout: 5 * time.Second, EnableManualAcknowledgment: true, SendAcksInterval: 10 * time.Millisecond}}
	options.ConnectPacketBuilder = func(packet *paho.Connect, _ *url.URL) (*paho.Connect, error) {
		if packet.Properties == nil {
			packet.Properties = &paho.ConnectProperties{}
		}
		maxPacket := uint32(70000)
		receive := uint16(20)
		packet.Properties.MaximumPacketSize = &maxPacket
		packet.Properties.ReceiveMaximum = &receive
		return packet, nil
	}
	options.OnConnectError = func(err error) { log.Printf("transfer MQTT connection: %v", err) }
	options.OnConnectionUp = func(m *autopaho.ConnectionManager, _ *paho.Connack) {
		go func() {
			subctx, done := context.WithTimeout(owner, 5*time.Second)
			defer done()
			subscriptions := make([]paho.SubscribeOptions, 0, 3)
			for _, topic := range topics {
				subscriptions = append(subscriptions, paho.SubscribeOptions{Topic: topic, QoS: 1, RetainAsPublished: true})
			}
			ack, err := m.Subscribe(subctx, &paho.Subscribe{Subscriptions: subscriptions})
			if err == nil && len(ack.Reasons) != 3 {
				err = fmt.Errorf("transfer subscription rejected")
			}
			if err == nil {
				for _, reason := range ack.Reasons {
					if reason > 1 {
						err = fmt.Errorf("transfer subscription rejected")
					}
				}
			}
			notify(err)
		}()
	}
	options.OnPublishReceived = []func(paho.PublishReceived) (bool, error){func(event paho.PublishReceived) (bool, error) {
		packet := event.Packet
		var err error
		if packet.Retain && packet.Topic == topic {
			err = fmt.Errorf("retained telemetry rejected")
		} else {
			err = a.Intake(packet.Topic, packet.Payload)
		}
		if err != nil {
			// Reject-newest is deliberately lossy for transfer only. Do not withhold local
			// delivery behind a full cloud queue. Digest-only logs avoid payload disclosure.
			if errors.Is(err, storage.ErrFull) {
				log.Printf("transfer_drop reason=capacity identity_digest=%s", Digest(packet.Topic, packet.Payload))
				err = nil
			} else if err == ErrConflict || !isStorageError(err) {
				if qerr := diagnostic(packet.Topic, packet.Payload, err); qerr == nil {
					err = nil
				} else {
					err = qerr
				}
			}
		}
		if err != nil {
			fail(err)
			return true, err
		}
		if err = event.Client.Ack(packet); err != nil {
			fail(err)
		}
		return true, err
	}}
	manager, err := autopaho.NewConnection(owner, options)
	if err != nil {
		cancel()
		return nil, err
	}
	s.manager = manager
	select {
	case <-time.After(15 * time.Second):
		s.Close()
		return nil, fmt.Errorf("MQTT subscribe timeout")
	case <-ctx.Done():
		s.Close()
		return nil, ctx.Err()
	case err = <-ready:
		if err != nil {
			s.Close()
			return nil, err
		}
		return s, nil
	case err = <-s.Errors:
		s.Close()
		return nil, err
	}
}
func (s *Subscription) Close() {
	s.once.Do(func() {
		ctx, done := context.WithTimeout(context.Background(), 3*time.Second)
		defer done()
		s.manager.Disconnect(ctx)
		s.cancel()
		select {
		case <-s.manager.Done():
		case <-ctx.Done():
		}
	})
}
