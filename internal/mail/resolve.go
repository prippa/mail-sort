package mail

import (
	"fmt"
	"strings"

	"github.com/prippa/mail-sort/internal/config"
)

// Resolve turns a profile into an account. An explicit host wins over the
// preset. Discovery is left for the caller and only when the provider is
// custom, the host is empty, and discover is set.
func Resolve(profile config.Profile, presets []Preset) (Account, error) {
	provider := strings.TrimSpace(profile.Provider)
	custom := provider == "" || provider == "custom"
	var preset Preset
	var ok bool
	if !custom {
		preset, ok = findPreset(presets, provider)
		if !ok {
			return Account{}, fmt.Errorf("imap: unknown provider %q", provider)
		}
	}
	auth, err := selectAuth(profile, preset, custom)
	if err != nil {
		return Account{}, err
	}
	endpoint, discover, err := resolveEndpoint(profile, preset, custom)
	if err != nil {
		return Account{}, err
	}
	endpoint.CAFile = profile.CAFile
	endpoint.CertSHA256 = profile.CertSHA256
	account := Account{
		Endpoint:        endpoint,
		Auth:            auth,
		Username:        profile.Username,
		AuthFailureHint: preset.AuthFailureHint,
		Discover:        discover,
		Email:           profile.Email,
	}
	if auth == AuthPassword && strings.TrimSpace(account.Username) == "" {
		return Account{}, fmt.Errorf("imap: profile %q is missing a username", profile.Name)
	}
	if (auth == AuthOAuthGoogle || auth == AuthOAuthMicrosoft) && strings.TrimSpace(account.Email) == "" {
		return Account{}, fmt.Errorf("imap: profile %q is missing an email", profile.Name)
	}
	return account, nil
}

func selectAuth(profile config.Profile, preset Preset, custom bool) (AuthMode, error) {
	allowed := []string{string(AuthPassword), "oauth_google", "oauth_microsoft"}
	if !custom {
		allowed = preset.Auth
	}
	choice := profile.Auth
	if choice == "" {
		switch {
		case contains(allowed, string(AuthPassword)) && profile.PasswordEnv != "":
			choice = string(AuthPassword)
		case contains(allowed, string(AuthOAuthGoogle)):
			choice = string(AuthOAuthGoogle)
		case contains(allowed, string(AuthOAuthMicrosoft)):
			choice = string(AuthOAuthMicrosoft)
		case contains(allowed, string(AuthPassword)):
			choice = string(AuthPassword)
		default:
			return "", fmt.Errorf("imap: profile %q has no authentication method", profile.Name)
		}
	}
	if !contains(allowed, choice) {
		if !custom && preset.ID == "microsoft" {
			return "", fmt.Errorf("imap: Microsoft 365 does not accept a password. %s", preset.AuthFailureHint)
		}
		return "", fmt.Errorf("imap: auth %q is not supported for this provider", choice)
	}
	switch AuthMode(choice) {
	case AuthOAuthGoogle, AuthOAuthMicrosoft, AuthPassword:
		return AuthMode(choice), nil
	default:
		return "", fmt.Errorf("imap: auth %q is not supported", choice)
	}
}

func resolveEndpoint(profile config.Profile, preset Preset, custom bool) (Endpoint, bool, error) {
	if profile.Host != "" {
		endpoint, err := endpointFrom(profile.Host, profile.Port, profile.Security, Host{})
		return endpoint, false, err
	}
	if custom {
		if profile.Discover {
			return Endpoint{}, true, nil
		}
		return Endpoint{}, false, fmt.Errorf("imap: profile %q needs a host, or discover: true", profile.Name)
	}
	host, err := pickHost(preset, profile.HostID)
	if err != nil {
		return Endpoint{}, false, err
	}
	endpoint, err := endpointFrom(host.Host, profile.Port, profile.Security, host)
	return endpoint, false, err
}

func pickHost(preset Preset, hostID string) (Host, error) {
	if hostID != "" {
		for _, host := range preset.Hosts {
			if host.ID == hostID {
				return host, nil
			}
		}
		return Host{}, fmt.Errorf("imap: host_id %q is not in the %s preset", hostID, preset.ID)
	}
	if preset.DefaultHost != "" {
		for _, host := range preset.Hosts {
			if host.ID == preset.DefaultHost {
				return host, nil
			}
		}
	}
	if len(preset.Hosts) == 1 {
		return preset.Hosts[0], nil
	}
	ids := make([]string, 0, len(preset.Hosts))
	for _, host := range preset.Hosts {
		ids = append(ids, host.ID)
	}
	return Host{}, fmt.Errorf("imap: set host or host_id for %s (%s)", preset.ID, strings.Join(ids, ", "))
}

func endpointFrom(host string, port int, security string, fallback Host) (Endpoint, error) {
	if port == 0 {
		port = fallback.Port
	}
	if security == "" {
		security = fallback.Security
	}
	if port == 0 {
		if security == string(StartTLS) {
			port = 143
		} else {
			port = 993
		}
	}
	if security == "" {
		if port == 143 {
			security = string(StartTLS)
		} else {
			security = string(ImplicitTLS)
		}
	}
	switch Security(security) {
	case ImplicitTLS, StartTLS:
	default:
		return Endpoint{}, fmt.Errorf("imap: security %q is invalid", security)
	}
	if host == "" || port < 1 || port > 65535 {
		return Endpoint{}, fmt.Errorf("imap: host %q is incomplete", host)
	}
	return Endpoint{Host: host, Port: port, Security: Security(security)}, nil
}

func contains(list []string, want string) bool {
	for _, item := range list {
		if item == want {
			return true
		}
	}
	return false
}
