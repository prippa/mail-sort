package classify

import (
	"strings"
	"testing"
)

func TestStarterExportRoundTrip(t *testing.T) {
	t.Parallel()
	data, err := ExportStarter()
	if err != nil {
		t.Fatal(err)
	}
	set, err := ParseCategories(data)
	if err != nil {
		t.Fatal(err)
	}
	if len(set.Categories) != 12 {
		t.Fatalf("categories = %d", len(set.Categories))
	}
	needs, ok := findCategory(set.Categories, NeedsReview)
	if !ok || needs.Criteria() != NeedsReviewCriteria || needs.Folder != NeedsReviewFolder || needs.Action != "move" {
		t.Fatalf("needs_review = %+v", needs)
	}
	keep, ok := findCategory(set.Categories, KeepInInbox)
	if !ok || keep.Action != "none" || keep.Folder != "" {
		t.Fatalf("keep_in_inbox = %+v", keep)
	}
	security, ok := findCategory(set.Categories, "security_alerts")
	if !ok || security.Action != "none" {
		t.Fatalf("security = %+v", security)
	}
}

func TestCategoriesRejectSecretAnchorAndPattern(t *testing.T) {
	t.Parallel()
	const secret = "super-secret-value"
	_, err := ParseCategories([]byte("categories:\n  - key: work\n    api_key: " + secret + "\n"))
	if err == nil || strings.Contains(err.Error(), secret) || !strings.Contains(err.Error(), "api_key") {
		t.Fatalf("err = %v", err)
	}
	_, err = ParseCategories([]byte("base: &a\n  key: work\ncategories:\n  - *a\n"))
	if err == nil || !strings.Contains(err.Error(), "anchors") {
		t.Fatalf("err = %v", err)
	}
	const token = "zebra-token-pattern"
	_, err = ParseCategories([]byte("rules:\n  - subject: '(?P<" + token + "'\n    action: never_touch\n"))
	if err == nil || strings.Contains(err.Error(), token) || !strings.Contains(err.Error(), "regular expression") {
		t.Fatalf("err = %v", err)
	}
}

func TestTooManyCategories(t *testing.T) {
	t.Parallel()
	var b strings.Builder
	b.WriteString("categories:\n")
	for i := 0; i < 254; i++ {
		b.WriteString("  - key: c")
		if i < 10 {
			b.WriteString("00")
		} else if i < 100 {
			b.WriteString("0")
		}
		b.WriteString(itoa(i))
		b.WriteString("\n    name: N\n    description: Belongs here. Not elsewhere.\n    folder: F\n    action: move\n")
	}
	_, err := ParseCategories([]byte(b.String()))
	if err == nil || !strings.Contains(err.Error(), "253") {
		t.Fatalf("err = %v", err)
	}
}

func TestNeedsReviewCriteriaAreFixed(t *testing.T) {
	t.Parallel()
	set, err := ParseCategories([]byte(`
categories:
  - key: needs_review
    name: Custom
    description: Something else
    folder: Other
    action: none
`))
	if err != nil {
		t.Fatal(err)
	}
	cat, _ := findCategory(set.Categories, NeedsReview)
	if cat.Criteria() != NeedsReviewCriteria || cat.Folder != NeedsReviewFolder || cat.Action != "move" || cat.Name != "Custom" {
		t.Fatalf("category = %+v criteria=%q", cat, cat.Criteria())
	}
}

func itoa(n int) string {
	if n == 0 {
		return "0"
	}
	var digits [8]byte
	i := len(digits)
	for n > 0 {
		i--
		digits[i] = byte('0' + n%10)
		n /= 10
	}
	return string(digits[i:])
}
