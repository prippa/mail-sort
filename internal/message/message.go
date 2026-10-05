package message

import (
	"strings"
	"time"
)

const (
	// DefaultMaxChars is the body cap used when a profile leaves max_chars unset.
	DefaultMaxChars = 1500
	// PlainBytes is the partial-fetch size for a text/plain part.
	PlainBytes = 8192
	// HTMLBytes is the partial-fetch size for an HTML-only message.
	HTMLBytes = 65536
)

// Address is a display name and mailbox. Both are already decoded text.
type Address struct {
	Name  string
	Email string
}

// Attachment is metadata only. The file bytes are never fetched.
type Attachment struct {
	Name      string
	MediaType string
	Size      uint32
}

// Message is the cleaned view used by later classification.
type Message struct {
	UID         uint32
	From        []Address
	To          []Address
	Cc          []Address
	Subject     string
	Date        time.Time
	MessageID   string
	IsBulk      bool
	Attachments []Attachment
	Body        string
}

// Input is one fetched message. Header keys are canonical MIME keys
// ("List-Id"). Body is the selected part after the server transfer, still
// in its Content-Transfer-Encoding.
type Input struct {
	UID         uint32
	From        []Address
	To          []Address
	Cc          []Address
	Subject     string
	Date        time.Time
	MessageID   string
	Header      map[string][]string
	Attachments []Attachment
	MediaType   string
	Charset     string
	Encoding    string
	HTML        bool
	Body        []byte
	MaxChars    int
}

// Prepare decodes the selected part, converts HTML to text, and cleans it.
func Prepare(in Input) (Message, error) {
	text, err := DecodeText(in.Encoding, in.Charset, in.Body)
	if err != nil {
		return Message{}, err
	}
	if in.HTML {
		text = HTMLToText(text)
	}
	maxChars := in.MaxChars
	if maxChars <= 0 {
		maxChars = DefaultMaxChars
	}
	return Message{
		UID:         in.UID,
		From:        in.From,
		To:          in.To,
		Cc:          in.Cc,
		Subject:     in.Subject,
		Date:        in.Date,
		MessageID:   in.MessageID,
		IsBulk:      isBulk(in.Header),
		Attachments: in.Attachments,
		Body:        Clean(text, maxChars),
	}, nil
}

func isBulk(header map[string][]string) bool {
	if header == nil {
		return false
	}
	if field(header, "List-Id") != "" || field(header, "List-Unsubscribe") != "" {
		return true
	}
	switch strings.ToLower(field(header, "Precedence")) {
	case "bulk", "list", "junk":
		return true
	default:
		return false
	}
}

func field(header map[string][]string, key string) string {
	for name, values := range header {
		if strings.EqualFold(name, key) && len(values) > 0 {
			return strings.TrimSpace(values[0])
		}
	}
	return ""
}
