package backend

import (
	"context"
	"errors"

	"github.com/prippa/mail-sort/internal/mail"
)

// Access returns a bearer token. The token must not be logged.
type Access func(context.Context) (string, error)

// ErrGraphSignIn means Graph filing was selected and this computer has no
// Graph refresh token yet. The IMAP sign-in is a different permission.
var ErrGraphSignIn = errors.New("oauth: Graph is not signed in. Open the local page and sign in for Microsoft Graph")

// Bind attaches an API filer when the profile asks for one.
// An empty name and "imap" leave the session on IMAP.
// A missing Graph token still binds a filer that fails the move and leaves
// the message where it is.
func Bind(session *mail.Session, name string, imap Access, graph Access) {
	if session == nil {
		return
	}
	switch name {
	case "gmail":
		session.UseFiler(NewGmail(imap, "", nil))
	case "graph":
		if graph == nil {
			graph = func(context.Context) (string, error) {
				return "", ErrGraphSignIn
			}
		}
		session.UseFiler(NewGraph(graph, "", nil))
	}
}
