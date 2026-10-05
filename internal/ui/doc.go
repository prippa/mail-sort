// Package ui serves the embedded local web page and its JSON API.
//
// The page binds to 127.0.0.1 on a random port. Every API call needs the
// per-launch token in both the mailsorter_session cookie and the
// X-MailSorter-Token header. Host and Origin must match the listener.
// The CSP build of Alpine.js 3.17.4 is embedded so script-src does not
// need unsafe-eval. The page makes no external requests and does not render
// message HTML.
package ui
