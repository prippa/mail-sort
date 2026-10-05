package classify

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"

	"gopkg.in/yaml.v3"

	"github.com/prippa/mail-sort/internal/secrets"
)

const (
	// NeedsReview is the reserved category for an unclear message.
	NeedsReview = "needs_review"
	// KeepInInbox is the reserved no-op category.
	KeepInInbox = "keep_in_inbox"
	// NeverTouch is a rule action. It is not sent to a model.
	NeverTouch = "never_touch"

	// NeedsReviewCriteria is the exact rubric sent for needs_review.
	NeedsReviewCriteria = "Unclear, ambiguous, or none of the above"
	// KeepInInboxCriteria is the rubric used when the user does not supply one.
	// The product spec names the category and not this sentence.
	KeepInInboxCriteria = "The message should stay in the inbox. It does not belong in another folder."
	// NeedsReviewFolder is the mailbox name for needs_review.
	NeedsReviewFolder = "Needs review"

	maxUserCategories = 253
	maxCategoryBytes  = 1 << 20
)

var (
	slugPattern = regexp.MustCompile(`^[a-z][a-z0-9_]*$`)

	errInvalidCategories = errors.New("categories: invalid YAML")
)

// Category is one filing destination. Description is the English rubric sent
// to a model. NameRU is display text and is not sent.
type Category struct {
	Key           string   `yaml:"key"`
	Name          string   `yaml:"name"`
	NameRU        string   `yaml:"name_ru,omitempty"`
	Description   string   `yaml:"description"`
	Examples      []string `yaml:"examples,omitempty"`
	Folder        string   `yaml:"folder,omitempty"`
	Action        string   `yaml:"action"`
	MinConfidence float64  `yaml:"min_confidence,omitempty"`
}

// Criteria is the English text sent to a model for this category.
func (c Category) Criteria() string {
	if c.Key == NeedsReview {
		return NeedsReviewCriteria
	}
	text := strings.TrimSpace(c.Description)
	if c.Key == KeepInInbox && text == "" {
		text = KeepInInboxCriteria
	}
	var examples []string
	for _, example := range c.Examples {
		example = strings.TrimSpace(example)
		if example != "" {
			examples = append(examples, example)
		}
	}
	if len(examples) == 0 {
		return text
	}
	return text + " Examples: " + strings.Join(examples, "; ") + "."
}

// Set is the user's categories and the rules that run before any model.
type Set struct {
	Categories []Category
	Rules      []Rule
}

type categoryFile struct {
	Categories []Category `yaml:"categories"`
	Rules      []Rule     `yaml:"rules,omitempty"`
}

var categoryFields = map[string]struct{}{
	"key":            {},
	"name":           {},
	"name_ru":        {},
	"description":    {},
	"examples":       {},
	"folder":         {},
	"action":         {},
	"min_confidence": {},
}

var ruleFields = map[string]struct{}{
	"name":         {},
	"from":         {},
	"from_domain":  {},
	"to":           {},
	"subject":      {},
	"header":       {},
	"header_value": {},
	"keywords":     {},
	"attachment":   {},
	"category":     {},
	"action":       {},
}

var fileFields = map[string]struct{}{
	"categories": {},
	"rules":      {},
}

// Starter is the built-in category list, including the two reserved categories.
func Starter() Set {
	return Set{Categories: withReserved(starterCategories())}
}

// ExportStarter writes the built-in categories as YAML. Reserved categories are
// added again on load, so they are not part of the export.
func ExportStarter() ([]byte, error) {
	var buf bytes.Buffer
	enc := yaml.NewEncoder(&buf)
	enc.SetIndent(2)
	if err := enc.Encode(categoryFile{Categories: starterCategories()}); err != nil {
		return nil, fmt.Errorf("categories: export: %w", err)
	}
	if err := enc.Close(); err != nil {
		return nil, fmt.Errorf("categories: export: %w", err)
	}
	return buf.Bytes(), nil
}

