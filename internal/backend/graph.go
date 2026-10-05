package backend

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"strings"
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
	Access   Access
	Base     string
	HTTP     *http.Client
	blocked  map[string]struct{}
	resolved bool
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
		if _, err = cleanFolder(from); err != nil {
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
	query.Set("$select", "id,parentFolderId")
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
			ID             string `json:"id"`
			ParentFolderID string `json:"parentFolderId"`
		} `json:"value"`
	}
	if err := json.Unmarshal(raw, &listed); err != nil {
		return "", "", errors.New("graph: response was not a message list")
	}
	if len(listed.Value) != 1 || listed.Value[0].ID == "" {
		if len(listed.Value) == 0 {
			return "", "", errors.New("graph: no message matched that Message-ID")
		}
		return "", "", errors.New("graph: more than one message matched that Message-ID")
	}
	return listed.Value[0].ID, listed.Value[0].ParentFolderID, nil
}

func (g *Graph) refuseParent(ctx context.Context, parent string) error {
	if parent == "" {
		return errors.New("backend: refusing to open that folder")
	}
	if err := g.loadProtected(ctx); err != nil {
		return err
	}
	if _, ok := g.blocked[parent]; ok {
		return errors.New("backend: refusing to open that folder")
	}
	return nil
}

func (g *Graph) loadProtected(ctx context.Context) error {
	if g.resolved {
		return nil
	}
	blocked := make(map[string]struct{}, len(protectedGraph))
	token, err := tokenOf(ctx, g.Access)
	if err != nil {
		return err
	}
	for _, name := range protectedGraph {
		raw, status, err := call(ctx, g.HTTP, "graph", token, http.MethodGet, g.base()+"/mailFolders/"+name, nil)
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
	g.blocked = blocked
	g.resolved = true
	return nil
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
	return created.ID, nil
}

func (g *Graph) lookupFolder(ctx context.Context, name string) (string, error) {
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
	return listed.Value[0].ID, nil
}

var errFolderMissing = fmt.Errorf("graph: folder was not found")
