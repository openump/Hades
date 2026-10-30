package gateway

import (
	"context"
	"fmt"
	"sort"
	"time"

	"github.com/openump/Hades/internal/sample"
)

type FactoryOptions struct {
	TelegrafBinary string
	OPCUAPKI       string
	PLC4Go         PLC4GoConnector
	// PLC4GoTags maps each measurement to its controller tag address. Every
	// measurement must be mapped; addresses are never derived from names.
	PLC4GoTags map[string]string
}

type ProtocolDriverFactory struct {
	OpenTelegraf func(ctx context.Context, binary, endpoint string, profile Profile, options DriverOptions) (ProtocolDriver, error)
	OpenOPCUA    func(ctx context.Context, endpoint, pki string, options DriverOptions) (ProtocolDriver, error)
	OpenPLC4Go   func(ctx context.Context, connector PLC4GoConnector, endpoint string, tags []PLC4GoTag, options DriverOptions) (ProtocolDriver, error)
}

func DefaultProtocolDriverFactory() ProtocolDriverFactory {
	return ProtocolDriverFactory{
		OpenTelegraf: func(ctx context.Context, binary, endpoint string, profile Profile, options DriverOptions) (ProtocolDriver, error) {
			return OpenDriverWithOptions(ctx, binary, endpoint, profile, options)
		},
		OpenOPCUA: func(ctx context.Context, endpoint, pki string, options DriverOptions) (ProtocolDriver, error) {
			return OpenOPCUADriver(ctx, endpoint, pki, options)
		},
		OpenPLC4Go: func(ctx context.Context, connector PLC4GoConnector, endpoint string, tags []PLC4GoTag, options DriverOptions) (ProtocolDriver, error) {
			return OpenPLC4GoDriver(ctx, connector, endpoint, tags, options)
		},
	}
}

func OpenConfiguredDriver(ctx context.Context, cfg *sample.Config, endpoint string, options DriverOptions, factoryOptions FactoryOptions) (ProtocolDriver, error) {
	return DefaultProtocolDriverFactory().Open(ctx, cfg, endpoint, options, factoryOptions)
}

func (f ProtocolDriverFactory) Open(ctx context.Context, cfg *sample.Config, endpoint string, options DriverOptions, factoryOptions FactoryOptions) (ProtocolDriver, error) {
	if cfg == nil || cfg.Metadata == nil {
		return nil, fmt.Errorf("gateway configuration is required")
	}
	acquisition, ok := cfg.Metadata["acquisition"].(map[string]any)
	if !ok {
		return nil, fmt.Errorf("metadata missing acquisition block")
	}
	protocol, _ := acquisition["protocol"].(string)
	if protocol == "" {
		return nil, fmt.Errorf("metadata missing acquisition protocol")
	}
	if options.PollInterval == 0 && cfg.Interval > 0 {
		options.PollInterval = cfg.Interval
	}
	switch protocol {
	case "modbus-tcp":
		if err := validateModbusLabContext(cfg); err != nil {
			return nil, err
		}
		if f.OpenTelegraf == nil {
			return nil, fmt.Errorf("telegraf opener is not configured")
		}
		profile := DefaultLabProfile()
		options.Deadbands = deadbandsForProfile(cfg, profile, options.Deadbands)
		return f.OpenTelegraf(ctx, factoryOptions.TelegrafBinary, endpoint, profile, options)
	case "opc-ua":
		if err := sample.CheckOPCUAMock(cfg); err != nil {
			return nil, err
		}
		if f.OpenOPCUA == nil {
			return nil, fmt.Errorf("opc ua opener is not configured")
		}
		return f.OpenOPCUA(ctx, endpoint, factoryOptions.OPCUAPKI, options)
	case "plc4go":
		if err := validatePLC4GoMetadata(cfg); err != nil {
			return nil, err
		}
		if factoryOptions.PLC4Go == nil {
			return nil, fmt.Errorf("plc4go connector is required")
		}
		if f.OpenPLC4Go == nil {
			return nil, fmt.Errorf("plc4go opener is not configured")
		}
		tags, err := measurementsToPLC4GoTags(cfg, factoryOptions.PLC4GoTags)
		if err != nil {
			return nil, err
		}
		return f.OpenPLC4Go(ctx, factoryOptions.PLC4Go, endpoint, tags, options)
	case "simulator":
		return nil, fmt.Errorf("simulator does not use a gateway protocol driver")
	default:
		return nil, fmt.Errorf("unsupported gateway protocol %q", protocol)
	}
}

// deadbandsForProfile re-keys measurement deadbands to the register name the
// Telegraf driver reports. The lab context has exactly one measurement and the
// lab profile exactly one register, so the mapping is unambiguous.
func deadbandsForProfile(cfg *sample.Config, profile Profile, deadbands map[string]DeadbandConfig) map[string]DeadbandConfig {
	measurements, _ := cfg.Metadata["measurements"].(map[string]any)
	if len(deadbands) == 0 || len(measurements) != 1 || len(profile.Registers) != 1 {
		return deadbands
	}
	for name := range measurements {
		if band, ok := deadbands[name]; ok {
			return map[string]DeadbandConfig{profile.Registers[0].Name: band}
		}
	}
	return deadbands
}

func validateModbusLabContext(cfg *sample.Config) error {
	measurements, ok := cfg.Metadata["measurements"].(map[string]any)
	if !ok {
		return fmt.Errorf("metadata missing measurements")
	}
	definition, ok := measurements["motor_temperature"].(map[string]any)
	if !ok || len(measurements) != 1 || definition["type"] != "number" || definition["unit"] != "Cel" {
		return fmt.Errorf("unsupported lab metadata")
	}
	interval := time.Duration(sample.Num(definition["expected_interval_ms"])) * time.Millisecond
	if interval < time.Second || interval > time.Hour {
		return fmt.Errorf("lab poll interval must be 1s..1h")
	}
	return nil
}

func validatePLC4GoMetadata(cfg *sample.Config) error {
	measurements, ok := cfg.Metadata["measurements"].(map[string]any)
	if !ok || len(measurements) == 0 {
		return fmt.Errorf("metadata missing measurements")
	}
	for name, raw := range measurements {
		def, ok := raw.(map[string]any)
		if !ok || def["type"] != "number" {
			return fmt.Errorf("plc4go metadata requires numeric measurements")
		}
		if name == "" {
			return fmt.Errorf("plc4go metadata requires named measurements")
		}
	}
	return nil
}

func measurementsToPLC4GoTags(cfg *sample.Config, addresses map[string]string) ([]PLC4GoTag, error) {
	measurements, ok := cfg.Metadata["measurements"].(map[string]any)
	if !ok {
		return nil, fmt.Errorf("metadata missing measurements")
	}
	names := make([]string, 0, len(measurements))
	for name := range measurements {
		names = append(names, name)
	}
	sort.Strings(names)
	tags := make([]PLC4GoTag, 0, len(names))
	for _, name := range names {
		address := addresses[name]
		if address == "" {
			return nil, fmt.Errorf("plc4go measurement %q has no configured tag address", name)
		}
		tags = append(tags, PLC4GoTag{
			Name:    name,
			Address: address,
			Unit:    sample.MetadataUnitOrDefault(measurements[name]),
		})
	}
	for name := range addresses {
		if _, ok := measurements[name]; !ok {
			return nil, fmt.Errorf("plc4go tag address configured for unknown measurement %q", name)
		}
	}
	return tags, nil
}
