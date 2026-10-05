// Package buildinfo holds values injected at link time.
// The repository ships the OAuth client ids empty. A packager may set them
// with -ldflags; do not commit a shared client id.
package buildinfo

var (
	Version                  = "dev"
	DefaultGoogleClientID    = ""
	DefaultMicrosoftClientID = ""
)

func init() {
	// Keep the ldflags targets referenced before any caller reads them.
	_ = DefaultGoogleClientID
	_ = DefaultMicrosoftClientID
}
