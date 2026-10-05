package message

import (
	"strings"
	"testing"
	"unicode/utf8"

	"golang.org/x/text/encoding/charmap"
)

func TestDecodeQuotedPrintableAndBase64(t *testing.T) {
	t.Parallel()
	got, err := DecodeText("quoted-printable", "utf-8", []byte("Caf=C3=A9"))
	if err != nil {
		t.Fatal(err)
	}
	if got != "Café" {
		t.Fatalf("qp = %q", got)
	}
	got, err = DecodeText("base64", "utf-8", []byte("SGVs bG8="))
	if err != nil {
		t.Fatal(err)
	}
	if got != "Hello" {
		t.Fatalf("b64 = %q", got)
	}
}

func TestDecodeCharsets(t *testing.T) {
	t.Parallel()
	hello := "Привет"
	for _, enc := range []struct {
		name string
		enc  interface{ Bytes([]byte) ([]byte, error) }
	}{
		{"windows-1251", charmap.Windows1251.NewEncoder()},
		{"koi8-r", charmap.KOI8R.NewEncoder()},
	} {
		raw, err := enc.enc.Bytes([]byte(hello))
		if err != nil {
			t.Fatal(err)
		}
		got, err := DecodeText("8bit", enc.name, raw)
		if err != nil {
			t.Fatal(err)
		}
		if got != hello {
			t.Fatalf("%s = %q", enc.name, got)
		}
	}
}

func TestHTMLOnlyDropsScript(t *testing.T) {
	t.Parallel()
	in := Input{
		HTML:      true,
		MediaType: "text/html",
		Charset:   "utf-8",
		Body: []byte(`<html><head><title>secret title</title></head><body>
<p>Hello <a href="https://shop.example/a">invoice</a></p>
<script>alert(1)</script></body></html>`),
	}
	got, err := Prepare(in)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(got.Body, "alert") || strings.Contains(got.Body, "secret title") || strings.Contains(got.Body, "<p>") {
		t.Fatalf("body = %q", got.Body)
	}
	if !strings.Contains(got.Body, "Hello") || !strings.Contains(got.Body, "invoice") {
		t.Fatalf("body = %q", got.Body)
	}
}

func TestCleanQuotesSignatureAndURL(t *testing.T) {
	t.Parallel()
	in := "Invoice https://shop.example/pay\n\nOn Monday Ada wrote:\n> old secret\n\n-- \nAda Lovelace\n"
	got := Clean(in, 1500)
	if strings.Contains(got, "old secret") || strings.Contains(got, "Lovelace") || strings.Contains(got, "https://") {
		t.Fatalf("cleaned = %q", got)
	}
	if !strings.Contains(got, "Invoice") || !strings.Contains(got, "shop.example") {
		t.Fatalf("cleaned = %q", got)
	}
}

func TestCleanRussianQuoteAndForward(t *testing.T) {
	t.Parallel()
	in := "Коротко\n\nИван Иванов написал(а):\n> цитата\n"
	got := Clean(in, 1500)
	if strings.Contains(got, "цитата") || !strings.Contains(got, "Коротко") {
		t.Fatalf("cleaned = %q", got)
	}
	forwarded := "Заметка\n\n-------- Пересылаемое сообщение --------\nОт: ada@example.com\n"
	got = Clean(forwarded, 1500)
	if strings.Contains(got, "ada@example.com") || !strings.Contains(got, "Заметка") {
		t.Fatalf("forward = %q", got)
	}
}

func TestCleanKeepsQuoteOnlyBody(t *testing.T) {
	t.Parallel()
	in := "> only a quote\n"
	got := Clean(in, 1500)
	if !strings.Contains(got, "only a quote") {
		t.Fatalf("cleaned = %q", got)
	}
}

func TestCleanCapsHugeBody(t *testing.T) {
	t.Parallel()
	got := Clean(strings.Repeat("я", 5000), 1500)
	if utf8.RuneCountInString(got) != 1500 {
		t.Fatalf("runes = %d", utf8.RuneCountInString(got))
	}
}

func TestIsBulk(t *testing.T) {
	t.Parallel()
	got, err := Prepare(Input{
		Header: map[string][]string{"List-Unsubscribe": {"<mailto:off@example.com>"}},
		Body:   []byte("News"),
	})
	if err != nil {
		t.Fatal(err)
	}
	if !got.IsBulk {
		t.Fatal("expected bulk")
	}
}

func FuzzClean(f *testing.F) {
	f.Add("Hello\n\nOn Monday Ada wrote:\n> secret\n")
	f.Add("Привет\n\nИван написал(а):\n> цитата\n")
	f.Add(strings.Repeat("a", 4000))
	f.Add("https://shop.example/a\n-- \nsig")
	f.Fuzz(func(t *testing.T, text string) {
		if len(text) > 32<<10 {
			text = text[:32<<10]
		}
		out := Clean(text, 1500)
		if utf8.RuneCountInString(out) > 1500 {
			t.Fatalf("runes = %d", utf8.RuneCountInString(out))
		}
	})
}
