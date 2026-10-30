package opcuapilot

import (
	"context"
	"fmt"
	"sync"

	"github.com/awcullen/opcua/ua"
)

// Subscription keeps one OPC UA subscription with a MonitoredItem per signal on
// the reader's persistent encrypted session. A background loop keeps Publish
// requests outstanding, so the subscription stays alive (and keep-alives are
// acknowledged) even while the gateway skips polls; Next returns the latest
// value of every signal.
type Subscription struct {
	reader         *Reader
	subscriptionID uint32
	cancel         context.CancelFunc
	done           chan struct{}
	mu             sync.Mutex
	latest         []ua.DataValue
	seen           []bool
	err            error
	updated        chan struct{}
}

// Subscribe creates the subscription and monitored items and starts publishing.
// publishing is the requested publishing and sampling interval in milliseconds.
func (r *Reader) Subscribe(ctx context.Context, publishing float64) (*Subscription, error) {
	sigs := r.activeSignals()
	created, err := r.Client.CreateSubscription(ctx, &ua.CreateSubscriptionRequest{
		RequestedPublishingInterval: publishing,
		RequestedMaxKeepAliveCount:  10,
		RequestedLifetimeCount:      30,
		PublishingEnabled:           true,
	})
	if err != nil {
		return nil, err
	}
	items := make([]ua.MonitoredItemCreateRequest, 0, len(sigs))
	for index, signal := range sigs {
		items = append(items, ua.MonitoredItemCreateRequest{
			ItemToMonitor:  ua.ReadValueID{NodeID: ua.NodeIDString{NamespaceIndex: r.Namespace, ID: signal.Node}, AttributeID: ua.AttributeIDValue},
			MonitoringMode: ua.MonitoringModeReporting,
			RequestedParameters: ua.MonitoringParameters{
				ClientHandle: uint32(index), SamplingInterval: publishing, QueueSize: 1, DiscardOldest: true,
			},
		})
	}
	response, err := r.Client.CreateMonitoredItems(ctx, &ua.CreateMonitoredItemsRequest{
		SubscriptionID:     created.SubscriptionID,
		TimestampsToReturn: ua.TimestampsToReturnBoth,
		ItemsToCreate:      items,
	})
	if err == nil && len(response.Results) != len(items) {
		err = fmt.Errorf("unexpected monitored item cardinality")
	}
	if err == nil {
		for _, result := range response.Results {
			if result.StatusCode.IsBad() {
				err = fmt.Errorf("monitored item rejected: %w", result.StatusCode)
				break
			}
		}
	}
	if err != nil {
		_, _ = r.Client.DeleteSubscriptions(ctx, &ua.DeleteSubscriptionsRequest{SubscriptionIDs: []uint32{created.SubscriptionID}})
		return nil, err
	}
	loopCtx, cancel := context.WithCancel(context.Background())
	s := &Subscription{
		reader: r, subscriptionID: created.SubscriptionID, cancel: cancel, done: make(chan struct{}),
		latest: make([]ua.DataValue, len(sigs)), seen: make([]bool, len(sigs)), updated: make(chan struct{}),
	}
	go s.publish(loopCtx)
	return s, nil
}

// publish keeps one Publish request outstanding until cancelled or the session
// fails; a failure is sticky and returned by every later Next.
func (s *Subscription) publish(ctx context.Context) {
	defer close(s.done)
	var acks []ua.SubscriptionAcknowledgement
	for {
		response, err := s.reader.Client.Publish(ctx, &ua.PublishRequest{SubscriptionAcknowledgements: acks})
		acks = nil
		s.mu.Lock()
		if err != nil {
			if ctx.Err() == nil {
				s.err = err
			} else {
				s.err = ctx.Err()
			}
			close(s.updated)
			s.mu.Unlock()
			return
		}
		if len(response.NotificationMessage.NotificationData) > 0 {
			acks = []ua.SubscriptionAcknowledgement{{SubscriptionID: response.SubscriptionID, SequenceNumber: response.NotificationMessage.SequenceNumber}}
		}
		for _, data := range response.NotificationMessage.NotificationData {
			change, ok := data.(ua.DataChangeNotification)
			if !ok {
				continue
			}
			for _, item := range change.MonitoredItems {
				if int(item.ClientHandle) < len(s.latest) {
					s.latest[item.ClientHandle] = item.Value
					s.seen[item.ClientHandle] = true
				}
			}
		}
		close(s.updated)
		s.updated = make(chan struct{})
		s.mu.Unlock()
	}
}

// Next returns the latest value per signal once every monitored item has
// reported at least once, or the error that ended publishing.
func (s *Subscription) Next(ctx context.Context) ([]ua.DataValue, error) {
	for {
		s.mu.Lock()
		if s.err != nil {
			err := s.err
			s.mu.Unlock()
			return nil, err
		}
		complete := true
		for _, seen := range s.seen {
			complete = complete && seen
		}
		if complete {
			values := append([]ua.DataValue(nil), s.latest...)
			s.mu.Unlock()
			return values, nil
		}
		updated := s.updated
		s.mu.Unlock()
		select {
		case <-ctx.Done():
			return nil, ctx.Err()
		case <-updated:
		}
	}
}

// Close stops publishing and deletes the subscription; the session itself is
// owned by the Reader.
func (s *Subscription) Close(ctx context.Context) error {
	s.cancel()
	<-s.done
	_, err := s.reader.Client.DeleteSubscriptions(ctx, &ua.DeleteSubscriptionsRequest{SubscriptionIDs: []uint32{s.subscriptionID}})
	return err
}
