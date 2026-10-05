package mail

import (
	"context"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strconv"
	"strings"
	"testing"
)

func TestDiscoverStopsAtFirstIMAP(t *testing.T) {
	const local = "unique-local-part"
	var seen []string
	srv := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		seen = append(seen, r.Host+r.URL.RequestURI())
		if strings.Contains(r.Host+r.URL.RequestURI(), local) {
			t.Errorf("request leaked the local part: %s", r.URL)
		}
		if r.URL.Path != "/mail/config-v1.1.xml" {
			http.NotFound(w, r)
			return
		}
		w.Header().Set("Content-Type", "application/xml")
		_, _ = io.WriteString(w, autoconfigXML("imap.example.com", 993, "SSL"))
	}))
	defer srv.Close()
	endpoint, err := DiscoverIMAP(context.Background(), local+"@example.com", Discover{HTTP: rewriteClient(t, srv)})
	if err != nil {
		t.Fatal(err)
	}
	if endpoint.Host != "imap.example.com" || endpoint.Port != 993 || endpoint.Security != ImplicitTLS {
		t.Fatalf("endpoint = %+v", endpoint)
	}
	if len(seen) != 1 || !strings.Contains(seen[0], "autoconfig.example.com") {
		t.Fatalf("requests = %v", seen)
	}
}

func TestDiscoverSkipsRedirectAndUsesNextFile(t *testing.T) {
	srv := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/mail/config-v1.1.xml":
			http.Redirect(w, r, "https://evil.example/mail/config-v1.1.xml", http.StatusFound)
		case "/.well-known/autoconfig/mail/config-v1.1.xml":
			w.Header().Set("Content-Type", "text/xml")
			_, _ = io.WriteString(w, autoconfigXML("imap.other.example", 143, "STARTTLS"))
		default:
			http.NotFound(w, r)
		}
	}))
	defer srv.Close()
	endpoint, err := DiscoverIMAP(context.Background(), "ada@example.com", Discover{HTTP: rewriteClient(t, srv)})
	if err != nil {
		t.Fatal(err)
	}
	if endpoint.Host != "imap.other.example" || endpoint.Security != StartTLS || endpoint.Port != 143 {
		t.Fatalf("endpoint = %+v", endpoint)
	}
}

func TestDiscoverFallsBackToSRV(t *testing.T) {
	srv := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.NotFound(w, r)
	}))
	defer srv.Close()
	called := false
	endpoint, err := DiscoverIMAP(context.Background(), "ada@example.com", Discover{
		HTTP: rewriteClient(t, srv),
		LookupSRV: func(context.Context, string, string, string) (string, []*net.SRV, error) {
			called = true
			return "", []*net.SRV{{Target: "imap.srv.example.", Port: 993}}, nil
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	if !called || endpoint.Host != "imap.srv.example" || endpoint.Port != 993 || endpoint.Security != ImplicitTLS {
		t.Fatalf("endpoint = %+v called=%v", endpoint, called)
	}
}

func rewriteClient(t *testing.T, srv *httptest.Server) *http.Client {
	t.Helper()
	target, err := url.Parse(srv.URL)
	if err != nil {
		t.Fatal(err)
	}
	base := srv.Client().Transport
	client := srv.Client()
	client.CheckRedirect = func(*http.Request, []*http.Request) error {
		return http.ErrUseLastResponse
	}
	client.Transport = roundTripperFunc(func(req *http.Request) (*http.Response, error) {
		cloned := req.Clone(req.Context())
		cloned.URL.Scheme = target.Scheme
		cloned.URL.Host = target.Host
		return base.RoundTrip(cloned)
	})
	return client
}

type roundTripperFunc func(*http.Request) (*http.Response, error)

func (f roundTripperFunc) RoundTrip(req *http.Request) (*http.Response, error) {
	return f(req)
}

func autoconfigXML(host string, port int, socket string) string {
	return `<?xml version="1.0"?>
<clientConfig version="1.1">
  <emailProvider id="example.com">
    <incomingServer type="imap">
      <hostname>` + host + `</hostname>
      <port>` + strconv.Itoa(port) + `</port>
      <socketType>` + socket + `</socketType>
    </incomingServer>
  </emailProvider>
</clientConfig>`
}
