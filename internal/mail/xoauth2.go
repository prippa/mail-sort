package mail

import "errors"

// xoauth2Client is the SASL XOAUTH2 mechanism documented by Google and
// Microsoft. The initial response is user, a Ctrl-A, "auth=Bearer ", the
// access token, and two trailing Ctrl-A bytes. The IMAP client base64-encodes
// that response.
type xoauth2Client struct {
	user  string
	token string
	sent  bool
}

func (c *xoauth2Client) Start() (string, []byte, error) {
	raw := "user=" + c.user + "\x01auth=Bearer " + c.token + "\x01\x01"
	return "XOAUTH2", []byte(raw), nil
}

func (c *xoauth2Client) Next(challenge []byte) ([]byte, error) {
	// A challenge is the server's failure JSON. An empty response lets it
	// finish with NO. The challenge is not returned to the caller.
	if !c.sent {
		c.sent = true
		return []byte{}, nil
	}
	return nil, errors.New("imap: authentication failed")
}

func xoauth2Initial(user, token string) string {
	client := &xoauth2Client{user: user, token: token}
	_, raw, _ := client.Start()
	return string(raw)
}
