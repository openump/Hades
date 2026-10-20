package actuator

import (
	"fmt"
	"github.com/openump/Hades/internal/contracts"
	"github.com/openump/Hades/internal/sample"
)

func validateTelemetry(p map[string]any, cfg *sample.Config) error {
	if p["metadata_revision"] != cfg.Metadata["metadata_revision"] {
		return fmt.Errorf("unapproved telemetry revision")
	}
	return contracts.ValidateContext(p, cfg.Metadata)
}
