package classify

import (
	"bufio"
	"bytes"
	"encoding/json"
	"errors"
	"io"
	"strings"
	"time"

	netmail "net/mail"

	"github.com/prippa/mail-sort/internal/message"
)

var (
	// ErrEmptyStdin means classify --stdin was given no message.
	ErrEmptyStdin = errors.New("classify: stdin is empty")
	// ErrBadStdin means the input was neither JSON nor a header block.
	ErrBadStdin = errors.New("classify: stdin is not a message")
)

// ParseStdin reads one message. A leading "{" is JSON. Anything else is a
// header block, a blank line, then the body. The body is cleaned.
func ParseStdin(r io.Reader, maxChars int) (Input, error) {
	data, err := io.ReadAll(io.LimitReader(r, maxCategoryBytes+1))
	if err != nil {
		return Input{}, err
	}
	if len(data) > maxCategoryBytes {
		return Input{}, errors.New("classify: stdin exceeds 1MiB")
	}
	if len(bytes.TrimSpace(data)) == 0 {
		return Input{}, ErrEmptyStdin
	}
	if bytes.HasPrefix(bytes.TrimSpace(data), []byte("{")) {
		return parseJSONMessage(data, maxChars)
	}
	return parseHeaderMessage(data, maxChars)
}

type stdinDoc struct {
	From        json.RawMessage     `json:"from"`
	To          json.RawMessage     `json:"to"`
	Cc          json.RawMessage     `json:"cc"`
	Subject     string              `json:"subject"`
	Date        string              `json:"date"`
	IsBulk      *bool               `json:"is_bulk"`
	Attachments json.RawMessage     `json:"attachments"`
	Headers     map[string][]string `json:"headers"`
	Body        string              `json:"body"`
	HTML        bool                `json:"html"`
}

func parseJSONMessage(data []byte, maxChars int) (Input, error) {
	var doc stdinDoc
	if err := json.Unmarshal(bytes.TrimSpace(data), &doc); err != nil {
		return Input{}, ErrBadStdin
	}
	from, err := parseAddressField(doc.From)
	if err != nil {
		return Input{}, ErrBadStdin
	}
	to, err := parseAddressField(doc.To)
	if err != nil {
		return Input{}, ErrBadStdin
	}
	cc, err := parseAddressField(doc.Cc)
	if err != nil {
		return Input{}, ErrBadStdin
	}
	attachments, err := parseAttachments(doc.Attachments)
	if err != nil {
		return Input{}, ErrBadStdin
	}
	var when time.Time
	if strings.TrimSpace(doc.Date) != "" {
		when, err = time.Parse(time.RFC3339, doc.Date)
		if err != nil {
			return Input{}, ErrBadStdin
		}
	}
	msg := message.Message{
		From:        from,
		To:          to,
		Cc:          cc,
		Subject:     doc.Subject,
		Date:        when,
		Attachments: attachments,
		Body:        cleanBody(doc.Body, doc.HTML, maxChars),
	}
	if doc.IsBulk != nil {
		msg.IsBulk = *doc.IsBulk
	} else {
		msg.IsBulk = bulkHeaders(doc.Headers)
	}
	return Input{Message: msg, Headers: doc.Headers}, nil
}

func parseHeaderMessage(data []byte, maxChars int) (Input, error) {
	text := strings.ReplaceAll(string(data), "\r\n", "\n")
	head, body, _ := strings.Cut(text, "\n\n")
	headers := make(map[string][]string)
	var attachments []message.Attachment
	scanner := bufio.NewScanner(strings.NewReader(head))
	var name, value string
	flush := func() {
		if name == "" {
			return
		}
		headers[name] = append(headers[name], strings.TrimSpace(value))
		name, value = "", ""
	}
	for scanner.Scan() {
		line := scanner.Text()
		if strings.HasPrefix(line, " ") || strings.HasPrefix(line, "\t") {
			if name == "" {
				return Input{}, ErrBadStdin
			}
			value += " " + strings.TrimSpace(line)
			continue
		}
		flush()
		key, rest, ok := strings.Cut(line, ":")
		if !ok || strings.TrimSpace(key) == "" {
			return Input{}, ErrBadStdin
		}
		name = strings.TrimSpace(key)
		value = strings.TrimSpace(rest)
	}
	if err := scanner.Err(); err != nil {
		return Input{}, ErrBadStdin
	}
	flush()
	if len(headers) == 0 && strings.TrimSpace(body) == "" {
		return Input{}, ErrBadStdin
	}
	msg := message.Message{
		From:    parseAddressList(headerJoin(headers, "From")),
		To:      parseAddressList(headerJoin(headers, "To")),
		Cc:      parseAddressList(headerJoin(headers, "Cc")),
		Subject: headerJoin(headers, "Subject"),
		Body:    cleanBody(body, strings.Contains(strings.ToLower(headerJoin(headers, "Content-Type")), "text/html"), maxChars),
	}
	if raw := headerJoin(headers, "Date"); raw != "" {
		when, err := netmail.ParseDate(raw)
		if err != nil {
			when, err = time.Parse(time.RFC3339, raw)
			if err != nil {
				return Input{}, ErrBadStdin
			}
		}
		msg.Date = when
	}
	if raw, ok := takeHeader(headers, "Bulk"); ok {
		switch strings.ToLower(raw) {
		case "true", "yes":
			msg.IsBulk = true
		case "false", "no":
			msg.IsBulk = false
		default:
			return Input{}, ErrBadStdin
		}
	} else {
		msg.IsBulk = bulkHeaders(headers)
	}
	for _, value := range takeHeaders(headers, "Attachment") {
		attachments = append(attachments, attachmentFromLine(value))
	}
	msg.Attachments = attachments
	return Input{Message: msg, Headers: headers}, nil
}

