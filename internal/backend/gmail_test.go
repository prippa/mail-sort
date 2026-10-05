package backend

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestGmailMoveRemovesInboxAndDoesNotDelete(t *testing.T) {
	t.Parallel()
	const token = "gmail-access-token"
	var modified []byte
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodDelete || strings.Contains(r.URL.Path, "trash") || strings.Contains(r.URL.Path, "delete") {
			t.Errorf("unexpected %s %s", r.Method, r.URL.Path)
		}
		if r.Header.Get("Authorization") != "Bearer "+token {
			t.Errorf("authorization missing")
		}
		switch {
		case r.Method == http.MethodGet && r.URL.Path == "/messages":
			if !strings.Contains(r.URL.Query().Get("q"), "rfc822msgid:<a@b.c>") {
				t.Errorf("query %s", r.URL.RawQuery)
			}
			if r.URL.Query().Get("includeSpamTrash") != "" {
				t.Error("spam and trash were included")
			}
			_, _ = io.WriteString(w, `{"messages":[{"id":"msg-1","threadId":"th"}]}`)
		case r.Method == http.MethodGet && r.URL.Path == "/messages/msg-1":
			if r.URL.Query().Get("format") != "minimal" {
				t.Errorf("format %s", r.URL.Query().Get("format"))
			}
			_, _ = io.WriteString(w, `{"id":"msg-1","labelIds":["INBOX","UNREAD"]}`)
		case r.Method == http.MethodGet && r.URL.Path == "/labels":
			_, _ = io.WriteString(w, `{"labels":[{"id":"Label_1","name":"Work","type":"user"}]}`)
		case r.Method == http.MethodPost && r.URL.Path == "/messages/msg-1/modify":
			raw, _ := io.ReadAll(r.Body)
			modified = raw
			w.WriteHeader(http.StatusOK)
			_, _ = io.WriteString(w, `{"id":"msg-1"}`)
		default:
			t.Errorf("unexpected %s %s", r.Method, r.URL.Path)
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	t.Cleanup(srv.Close)

	client := NewGmail(func(context.Context) (string, error) { return token, nil }, srv.URL, srv.Client())
	if err := client.MoveMessage(t.Context(), "<a@b.c>", "INBOX", "Work"); err != nil {
		t.Fatal(err)
	}
	var body map[string][]string
	if err := json.Unmarshal(modified, &body); err != nil {
		t.Fatal(err)
	}
	if len(body["addLabelIds"]) != 1 || body["addLabelIds"][0] != "Label_1" {
		t.Fatalf("add %v", body["addLabelIds"])
	}
	if len(body["removeLabelIds"]) != 1 || body["removeLabelIds"][0] != "INBOX" {
		t.Fatalf("remove %v", body["removeLabelIds"])
	}
	if strings.Contains(string(modified), "UNREAD") || strings.Contains(string(modified), "TRASH") {
		t.Fatalf("body %s", modified)
	}
}

func TestGmailCopyAddsALabel(t *testing.T) {
	t.Parallel()
	var modified []byte
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.URL.Path == "/messages":
			_, _ = io.WriteString(w, `{"messages":[{"id":"msg-1"}]}`)
		case r.URL.Path == "/messages/msg-1" && r.Method == http.MethodGet:
			_, _ = io.WriteString(w, `{"id":"msg-1","labelIds":["INBOX"]}`)
		case r.URL.Path == "/labels":
			_, _ = io.WriteString(w, `{"labels":[{"id":"Label_9","name":"Receipts"}]}`)
		case r.URL.Path == "/messages/msg-1/modify":
			modified, _ = io.ReadAll(r.Body)
			w.WriteHeader(http.StatusOK)
		default:
			t.Errorf("unexpected %s %s", r.Method, r.URL.Path)
		}
	}))
	t.Cleanup(srv.Close)
	client := NewGmail(func(context.Context) (string, error) { return "tok", nil }, srv.URL, srv.Client())
	if err := client.CopyMessage(t.Context(), "<a@b.c>", "Receipts"); err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(modified), "removeLabelIds") || !strings.Contains(string(modified), "Label_9") {
		t.Fatalf("body %s", modified)
	}
}

func TestGmailLeavesTrashAndDrafts(t *testing.T) {
	t.Parallel()
	for _, labels := range []string{`["TRASH"]`, `["DRAFT"]`, `["SENT"]`, `["SPAM"]`} {
		t.Run(labels, func(t *testing.T) {
			t.Parallel()
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.Method != http.MethodGet {
					t.Errorf("unexpected %s %s", r.Method, r.URL.Path)
				}
				if r.URL.Path == "/messages" {
					_, _ = io.WriteString(w, `{"messages":[{"id":"msg-1"}]}`)
					return
				}
				_, _ = io.WriteString(w, `{"id":"msg-1","labelIds":`+labels+`}`)
			}))
			t.Cleanup(srv.Close)
			client := NewGmail(func(context.Context) (string, error) { return "tok", nil }, srv.URL, srv.Client())
			err := client.MoveMessage(t.Context(), "<a@b.c>", "INBOX", "Work")
			if err == nil || !strings.Contains(err.Error(), "refusing") {
				t.Fatal(err)
			}
		})
	}
}

func TestGmailRefusesTrashDestination(t *testing.T) {
	t.Parallel()
	client := NewGmail(func(context.Context) (string, error) { return "tok", nil }, "http://127.0.0.1", nil)
	err := client.MoveMessage(t.Context(), "<a@b.c>", "INBOX", "Trash")
	if err == nil || !strings.Contains(err.Error(), "refusing") {
		t.Fatal(err)
	}
}

func TestGmailErrorOmitsTheToken(t *testing.T) {
	t.Parallel()
	const token = "gmail-secret-token"
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, "nope "+token, http.StatusInternalServerError)
	}))
	t.Cleanup(srv.Close)
	client := NewGmail(func(context.Context) (string, error) { return token, nil }, srv.URL, srv.Client())
	err := client.MoveMessage(t.Context(), "<a@b.c>", "INBOX", "Work")
	if err == nil || strings.Contains(err.Error(), token) {
		t.Fatal(err)
	}
}
