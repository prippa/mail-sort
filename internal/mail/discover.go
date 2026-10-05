package mail

import (
	"context"
	"encoding/xml"
	"fmt"
	"io"
	"net"
	"net/http"
	"strings"
)

// Discover looks up an IMAP server for an email domain. It runs only after
// the user sets discover: true. The order follows Thunderbird's autoconfig
// document (https://developer.mozilla.org/en-US/docs/Mozilla/Thunderbird/Autoconfiguration):
// the ISP autoconfig host, then the domain's .well-known file, then the
// ISPDB at https://autoconfig.thunderbird.net/v1.1/{domain}. HTTP and host
// guessing are skipped. RFC 6186 SRV (_imaps._tcp) is last; Thunderbird's
// page says it does not use SRV yet.
//
// The address local part is not sent.
type Discover struct {
	HTTP      *http.Client
	LookupSRV func(ctx context.Context, service, proto, name string) (string, []*net.SRV, error)
}

// DiscoverIMAP returns the first IMAP server from the opt-in lookup.
func DiscoverIMAP(ctx context.Context, email string, opts Discover) (Endpoint, error) {
	domain, err := domainOf(email)
	if err != nil {
		return Endpoint{}, err
	}
	client := opts.HTTP
	if client == nil {
		client = &http.Client{
			Timeout: commandTimeout,
			CheckRedirect: func(*http.Request, []*http.Request) error {
				return http.ErrUseLastResponse
			},
		}
	}
	urls := []string{
		"https://autoconfig." + domain + "/mail/config-v1.1.xml",
		"https://" + domain + "/.well-known/autoconfig/mail/config-v1.1.xml",
		"https://autoconfig.thunderbird.net/v1.1/" + domain,
	}
	var last error
	for _, rawURL := range urls {
		endpoint, err := fetchConfig(ctx, client, rawURL)
		if err == nil {
			return endpoint, nil
		}
		last = err
	}
	lookup := opts.LookupSRV
	if lookup == nil {
		lookup = net.DefaultResolver.LookupSRV
	}
	_, records, err := lookup(ctx, "imaps", "tcp", domain)
	if err == nil {
		for _, record := range records {
			if record == nil || record.Target == "" || record.Port == 0 {
				continue
			}
			return Endpoint{
				Host:     strings.TrimSuffix(record.Target, "."),
				Port:     int(record.Port),
				Security: ImplicitTLS,
			}, nil
		}
	} else {
		last = err
	}
	if last == nil {
		last = fmt.Errorf("no IMAP server found")
	}
	return Endpoint{}, fmt.Errorf("imap: discover %s: %w", domain, last)
}

func domainOf(email string) (string, error) {
	local, domain, ok := strings.Cut(email, "@")
	domain = strings.Trim(strings.TrimSpace(domain), ".")
	if !ok || local == "" || domain == "" || strings.Contains(domain, "@") || strings.ContainsAny(domain, " /\\") {
		return "", fmt.Errorf("imap: discover needs an email address with a domain")
	}
	return strings.ToLower(domain), nil
}

func fetchConfig(ctx context.Context, client *http.Client, rawURL string) (Endpoint, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, rawURL, nil)
	if err != nil {
		return Endpoint{}, err
	}
	req.Header.Set("Accept", "application/xml, text/xml")
	resp, err := client.Do(req)
	if err != nil {
		return Endpoint{}, err
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusOK {
		return Endpoint{}, fmt.Errorf("discover %s returned HTTP %d", rawURL, resp.StatusCode)
	}
	body, err := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if err != nil {
		return Endpoint{}, err
	}
	return parseAutoconfig(body)
}

type clientConfigXML struct {
	Providers []struct {
		Incoming []struct {
			Type       string `xml:"type,attr"`
			Hostname   string `xml:"hostname"`
			Port       int    `xml:"port"`
			SocketType string `xml:"socketType"`
		} `xml:"incomingServer"`
	} `xml:"emailProvider"`
}

func parseAutoconfig(body []byte) (Endpoint, error) {
	var doc clientConfigXML
	if err := xml.Unmarshal(body, &doc); err != nil {
		return Endpoint{}, fmt.Errorf("discover XML is invalid")
	}
	for _, provider := range doc.Providers {
		for _, server := range provider.Incoming {
			if !strings.EqualFold(server.Type, "imap") || server.Hostname == "" || server.Port < 1 || server.Port > 65535 {
				continue
			}
			var security Security
			switch strings.ToUpper(strings.TrimSpace(server.SocketType)) {
			case "SSL":
				security = ImplicitTLS
			case "STARTTLS":
				security = StartTLS
			default:
				continue
			}
			return Endpoint{Host: server.Hostname, Port: server.Port, Security: security}, nil
		}
	}
	return Endpoint{}, fmt.Errorf("discover XML has no IMAP server")
}