func cleanBody(body string, html bool, maxChars int) string {
	if html {
		body = message.HTMLToText(body)
	}
	return message.Clean(body, maxChars)
}

func parseAddressField(raw json.RawMessage) ([]message.Address, error) {
	if len(bytes.TrimSpace(raw)) == 0 || string(bytes.TrimSpace(raw)) == "null" {
		return nil, nil
	}
	var one string
	if err := json.Unmarshal(raw, &one); err == nil {
		if strings.TrimSpace(one) == "" {
			return nil, nil
		}
		return []message.Address{parseOneAddress(one)}, nil
	}
	var many []string
	if err := json.Unmarshal(raw, &many); err != nil {
		return nil, err
	}
	out := make([]message.Address, 0, len(many))
	for _, item := range many {
		if strings.TrimSpace(item) != "" {
			out = append(out, parseOneAddress(item))
		}
	}
	return out, nil
}

func parseAddressList(value string) []message.Address {
	if strings.TrimSpace(value) == "" {
		return nil
	}
	parts := strings.Split(value, ",")
	out := make([]message.Address, 0, len(parts))
	for _, part := range parts {
		if strings.TrimSpace(part) != "" {
			out = append(out, parseOneAddress(part))
		}
	}
	return out
}

func parseOneAddress(value string) message.Address {
	value = strings.TrimSpace(value)
	if start := strings.LastIndex(value, "<"); start >= 0 && strings.HasSuffix(value, ">") {
		return message.Address{
			Name:  strings.Trim(strings.TrimSpace(value[:start]), "\""),
			Email: strings.TrimSpace(strings.TrimSuffix(value[start+1:], ">")),
		}
	}
	return message.Address{Email: value}
}

func parseAttachments(raw json.RawMessage) ([]message.Attachment, error) {
	if len(bytes.TrimSpace(raw)) == 0 || string(bytes.TrimSpace(raw)) == "null" {
		return nil, nil
	}
	var objects []struct {
		Name      string `json:"name"`
		MediaType string `json:"media_type"`
		Size      uint32 `json:"size"`
	}
	if err := json.Unmarshal(raw, &objects); err == nil {
		out := make([]message.Attachment, 0, len(objects))
		for _, item := range objects {
			if item.Name == "" && item.MediaType == "" {
				continue
			}
			out = append(out, message.Attachment{Name: item.Name, MediaType: item.MediaType, Size: item.Size})
		}
		if len(out) > 0 || len(objects) == 0 {
			return out, nil
		}
	}
	var names []string
	if err := json.Unmarshal(raw, &names); err != nil {
		return nil, err
	}
	out := make([]message.Attachment, 0, len(names))
	for _, name := range names {
		if strings.TrimSpace(name) != "" {
			out = append(out, message.Attachment{Name: name})
		}
	}
	return out, nil
}

func attachmentFromLine(value string) message.Attachment {
	fields := strings.Fields(value)
	if len(fields) == 0 {
		return message.Attachment{}
	}
	if len(fields) == 1 {
		return message.Attachment{Name: fields[0]}
	}
	return message.Attachment{Name: fields[0], MediaType: fields[1]}
}

func headerJoin(headers map[string][]string, name string) string {
	for key, values := range headers {
		if strings.EqualFold(key, name) && len(values) > 0 {
			return values[0]
		}
	}
	return ""
}

func takeHeader(headers map[string][]string, name string) (string, bool) {
	for key, values := range headers {
		if strings.EqualFold(key, name) && len(values) > 0 {
			delete(headers, key)
			return values[0], true
		}
	}
	return "", false
}

func takeHeaders(headers map[string][]string, name string) []string {
	var out []string
	for key, values := range headers {
		if strings.EqualFold(key, name) {
			out = append(out, values...)
			delete(headers, key)
		}
	}
	return out
}

func bulkHeaders(headers map[string][]string) bool {
	if headerJoin(headers, "List-Id") != "" || headerJoin(headers, "List-Unsubscribe") != "" {
		return true
	}
	switch strings.ToLower(headerJoin(headers, "Precedence")) {
	case "bulk", "list", "junk":
		return true
	default:
		return false
	}
}