// SaveCategories writes categories and rules. Reserved categories are added
// again on load, so they are omitted here. The file mode is 0600.
func SaveCategories(ctx context.Context, path string, set Set) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if path == "" {
		return errors.New("categories: path is empty")
	}
	file := categoryFile{Categories: withoutReserved(set.Categories), Rules: set.Rules}
	var buf bytes.Buffer
	enc := yaml.NewEncoder(&buf)
	enc.SetIndent(2)
	if err := enc.Encode(file); err != nil {
		return fmt.Errorf("categories: save: %w", err)
	}
	if err := enc.Close(); err != nil {
		return fmt.Errorf("categories: save: %w", err)
	}
	if _, err := ParseCategories(buf.Bytes()); err != nil {
		return err
	}
	if err := writePrivateFile(path, buf.Bytes()); err != nil {
		return fmt.Errorf("categories: save: %w", err)
	}
	return nil
}

func withoutReserved(cats []Category) []Category {
	out := make([]Category, 0, len(cats))
	for _, cat := range cats {
		if cat.Key == NeedsReview || cat.Key == KeepInInbox {
			continue
		}
		out = append(out, cat)
	}
	return out
}

func writePrivateFile(path string, data []byte) error {
	dir := filepath.Dir(path)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return err
	}
	tmp, err := os.CreateTemp(dir, ".tmp-*")
	if err != nil {
		return err
	}
	tmpName := tmp.Name()
	ok := false
	defer func() {
		if !ok {
			_ = os.Remove(tmpName)
		}
	}()
	if err := tmp.Chmod(0o600); err != nil {
		_ = tmp.Close()
		return err
	}
	if _, err := tmp.Write(data); err != nil {
		_ = tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	if err := os.Rename(tmpName, path); err != nil {
		return err
	}
	ok = true
	return os.Chmod(path, 0o600)
}

// LoadCategories reads a categories file. The result always includes
// needs_review and keep_in_inbox.
func LoadCategories(ctx context.Context, path string) (Set, error) {
	if err := ctx.Err(); err != nil {
		return Set{}, err
	}
	if path == "" {
		return Set{}, errors.New("categories: path is empty")
	}
	info, err := os.Stat(path)
	if err != nil {
		return Set{}, fmt.Errorf("categories: open %s: %w", path, err)
	}
	if info.Size() > maxCategoryBytes {
		return Set{}, errors.New("categories: file exceeds 1MiB limit")
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return Set{}, fmt.Errorf("categories: read %s: %w", path, err)
	}
	if err := ctx.Err(); err != nil {
		return Set{}, err
	}
	return ParseCategories(data)
}

// ParseCategories validates YAML and returns categories plus rules.
func ParseCategories(data []byte) (Set, error) {
	if len(strings.TrimSpace(string(data))) == 0 {
		return Set{}, errors.New("categories: file is empty")
	}
	var doc yaml.Node
	if err := yaml.Unmarshal(data, &doc); err != nil {
		return Set{}, errInvalidCategories
	}
	if err := validateCategoryTree(&doc); err != nil {
		return Set{}, err
	}
	var file categoryFile
	dec := yaml.NewDecoder(bytes.NewReader(data))
	dec.KnownFields(true)
	if err := dec.Decode(&file); err != nil {
		if errors.Is(err, io.EOF) {
			return Set{}, errors.New("categories: file is empty")
		}
		return Set{}, errInvalidCategories
	}
	set, err := normalize(file)
	if err != nil {
		return Set{}, err
	}
	return set, nil
}

func normalize(file categoryFile) (Set, error) {
	user := 0
	seen := make(map[string]struct{}, len(file.Categories))
	for i := range file.Categories {
		cat := file.Categories[i]
		if !slugPattern.MatchString(cat.Key) {
			return Set{}, errors.New("categories: a category key must be an ASCII slug")
		}
		if _, ok := seen[cat.Key]; ok {
			return Set{}, fmt.Errorf("categories: duplicate key %q", cat.Key)
		}
		seen[cat.Key] = struct{}{}
		if cat.Key != NeedsReview && cat.Key != KeepInInbox {
			user++
		}
		if cat.Key == NeverTouch {
			return Set{}, errors.New("categories: never_touch is a rule action, not a category")
		}
		if cat.Key != NeedsReview && cat.Key != KeepInInbox {
			if strings.TrimSpace(cat.Description) == "" {
				return Set{}, fmt.Errorf("categories: %q is missing a description", cat.Key)
			}
			switch cat.Action {
			case "move", "label":
				if strings.TrimSpace(cat.Folder) == "" {
					return Set{}, fmt.Errorf("categories: %q is missing a folder", cat.Key)
				}
			case "none":
			default:
				return Set{}, fmt.Errorf("categories: %q has an invalid action", cat.Key)
			}
		}
		if cat.MinConfidence < 0 || cat.MinConfidence > 1 {
			return Set{}, fmt.Errorf("categories: %q has an invalid min_confidence", cat.Key)
		}
	}
	if user > maxUserCategories {
		return Set{}, fmt.Errorf("categories: at most %d categories", maxUserCategories)
	}
	rules, err := prepareRules(file.Rules, append(file.Categories, reservedNeeds(), reservedKeep()))
	if err != nil {
		return Set{}, err
	}
	return Set{Categories: withReserved(file.Categories), Rules: rules}, nil
}

