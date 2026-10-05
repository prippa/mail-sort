package config

import (
	"context"
	"encoding/hex"
	"errors"
	"fmt"
	"math"
	"net/url"
	"os"
	"regexp"
	"strconv"
	"strings"

	"gopkg.in/yaml.v3"

	"github.com/prippa/mail-sort/internal/secrets"
)

const maxConfigBytes = 1 << 20

// errInvalidYAML is returned for syntax and type failures. The underlying
// decoder error is dropped because yaml.v3 includes the offending scalar in
// that text.
var errInvalidYAML = errors.New("config: invalid YAML")

var envNamePattern = regexp.MustCompile(`^[A-Za-z_][A-Za-z0-9_]*$`)

// Config is the on-disk settings file. Credentials are references only.
type Config struct {
	Language       string       `yaml:"language"`
	Profiles       []Profile    `yaml:"profiles"`
	CategoriesFile string       `yaml:"categories_file"`
	Classifiers    []Classifier `yaml:"classifiers"`
}

// Classifier is one enabled model. The API key is the environment variable
// named by key_env, never a value in this file. An empty list calls no provider.
type Classifier struct {
	Provider      string  `yaml:"provider"`
	Model         string  `yaml:"model"`
	BaseURL       string  `yaml:"base_url"`
	KeyEnv        string  `yaml:"key_env"`
	MinConfidence float64 `yaml:"min_confidence"`
	MinMargin     float64 `yaml:"min_margin"`
	RPS           float64 `yaml:"rps"`
	Burst         int     `yaml:"burst"`
	PriceInput    float64 `yaml:"price_input"`
	PriceOutput   float64 `yaml:"price_output"`
}

// Profile is one mailbox. Empty host, port, and security are filled from the
// provider preset. max_chars 0 means the default body cap (1500).
type Profile struct {
	Name        string `yaml:"name"`
	Provider    string `yaml:"provider"`
	Host        string `yaml:"host"`
	HostID      string `yaml:"host_id"`
	Port        int    `yaml:"port"`
	Security    string `yaml:"security"`
	Username    string `yaml:"username"`
	PasswordEnv string `yaml:"password_env"`
	Email       string `yaml:"email"`
	Auth        string `yaml:"auth"`
	CAFile      string `yaml:"ca_file"`
	CertSHA256  string `yaml:"cert_sha256"`
	Discover    bool   `yaml:"discover"`
	MaxChars    int    `yaml:"max_chars"`
}

var configFields = map[string]struct{}{
	"language":        {},
	"profiles":        {},
	"categories_file": {},
	"classifiers":     {},
}

var classifierFields = map[string]struct{}{
	"provider":       {},
	"model":          {},
	"base_url":       {},
	"key_env":        {},
	"min_confidence": {},
	"min_margin":     {},
	"rps":            {},
	"burst":          {},
	"price_input":    {},
	"price_output":   {},
}

var profileFields = map[string]struct{}{
	"name":         {},
	"provider":     {},
	"host":         {},
	"host_id":      {},
	"port":         {},
	"security":     {},
	"username":     {},
	"password_env": {},
	"email":        {},
	"auth":         {},
	"ca_file":      {},
	"cert_sha256":  {},
	"discover":     {},
	"max_chars":    {},
}

// Load reads path and rejects secret fields, aliases, and unknown keys.
// Errors name the field and line. They do not include field values.
func Load(ctx context.Context, path string) (Config, error) {
	if err := ctx.Err(); err != nil {
		return Config{}, err
	}
	if path == "" {
		return Config{}, errors.New("config: path is empty")
	}
	info, err := os.Stat(path)
	if err != nil {
		return Config{}, fmt.Errorf("config: open %s: %w", path, err)
	}
	if info.Size() > maxConfigBytes {
		return Config{}, errors.New("config: file exceeds 1MiB limit")
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return Config{}, fmt.Errorf("config: read %s: %w", path, err)
	}
	if err := ctx.Err(); err != nil {
		return Config{}, err
	}
	if len(data) > maxConfigBytes {
		return Config{}, errors.New("config: file exceeds 1MiB limit")
	}
	cfg, err := parse(data)
	if err != nil {
		return Config{}, err
	}
	return cfg, nil
}

func parse(data []byte) (Config, error) {
	if len(strings.TrimSpace(string(data))) == 0 {
		return Config{}, nil
	}
	var doc yaml.Node
	if err := yaml.Unmarshal(data, &doc); err != nil {
		return Config{}, errInvalidYAML
	}
	if err := validateTree(&doc); err != nil {
		return Config{}, err
	}
	var cfg Config
	if err := yaml.Unmarshal(data, &cfg); err != nil {
		return Config{}, errInvalidYAML
	}
	return cfg, nil
}

