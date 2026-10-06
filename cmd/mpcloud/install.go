package main

import (
	"context"
	_ "embed"
	"errors"
	"fmt"
	"net/url"
	"os"
	"os/exec"
	"os/user"
	"path/filepath"
	"strconv"
	"strings"
)

// A minimal generic PostScript PPD with A4 default and duplex options.
//
//go:embed mpcloud.ppd
var ppd []byte

const (
	backendPath = "/usr/lib/cups/backend/mpcloud"
	ppdPath     = "/usr/share/mpcloud/mpcloud.ppd"
)

// queueName turns "2nd Floor Color Printer Mobility Queue" into "2nd-Floor-Color-Printer".
func queueName(printer string) string {
	n := strings.TrimSpace(strings.TrimSuffix(strings.TrimSpace(printer), "Mobility Queue"))
	n = strings.Map(func(r rune) rune {
		switch {
		case r == ' ' || r == '/' || r == '#' || r == '\\' || r == '\'' || r == '"' || r == '@':
			return '-'
		case r < 0x21 || r > 0x7e:
			return -1
		}
		return r
	}, n)
	for strings.Contains(n, "--") {
		n = strings.ReplaceAll(n, "--", "-")
	}
	return strings.Trim(n, "-")
}

// displayName is the name people see for a printer: its Mobility Print
// name without the "Mobility Queue" suffix.
func displayName(printer string) string {
	n := strings.TrimSpace(strings.TrimSuffix(strings.TrimSpace(printer), "Mobility Queue"))
	if n == "" {
		return strings.TrimSpace(printer)
	}
	return n
}

func runCmd(name string, args ...string) error {
	out, err := exec.Command(name, args...).CombinedOutput()
	if err != nil {
		return fmt.Errorf("%s %s: %v: %s", name, strings.Join(args, " "), err, strings.TrimSpace(string(out)))
	}
	return nil
}

func requireRoot(cmd string) error {
	if os.Geteuid() != 0 {
		self, _ := os.Executable()
		return fmt.Errorf("this needs root: sudo %s %s", self, cmd)
	}
	if _, err := exec.LookPath("lpadmin"); err != nil {
		return errors.New("CUPS is not installed; on Debian/Ubuntu run: sudo apt install cups cups-filters")
	}
	return nil
}

