//go:build plc4x

package gateway

// PLC4XAvailable reports whether this binary was built with the PLC4X tier.
const PLC4XAvailable = true

// NewPLC4XGatewayConnector returns the PLC4X connector for gated acquisition.
func NewPLC4XGatewayConnector() (PLC4GoConnector, error) {
	return NewPLC4XConnector(), nil
}
