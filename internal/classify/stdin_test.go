package classify

import (
	"strings"
	"testing"
)

func TestParseStdinJSONAndHeaders(t *testing.T) {
	t.Parallel()
	in, err := ParseStdin(strings.NewReader(`{
		"from":"Ada <ada@example.com>",
		"to":["me+news@example.com"],
		"subject":"Hello",
		"date":"2026-10-05T12:00:00Z",
		"attachments":[{"name":"a.pdf","media_type":"application/pdf"}],
		"body":"Fresh text\n\nOn Monday Ada wrote:\n> old"
	}`), 0)
	if err != nil {
		t.Fatal(err)
	}
	if len(in.Message.From) != 1 || in.Message.From[0].Email != "ada@example.com" || in.Message.To[0].Email != "me+news@example.com" {
		t.Fatalf("message=%+v", in.Message)
	}
	if !strings.Contains(in.Message.Body, "Fresh") || strings.Contains(in.Message.Body, "old") || strings.Contains(in.Message.Body, "wrote") {
		t.Fatalf("body=%q", in.Message.Body)
	}

	in, err = ParseStdin(strings.NewReader("From: ada@example.com\nList-Id: <news.example.com>\n\nHello list\n"), 0)
	if err != nil {
		t.Fatal(err)
	}
	if !in.Message.IsBulk || in.Headers["List-Id"][0] == "" {
		t.Fatalf("input=%+v", in)
	}

	_, err = ParseStdin(strings.NewReader("   \n"), 0)
	if err != ErrEmptyStdin {
		t.Fatal(err)
	}
	_, err = ParseStdin(strings.NewReader("{"), 0)
	if err != ErrBadStdin {
		t.Fatal(err)
	}
}
