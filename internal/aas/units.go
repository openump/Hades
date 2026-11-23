package aas

import (
	"fmt"
	"strings"
)

// Units is the closed OpenUMP v1.0 unit enumeration (UCUM case-sensitive codes).
var Units = []string{"1", "%", "Cel", "K", "bar", "Pa", "kPa", "m", "s", "Hz", "V", "A", "W"}

// unitAliases maps spellings found in PLC tag tables, SCADA exports and AAS
// IEC 61360 content to OpenUMP units. Symbols match exactly (case matters:
// "m" is not "M"); word spellings match case-insensitively. Spellings that are
// ambiguous, such as a bare "C" (coulomb or Celsius), are deliberately absent.
var unitAliases = map[string]string{
	"°C": "Cel", "℃": "Cel", "degC": "Cel", "deg C": "Cel", "deg_C": "Cel",
	"°K":  "K",
	"KPa": "kPa",
	"1/s": "Hz",
	"-":   "1",
}

var unitWords = map[string]string{
	"": "1", "unitless": "1", "dimensionless": "1", "none": "1", "count": "1",
	"percent": "%", "pct": "%",
	"celsius": "Cel", "degree celsius": "Cel", "degrees celsius": "Cel",
	"kelvin": "K",
	"bar":    "bar",
	"pascal": "Pa", "kilopascal": "kPa",
	"meter": "m", "metre": "m",
	"sec": "s", "second": "s", "seconds": "s",
	"hertz": "Hz",
	"volt":  "V", "volts": "V",
	"amp": "A", "amps": "A", "ampere": "A", "amperes": "A",
	"watt": "W", "watts": "W",
}

// NormalizeUnit returns the OpenUMP unit for a source spelling, or an error
// naming the accepted units: an unmappable unit is refused, never guessed.
func NormalizeUnit(source string) (string, error) {
	s := strings.TrimSpace(source)
	for _, u := range Units {
		if s == u {
			return u, nil
		}
	}
	if u, ok := unitAliases[s]; ok {
		return u, nil
	}
	if u, ok := unitWords[strings.ToLower(s)]; ok {
		return u, nil
	}
	return "", fmt.Errorf("unit %q has no OpenUMP v1.0 equivalent (accepted: %s)", source, strings.Join(Units, " "))
}
