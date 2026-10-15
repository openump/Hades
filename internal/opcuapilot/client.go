package opcuapilot

import (
	"context"
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
}

func Dial(ctx context.Context, endpoint, pki, identity string) (*Reader, error) {
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
	c, err := client.Dial(ctx, endpoint, client.WithSecurityPolicyURI(Policy, ua.MessageSecurityModeSignAndEncrypt), client.WithClientCertificatePaths(filepath.Join(pki, identity+".crt"), filepath.Join(pki, identity+".key")), client.WithX509IdentityPaths(filepath.Join(pki, identity+".crt"), filepath.Join(pki, identity+".key")), client.WithTrustedCertificatesPaths(filepath.Join(pki, "server.crt"), ""), client.WithConnectTimeout(1500), client.WithTimeoutHint(500), client.WithSessionTimeout(10000))
	if err != nil {
		return nil, err
	}
	res, err := c.Read(ctx, &ua.ReadRequest{NodesToRead: []ua.ReadValueID{{NodeID: ua.VariableIDServerNamespaceArray, AttributeID: ua.AttributeIDValue}}})
	if err != nil {
		c.Abort(context.Background())
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
				return &Reader{c, uint16(index)}, nil
			}
		}
	}
	c.Abort(context.Background())
	return nil, fmt.Errorf("approved namespace unavailable")
}
func (r *Reader) Read(ctx context.Context) ([]ua.DataValue, error) {
	nodes := make([]ua.ReadValueID, 0, len(Signals))
	for _, signal := range Signals {
		nodes = append(nodes, ua.ReadValueID{NodeID: ua.NodeIDString{NamespaceIndex: r.Namespace, ID: signal.Node}, AttributeID: ua.AttributeIDValue})
	}
	response, err := r.Client.Read(ctx, &ua.ReadRequest{MaxAge: 0, TimestampsToReturn: ua.TimestampsToReturnBoth, NodesToRead: nodes})
	if err != nil {
		return nil, err
	}
	if len(response.Results) != len(Signals) {
		return nil, fmt.Errorf("unexpected read result cardinality")
	}
	return response.Results, nil
}
func (r *Reader) Close() {
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	r.Client.Abort(ctx)
}