func withReserved(cats []Category) []Category {
	out := make([]Category, 0, len(cats)+2)
	var hasNeeds, hasKeep bool
	for _, cat := range cats {
		switch cat.Key {
		case NeedsReview:
			hasNeeds = true
			base := reservedNeeds()
			if cat.Name != "" {
				base.Name = cat.Name
			}
			if cat.NameRU != "" {
				base.NameRU = cat.NameRU
			}
			out = append(out, base)
		case KeepInInbox:
			hasKeep = true
			base := reservedKeep()
			if cat.Name != "" {
				base.Name = cat.Name
			}
			if cat.NameRU != "" {
				base.NameRU = cat.NameRU
			}
			if strings.TrimSpace(cat.Description) != "" {
				base.Description = cat.Description
			}
			if cat.MinConfidence > 0 {
				base.MinConfidence = cat.MinConfidence
			}
			out = append(out, base)
		default:
			out = append(out, cat)
		}
	}
	if !hasNeeds {
		out = append(out, reservedNeeds())
	}
	if !hasKeep {
		out = append(out, reservedKeep())
	}
	return out
}

func reservedNeeds() Category {
	return Category{
		Key:         NeedsReview,
		Name:        "Needs review",
		NameRU:      "Нужно проверить",
		Description: NeedsReviewCriteria,
		Folder:      NeedsReviewFolder,
		Action:      "move",
	}
}

func reservedKeep() Category {
	return Category{
		Key:         KeepInInbox,
		Name:        "Keep in Inbox",
		NameRU:      "Оставить во входящих",
		Description: KeepInInboxCriteria,
		Action:      "none",
	}
}

func findCategory(cats []Category, key string) (Category, bool) {
	for _, cat := range cats {
		if cat.Key == key {
			return cat, true
		}
	}
	return Category{}, false
}

func starterCategories() []Category {
	return []Category{
		{
			Key:         "invoices_receipts",
			Name:        "Invoices",
			NameRU:      "Счета и чеки",
			Description: "Bills, invoices, receipts, and payment confirmations. Not shipping notices or marketing that only mentions a price.",
			Folder:      "Invoices",
			Action:      "move",
		},
		{
			Key:         "newsletters",
			Name:        "Newsletters",
			NameRU:      "Рассылки",
			Description: "Recurring editorial mail the recipient subscribed to. Not one-off account notices or receipts.",
			Folder:      "Newsletters",
			Action:      "move",
		},
		{
			Key:         "notifications",
			Name:        "Notifications",
			NameRU:      "Уведомления",
			Description: "Automated account, shipping, or product notices that are not newsletters and not security alerts. Not a personal conversation.",
			Folder:      "Notifications",
			Action:      "move",
		},
		{
			Key:         "social",
			Name:        "Social",
			NameRU:      "Соцсети",
			Description: "Notifications and messages from social networks. Not work mail and not receipts.",
			Folder:      "Social",
			Action:      "move",
		},
		{
			Key:         "travel",
			Name:        "Travel",
			NameRU:      "Поездки",
			Description: "Tickets, bookings, itineraries, and travel changes. Not a general newsletter from a travel brand.",
			Folder:      "Travel",
			Action:      "move",
		},
		{
			Key:         "support_requests",
			Name:        "Support",
			NameRU:      "Поддержка",
			Description: "A request for help from a person, or a reply in a support thread. Not an automated security alert.",
			Folder:      "Support",
			Action:      "move",
		},
		{
			Key:         "security_alerts",
			Name:        "Security",
			NameRU:      "Оповещения безопасности",
			Description: "Sign-in alerts, password resets, and security warnings. Not an ordinary newsletter.",
			Action:      "none",
		},
		{
			Key:         "personal",
			Name:        "Personal",
			NameRU:      "Личное",
			Description: "Mail from a person that is not about work. Not bulk mail.",
			Folder:      "Personal",
			Action:      "move",
		},
		{
			Key:         "work",
			Name:        "Work",
			NameRU:      "Работа",
			Description: "Mail about the recipient's job, colleagues, or workplace. Not personal mail and not a newsletter.",
			Folder:      "Work",
			Action:      "move",
		},
		{
			Key:         "promotions",
			Name:        "Promotions",
			NameRU:      "Акции",
			Description: "Marketing, discounts, and sales offers. Not a receipt and not an account notice.",
			Folder:      "Promotions",
			Action:      "move",
		},
	}
}

