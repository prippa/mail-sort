package mail

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"

	_ "embed"
)

//go:embed presets.json
var builtinPresets []byte

// Host is one IMAP endpoint inside a provider preset.
type Host struct {
	ID       string `json:"id"`
	Host     string `json:"host"`
	Port     int    `json:"port"`
	Security string `json:"security"`
	Note     string `json:"note,omitempty"`
}

// Preset is a provider's documented IMAP settings.
type Preset struct {
	ID              string   `json:"id"`
	Name            string   `json:"name"`
	Source          string   `json:"source"`
	Verified        bool     `json:"verified"`
	DefaultHost     string   `json:"default_host,omitempty"`
	Hosts           []Host   `json:"hosts"`
	Auth            []string `json:"auth"`
	UsernameHint    string   `json:"username_hint"`
	Note            string   `json:"note"`
	AuthFailureHint string   `json:"auth_failure_hint"`
}

// LoadPresets reads the embedded presets and, when overlayPath exists, replaces
// entries with the same id. A missing overlay file is not an error.
func LoadPresets(overlayPath string) ([]Preset, error) {
	base, err := decodePresets(builtinPresets)
	if err != nil {
		return nil, fmt.Errorf("presets: embedded file: %w", err)
	}
	if overlayPath == "" {
		return base, nil
	}
	data, err := os.ReadFile(overlayPath)
	if errors.Is(err, os.ErrNotExist) {
		return base, nil
	}
	if err != nil {
		return nil, fmt.Errorf("presets: read overlay: %w", err)
	}
	overlay, err := decodePresets(data)
	if err != nil {
		return nil, fmt.Errorf("presets: overlay is invalid JSON")
	}
	return mergePresets(base, overlay), nil
}

func decodePresets(data []byte) ([]Preset, error) {
	dec := json.NewDecoder(bytes.NewReader(data))
	dec.DisallowUnknownFields()
	var presets []Preset
	if err := dec.Decode(&presets); err != nil {
		return nil, err
	}
	if err := dec.Decode(&struct{}{}); err != io.EOF {
		return nil, errors.New("presets: extra JSON after the list")
	}
	for _, preset := range presets {
		if err := checkPreset(preset); err != nil {
			return nil, err
		}
	}
	return presets, nil
}

func checkPreset(preset Preset) error {
	if preset.ID == "" || preset.Source == "" || len(preset.Hosts) == 0 || len(preset.Auth) == 0 {
		return fmt.Errorf("presets: %q is missing id, source, hosts, or auth", preset.ID)
	}
	for _, host := range preset.Hosts {
		if host.ID == "" || host.Host == "" || host.Port < 1 || host.Port > 65535 {
			return fmt.Errorf("presets: %q has an invalid host entry", preset.ID)
		}
		if host.Security != string(ImplicitTLS) && host.Security != string(StartTLS) {
			return fmt.Errorf("presets: %q has an invalid security value", preset.ID)
		}
	}
	return nil
}

func mergePresets(base, overlay []Preset) []Preset {
	out := append([]Preset(nil), base...)
	index := make(map[string]int, len(out))
	for i, preset := range out {
		index[preset.ID] = i
	}
	for _, preset := range overlay {
		if i, ok := index[preset.ID]; ok {
			out[i] = preset
			continue
		}
		index[preset.ID] = len(out)
		out = append(out, preset)
	}
	return out
}

func findPreset(presets []Preset, id string) (Preset, bool) {
	for _, preset := range presets {
		if preset.ID == id {
			return preset, true
		}
	}
	return Preset{}, false
}
