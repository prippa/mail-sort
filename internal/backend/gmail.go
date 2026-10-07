package backend

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/url"
	"strings"
	"sync"
)

// Gmail files by RFC 5322 Message-ID using users.messages.list, messages.get,
// labels, and messages.modify.
// VERIFY: a live Gmail mailbox was not modified.
type Gmail struct {
	Access Access
	Base   string
	HTTP   *http.Client

	mu     sync.Mutex
	labels []gmailLabel
	loaded bool
}

type gmailLabel struct {
	ID   string
	Name string
}

// NewGmail returns a filer. An empty base uses the documented Gmail API host.
func NewGmail(access Access, base string, client *http.Client) *Gmail {
	return &Gmail{Access: access, Base: strings.TrimRight(base, "/"), HTTP: client}
}

func (g *Gmail) base() string {
	if g.Base != "" {
		return g.Base
	}
	return "https://gmail.googleapis.com/gmail/v1/users/me"
}

// MoveMessage adds the destination label. Leaving INBOX also removes the
// INBOX label. Coming back to INBOX removes the folder label. UNREAD is never
// removed. The message is not deleted.
func (g *Gmail) MoveMessage(ctx context.Context, messageID, from, to string) error {
	id, add, remove, err := g.plan(ctx, messageID, from, to, true)
	if err != nil {
		return err
	}
	return g.modify(ctx, id, add, remove)
}

// CopyMessage adds the destination label and leaves every other label,
// including INBOX.
func (g *Gmail) CopyMessage(ctx context.Context, messageID, to string) error {
	id, add, _, err := g.plan(ctx, messageID, "", to, false)
	if err != nil {
		return err
	}
	return g.modify(ctx, id, add, nil)
}

func (g *Gmail) plan(ctx context.Context, messageID, from, to string, move bool) (string, []string, []string, error) {
	messageID, err := cleanMessageID(messageID)
	if err != nil {
		return "", nil, nil, err
	}
	to, err = cleanFolder(to)
	if err != nil {
		return "", nil, nil, err
	}
	if move && !isInbox(from) {
		from, err = cleanFolder(from)
		if err != nil {
			return "", nil, nil, err
		}
	}
	found, labels, err := g.find(ctx, messageID)
	if err != nil {
		return "", nil, nil, err
	}
	if protectedGmail(labels) {
		return "", nil, nil, errors.New("backend: refusing to open that folder")
	}
	if move {
		if err := g.requireSource(ctx, labels, from); err != nil {
			return "", nil, nil, err
		}
	}
	toID, err := g.ensureLabel(ctx, to)
	if err != nil {
		return "", nil, nil, err
	}
	var remove []string
	if move && !strings.EqualFold(from, to) {
		switch {
		case isInbox(from) && !isInbox(to):
			remove = []string{"INBOX"}
		case !isInbox(from):
			fromID, lookupErr := g.lookupLabel(ctx, from)
			if lookupErr != nil {
				return "", nil, nil, lookupErr
			}
			remove = []string{fromID}
		}
	}
	for _, id := range remove {
		if id == "UNREAD" || id == "TRASH" || id == "SPAM" {
			return "", nil, nil, errors.New("backend: refusing to change that label")
		}
	}
	return found, []string{toID}, remove, nil
}

func (g *Gmail) requireSource(ctx context.Context, labels []string, from string) error {
	id := "INBOX"
	if !isInbox(from) {
		var err error
		id, err = g.lookupLabel(ctx, from)
		if errors.Is(err, errLabelMissing) {
			return errors.New("backend: message is not in that folder")
		}
		if err != nil {
			return err
		}
	}
	if !hasLabel(labels, id) {
		return errors.New("backend: message is not in that folder")
	}
	return nil
}

func hasLabel(labels []string, id string) bool {
	for _, label := range labels {
		if label == id {
			return true
		}
	}
	return false
}

func protectedGmail(labels []string) bool {
	for _, label := range labels {
		switch label {
		case "TRASH", "SPAM", "DRAFT", "SENT":
			return true
		}
	}
	return false
}

func (g *Gmail) find(ctx context.Context, messageID string) (string, []string, error) {
	token, err := tokenOf(ctx, g.Access)
	if err != nil {
		return "", nil, err
	}
	endpoint, err := url.Parse(g.base() + "/messages")
	if err != nil {
		return "", nil, err
	}
	query := endpoint.Query()
	query.Set("q", "rfc822msgid:"+messageID)
	query.Set("maxResults", "2")
	endpoint.RawQuery = query.Encode()
	raw, status, err := call(ctx, g.HTTP, "gmail", token, http.MethodGet, endpoint.String(), nil)
	if err != nil {
		return "", nil, err
	}
	if status != http.StatusOK {
		return "", nil, &statusError{name: "gmail", status: status}
	}
	var listed struct {
		Messages []struct {
			ID string `json:"id"`
		} `json:"messages"`
		NextPageToken string `json:"nextPageToken"`
	}
	if err := json.Unmarshal(raw, &listed); err != nil {
		return "", nil, errors.New("gmail: response was not a message list")
	}
	if listed.NextPageToken != "" || len(listed.Messages) > 1 {
		return "", nil, errors.New("gmail: more than one message matched that Message-ID")
	}
	if len(listed.Messages) == 0 || listed.Messages[0].ID == "" {
		return "", nil, errors.New("gmail: no message matched that Message-ID")
	}
	id := listed.Messages[0].ID
	meta, err := url.Parse(g.base() + "/messages/" + url.PathEscape(id))
	if err != nil {
		return "", nil, err
	}
	metaQuery := meta.Query()
	// format=minimal asks for the id and label ids, not the body.
	// VERIFY: the messages.get page was not re-read for this parameter.
	metaQuery.Set("format", "minimal")
	meta.RawQuery = metaQuery.Encode()
	raw, status, err = call(ctx, g.HTTP, "gmail", token, http.MethodGet, meta.String(), nil)
	if err != nil {
		return "", nil, err
	}
	if status != http.StatusOK {
		return "", nil, &statusError{name: "gmail", status: status}
	}
	var message struct {
		LabelIDs []string `json:"labelIds"`
	}
	if err := json.Unmarshal(raw, &message); err != nil {
		return "", nil, errors.New("gmail: response was not a message")
	}
	return id, message.LabelIDs, nil
}

