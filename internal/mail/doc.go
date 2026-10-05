// Package mail talks to the user's mailbox over IMAP.
// IMAP types stay in this package. Callers see folders and message.Input values.
package mail

import "fmt"

// PhaseError is a command this phase does not run. The CLI exits 2.
type PhaseError struct {
	Phase string
	Text  string
}

func (e *PhaseError) Error() string {
	if e == nil {
		return ""
	}
	return e.Text
}

func phaseError(phase, text string) error {
	return &PhaseError{Phase: phase, Text: text}
}

// Security is how the TCP connection is protected. Cleartext is not a value.
type Security string

const (
	ImplicitTLS Security = "implicit_tls"
	StartTLS    Security = "starttls"
)

// AuthMode is the login method. OAuth values fail before a connection is opened.
type AuthMode string

const (
	AuthPassword AuthMode = "password"
)

// Endpoint is the server a profile will dial.
type Endpoint struct {
	Host       string
	Port       int
	Security   Security
	CAFile     string
	CertSHA256 string
}

func (e Endpoint) addr() string {
	return fmt.Sprintf("%s:%d", e.Host, e.Port)
}

// Account is a resolved profile, ready to dial.
type Account struct {
	Endpoint        Endpoint
	Auth            AuthMode
	Username        string
	AuthFailureHint string
	Discover        bool
	Email           string
}

// Caps is the subset test-conn prints. IMAP4rev2 requires MOVE, UIDPLUS,
// IDLE, and SPECIAL-USE (RFC 9051), so those are reported when the server
// advertises IMAP4rev2 even if it omits the individual tokens.
type Caps struct {
	Move       bool
	UIDPlus    bool
	Idle       bool
	SpecialUse bool
	GmailExt   bool
}

// Folder is one LIST result. SpecialUse holds attributes such as \Sent.
type Folder struct {
	Name       string
	Delimiter  rune
	SpecialUse []string
}
