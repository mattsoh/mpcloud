package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/url"
	"os"
	"path/filepath"
	"strings"

	"github.com/mattsoh/mpcloud/internal/cloudprint"
)

type config struct {
	Link     string `json:"link"`
	Username string `json:"username,omitempty"`
	// RememberedToken is the server's remember-me token; it stands in for
	// the password on later jobs.
	RememberedToken string `json:"rememberedToken,omitempty"`
}

// systemConfigPath is the copy of the config used by the CUPS backend. It is
// shared with the installing user so a fresh login from `mpcloud` reaches it.
const systemConfigPath = "/var/lib/mpcloud/config.json"

func configPath() string {
	if p := os.Getenv("MPCLOUD_CONFIG"); p != "" {
		return p
	}
	dir, err := os.UserConfigDir()
	if err != nil {
		dir = "."
	}
	return filepath.Join(dir, "mpcloud", "config.json")
}

var errNotConfigured = errors.New("not connected to Mobility Print yet; run `mpcloud` and paste your link")

func loadConfig() (*config, error) {
	b, err := os.ReadFile(configPath())
	if errors.Is(err, os.ErrNotExist) {
		return nil, errNotConfigured
	}
	if err != nil {
		return nil, err
	}
	var c config
	if err := json.Unmarshal(b, &c); err != nil {
		return nil, fmt.Errorf("reading %s: %w", configPath(), err)
	}
	return &c, nil
}

func (c *config) save() error {
	p := configPath()
	if err := os.MkdirAll(filepath.Dir(p), 0o700); err != nil {
		return err
	}
	b, _ := json.MarshalIndent(c, "", "  ")
	if err := os.WriteFile(p, b, 0o600); err != nil {
		return err
	}
	// Keep the CUPS backend's copy in sync when it is installed and writable,
	// but only for the user's own config, never for an MPCLOUD_CONFIG override.
	if os.Getenv("MPCLOUD_CONFIG") == "" {
		if f, err := os.OpenFile(systemConfigPath, os.O_WRONLY|os.O_TRUNC, 0); err == nil {
			f.Write(b)
			f.Close()
		}
	}
	return nil
}

// defaultCloudHost is used when someone pastes just the token.
const defaultCloudHost = "mp.cloud.papercut.com"

// parseLink extracts the cloud host and share token from whatever the user
// pasted. All of these work:
//
//	mobilityprint://mp.cloud.papercut.com?token=eyJ...   (the app link)
//	https://mp.cloud.papercut.com/?token=eyJ...          (the browser address)
//	eyJ...                                               (just the token)
func parseLink(input string) (host, token string, err error) {
	// Tolerate copy-paste debris: whitespace, quotes, angle brackets.
	s := strings.Trim(strings.TrimSpace(input), `"'<>`)
	if s == "" {
		return "", "", errors.New("nothing was pasted")
	}
	if !strings.Contains(s, "://") && !strings.Contains(s, "?") {
		// Just the token, possibly with a leading "token=".
		return defaultCloudHost, strings.TrimPrefix(s, "token="), nil
	}
	u, err := url.Parse(s)
	if err != nil {
		return "", "", fmt.Errorf("couldn't read that link: %w", err)
	}
	switch u.Scheme {
	case "mobilityprint", "http", "https":
	default:
		return "", "", errors.New("that doesn't look like a Mobility Print link")
	}
	token = u.Query().Get("token")
	if u.Hostname() == "" || token == "" {
		return "", "", errors.New("that link has no token in it (look for \"?token=\" in the address)")
	}
	return u.Hostname(), token, nil
}

// normalizeLink turns any accepted form into the canonical
// mobilityprint://<host>?token=<token> link that gets saved.
func normalizeLink(input string) (string, error) {
	host, token, err := parseLink(input)
	if err != nil {
		return "", err
	}
	return "mobilityprint://" + host + "?token=" + url.QueryEscape(token), nil
}

// verifyLink checks the link's token locally and with the cloud service.
func verifyLink(ctx context.Context, link string) error {
	host, token, err := parseLink(link)
	if err != nil {
		return err
	}
	return cloudprint.NewClient(host, token).Verify(ctx)
}
