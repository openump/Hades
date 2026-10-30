// Package conformance checks reviewed contract files and shared fixture expectations.
package conformance

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"

	"github.com/openump/Hades/internal/contracts"
)

const baselinePath = "tests/conformance/baseline.json"

func inventory(root string) (map[string]string, error) {
	result := map[string]string{}
	for _, dir := range []string{"schemas", "examples/contracts"} {
		err := filepath.WalkDir(filepath.Join(root, dir), func(path string, entry os.DirEntry, err error) error {
			if err != nil {
				return err
			}
			if entry.Type()&os.ModeSymlink != 0 {
				return fmt.Errorf("contract symlink prohibited: %s", path)
			}
			if entry.IsDir() || !strings.HasSuffix(path, ".json") {
				return nil
			}
			raw, err := os.ReadFile(path)
			if err != nil {
				return err
			}
			rel, _ := filepath.Rel(root, path)
			sum := sha256.Sum256(raw)
			result[filepath.ToSlash(rel)] = hex.EncodeToString(sum[:])
			return nil
		})
		if err != nil {
			return nil, err
		}
	}
	return result, nil
}

// WriteBaseline is an explicit review operation, never part of automatic verification.
func WriteBaseline(root string) error {
	files, err := inventory(root)
	if err != nil {
		return err
	}
	raw, err := json.MarshalIndent(files, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(filepath.Join(root, baselinePath), append(raw, '\n'), 0644)
}
func CheckBaseline(root string) error {
	raw, err := os.ReadFile(filepath.Join(root, baselinePath))
	if err != nil {
		return err
	}
	var expected map[string]string
	if err := json.Unmarshal(raw, &expected); err != nil {
		return err
	}
	actual, err := inventory(root)
	if err != nil {
		return err
	}
	if len(actual) != len(expected) {
		return fmt.Errorf("contract inventory changed; review version compatibility and baseline")
	}
	for path, sum := range actual {
		if expected[path] != sum {
			return fmt.Errorf("unreviewed contract/fixture drift: %s", path)
		}
	}
	return nil
}

type fixture struct {
	Name, Kind, Topic string
	AssetID           string `json:"asset_id"`
	Valid             bool
	Payload           json.RawMessage
	Raw               string
	Error             string
}

func fixtures(root, path string) ([]fixture, error) {
	raw, err := os.ReadFile(filepath.Join(root, path))
	if err != nil {
		return nil, err
	}
	var cases []fixture
	err = json.Unmarshal(raw, &cases)
	if err != nil {
		return nil, err
	}
	if len(cases) == 0 {
		return nil, fmt.Errorf("empty fixture set %s", path)
	}
	return cases, nil
}
func expectation(c fixture, err error) error {
	if c.Valid {
		if err != nil {
			return fmt.Errorf("%s: expected valid: %w", c.Name, err)
		}
		return nil
	}
	if err == nil {
		return fmt.Errorf("%s: invalid fixture was accepted", c.Name)
	}
	var failure *contracts.Error
	if !errors.As(err, &failure) || failure.Code != c.Error {
		return fmt.Errorf("%s: expected rejection %q, got %v", c.Name, c.Error, err)
	}
	return nil
}
func Run(root string, output io.Writer) error {
	if err := CheckBaseline(root); err != nil {
		return err
	}
	v, err := contracts.New(os.DirFS(filepath.Join(root, "schemas")))
	if err != nil {
		return err
	}
	raw, err := os.ReadFile(filepath.Join(root, "examples/contracts/assets.json"))
	if err != nil {
		return err
	}
	if _, err := contracts.Decode(raw); err != nil {
		return err
	}
	var assets []contracts.Asset
	if err := json.Unmarshal(raw, &assets); err != nil {
		return err
	}
	registry, err := contracts.NewRegistry(assets)
	if err != nil {
		return err
	}
	count := 0
	topics, err := fixtures(root, "examples/contracts/topics.json")
	if err != nil {
		return err
	}
	for _, c := range topics {
		if err := expectation(c, registry.ValidateTopic(c.Topic, c.AssetID)); err != nil {
			return err
		}
		count++
	}
	for _, group := range []string{"telemetry", "context", "control", "inference"} {
		cases, err := fixtures(root, "examples/contracts/"+group+"/cases.json")
		if err != nil {
			return err
		}
		for _, c := range cases {
			kind := c.Kind
			if kind == "" {
				kind = "telemetry"
			}
			_, err := v.Validate(kind, c.Payload)
			if err := expectation(c, err); err != nil {
				return err
			}
			count++
		}
	}
	wire, err := fixtures(root, "examples/contracts/wire-cases.json")
	if err != nil {
		return err
	}
	for _, c := range wire {
		_, err := v.Validate("telemetry", []byte(c.Raw))
		if err := expectation(c, err); err != nil {
			return err
		}
		count++
	}
	fmt.Fprintf(output, "conformance passed: nine schemas, validated asset registry, %d fixtures; baseline unchanged\n", count)
	return nil
}