func validateTree(doc *yaml.Node) error {
	if err := walkSecrets(doc); err != nil {
		return err
	}
	root := documentRoot(doc)
	if root == nil || isNull(root) {
		return nil
	}
	if root.Kind != yaml.MappingNode {
		return fmt.Errorf("config: line %d must be a mapping", root.Line)
	}
	return walkMapping(root, configFields, func(key string, keyNode, val *yaml.Node) error {
		switch key {
		case "language":
			return checkChoice(keyNode, val, "auto", "en", "ru")
		case "profiles":
			return validateProfiles(val)
		case "categories_file":
			return checkScalar(keyNode, val)
		case "classifiers":
			return validateClassifiers(val)
		default:
			return fmt.Errorf("config: unknown field %q at line %d", keyNode.Value, keyNode.Line)
		}
	})
}

func validateProfiles(val *yaml.Node) error {
	if isNull(val) {
		return nil
	}
	if val.Kind != yaml.SequenceNode {
		return fmt.Errorf("config: field \"profiles\" at line %d must be a list", val.Line)
	}
	seen := make(map[string]struct{})
	for _, item := range val.Content {
		if item.Kind != yaml.MappingNode {
			return fmt.Errorf("config: profile at line %d must be a mapping", item.Line)
		}
		var name string
		err := walkMapping(item, profileFields, func(key string, keyNode, field *yaml.Node) error {
			switch key {
			case "name":
				if field.Kind != yaml.ScalarNode || isNull(field) {
					return fmt.Errorf("config: profile name at line %d is empty or invalid", field.Line)
				}
				checked, ok := plainText(field.Value)
				if !ok || strings.TrimSpace(checked) == "" {
					return fmt.Errorf("config: profile name at line %d is empty or invalid", field.Line)
				}
				if len(checked) > 128 {
					return fmt.Errorf("config: profile name at line %d is too long", field.Line)
				}
				if _, exists := seen[checked]; exists {
					return fmt.Errorf("config: duplicate profile name %q", checked)
				}
				seen[checked] = struct{}{}
				name = checked
				return nil
			case "port":
				return checkPort(field)
			case "security":
				return checkChoice(keyNode, field, "implicit_tls", "starttls")
			case "auth":
				return checkChoice(keyNode, field, "password", "oauth_google", "oauth_microsoft")
			case "password_env":
				return checkEnvName(keyNode, field)
			case "cert_sha256":
				return checkFingerprint(field)
			case "discover":
				return checkBool(keyNode, field)
			case "max_chars":
				return checkMaxChars(field)
			default:
				return checkScalar(keyNode, field)
			}
		})
		if err != nil {
			return err
		}
		if name == "" {
			return fmt.Errorf("config: profile at line %d is missing a name", item.Line)
		}
	}
	return nil
}

func walkMapping(n *yaml.Node, allowed map[string]struct{}, visit func(key string, keyNode, val *yaml.Node) error) error {
	if len(n.Content)%2 != 0 {
		return errInvalidYAML
	}
	seen := make(map[string]struct{}, len(n.Content)/2)
	for i := 0; i < len(n.Content); i += 2 {
		keyNode := n.Content[i]
		val := n.Content[i+1]
		if keyNode.Kind != yaml.ScalarNode {
			return errInvalidYAML
		}
		if keyNode.Value == "<<" {
			return errors.New("config: YAML anchors and aliases are not allowed")
		}
		canonical := secrets.CanonicalKey(keyNode.Value)
		if _, ok := seen[canonical]; ok {
			return fmt.Errorf("config: duplicate field %q at line %d", keyNode.Value, keyNode.Line)
		}
		seen[canonical] = struct{}{}
		if _, ok := allowed[canonical]; !ok {
			return fmt.Errorf("config: unknown field %q at line %d", keyNode.Value, keyNode.Line)
		}
		if err := visit(canonical, keyNode, val); err != nil {
			return err
		}
	}
	return nil
}

func walkSecrets(n *yaml.Node) error {
	if n == nil {
		return nil
	}
	if n.Kind == yaml.AliasNode || n.Anchor != "" {
		return errors.New("config: YAML anchors and aliases are not allowed")
	}
	switch n.Kind {
	case yaml.DocumentNode, yaml.SequenceNode:
		for _, child := range n.Content {
			if err := walkSecrets(child); err != nil {
				return err
			}
		}
	case yaml.MappingNode:
		if len(n.Content)%2 != 0 {
			return errInvalidYAML
		}
		for i := 0; i < len(n.Content); i += 2 {
			keyNode := n.Content[i]
			if keyNode.Kind == yaml.ScalarNode && secrets.IsSecretConfigKey(keyNode.Value) {
				return fmt.Errorf("config: field %q at line %d is not allowed in config files; use the keyring or an environment variable", keyNode.Value, keyNode.Line)
			}
			if err := walkSecrets(keyNode); err != nil {
				return err
			}
			if err := walkSecrets(n.Content[i+1]); err != nil {
				return err
			}
		}
	}
	return nil
}

