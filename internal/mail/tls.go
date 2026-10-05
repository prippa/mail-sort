package mail

import (
	"crypto/hmac"
	"crypto/sha256"
	"crypto/tls"
	"crypto/x509"
	"encoding/hex"
	"errors"
	"fmt"
	"os"
)

// tlsConfig verifies the server certificate. A fingerprint may stand in for
// a private CA: InsecureSkipVerify is set only in that case, and only so the
// handshake reaches VerifyPeerCertificate. crypto/tls still calls that
// callback, and the callback rejects every leaf whose SHA-256 does not match.
func tlsConfig(ep Endpoint) (*tls.Config, error) {
	cfg := &tls.Config{
		MinVersion: tls.VersionTLS12,
		ServerName: ep.Host,
	}
	if ep.CAFile != "" {
		pool, err := x509.SystemCertPool()
		if err != nil || pool == nil {
			pool = x509.NewCertPool()
		}
		pem, err := os.ReadFile(ep.CAFile)
		if err != nil {
			return nil, fmt.Errorf("imap: read CA file: %w", err)
		}
		if !pool.AppendCertsFromPEM(pem) {
			return nil, errors.New("imap: CA file contains no certificates")
		}
		cfg.RootCAs = pool
	}
	if ep.CertSHA256 == "" {
		return cfg, nil
	}
	pin, err := hex.DecodeString(ep.CertSHA256)
	if err != nil || len(pin) != sha256.Size {
		return nil, errors.New("imap: cert_sha256 must be 64 hex characters")
	}
	if ep.CAFile == "" {
		cfg.InsecureSkipVerify = true
		cfg.VerifyPeerCertificate = func(raw [][]byte, _ [][]*x509.Certificate) error {
			if len(raw) == 0 {
				return errors.New("imap: server presented no certificate")
			}
			sum := sha256.Sum256(raw[0])
			if !hmac.Equal(sum[:], pin) {
				return errors.New("imap: server certificate does not match cert_sha256")
			}
			return nil
		}
		return cfg, nil
	}
	cfg.VerifyConnection = func(state tls.ConnectionState) error {
		if len(state.PeerCertificates) == 0 {
			return errors.New("imap: server presented no certificate")
		}
		sum := sha256.Sum256(state.PeerCertificates[0].Raw)
		if !hmac.Equal(sum[:], pin) {
			return errors.New("imap: server certificate does not match cert_sha256")
		}
		return nil
	}
	return cfg, nil
}
