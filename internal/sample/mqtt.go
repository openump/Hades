package sample

import (
	"context"
	"fmt"
	"log"
	"net/url"
	"sync"
	"time"

	"github.com/eclipse/paho.golang/autopaho"
	"github.com/eclipse/paho.golang/paho"
	"github.com/openump/Hades/internal/contracts"
)

type Received struct {
	Topic               string
	Payload             []byte
	Retained, Duplicate bool
}
type Client struct {
	Manager  *autopaho.ConnectionManager
	Messages chan Received
	Ready    chan error
	Errors   chan error
	BootID   string
	mu       sync.Mutex
	session  string
	cancel   context.CancelFunc
}

func (c *Client) Session() string { c.mu.Lock(); defer c.mu.Unlock(); return c.session }
func LocalBroker(address string) (*url.URL, error) {
	u, err := url.Parse(address)
	if err != nil {
		return nil, err
	}
	if u.Scheme != "mqtt" || u.User != nil || u.Path != "" || u.RawQuery != "" || u.Fragment != "" || (u.Hostname() != "127.0.0.1" && u.Hostname() != "localhost" && u.Hostname() != "::1") {
		return nil, fmt.Errorf("development examples require mqtt://localhost:<port> without credentials")
	}
	return u, nil
}
func Connect(ctx context.Context, address string, cfg *Config, producer bool) (*Client, error) {
	u, err := LocalBroker(address)
	if err != nil {
		return nil, err
	}
	owner, cancel := context.WithCancel(context.Background())
	c := &Client{Messages: make(chan Received, 64), Ready: make(chan error, 1), Errors: make(chan error, 1), BootID: UUID(), cancel: cancel}
	notify := func(err error) {
		select {
		case c.Ready <- err:
		default:
		}
	}
	options := autopaho.ClientConfig{ServerUrls: []*url.URL{u}, KeepAlive: 5, CleanStartOnInitialConnection: true, SessionExpiryInterval: 60, ConnectTimeout: 5 * time.Second, ReconnectBackoff: func(attempt int) time.Duration { return time.Duration(1<<min(attempt, 3)) * 100 * time.Millisecond }, ClientConfig: paho.ClientConfig{ClientID: "hades-sample-" + c.BootID, PacketTimeout: 5 * time.Second}}
	options.OnConnectError = func(err error) { log.Printf("local MQTT connection failed: %v", err) }
	options.ConnectPacketBuilder = func(packet *paho.Connect, _ *url.URL) (*paho.Connect, error) {
		c.mu.Lock()
		c.session = UUID()
		session := c.session
		c.mu.Unlock()
		if packet.Properties == nil {
			packet.Properties = &paho.ConnectProperties{}
		}
		maxPacket := uint32(70000)
		packet.Properties.MaximumPacketSize = &maxPacket
		if producer {
			raw, err := cfg.Status("last_will", c.BootID, session, nil, false)
			if err != nil {
				return nil, err
			}
			expiry := cfg.Expiry()
			delay := uint32(0)
			packet.WillMessage = &paho.WillMessage{Topic: cfg.Prefix + "status", QoS: 1, Retain: true, Payload: raw}
			packet.WillProperties = &paho.WillProperties{MessageExpiry: &expiry, WillDelayInterval: &delay, ContentType: "application/json"}
		}
		return packet, nil
	}
	options.OnConnectionUp = func(manager *autopaho.ConnectionManager, _ *paho.Connack) {
		if producer {
			go func() {
				publishctx, done := context.WithTimeout(owner, 5*time.Second)
				defer done()
				status, err := cfg.Status("heartbeat", c.BootID, c.Session(), nil, false)
				if err == nil {
					for _, message := range []struct {
						topic  string
						raw    []byte
						expiry uint32
					}{{cfg.Prefix + "metadata", cfg.MetadataBytes, 0}, {cfg.Prefix + "status", status, cfg.Expiry()}} {
						properties := &paho.PublishProperties{ContentType: "application/json"}
						if message.expiry > 0 {
							properties.MessageExpiry = &message.expiry
						}
						var response *paho.PublishResponse
						response, err = manager.Publish(publishctx, &paho.Publish{Topic: message.topic, QoS: 1, Retain: true, Payload: message.raw, Properties: properties})
						if err == nil && (response == nil || response.ReasonCode >= 128) {
							err = fmt.Errorf("reconnect publication rejected")
						}
						if err != nil {
							break
						}
					}
				}
				notify(err)
			}()
			return
		}
		go func() {
			subctx, done := context.WithTimeout(owner, 5*time.Second)
			defer done()
			ack, err := manager.Subscribe(subctx, &paho.Subscribe{Subscriptions: []paho.SubscribeOptions{{Topic: cfg.Prefix + "+", QoS: 1, RetainAsPublished: true}}})
			if err == nil && (len(ack.Reasons) != 1 || ack.Reasons[0] > 1) {
				err = fmt.Errorf("subscription denied or unexpected QoS")
			}
			notify(err)
		}()
	}
	options.OnPublishReceived = []func(paho.PublishReceived) (bool, error){func(message paho.PublishReceived) (bool, error) {
		if producer {
			return false, nil
		}
		p := message.Packet
		if len(p.Payload) > contracts.MaxPayloadBytes {
			err := fmt.Errorf("oversized incoming payload")
			select {
			case c.Errors <- err:
			default:
			}
			return true, err
		}
		event := Received{p.Topic, append([]byte(nil), p.Payload...), p.Retain, p.Duplicate()}
		select {
		case c.Messages <- event:
		default:
			select {
			case c.Errors <- fmt.Errorf("bounded consumer queue overflow"):
			default:
			}
		}
		return true, nil
	}}
	manager, err := autopaho.NewConnection(owner, options)
	if err != nil {
		cancel()
		return nil, err
	}
	c.Manager = manager
	select {
	case <-ctx.Done():
		c.Close()
		return nil, ctx.Err()
	case err := <-c.Ready:
		if err != nil {
			c.Close()
			return nil, err
		}
	}
	return c, nil
}
func (c *Client) Publish(ctx context.Context, topic string, raw []byte, retained bool, expiry uint32) error {
	attempt, done := context.WithTimeout(ctx, 5*time.Second)
	defer done()
	if err := c.Manager.AwaitConnection(attempt); err != nil {
		return err
	}
	properties := &paho.PublishProperties{ContentType: "application/json"}
	if expiry > 0 {
		properties.MessageExpiry = &expiry
	}
	response, err := c.Manager.Publish(attempt, &paho.Publish{Topic: topic, QoS: 1, Retain: retained, Payload: raw, Properties: properties})
	if err == nil && (response == nil || response.ReasonCode >= 128) {
		return fmt.Errorf("publication rejected")
	}
	return err
}
func (c *Client) Close() {
	ctx, done := context.WithTimeout(context.Background(), 3*time.Second)
	defer done()
	if c.Manager != nil {
		_ = c.Manager.Disconnect(ctx)
	}
	c.cancel()
}
