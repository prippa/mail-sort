package mail

import (
	"strings"

	"github.com/emersion/go-imap/v2"
	"github.com/prippa/mail-sort/internal/message"
)

type bodyChoice struct {
	Part      []int
	Specifier imap.PartSpecifier
	Media     string
	Charset   string
	Encoding  string
	HTML      bool
	Limit     int64
}

func chooseBody(structure imap.BodyStructure) (bodyChoice, []message.Attachment) {
	fallback := bodyChoice{
		Specifier: imap.PartSpecifierText,
		Media:     "text/plain",
		Charset:   "utf-8",
		Limit:     message.PlainBytes,
	}
	if structure == nil {
		return fallback, nil
	}
	var plain, html *bodyChoice
	var attachments []message.Attachment
	walkParts(structure, nil, func(path []int, part *imap.BodyStructureSinglePart) {
		media := part.MediaType()
		filename := part.Filename()
		disposition := ""
		if disp := part.Disposition(); disp != nil {
			disposition = strings.ToLower(disp.Value)
		}
		attached := disposition == "attachment" || (filename != "" && media != "text/plain" && media != "text/html")
		if attached {
			name := filename
			if name == "" {
				name = "(unnamed)"
			}
			attachments = append(attachments, message.Attachment{
				Name:      name,
				MediaType: media,
				Size:      part.Size,
			})
			return
		}
		choice := bodyChoice{
			Part:     append([]int(nil), path...),
			Media:    media,
			Charset:  param(part.Params, "charset"),
			Encoding: part.Encoding,
		}
		switch media {
		case "text/plain":
			if plain == nil {
				choice.Limit = message.PlainBytes
				plain = &choice
			}
		case "text/html":
			if html == nil {
				choice.HTML = true
				choice.Limit = message.HTMLBytes
				html = &choice
			}
		}
	})
	switch {
	case plain != nil:
		return *plain, attachments
	case html != nil:
		return *html, attachments
	default:
		return fallback, attachments
	}
}

func walkParts(part imap.BodyStructure, path []int, visit func([]int, *imap.BodyStructureSinglePart)) {
	switch part := part.(type) {
	case *imap.BodyStructureMultiPart:
		for i, child := range part.Children {
			next := append(append([]int{}, path...), i+1)
			walkParts(child, next, visit)
		}
	case *imap.BodyStructureSinglePart:
		section := path
		if len(section) == 0 {
			section = []int{1}
		}
		visit(section, part)
		if part.MessageRFC822 == nil || part.MessageRFC822.BodyStructure == nil {
			return
		}
		nested := part.MessageRFC822.BodyStructure
		if _, single := nested.(*imap.BodyStructureSinglePart); single {
			walkParts(nested, append(append([]int{}, section...), 1), visit)
			return
		}
		walkParts(nested, section, visit)
	}
}

func param(params map[string]string, key string) string {
	for name, value := range params {
		if strings.EqualFold(name, key) {
			return value
		}
	}
	return ""
}
