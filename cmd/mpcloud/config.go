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

var errNotConfigured = errors.New("no Cloud Print link configured; run: mpcloud setup 'mobilityprint://...'")

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
	// Keep the CUPS backend's copy in sync when it is installed and writable.
	if p != systemConfigPath {
		if f, err := os.OpenFile(systemConfigPath, os.O_WRONLY|os.O_TRUNC, 0); err == nil {
			f.Write(b)
			f.Close()
		}
	}
	return nil
}

// parseLink extracts the cloud host and share token from a
// mobilityprint://<host>?token=<jwt> link.
func parseLink(link string) (host, token string, err error) {
	u, err := url.Parse(cleanLink(link))
	if err != nil {
		return "", "", err
	}
	if u.Scheme != "mobilityprint" {
		return "", "", fmt.Errorf("expected a mobilityprint:// link, got %q", link)
	}
	token = u.Query().Get("token")
	if u.Host == "" || token == "" {
		return "", "", errors.New("the link is missing its host or token")
	}
	return u.Host, token, nil
}

// cleanLink strips copy-paste debris such as whitespace, quotes or angle
// brackets from a pasted link.
func cleanLink(link string) string {
	return strings.Trim(strings.TrimSpace(link), `"'<>`)
}

// verifyLink checks the link's token locally and with the cloud service.
func verifyLink(ctx context.Context, link string) error {
	host, token, err := parseLink(link)
	if err != nil {
		return err
	}
	return cloudprint.NewClient(host, token).Verify(ctx)
}
