// Package deps pins module versions for packages that later phases import.
// The mailsorter binary does not import this package, so the pins are not
// linked into the release build. Blank imports are enough for the module graph.
package deps

import (
	_ "github.com/emersion/go-imap/v2"
	_ "github.com/emersion/go-imap/v2/imapclient"
	_ "github.com/emersion/go-imap/v2/imapserver/imapmemserver"
	_ "github.com/emersion/go-message"
	_ "github.com/zalando/go-keyring"
	_ "golang.org/x/crypto/argon2"
	_ "golang.org/x/crypto/chacha20poly1305"
	_ "golang.org/x/net/html"
	_ "golang.org/x/oauth2"
	_ "golang.org/x/text/encoding/charmap"
	_ "modernc.org/sqlite"
)
