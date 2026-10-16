package opcuapilot

import (
	"context"
	"errors"
	"fmt"
	"github.com/awcullen/opcua/client"
	"github.com/awcullen/opcua/ua"
	"net/url"
	"path/filepath"
	"time"
)

type Reader struct {
	Client    *client.Client
	Namespace uint16
	Signals   []Signal
}

func Dial(ctx context.Context, endpoint, pki, identity string) (*Reader, error) {
	return DialWithSignals(ctx, endpoint, pki, identity, Signals[:])
}

func DialWithSignals(ctx context.Context, endpoint, pki, identity string, signals []Signal) (*Reader, error) {
	if len(signals) == 0 {
		signals = Signals[:]
	}
	if err := ValidateEndpoint(endpoint); err != nil {
		return nil, err
	}
	pinned, err := Certificate(filepath.Join(pki, "server.crt"), ServerURI)
	if err != nil {
		return nil, err
	}
	u, _ := url.Parse(endpoint)
	if err = pinned.VerifyHostname(u.Hostname()); err != nil {
		return nil, err
	}
	uri := ClientURI
	if identity == "outsider" {
		uri = "urn:openump:opcua:outsider"
	}
	if identity != "client" && identity != "outsider" {
		return nil, fmt.Errorf("unknown local identity")
	}
	if _, err = Certificate(filepath.Join(pki, identity+".crt"), uri); err != nil {
		return nil, err
	}
	var c *client.Client
	var res *ua.ReadResponse
	for attempt := 0; attempt < 4; attempt++ {
		c, err = client.Dial(ctx, endpoint, client.WithSecurityPolicyURI(Policy, ua.MessageSecurityModeSignAndEncrypt), client.WithClientCertificatePaths(filepath.Join(pki, identity+".crt"), filepath.Join(pki, identity+".key")), client.WithX509IdentityPaths(filepath.Join(pki, identity+".crt"), filepath.Join(pki, identity+".key")), client.WithTrustedCertificatesPaths(filepath.Join(pki, "server.crt"), ""), client.WithConnectTimeout(3000), client.WithTimeoutHint(2000), client.WithSessionTimeout(10000))
		if err == nil {
			res, err = c.Read(ctx, &ua.ReadRequest{NodesToRead: []ua.ReadValueID{{NodeID: ua.VariableIDServerNamespaceArray, AttributeID: ua.AttributeIDValue}}})
			if err == nil {
				break
			}
			c.Abort(context.Background())
		}
		if ctx.Err() != nil || (!errors.Is(err, ua.BadSecureChannelClosed) && !errors.Is(err, ua.BadRequestTimeout) && !errors.Is(err, ua.BadCommunicationError)) {
			return nil, err
		}
	}
	if err != nil {
		return nil, err
	}
	if len(res.Results) != 1 {
		c.Abort(context.Background())
		return nil, fmt.Errorf("invalid namespace array response")
	}
	namespaces, ok := res.Results[0].Value.([]string)
	if ok {
		for index, uri := range namespaces {
			if uri == NamespaceURI {
				return &Reader{Client: c, Namespace: uint16(index), Signals: append([]Signal(nil), signals...)}, nil
			}
		}
	}
	c.Abort(context.Background())
	return nil, fmt.Errorf("approved namespace unavailable")
}

func (r *Reader) activeSignals() []Signal {
	if len(r.Signals) == 0 {
		return Signals[:]
	}
	return r.Signals
}

func (r *Reader) Read(ctx context.Context) ([]ua.DataValue, error) {
	sigs := r.activeSignals()
	nodes := make([]ua.ReadValueID, 0, len(sigs))
	for _, signal := range sigs {
		nodes = append(nodes, ua.ReadValueID{NodeID: ua.NodeIDString{NamespaceIndex: r.Namespace, ID: signal.Node}, AttributeID: ua.AttributeIDValue})
	}
	response, err := r.Client.Read(ctx, &ua.ReadRequest{MaxAge: 0, TimestampsToReturn: ua.TimestampsToReturnBoth, NodesToRead: nodes})
	if err != nil {
		return nil, err
	}
	if len(response.Results) != len(sigs) {
		return nil, fmt.Errorf("unexpected read result cardinality")
	}
	return response.Results, nil
}
func (r *Reader) Close() {
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	if err := r.Client.Close(ctx); err != nil {
		r.Client.Abort(ctx)
	}
}