func checkPort(val *yaml.Node) error {
	if isNull(val) {
		return nil
	}
	if val.Kind != yaml.ScalarNode {
		return fmt.Errorf("config: field \"port\" at line %d must be a number", val.Line)
	}
	port, err := strconv.Atoi(val.Value)
	if err != nil || port < 1 || port > 65535 {
		return fmt.Errorf("config: field \"port\" at line %d must be between 1 and 65535", val.Line)
	}
	return nil
}

func checkFingerprint(val *yaml.Node) error {
	if isNull(val) || (val.Kind == yaml.ScalarNode && val.Value == "") {
		return nil
	}
	if val.Kind != yaml.ScalarNode {
		return fmt.Errorf("config: field \"cert_sha256\" at line %d must be a string", val.Line)
	}
	sum, err := hex.DecodeString(val.Value)
	if err != nil || len(sum) != 32 {
		return fmt.Errorf("config: field \"cert_sha256\" at line %d must be 64 hex characters", val.Line)
	}
	return nil
}

func checkBool(keyNode, val *yaml.Node) error {
	if isNull(val) {
		return nil
	}
	if val.Kind != yaml.ScalarNode || (val.Value != "true" && val.Value != "false") {
		return fmt.Errorf("config: field %q at line %d must be true or false", keyNode.Value, keyNode.Line)
	}
	return nil
}

func checkMaxChars(val *yaml.Node) error {
	if isNull(val) || (val.Kind == yaml.ScalarNode && val.Value == "") {
		return nil
	}
	if val.Kind != yaml.ScalarNode {
		return fmt.Errorf("config: field \"max_chars\" at line %d must be a number", val.Line)
	}
	n, err := strconv.Atoi(val.Value)
	if err != nil || n < 0 || n > 100000 {
		return fmt.Errorf("config: field \"max_chars\" at line %d must be between 0 and 100000", val.Line)
	}
	return nil
}

func validateClassifiers(val *yaml.Node) error {
	if isNull(val) {
		return nil
	}
	if val.Kind != yaml.SequenceNode {
		return fmt.Errorf("config: field \"classifiers\" at line %d must be a list", val.Line)
	}
	for _, item := range val.Content {
		if item.Kind != yaml.MappingNode {
			return fmt.Errorf("config: classifier at line %d must be a mapping", item.Line)
		}
		var provider, model, keyEnv, base string
		err := walkMapping(item, classifierFields, func(key string, keyNode, field *yaml.Node) error {
			switch key {
			case "provider":
				if err := checkChoice(keyNode, field, "jev", "openai_compatible", "anthropic"); err != nil {
					return err
				}
				if field.Kind != yaml.ScalarNode || isNull(field) || field.Value == "" {
					return fmt.Errorf("config: classifier at line %d is missing a provider", field.Line)
				}
				provider = field.Value
				return nil
			case "model":
				if err := checkScalar(keyNode, field); err != nil {
					return err
				}
				if field.Kind == yaml.ScalarNode && !isNull(field) {
					model = field.Value
				}
				return nil
			case "base_url":
				if err := checkBaseURL(keyNode, field); err != nil {
					return err
				}
				if field.Kind == yaml.ScalarNode && !isNull(field) {
					base = field.Value
				}
				return nil
			case "key_env":
				if err := checkEnvName(keyNode, field); err != nil {
					return err
				}
				if field.Kind == yaml.ScalarNode && !isNull(field) {
					keyEnv = field.Value
				}
				return nil
			case "min_confidence":
				return checkPositiveUnit(keyNode, field)
			case "min_margin":
				return checkNumber(keyNode.Value, field, 0, 1)
			case "rps":
				return checkNumber(keyNode.Value, field, 0, 1000)
			case "burst":
				return checkInt(keyNode.Value, field, 0, 10000)
			case "price_input", "price_output":
				return checkNumber(keyNode.Value, field, 0, 1_000_000)
			default:
				return fmt.Errorf("config: unknown field %q at line %d", keyNode.Value, keyNode.Line)
			}
		})
		if err != nil {
			return err
		}
		if provider == "" {
			return fmt.Errorf("config: classifier at line %d is missing a provider", item.Line)
		}
		if provider != "jev" && strings.TrimSpace(model) == "" {
			return fmt.Errorf("config: classifier at line %d is missing a model", item.Line)
		}
		if provider != "jev" && keyEnv == "" {
			return fmt.Errorf("config: classifier at line %d is missing key_env", item.Line)
		}
		if provider == "openai_compatible" && strings.TrimSpace(base) == "" {
			return fmt.Errorf("config: classifier at line %d is missing base_url", item.Line)
		}
	}
	return nil
}

