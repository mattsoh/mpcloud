package main

// Self-update from GitHub releases.

import (
	"archive/tar"
	"bufio"
	"bytes"
	"compress/gzip"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"time"
)

const (
	repoURL = "https://github.com/mattsoh/mpcloud"
	// packagedPath is where .deb/.rpm packages install the binary; those
	// installs are updated through the package manager instead.
	packagedPath = "/usr/bin/mpcloud"
)

// latestRelease returns the newest release tag (e.g. "v0.4.0") by following
// GitHub's /releases/latest redirect, which isn't rate limited like the API.
func latestRelease(ctx context.Context) (string, error) {
	client := &http.Client{
		Timeout:       15 * time.Second,
		CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse },
	}
	req, err := http.NewRequestWithContext(ctx, "HEAD", repoURL+"/releases/latest", nil)
	if err != nil {
		return "", err
	}
	resp, err := client.Do(req)
	if err != nil {
		return "", fmt.Errorf("checking for updates: %w", err)
	}
	resp.Body.Close()
	loc := resp.Header.Get("Location")
	i := strings.LastIndex(loc, "/tag/")
	if i < 0 {
		return "", fmt.Errorf("checking for updates: unexpected response %s", resp.Status)
	}
	return loc[i+len("/tag/"):], nil
}

// newerVersion reports whether release tag b is newer than version a
// (both like "v1.2.3" or "1.2.3"). Unparseable versions are never newer.
func newerVersion(a, b string) bool {
	parse := func(v string) ([3]int, bool) {
		var out [3]int
		v = strings.TrimPrefix(v, "v")
		v, _, _ = strings.Cut(v, "-")
		parts := strings.Split(v, ".")
		if len(parts) != 3 {
			return out, false
		}
		for i, p := range parts {
			n, err := strconv.Atoi(p)
			if err != nil {
				return out, false
			}
			out[i] = n
		}
		return out, true
	}
	va, okA := parse(a)
	vb, okB := parse(b)
	if !okA || !okB {
		return false
	}
	for i := range va {
		if va[i] != vb[i] {
			return vb[i] > va[i]
		}
	}
	return false
}

func archiveName() (string, error) {
	switch runtime.GOARCH {
	case "amd64", "arm64":
		return "mpcloud_linux_" + runtime.GOARCH + ".tar.gz", nil
	case "arm":
		return "mpcloud_linux_armv7.tar.gz", nil
	}
	return "", fmt.Errorf("no release builds for %s", runtime.GOARCH)
}

func download(ctx context.Context, url string) ([]byte, error) {
	req, err := http.NewRequestWithContext(ctx, "GET", url, nil)
	if err != nil {
		return nil, err
	}
	resp, err := (&http.Client{Timeout: 5 * time.Minute}).Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("downloading %s: %s", url, resp.Status)
	}
	return io.ReadAll(resp.Body)
}

// fetchRelease downloads the archive for this machine, checks it against the
// release's checksums.txt and returns the mpcloud binary inside.
func fetchRelease(ctx context.Context, tag string) ([]byte, error) {
	name, err := archiveName()
	if err != nil {
		return nil, err
	}
	base := repoURL + "/releases/download/" + tag + "/"
	sums, err := download(ctx, base+"checksums.txt")
	if err != nil {
		return nil, err
	}
	var want string
	sc := bufio.NewScanner(bytes.NewReader(sums))
	for sc.Scan() {
		if f := strings.Fields(sc.Text()); len(f) == 2 && f[1] == name {
			want = f[0]
		}
	}
	if want == "" {
		return nil, fmt.Errorf("%s isn't listed in the release checksums", name)
	}
	archive, err := download(ctx, base+name)
	if err != nil {
		return nil, err
	}
	if got := sha256.Sum256(archive); hex.EncodeToString(got[:]) != want {
		return nil, errors.New("checksum mismatch; not updating")
	}
	gz, err := gzip.NewReader(bytes.NewReader(archive))
	if err != nil {
		return nil, err
	}
	tr := tar.NewReader(gz)
	for {
		h, err := tr.Next()
		if err != nil {
			return nil, errors.New("release archive has no mpcloud binary")
		}
		if h.Name == "mpcloud" {
			return io.ReadAll(tr)
		}
	}
}

// replaceFile atomically swaps path for data, keeping it executable.
func replaceFile(path string, data []byte) error {
	tmp := path + ".new"
	if err := os.WriteFile(tmp, data, 0o755); err != nil {
		return err
	}
	if err := os.Chmod(tmp, 0o755); err != nil {
		os.Remove(tmp)
		return err
	}
	if err := os.Rename(tmp, path); err != nil {
		os.Remove(tmp)
		return err
	}
	return nil
}

func update(ctx context.Context, args []string) error {
	checkOnly, quiet := false, false
	for _, a := range args {
		switch a {
		case "--check":
			checkOnly = true
		case "--quiet", "-q":
			quiet = true
		default:
			usage()
		}
	}
	say := func(format string, a ...any) {
		if !quiet {
			fmt.Printf(format+"\n", a...)
		}
	}

	tag, err := latestRelease(ctx)
	if err != nil {
		return err
	}
	if !newerVersion(version, tag) {
		say("mpcloud %s is up to date (latest release: %s).", version, tag)
		return nil
	}
	if checkOnly {
		fmt.Printf("Update available: %s -> %s. Run: sudo mpcloud update\n", version, tag)
		return nil
	}

	exe, err := os.Executable()
	if err != nil {
		return err
	}
	if exe, err = filepath.EvalSymlinks(exe); err != nil {
		return err
	}
	if exe == packagedPath {
		return fmt.Errorf("mpcloud was installed from a package; download %s from %s/releases/latest and install it with your package manager", tag, repoURL)
	}

	say("Updating mpcloud %s -> %s...", version, tag)
	bin, err := fetchRelease(ctx, tag)
	if err != nil {
		return err
	}
	if err := replaceFile(exe, bin); err != nil {
		if errors.Is(err, os.ErrPermission) {
			return fmt.Errorf("no permission to replace %s; run: sudo mpcloud update", exe)
		}
		return err
	}
	// Keep the CUPS backend (a root-owned copy) in step.
	if _, err := os.Stat(backendPath); err == nil {
		if err := replaceFile(backendPath, bin); err != nil {
			say("Updated %s, but not the CUPS backend (%v); run: sudo mpcloud install-cups", exe, err)
			return nil
		}
	}
	say("Updated to %s.", tag)
	return nil
}

// notifyUpdate prints a one-line notice when a newer release exists. It
// checks at most once a day and gives up quickly if offline.
func notifyUpdate() {
	if version == "dev" {
		return
	}
	dir, err := os.UserCacheDir()
	if err != nil {
		return
	}
	stamp := filepath.Join(dir, "mpcloud", "update-check")
	if fi, err := os.Stat(stamp); err == nil && time.Since(fi.ModTime()) < 24*time.Hour {
		return
	}
	os.MkdirAll(filepath.Dir(stamp), 0o700)
	os.WriteFile(stamp, nil, 0o600)
	os.Chtimes(stamp, time.Now(), time.Now())

	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	if tag, err := latestRelease(ctx); err == nil && newerVersion(version, tag) {
		fmt.Printf("A new version of mpcloud is available (%s -> %s). Update with: mpcloud update\n", version, tag)
		fmt.Printf("(use sudo if mpcloud is installed system-wide)\n\n")
	}
}