func validateCategoryTree(doc *yaml.Node) error {
	if err := walkCategorySecrets(doc); err != nil {
		return err
	}
	root := categoryRoot(doc)
	if root == nil || categoryNull(root) {
		return errors.New("categories: file is empty")
	}
	if root.Kind != yaml.MappingNode {
		return fmt.Errorf("categories: line %d must be a mapping", root.Line)
	}
	return walkCategoryMapping(root, fileFields, func(key string, keyNode, val *yaml.Node) error {
		switch key {
		case "categories":
			return validateCategoryList(val)
		case "rules":
			return validateRuleList(val)
		default:
			return fmt.Errorf("categories: unknown field %q at line %d", keyNode.Value, keyNode.Line)
		}
	})
}

func validateCategoryList(val *yaml.Node) error {
	if categoryNull(val) {
		return nil
	}
	if val.Kind != yaml.SequenceNode {
		return fmt.Errorf("categories: field \"categories\" at line %d must be a list", val.Line)
	}
	for _, item := range val.Content {
		if item.Kind != yaml.MappingNode {
			return fmt.Errorf("categories: category at line %d must be a mapping", item.Line)
		}
		if err := walkCategoryMapping(item, categoryFields, func(key string, keyNode, field *yaml.Node) error {
			switch key {
			case "examples":
				return validateStringList(keyNode, field)
			case "action":
				return checkCategoryChoice(keyNode, field, "move", "label", "none")
			case "min_confidence":
				return checkCategoryUnit(keyNode, field)
			default:
				return checkCategoryScalar(keyNode, field)
			}
		}); err != nil {
			return err
		}
	}
	return nil
}

func validateRuleList(val *yaml.Node) error {
	if categoryNull(val) {
		return nil
	}
	if val.Kind != yaml.SequenceNode {
		return fmt.Errorf("categories: field \"rules\" at line %d must be a list", val.Line)
	}
	for _, item := range val.Content {
		if item.Kind != yaml.MappingNode {
			return fmt.Errorf("categories: rule at line %d must be a mapping", item.Line)
		}
		if err := walkCategoryMapping(item, ruleFields, func(key string, keyNode, field *yaml.Node) error {
			switch key {
			case "keywords":
				return validateStringList(keyNode, field)
			case "subject":
				return checkSubjectPattern(field)
			case "action":
				return checkCategoryChoice(keyNode, field, KeepInInbox, NeverTouch)
			default:
				return checkCategoryScalar(keyNode, field)
			}
		}); err != nil {
			return err
		}
	}
	return nil
}

func validateStringList(keyNode, val *yaml.Node) error {
	if categoryNull(val) {
		return nil
	}
	if val.Kind != yaml.SequenceNode {
		return fmt.Errorf("categories: field %q at line %d must be a list", keyNode.Value, keyNode.Line)
	}
	for _, item := range val.Content {
		if err := checkCategoryScalar(keyNode, item); err != nil {
			return err
		}
	}
	return nil
}

func checkSubjectPattern(val *yaml.Node) error {
	if categoryNull(val) || (val.Kind == yaml.ScalarNode && val.Value == "") {
		return nil
	}
	if val.Kind != yaml.ScalarNode {
		return fmt.Errorf("categories: field \"subject\" at line %d must be a string", val.Line)
	}
	if _, err := regexp.Compile(val.Value); err != nil {
		return fmt.Errorf("categories: rule subject at line %d is not a valid regular expression", val.Line)
	}
	return nil
}