func checkEnvName(keyNode, val *yaml.Node) error {
	if isNull(val) || (val.Kind == yaml.ScalarNode && val.Value == "") {
		return nil
	}
	if val.Kind != yaml.ScalarNode || !envNamePattern.MatchString(val.Value) {
		return fmt.Errorf("config: field %q at line %d must be an environment variable name", keyNode.Value, val.Line)
	}
	return nil
}

func checkBaseURL(keyNode, val *yaml.Node) error {
	if isNull(val) || (val.Kind == yaml.ScalarNode && strings.TrimSpace(val.Value) == "") {
		return nil
	}
	if val.Kind != yaml.ScalarNode {
		return fmt.Errorf("config: field %q at line %d must be an http or https URL", keyNode.Value, keyNode.Line)
	}
	parsed, err := url.Parse(val.Value)
	if err != nil || parsed.Host == "" || (parsed.Scheme != "http" && parsed.Scheme != "https") || parsed.User != nil {
		return fmt.Errorf("config: field %q at line %d must be an http or https URL", keyNode.Value, keyNode.Line)
	}
	return nil
}

func checkPositiveUnit(keyNode, val *yaml.Node) error {
	if isNull(val) || (val.Kind == yaml.ScalarNode && val.Value == "") {
		return nil
	}
	if val.Kind != yaml.ScalarNode {
		return fmt.Errorf("config: field %q at line %d must be a number", keyNode.Value, val.Line)
	}
	n, err := strconv.ParseFloat(val.Value, 64)
	if err != nil || math.IsNaN(n) || math.IsInf(n, 0) || n <= 0 || n > 1 {
		return fmt.Errorf("config: field %q at line %d must be greater than 0 and at most 1", keyNode.Value, val.Line)
	}
	return nil
}

func checkNumber(key string, val *yaml.Node, min, max float64) error {
	if isNull(val) || (val.Kind == yaml.ScalarNode && val.Value == "") {
		return nil
	}
	if val.Kind != yaml.ScalarNode {
		return fmt.Errorf("config: field %q at line %d must be a number", key, val.Line)
	}
	n, err := strconv.ParseFloat(val.Value, 64)
	if err != nil || math.IsNaN(n) || math.IsInf(n, 0) || n < min || n > max {
		return fmt.Errorf("config: field %q at line %d is out of range", key, val.Line)
	}
	return nil
}

func checkInt(key string, val *yaml.Node, min, max int) error {
	if isNull(val) || (val.Kind == yaml.ScalarNode && val.Value == "") {
		return nil
	}
	if val.Kind != yaml.ScalarNode {
		return fmt.Errorf("config: field %q at line %d must be a whole number", key, val.Line)
	}
	n, err := strconv.Atoi(val.Value)
	if err != nil || n < min || n > max {
		return fmt.Errorf("config: field %q at line %d is out of range", key, val.Line)
	}
	return nil
}

func checkChoice(keyNode, val *yaml.Node, choices ...string) error {
	if isNull(val) || (val.Kind == yaml.ScalarNode && val.Value == "") {
		return nil
	}
	if val.Kind != yaml.ScalarNode {
		return fmt.Errorf("config: field %q at line %d has an invalid value", keyNode.Value, keyNode.Line)
	}
	for _, choice := range choices {
		if val.Value == choice {
			return nil
		}
	}
	return fmt.Errorf("config: field %q at line %d has an invalid value", keyNode.Value, keyNode.Line)
}

func checkScalar(keyNode, val *yaml.Node) error {
	if isNull(val) {
		return nil
	}
	if val.Kind != yaml.ScalarNode {
		return fmt.Errorf("config: field %q at line %d must be a string", keyNode.Value, keyNode.Line)
	}
	if _, ok := plainText(val.Value); !ok {
		return fmt.Errorf("config: field %q at line %d is empty or invalid", keyNode.Value, keyNode.Line)
	}
	return nil
}

func plainText(value string) (string, bool) {
	if strings.TrimSpace(value) == "" && value != "" {
		return "", false
	}
	for _, r := range value {
		if r < 0x20 || r == 0x7f {
			return "", false
		}
	}
	return value, true
}

func isNull(n *yaml.Node) bool {
	return n.Tag == "!!null" || (n.Kind == yaml.ScalarNode && n.Value == "" && n.Tag == "!!null")
}

func documentRoot(doc *yaml.Node) *yaml.Node {
	if doc == nil || doc.Kind == 0 {
		return nil
	}
	if doc.Kind == yaml.DocumentNode {
		if len(doc.Content) == 0 {
			return nil
		}
		return doc.Content[0]
	}
	return doc
}
