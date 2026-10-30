//go:build !plc4x

package gateway

import "errors"

// PLC4XAvailable reports whether this binary was built with the PLC4X tier.
const PLC4XAvailable = false

// NewPLC4XGatewayConnector fails in builds without the PLC4X tier.
func NewPLC4XGatewayConnector() (PLC4GoConnector, error) {
	return nil, errors.New("plc4go acquisition requires a binary built with -tags plc4x")
}
