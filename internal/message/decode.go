package message

import (
	"bytes"
	"encoding/base64"
	"fmt"
	"io"
	"mime/quotedprintable"
	"strings"

	"github.com/emersion/go-message/charset"
)

// DecodeText undoes Content-Transfer-Encoding and converts the charset to UTF-8.
// The error names the encoding or charset. It does not include the body.
func DecodeText(encoding, charsetName string, raw []byte) (string, error) {
	decoded, err := decodeTransfer(encoding, raw)
	if err != nil {
		return "", err
	}
	if len(decoded) > 1<<20 {
		decoded = decoded[:1<<20]
	}
	text, err := decodeCharset(charsetName, decoded)
	if err != nil {
		return "", err
	}
	return text, nil
}

func decodeTransfer(encoding string, raw []byte) ([]byte, error) {
	switch strings.ToLower(strings.TrimSpace(encoding)) {
	case "", "7bit", "8bit", "binary":
		return raw, nil
	case "base64":
		cleaned := bytes.Map(func(r rune) rune {
			switch r {
			case ' ', '\t', '\r', '\n':
				return -1
			default:
				return r
			}
		}, raw)
		out, err := base64.StdEncoding.DecodeString(string(cleaned))
		if err != nil {
			out, err = base64.RawStdEncoding.DecodeString(string(cleaned))
		}
		if err != nil {
			return nil, fmt.Errorf("message: base64 body cannot be decoded")
		}
		return out, nil
	case "quoted-printable":
		out, err := io.ReadAll(quotedprintable.NewReader(bytes.NewReader(raw)))
		if err != nil {
			return nil, fmt.Errorf("message: quoted-printable body cannot be decoded")
		}
		return out, nil
	default:
		return nil, fmt.Errorf("message: unsupported content transfer encoding %q", strings.ToLower(strings.TrimSpace(encoding)))
	}
}

func decodeCharset(charsetName string, raw []byte) (string, error) {
	name := strings.Trim(strings.TrimSpace(charsetName), "\"")
	switch strings.ToLower(name) {
	case "", "utf-8", "utf8", "us-ascii", "ascii":
		return string(raw), nil
	}
	reader, err := charset.Reader(name, bytes.NewReader(raw))
	if err != nil {
		return "", fmt.Errorf("message: charset %q is not supported", name)
	}
	out, err := io.ReadAll(io.LimitReader(reader, 1<<20))
	if err != nil {
		return "", fmt.Errorf("message: charset %q cannot be decoded", name)
	}
	return string(out), nil
}
