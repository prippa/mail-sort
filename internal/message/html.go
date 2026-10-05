package message

import (
	"strings"

	"golang.org/x/net/html"
)

// HTMLToText extracts visible text. It does not fetch remote images, follow
// links, or run scripts.
func HTMLToText(src string) string {
	tokenizer := html.NewTokenizer(strings.NewReader(src))
	var b strings.Builder
	skip := 0
	for {
		token := tokenizer.Next()
		switch token {
		case html.ErrorToken:
			return b.String()
		case html.StartTagToken, html.SelfClosingTagToken:
			name := tagName(tokenizer)
			if isSkipped(name) && token != html.SelfClosingTagToken {
				skip++
				continue
			}
			if skip > 0 {
				continue
			}
			if isBlock(name) {
				b.WriteByte('\n')
			}
		case html.EndTagToken:
			name := tagName(tokenizer)
			if isSkipped(name) && skip > 0 {
				skip--
				continue
			}
			if skip > 0 {
				continue
			}
			if isBlock(name) {
				b.WriteByte('\n')
			}
		case html.TextToken:
			if skip > 0 {
				continue
			}
			b.Write(tokenizer.Text())
		}
	}
}

func tagName(tokenizer *html.Tokenizer) string {
	name, _ := tokenizer.TagName()
	return string(name)
}

func isSkipped(tag string) bool {
	switch tag {
	case "script", "style", "noscript", "head", "title":
		return true
	default:
		return false
	}
}

func isBlock(tag string) bool {
	switch tag {
	case "br", "p", "div", "li", "tr", "blockquote", "pre", "h1", "h2", "h3", "h4", "h5", "h6":
		return true
	default:
		return false
	}
}
