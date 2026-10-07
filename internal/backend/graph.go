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

// protectedGraph lists well-known folders this client will not file from.
// recoverableitemsdeletions holds soft-deleted mail.
var protectedGraph = []string{
	"deleteditems",
	"junkemail",
	"drafts",
	"sentitems",
	"recoverableitemsdeletions",
}

// Graph files by internetMessageId using message move and copy.
// VERIFY: a live Microsoft mailbox was not modified.
type Graph struct {
	Access Access
	Base   string
	HTTP   *http.Client

	mu       sync.Mutex
	blocked  map[string]struct{}
	resolved bool
	folders  map[string]string
}

// NewGraph returns a filer. An empty base uses the Graph v1.0 host.
func NewGraph(access Access, base string, client *http.Client) *Graph {
	return &Graph{Access: access, Base: strings.TrimRight(base, "/"), HTTP: client}
}

func (g *Graph) base() string {
	if g.Base != "" {
		return g.Base
	}
	return "https://graph.microsoft.com/v1.0/me"
}

// MoveMessage moves the message to the destination folder.
// INBOX uses the well-known name inbox. The message is not deleted.
func (g *Graph) MoveMessage(ctx context.Context, messageID, from, to string) error {
	return g.file(ctx, messageID, from, to, true)
}

// CopyMessage copies the message and leaves the original where it is.
func (g *Graph) CopyMessage(ctx context.Context, messageID, to string) error {
	return g.file(ctx, messageID, "", to, false)
}

func (g *Graph) file(ctx context.Context, messageID, from, to string, move bool) error {
	messageID, err := cleanMessageID(messageID)
	if err != nil {
		return err
	}
	to, err = cleanFolder(to)
	if err != nil {
		return err
	}
	if move && !isInbox(from) {
		from, err = cleanFolder(from)
		if err != nil {
			return err
		}
	}
	id, parent, err := g.find(ctx, messageID)
	if err != nil {
		return err
	}
	if err := g.refuseParent(ctx, parent); err != nil {
		return err
	}
	if move {
		sourceID, err := g.folderID(ctx, from)
		if errors.Is(err, errFolderMissing) || (err == nil && sourceID != parent) {
			return errors.New("backend: message is not in that folder")
		}
		if err != nil {
			return err
		}
	}
	dest, err := g.destination(ctx, to)
	if err != nil {
		return err
	}
	action := "copy"
	if move {
		action = "move"
	}
	token, err := tokenOf(ctx, g.Access)
	if err != nil {
		return err
	}
	endpoint := g.base() + "/messages/" + url.PathEscape(id) + "/" + action
	_, status, err := call(ctx, g.HTTP, "graph", token, http.MethodPost, endpoint, map[string]string{"destinationId": dest})
	if err != nil {
		return err
	}
	if status != http.StatusOK && status != http.StatusCreated {
		return &statusError{name: "graph", status: status}
	}
	return nil
}

func (g *Graph) find(ctx context.Context, messageID string) (string, string, error) {
	token, err := tokenOf(ctx, g.Access)
	if err != nil {
		return "", "", err
	}
	filter := "internetMessageId eq '" + strings.ReplaceAll(messageID, "'", "''") + "'"
	endpoint, err := url.Parse(g.base() + "/messages")
	if err != nil {
		return "", "", err
	}
	query := endpoint.Query()
	query.Set("$filter", filter)
	query.Set("$select", "id,parentFolderId,internetMessageId")
	query.Set("$top", "2")
	endpoint.RawQuery = query.Encode()
	raw, status, err := call(ctx, g.HTTP, "graph", token, http.MethodGet, endpoint.String(), nil)
	if err != nil {
		return "", "", err
	}
	if status != http.StatusOK {
		return "", "", &statusError{name: "graph", status: status}
	}
	var listed struct {
		Value []struct {
			ID                string `json:"id"`
			ParentFolderID    string `json:"parentFolderId"`
			InternetMessageID string `json:"internetMessageId"`
		} `json:"value"`
		Next string `json:"@odata.nextLink"`
	}
	if err := json.Unmarshal(raw, &listed); err != nil {
		return "", "", errors.New("graph: response was not a message list")
	}
	if listed.Next != "" || len(listed.Value) > 1 {
		return "", "", errors.New("graph: more than one message matched that Message-ID")
	}
	if len(listed.Value) == 0 || listed.Value[0].ID == "" {
		return "", "", errors.New("graph: no message matched that Message-ID")
	}
	if !sameMessageID(listed.Value[0].InternetMessageID, messageID) {
		return "", "", errors.New("graph: response did not confirm the Message-ID")
	}
	return listed.Value[0].ID, listed.Value[0].ParentFolderID, nil
}

func sameMessageID(got, want string) bool {
	got = strings.Trim(strings.TrimSpace(got), "<>")
	want = strings.Trim(strings.TrimSpace(want), "<>")
	return got != "" && got == want
}

func (g *Graph) refuseParent(ctx context.Context, parent string) error {
	if parent == "" {
		return errors.New("backend: refusing to open that folder")
	}
	if err := g.loadProtected(ctx); err != nil {
		return err
	}
	g.mu.Lock()
	_, blocked := g.blocked[parent]
	g.mu.Unlock()
	if blocked {
		return errors.New("backend: refusing to open that folder")
	}
	return nil
}

