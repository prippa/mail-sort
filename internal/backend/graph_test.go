package backend

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestGraphMovePostsDestinationAndSkipsProtectedFolders(t *testing.T) {
	t.Parallel()
	const token = "graph-access-token"
	var moved string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodDelete || strings.Contains(strings.ToLower(r.URL.Path), "trash") {
			t.Errorf("unexpected %s %s", r.Method, r.URL.Path)
		}
		if r.Header.Get("Authorization") != "Bearer "+token {
			t.Errorf("authorization missing")
		}
		switch {
		case r.Method == http.MethodGet && r.URL.Path == "/messages":
			if !strings.Contains(r.URL.Query().Get("$filter"), "internetMessageId eq '<a@b.c>'") {
				t.Errorf("filter %s", r.URL.RawQuery)
			}
			_, _ = io.WriteString(w, `{"value":[{"id":"AAMk","parentFolderId":"inbox-id"}]}`)
		case r.Method == http.MethodGet && strings.HasPrefix(r.URL.Path, "/mailFolders/") && !strings.Contains(r.URL.Path, "child"):
			name := strings.TrimPrefix(r.URL.Path, "/mailFolders/")
			_, _ = io.WriteString(w, `{"id":"`+name+`-id"}`)
		case r.Method == http.MethodGet && r.URL.Path == "/mailFolders":
			if !strings.Contains(r.URL.Query().Get("$filter"), "displayName eq 'Work'") {
				t.Errorf("folder filter %s", r.URL.RawQuery)
			}
			_, _ = io.WriteString(w, `{"value":[{"id":"work-id","displayName":"Work"}]}`)
		case r.Method == http.MethodPost && r.URL.Path == "/messages/AAMk/move":
			raw, _ := io.ReadAll(r.Body)
			moved = string(raw)
			w.WriteHeader(http.StatusCreated)
			_, _ = io.WriteString(w, `{"id":"AAMk-new"}`)
		default:
			t.Errorf("unexpected %s %s", r.Method, r.URL.Path)
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	t.Cleanup(srv.Close)
	client := NewGraph(func(context.Context) (string, error) { return token, nil }, srv.URL, srv.Client())
	if err := client.MoveMessage(t.Context(), "<a@b.c>", "INBOX", "Work"); err != nil {
		t.Fatal(err)
	}
	if moved != `{"destinationId":"work-id"}` {
		t.Fatalf("move %s", moved)
	}
}

func TestGraphCopyLeavesTheOriginal(t *testing.T) {
	t.Parallel()
	var path string
	srv := graphServer(t, func(r *http.Request) {
		if strings.HasSuffix(r.URL.Path, "/copy") {
			path = r.URL.Path
		}
		if strings.HasSuffix(r.URL.Path, "/move") {
			t.Error("copy used move")
		}
	})
	t.Cleanup(srv.Close)
	client := NewGraph(func(context.Context) (string, error) { return "tok", nil }, srv.URL, srv.Client())
	if err := client.CopyMessage(t.Context(), "<a@b.c>", "Work"); err != nil {
		t.Fatal(err)
	}
	if path != "/messages/AAMk/copy" {
		t.Fatalf("path %s", path)
	}
}

func TestGraphRefusesDeletedMail(t *testing.T) {
	t.Parallel()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet {
			t.Errorf("unexpected %s %s", r.Method, r.URL.Path)
		}
		if r.URL.Path == "/messages" {
			_, _ = io.WriteString(w, `{"value":[{"id":"AAMk","parentFolderId":"deleteditems-id"}]}`)
			return
		}
		if strings.HasPrefix(r.URL.Path, "/mailFolders/") {
			name := strings.TrimPrefix(r.URL.Path, "/mailFolders/")
			_, _ = io.WriteString(w, `{"id":"`+name+`-id"}`)
			return
		}
		w.WriteHeader(http.StatusNotFound)
	}))
	t.Cleanup(srv.Close)
	client := NewGraph(func(context.Context) (string, error) { return "tok", nil }, srv.URL, srv.Client())
	err := client.MoveMessage(t.Context(), "<a@b.c>", "INBOX", "Work")
	if err == nil || !strings.Contains(err.Error(), "refusing") {
		t.Fatal(err)
	}
}

func TestGraphRefusesTrashDestination(t *testing.T) {
	t.Parallel()
	client := NewGraph(func(context.Context) (string, error) { return "tok", nil }, "http://127.0.0.1", nil)
	err := client.MoveMessage(t.Context(), "<a@b.c>", "INBOX", "deleteditems")
	if err == nil || !strings.Contains(err.Error(), "refusing") {
		t.Fatal(err)
	}
}

func TestGraphUndoUsesInbox(t *testing.T) {
	t.Parallel()
	var moved string
	srv := graphServer(t, func(r *http.Request) {
		if strings.HasSuffix(r.URL.Path, "/move") {
			raw, _ := io.ReadAll(r.Body)
			moved = string(raw)
		}
	})
	t.Cleanup(srv.Close)
	client := NewGraph(func(context.Context) (string, error) { return "tok", nil }, srv.URL, srv.Client())
	if err := client.MoveMessage(t.Context(), "<a@b.c>", "Work", "INBOX"); err != nil {
		t.Fatal(err)
	}
	if moved != `{"destinationId":"inbox"}` {
		t.Fatalf("move %s", moved)
	}
}

func TestGraphErrorOmitsTheToken(t *testing.T) {
	t.Parallel()
	const token = "graph-secret-token"
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, "nope "+token, http.StatusInternalServerError)
	}))
	t.Cleanup(srv.Close)
	client := NewGraph(func(context.Context) (string, error) { return token, nil }, srv.URL, srv.Client())
	err := client.MoveMessage(t.Context(), "<a@b.c>", "INBOX", "Work")
	if err == nil || strings.Contains(err.Error(), token) {
		t.Fatal(err)
	}
}

func graphServer(t *testing.T, extra func(*http.Request)) *httptest.Server {
	t.Helper()
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if extra != nil {
			extra(r)
		}
		switch {
		case r.URL.Path == "/messages" && r.Method == http.MethodGet:
			_, _ = io.WriteString(w, `{"value":[{"id":"AAMk","parentFolderId":"inbox-id"}]}`)
		case strings.HasPrefix(r.URL.Path, "/mailFolders/") && r.Method == http.MethodGet:
			name := strings.TrimPrefix(r.URL.Path, "/mailFolders/")
			_, _ = io.WriteString(w, `{"id":"`+name+`-id"}`)
		case r.URL.Path == "/mailFolders" && r.Method == http.MethodGet:
			_, _ = io.WriteString(w, `{"value":[{"id":"work-id"}]}`)
		case strings.HasSuffix(r.URL.Path, "/move") || strings.HasSuffix(r.URL.Path, "/copy"):
			w.WriteHeader(http.StatusCreated)
			_, _ = io.WriteString(w, `{"id":"AAMk-new"}`)
		default:
			t.Errorf("unexpected %s %s", r.Method, r.URL.Path)
			w.WriteHeader(http.StatusNotFound)
		}
	}))
}