func installCUPS(ctx context.Context) error {
	if err := requireRoot("install-cups"); err != nil {
		return err
	}
	sudoUser := os.Getenv("SUDO_USER")
	if sudoUser == "" || sudoUser == "root" {
		return errors.New("run this with sudo from your own account, so your Cloud Print setup can be shared with CUPS")
	}
	u, err := user.Lookup(sudoUser)
	if err != nil {
		return err
	}
	lp, err := user.Lookup("lp")
	if err != nil {
		return fmt.Errorf("no lp user: %w", err)
	}
	lpUID, _ := strconv.Atoi(lp.Uid)
	gid, _ := strconv.Atoi(u.Gid)

	userConfig := filepath.Join(u.HomeDir, ".config", "mpcloud", "config.json")
	if _, err := os.Stat(systemConfigPath); err == nil {
		// Already installed: the shared copy has the newest login.
		userConfig = systemConfigPath
	}
	os.Setenv("MPCLOUD_CONFIG", userConfig)
	cfg, err := loadConfig()
	if err != nil {
		return fmt.Errorf("%w (as %s)", err, sudoUser)
	}

	// Backend: a root-owned copy of this binary, which CUPS runs as lp.
	self, err := os.Executable()
	if err != nil {
		return err
	}
	if self, err = filepath.EvalSymlinks(self); err != nil {
		return err
	}
	bin, err := os.ReadFile(self)
	if err != nil {
		return err
	}
	if err := os.WriteFile(backendPath+".new", bin, 0o755); err != nil {
		return err
	}
	if err := os.Rename(backendPath+".new", backendPath); err != nil {
		return err
	}
	fmt.Println("Installed CUPS backend", backendPath)

	if err := os.MkdirAll(filepath.Dir(ppdPath), 0o755); err != nil {
		return err
	}
	if err := os.WriteFile(ppdPath, ppd, 0o644); err != nil {
		return err
	}

	// Shared config: writable by lp (to refresh the login token) and by the
	// user's group (so interactive logins reach the backend).
	dir := filepath.Dir(systemConfigPath)
	if err := os.MkdirAll(dir, 0o770); err != nil {
		return err
	}
	os.Chown(dir, lpUID, gid)
	os.Chmod(dir, 0o770)
	os.Setenv("MPCLOUD_CONFIG", systemConfigPath)
	if err := cfg.save(); err != nil {
		return err
	}
	os.Chown(systemConfigPath, lpUID, gid)
	os.Chmod(systemConfigPath, 0o660)
	fmt.Println("Shared Cloud Print setup with CUPS:", systemConfigPath)

	fmt.Println("Fetching printers from Mobility Print...")
	s, err := openSession(ctx, cfg, false)
	if err != nil {
		return err
	}
	defer s.close()
	var first string
	for _, p := range s.printers {
		q := queueName(p.Name)
		// Queues that need a PaperCut login ask for it in the print dialog
		// until one is saved; the backend switches this off after that.
		auth := "none"
		if p.RequiresAuth() && cfg.RememberedToken == "" {
			auth = "username,password"
		}
		err := runCmd("lpadmin", "-p", q, "-E",
			"-v", "mpcloud:/"+url.PathEscape(p.Name),
			"-P", ppdPath,
			"-D", displayName(p.Name),
			"-L", "Mobility Print (cloud)",
			"-o", "printer-error-policy=abort-job",
			"-o", "printer-is-shared=false",
			"-o", "auth-info-required="+auth,
			"-o", "PageSize=A4")
		if err != nil {
			return err
		}
		fmt.Printf("  added %-22s (%s)\n", q, p.Name)
		if first == "" {
			first = q
		}
	}
	if out, _ := exec.Command("lpstat", "-d").CombinedOutput(); first != "" && strings.Contains(string(out), "no system default") {
		if runCmd("lpadmin", "-d", first) == nil {
			fmt.Println("Default printer:", first)
		}
	}
	fmt.Println("\nDone. Press Ctrl+P in any app and pick one of these printers,")
	fmt.Println("or from a terminal: lp -d", first, "file.pdf")
	if cfg.RememberedToken == "" {
		fmt.Println("\nThe first time you print, you'll be asked for your PaperCut username")
		fmt.Println("and password. After that, it remembers you.")
	}
	return nil
}

func uninstallCUPS() error {
	if err := requireRoot("uninstall-cups"); err != nil {
		return err
	}
	out, _ := exec.Command("lpstat", "-v").Output()
	for _, line := range strings.Split(string(out), "\n") {
		// "device for NAME: mpcloud:/..."
		if !strings.Contains(line, " mpcloud:/") {
			continue
		}
		name := strings.TrimSuffix(strings.Fields(line)[2], ":")
		if err := runCmd("lpadmin", "-x", name); err != nil {
			fmt.Fprintln(os.Stderr, "warning:", err)
			continue
		}
		fmt.Println("  removed", name)
	}
	// Hand the shared config (with the newest login) back to the user.
	if b, err := os.ReadFile(systemConfigPath); err == nil {
		if u, err := user.Lookup(os.Getenv("SUDO_USER")); err == nil && u.Username != "root" {
			// install-cups read the user's config from here, so the folder exists.
			p := filepath.Join(u.HomeDir, ".config", "mpcloud", "config.json")
			uid, _ := strconv.Atoi(u.Uid)
			gid, _ := strconv.Atoi(u.Gid)
			if os.WriteFile(p, b, 0o600) == nil {
				os.Chown(p, uid, gid)
			}
		}
	}
	for _, p := range []string{backendPath, ppdPath, systemConfigPath} {
		if err := os.Remove(p); err == nil {
			fmt.Println("Removed", p)
		}
	}
	os.Remove(filepath.Dir(systemConfigPath))
	os.Remove(filepath.Dir(ppdPath))
	return nil
}
