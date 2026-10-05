package classify

import (
	"fmt"
	"regexp"
	"strings"

	"github.com/prippa/mail-sort/internal/message"
)

// Rule matches a message before any model runs. Every set field must match.
// The first matching rule wins.
type Rule struct {
	Name        string   `yaml:"name,omitempty"`
	From        string   `yaml:"from,omitempty"`
	FromDomain  string   `yaml:"from_domain,omitempty"`
	To          string   `yaml:"to,omitempty"`
	Subject     string   `yaml:"subject,omitempty"`
	Header      string   `yaml:"header,omitempty"`
	HeaderValue string   `yaml:"header_value,omitempty"`
	Keywords    []string `yaml:"keywords,omitempty"`
	Attachment  string   `yaml:"attachment,omitempty"`
	Category    string   `yaml:"category,omitempty"`
	Action      string   `yaml:"action,omitempty"`

	subject *regexp.Regexp
}

func prepareRules(rules []Rule, cats []Category) ([]Rule, error) {
	out := make([]Rule, len(rules))
	for i, rule := range rules {
		if !rule.hasCondition() {
			return nil, fmt.Errorf("categories: rule %d has no condition", i+1)
		}
		if rule.HeaderValue != "" && strings.TrimSpace(rule.Header) == "" {
			return nil, fmt.Errorf("categories: rule %d sets header_value without header", i+1)
		}
		switch {
		case rule.Category != "" && rule.Action != "":
			return nil, fmt.Errorf("categories: rule %d sets both a category and an action", i+1)
		case rule.Category != "":
			if _, ok := findCategory(cats, rule.Category); !ok {
				return nil, fmt.Errorf("categories: rule %d refers to an unknown category", i+1)
			}
		case rule.Action == KeepInInbox || rule.Action == NeverTouch:
		default:
			return nil, fmt.Errorf("categories: rule %d is missing an action", i+1)
		}
		if rule.Subject != "" {
			re, err := regexp.Compile(rule.Subject)
			if err != nil {
				return nil, fmt.Errorf("categories: rule %d subject is not a valid regular expression", i+1)
			}
			rule.subject = re
		}
		out[i] = rule
	}
	return out, nil
}

func (r Rule) hasCondition() bool {
	return r.From != "" || r.FromDomain != "" || r.To != "" || r.Subject != "" ||
		r.Header != "" || len(r.Keywords) > 0 || r.Attachment != ""
}

func matchRules(rules []Rule, cats []Category, in Input) (Decision, bool) {
	for _, rule := range rules {
		if rule.matches(in) {
			return decisionFromRule(rule, cats), true
		}
	}
	return Decision{}, false
}

func (r Rule) matches(in Input) bool {
	if r.From != "" && !anyAddress(in.Message.From, func(addr message.Address) bool {
		return matchMailbox(r.From, addr.Email)
	}) {
		return false
	}
	if r.FromDomain != "" && !anyAddress(in.Message.From, func(addr message.Address) bool {
		return matchDomain(r.FromDomain, domainOf(addr.Email))
	}) {
		return false
	}
	if r.To != "" && !anyAddress(in.Message.To, func(addr message.Address) bool {
		return matchMailbox(r.To, addr.Email)
	}) {
		return false
	}
	if r.subject != nil && !r.subject.MatchString(in.Message.Subject) {
		return false
	}
	if r.Header != "" && !matchHeader(in.Headers, r.Header, r.HeaderValue) {
		return false
	}
	if len(r.Keywords) > 0 && !matchKeywords(r.Keywords, in.Message.Subject, in.Message.Body) {
		return false
	}
	if r.Attachment != "" && !matchAttachment(r.Attachment, in.Message.Attachments) {
		return false
	}
	return true
}

func decisionFromRule(rule Rule, cats []Category) Decision {
	decision := Decision{Confidence: 1, Provider: "rule", Source: "rule"}
	switch rule.Action {
	case NeverTouch:
		decision.Category = NeverTouch
		decision.Action = NeverTouch
		return decision
	case KeepInInbox:
		decision.Category = KeepInInbox
		return decorate(decision, cats)
	default:
		decision.Category = rule.Category
		return decorate(decision, cats)
	}
}

func anyAddress(list []message.Address, pred func(message.Address) bool) bool {
	for _, addr := range list {
		if pred(addr) {
			return true
		}
	}
	return false
}

func matchMailbox(rule, actual string) bool {
	rule = emailOf(rule)
	actual = emailOf(actual)
	if rule == "" || actual == "" || !strings.Contains(rule, "@") || !strings.Contains(actual, "@") {
		return false
	}
	if rule == actual {
		return true
	}
	ruleLocal, ruleDomain, _ := strings.Cut(rule, "@")
	if strings.Contains(ruleLocal, "+") {
		return false
	}
	actualLocal, actualDomain, _ := strings.Cut(actual, "@")
	if ruleDomain != actualDomain {
		return false
	}
	base, _, tagged := strings.Cut(actualLocal, "+")
	return tagged && base == ruleLocal
}

func matchDomain(rule, host string) bool {
	rule = strings.ToLower(strings.TrimPrefix(strings.TrimSpace(rule), "@"))
	host = strings.ToLower(strings.TrimSpace(host))
	if rule == "" || host == "" {
		return false
	}
	return host == rule || strings.HasSuffix(host, "."+rule)
}

func domainOf(email string) string {
	email = emailOf(email)
	_, domain, ok := strings.Cut(email, "@")
	if !ok {
		return ""
	}
	return domain
}

func emailOf(value string) string {
	value = strings.TrimSpace(value)
	if start := strings.LastIndex(value, "<"); start >= 0 && strings.HasSuffix(value, ">") {
		value = strings.TrimSuffix(value[start+1:], ">")
	}
	return strings.ToLower(strings.TrimSpace(value))
}

func matchHeader(headers map[string][]string, name, want string) bool {
	var values []string
	for key, list := range headers {
		if strings.EqualFold(key, name) {
			values = append(values, list...)
		}
	}
	if want == "" {
		for _, value := range values {
			if strings.TrimSpace(value) != "" {
				return true
			}
		}
		return false
	}
	for _, value := range values {
		if strings.EqualFold(strings.TrimSpace(value), strings.TrimSpace(want)) {
			return true
		}
	}
	return false
}

func matchKeywords(keywords []string, subject, body string) bool {
	haystack := strings.ToLower(subject + "\n" + body)
	for _, keyword := range keywords {
		keyword = strings.ToLower(strings.TrimSpace(keyword))
		if keyword == "" || !strings.Contains(haystack, keyword) {
			return false
		}
	}
	return true
}

func matchAttachment(rule string, attachments []message.Attachment) bool {
	rule = strings.ToLower(strings.TrimSpace(rule))
	if rule == "" {
		return false
	}
	for _, attachment := range attachments {
		name := strings.ToLower(attachment.Name)
		media := strings.ToLower(attachment.MediaType)
		switch {
		case strings.HasPrefix(rule, "."):
			if strings.HasSuffix(name, rule) {
				return true
			}
		case strings.Contains(rule, "/"):
			if media == rule || strings.HasPrefix(media, rule) {
				return true
			}
		default:
			if strings.HasSuffix(name, "."+rule) || media == rule {
				return true
			}
		}
	}
	return false
}