func (g *Graph) loadProtected(ctx context.Context) error {
	g.mu.Lock()
	if g.resolved {
		g.mu.Unlock()
		return nil
	}
	g.mu.Unlock()
	blocked := make(map[string]struct{}, len(protectedGraph))
	token, err := tokenOf(ctx, g.Access)
	if err != nil {
		return err
	}
	for _, name := range protectedGraph {
		raw, status, err := call(ctx, g.HTTP, "graph", token, http.MethodGet, g.base()+"/mailFolders/"+url.PathEscape(name), nil)
		if err != nil {
			return err
		}
		if status == http.StatusNotFound {
			continue
		}
		if status != http.StatusOK {
			return &statusError{name: "graph", status: status}
		}
		var folder struct {
			ID string `json:"id"`
		}
		if err := json.Unmarshal(raw, &folder); err != nil || folder.ID == "" {
			return errors.New("graph: response was not a folder")
		}
		blocked[folder.ID] = struct{}{}
	}
	g.mu.Lock()
	if !g.resolved {
		g.blocked = blocked
		g.resolved = true
	}
	g.mu.Unlock()
	return nil
}

func (g *Graph) folderID(ctx context.Context, name string) (string, error) {
	if isInbox(name) {
		if id, ok := g.cachedFolder("inbox"); ok {
			return id, nil
		}
		id, err := g.wellKnown(ctx, "inbox")
		if err != nil {
			return "", err
		}
		g.rememberFolder("inbox", id)
		return id, nil
	}
	return g.lookupFolder(ctx, name)
}

func (g *Graph) wellKnown(ctx context.Context, name string) (string, error) {
	token, err := tokenOf(ctx, g.Access)
	if err != nil {
		return "", err
	}
	raw, status, err := call(ctx, g.HTTP, "graph", token, http.MethodGet, g.base()+"/mailFolders/"+url.PathEscape(name), nil)
	if err != nil {
		return "", err
	}
	if status == http.StatusNotFound {
		return "", errFolderMissing
	}
	if status != http.StatusOK {
		return "", &statusError{name: "graph", status: status}
	}
	var folder struct {
		ID string `json:"id"`
	}
	if err := json.Unmarshal(raw, &folder); err != nil || folder.ID == "" {
		return "", errors.New("graph: response was not a folder")
	}
	return folder.ID, nil
}

func (g *Graph) destination(ctx context.Context, name string) (string, error) {
	if isInbox(name) {
		return "inbox", nil
	}
	id, err := g.lookupFolder(ctx, name)
	if err == nil {
		return id, nil
	}
	if !errors.Is(err, errFolderMissing) {
		return "", err
	}
	token, err := tokenOf(ctx, g.Access)
	if err != nil {
		return "", err
	}
	raw, status, err := call(ctx, g.HTTP, "graph", token, http.MethodPost, g.base()+"/mailFolders", map[string]string{"displayName": name})
	if err != nil {
		return "", err
	}
	if status == http.StatusConflict {
		return g.lookupFolder(ctx, name)
	}
	if status != http.StatusOK && status != http.StatusCreated {
		return "", &statusError{name: "graph", status: status}
	}
	var created struct {
		ID string `json:"id"`
	}
	if err := json.Unmarshal(raw, &created); err != nil || created.ID == "" {
		return "", errors.New("graph: response was not a folder")
	}
	g.rememberFolder(name, created.ID)
	return created.ID, nil
}

func (g *Graph) lookupFolder(ctx context.Context, name string) (string, error) {
	if id, ok := g.cachedFolder(name); ok {
		return id, nil
	}
	token, err := tokenOf(ctx, g.Access)
	if err != nil {
		return "", err
	}
	endpoint, err := url.Parse(g.base() + "/mailFolders")
	if err != nil {
		return "", err
	}
	query := endpoint.Query()
	query.Set("$filter", "displayName eq '"+strings.ReplaceAll(name, "'", "''")+"'")
	query.Set("$select", "id,displayName")
	query.Set("$top", "2")
	endpoint.RawQuery = query.Encode()
	raw, status, err := call(ctx, g.HTTP, "graph", token, http.MethodGet, endpoint.String(), nil)
	if err != nil {
		return "", err
	}
	if status != http.StatusOK {
		return "", &statusError{name: "graph", status: status}
	}
	var listed struct {
		Value []struct {
			ID string `json:"id"`
		} `json:"value"`
	}
	if err := json.Unmarshal(raw, &listed); err != nil {
		return "", errors.New("graph: response was not a folder list")
	}
	if len(listed.Value) == 0 {
		return "", errFolderMissing
	}
	if len(listed.Value) != 1 || listed.Value[0].ID == "" {
		return "", errors.New("graph: more than one folder has that name")
	}
	g.rememberFolder(name, listed.Value[0].ID)
	return listed.Value[0].ID, nil
}

func (g *Graph) cachedFolder(name string) (string, bool) {
	g.mu.Lock()
	defer g.mu.Unlock()
	id, ok := g.folders[name]
	return id, ok && id != ""
}

func (g *Graph) rememberFolder(name, id string) {
	if name == "" || id == "" {
		return
	}
	g.mu.Lock()
	defer g.mu.Unlock()
	if g.folders == nil {
		g.folders = map[string]string{}
	}
	g.folders[name] = id
}

var errFolderMissing = errors.New("graph: folder was not found")