func walkCategoryMapping(n *yaml.Node, allowed map[string]struct{}, visit func(key string, keyNode, val *yaml.Node) error) error {
	if len(n.Content)%2 != 0 {
		return errInvalidCategories
	}
	seen := make(map[string]struct{}, len(n.Content)/2)
	for i := 0; i < len(n.Content); i += 2 {
		keyNode := n.Content[i]
		field := n.Content[i+1]
		if keyNode.Kind != yaml.ScalarNode {
			return errInvalidCategories
		}
		if keyNode.Value == "<<" {
			return errors.New("categories: YAML anchors and aliases are not allowed")
		}
		canonical := secrets.CanonicalKey(keyNode.Value)
		if _, ok := seen[canonical]; ok {
			return fmt.Errorf("categories: duplicate field %q at line %d", keyNode.Value, keyNode.Line)
		}
		seen[canonical] = struct{}{}
		if _, ok := allowed[canonical]; !ok {
			return fmt.Errorf("categories: unknown field %q at line %d", keyNode.Value, keyNode.Line)
		}
		if err := visit(canonical, keyNode, field); err != nil {
			return err
		}
	}
	return nil
}

func walkCategorySecrets(n *yaml.Node) error {
	if n == nil {
		return nil
	}
	if n.Kind == yaml.AliasNode || n.Anchor != "" {
		return errors.New("categories: YAML anchors and aliases are not allowed")
	}
	switch n.Kind {
	case yaml.DocumentNode, yaml.SequenceNode:
		for _, child := range n.Content {
			if err := walkCategorySecrets(child); err != nil {
				return err
			}
		}
	case yaml.MappingNode:
		if len(n.Content)%2 != 0 {
			return errInvalidCategories
		}
		for i := 0; i < len(n.Content); i += 2 {
			keyNode := n.Content[i]
			if keyNode.Kind == yaml.ScalarNode && secrets.IsSecretConfigKey(keyNode.Value) {
				return fmt.Errorf("categories: field %q at line %d is not allowed; use the keyring or an environment variable", keyNode.Value, keyNode.Line)
			}
			if err := walkCategorySecrets(keyNode); err != nil {
				return err
			}
			if err := walkCategorySecrets(n.Content[i+1]); err != nil {
				return err
			}
		}
	}
	return nil
}

func checkCategoryChoice(keyNode, val *yaml.Node, choices ...string) error {
	if categoryNull(val) || (val.Kind == yaml.ScalarNode && val.Value == "") {
		return nil
	}
	if val.Kind != yaml.ScalarNode {
		return fmt.Errorf("categories: field %q at line %d has an invalid value", keyNode.Value, keyNode.Line)
	}
	for _, choice := range choices {
		if val.Value == choice {
			return nil
		}
	}
	return fmt.Errorf("categories: field %q at line %d has an invalid value", keyNode.Value, keyNode.Line)
}

func checkCategoryUnit(keyNode, val *yaml.Node) error {
	if categoryNull(val) || (val.Kind == yaml.ScalarNode && val.Value == "") {
		return nil
	}
	if val.Kind != yaml.ScalarNode {
		return fmt.Errorf("categories: field %q at line %d must be a number", keyNode.Value, val.Line)
	}
	n, err := strconv.ParseFloat(val.Value, 64)
	if err != nil || n <= 0 || n > 1 {
		return fmt.Errorf("categories: field %q at line %d must be greater than 0 and at most 1", keyNode.Value, val.Line)
	}
	return nil
}

func checkCategoryScalar(keyNode, val *yaml.Node) error {
	if categoryNull(val) {
		return nil
	}
	if val.Kind != yaml.ScalarNode {
		return fmt.Errorf("categories: field %q at line %d must be a string", keyNode.Value, keyNode.Line)
	}
	if _, ok := categoryText(val.Value); !ok {
		return fmt.Errorf("categories: field %q at line %d is empty or invalid", keyNode.Value, keyNode.Line)
	}
	return nil
}

func categoryText(value string) (string, bool) {
	if strings.TrimSpace(value) == "" && value != "" {
		return "", false
	}
	for _, r := range value {
		if r == '\n' || r == '\t' || r == '\r' {
			continue
		}
		if r < 0x20 || r == 0x7f {
			return "", false
		}
	}
	return value, true
}

func categoryNull(n *yaml.Node) bool {
	return n != nil && n.Tag == "!!null"
}

func categoryRoot(doc *yaml.Node) *yaml.Node {
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