func (g *Gmail) ensureLabel(ctx context.Context, name string) (string, error) {
	if isInbox(name) {
		return "INBOX", nil
	}
	id, err := g.lookupLabel(ctx, name)
	if err == nil {
		return id, nil
	}
	if !errors.Is(err, errLabelMissing) {
		return "", err
	}
	token, err := tokenOf(ctx, g.Access)
	if err != nil {
		return "", err
	}
	raw, status, err := call(ctx, g.HTTP, "gmail", token, http.MethodPost, g.base()+"/labels", map[string]string{"name": name})
	if err != nil {
		return "", err
	}
	if status == http.StatusConflict {
		g.forgetLabels()
		return g.lookupLabel(ctx, name)
	}
	if status != http.StatusOK && status != http.StatusCreated {
		return "", &statusError{name: "gmail", status: status}
	}
	id, err = labelID(raw)
	if err != nil {
		return "", err
	}
	g.rememberLabel(name, id)
	return id, nil
}

func (g *Gmail) lookupLabel(ctx context.Context, name string) (string, error) {
	if isInbox(name) {
		return "INBOX", nil
	}
	labels, err := g.labelList(ctx)
	if err != nil {
		return "", err
	}
	var found string
	for _, label := range labels {
		if label.Name != name {
			continue
		}
		if found != "" {
			return "", errors.New("gmail: more than one label has that name")
		}
		found = label.ID
	}
	if found == "" {
		return "", errLabelMissing
	}
	return found, nil
}

func (g *Gmail) labelList(ctx context.Context) ([]gmailLabel, error) {
	g.mu.Lock()
	if g.loaded {
		labels := append([]gmailLabel(nil), g.labels...)
		g.mu.Unlock()
		return labels, nil
	}
	g.mu.Unlock()
	token, err := tokenOf(ctx, g.Access)
	if err != nil {
		return nil, err
	}
	raw, status, err := call(ctx, g.HTTP, "gmail", token, http.MethodGet, g.base()+"/labels", nil)
	if err != nil {
		return nil, err
	}
	if status != http.StatusOK {
		return nil, &statusError{name: "gmail", status: status}
	}
	var listed struct {
		Labels []struct {
			ID   string `json:"id"`
			Name string `json:"name"`
		} `json:"labels"`
	}
	if err := json.Unmarshal(raw, &listed); err != nil {
		return nil, errors.New("gmail: response was not a label list")
	}
	labels := make([]gmailLabel, 0, len(listed.Labels))
	for _, label := range listed.Labels {
		if label.ID == "" || label.Name == "" {
			continue
		}
		labels = append(labels, gmailLabel{ID: label.ID, Name: label.Name})
	}
	g.mu.Lock()
	if !g.loaded {
		g.labels = labels
		g.loaded = true
	} else {
		labels = append([]gmailLabel(nil), g.labels...)
	}
	g.mu.Unlock()
	return labels, nil
}

func (g *Gmail) rememberLabel(name, id string) {
	g.mu.Lock()
	defer g.mu.Unlock()
	g.labels = append(g.labels, gmailLabel{ID: id, Name: name})
	g.loaded = true
}

func (g *Gmail) forgetLabels() {
	g.mu.Lock()
	g.labels = nil
	g.loaded = false
	g.mu.Unlock()
}

func (g *Gmail) modify(ctx context.Context, id string, add, remove []string) error {
	token, err := tokenOf(ctx, g.Access)
	if err != nil {
		return err
	}
	payload := map[string][]string{}
	if len(add) > 0 {
		payload["addLabelIds"] = add
	}
	if len(remove) > 0 {
		payload["removeLabelIds"] = remove
	}
	_, status, err := call(ctx, g.HTTP, "gmail", token, http.MethodPost, g.base()+"/messages/"+url.PathEscape(id)+"/modify", payload)
	if err != nil {
		return err
	}
	if status != http.StatusOK {
		return &statusError{name: "gmail", status: status}
	}
	return nil
}

func labelID(raw []byte) (string, error) {
	var created struct {
		ID string `json:"id"`
	}
	if err := json.Unmarshal(raw, &created); err != nil || created.ID == "" {
		return "", errors.New("gmail: response was not a label")
	}
	return created.ID, nil
}

var errLabelMissing = errors.New("gmail: label was not found")
