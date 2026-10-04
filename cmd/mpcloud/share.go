package main

// Sharing the mpcloud CUPS queues with your other devices over Tailscale.
//
// AirPrint discovery uses Bonjour (multicast), which Tailscale doesn't carry,
// but printing itself is plain IPP. So `share` opens CUPS to Tailscale
// addresses only and writes an Apple configuration profile that tells
// iPhones, iPads and Macs where the printers are.

import (
	"bytes"
	"crypto/sha1"
	"encoding/xml"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"os/user"
	"path/filepath"
	"strconv"
	"strings"
)

const (
	cupsdConf    = "/etc/cups/cupsd.conf"
	beginMarker  = "# BEGIN mpcloud share (managed by `mpcloud share`; remove with `mpcloud unshare`)"
	endMarker    = "# END mpcloud share"
	listenMarker = "#mpcloud# "
	profileName  = "mpcloud-airprint.mobileconfig"
)

// Tailscale's IPv4 (CGNAT) and IPv6 ranges.
var tailnetRanges = []string{"100.64.0.0/10", "fd7a:115c:a1e0::/48"}

// shareCupsdConf returns cupsd.conf with tailnet access added: CUPS listens
// on all addresses, but the root location only admits localhost and
// Tailscale peers. It is idempotent.
func shareCupsdConf(conf string) string {
	conf = unshareCupsdConf(conf)
	var out []string
	inRoot := false
	for _, line := range strings.Split(conf, "\n") {
		trimmed := strings.TrimSpace(line)
		switch {
		case strings.HasPrefix(trimmed, "Listen ") && strings.HasSuffix(trimmed, ":631") && !strings.HasPrefix(trimmed, "Listen /"):
			// Replaced by Port 631 below; restored by unshare.
			out = append(out, listenMarker+line)
			continue
		case trimmed == "<Location />":
			inRoot = true
		case inRoot && trimmed == "</Location>":
			out = append(out, "  "+beginMarker, "  Allow localhost")
			for _, r := range tailnetRanges {
				out = append(out, "  Allow from "+r)
			}
			out = append(out, "  "+endMarker)
			inRoot = false
		}
		out = append(out, line)
	}
	block := []string{beginMarker, "Port 631", "ServerAlias *", endMarker}
	return strings.Join(append(block, out...), "\n")
}

// unshareCupsdConf removes everything shareCupsdConf added.
func unshareCupsdConf(conf string) string {
	var out []string
	skipping := false
	for _, line := range strings.Split(conf, "\n") {
		trimmed := strings.TrimSpace(line)
		switch {
		case trimmed == beginMarker:
			skipping = true
			continue
		case trimmed == endMarker:
			skipping = false
			continue
		case skipping:
			continue
		case strings.HasPrefix(line, listenMarker):
			line = strings.TrimPrefix(line, listenMarker)
		}
		out = append(out, line)
	}
	return strings.Join(out, "\n")
}

func tailscaleIPv4() (string, error) {
	out, err := exec.Command("tailscale", "ip", "-4").Output()
	if err != nil {
		return "", errors.New("couldn't get this machine's Tailscale address; is Tailscale installed and connected? (tailscale up)")
	}
	ip := strings.TrimSpace(strings.SplitN(string(out), "\n", 2)[0])
	if ip == "" {
		return "", errors.New("Tailscale has no IPv4 address; run: tailscale up")
	}
	return ip, nil
}

// mpcloudQueues lists CUPS queues that use the mpcloud backend.
func mpcloudQueues() []string {
	out, _ := exec.Command("lpstat", "-v").Output()
	var qs []string
	for _, line := range strings.Split(string(out), "\n") {
		// "device for NAME: mpcloud:/..."
		if f := strings.Fields(line); len(f) >= 4 && strings.HasPrefix(f[3], "mpcloud:/") {
			qs = append(qs, strings.TrimSuffix(f[2], ":"))
		}
	}
	return qs
}

func ufwActive() bool {
	out, err := exec.Command("ufw", "status").Output()
	return err == nil && strings.Contains(string(out), "Status: active")
}

var ufwRule = []string{"allow", "in", "on", "tailscale0", "to", "any", "port", "631", "proto", "tcp"}

func shareTailscale() error {
	if err := requireRoot("share"); err != nil {
		return err
	}
	ip, err := tailscaleIPv4()
	if err != nil {
		return err
	}
	queues := mpcloudQueues()
	if len(queues) == 0 {
		return errors.New("no mpcloud printers in CUPS yet; run: sudo mpcloud install-cups")
	}

	b, err := os.ReadFile(cupsdConf)
	if err != nil {
		return err
	}
	if _, err := os.Stat(cupsdConf + ".mpcloud-backup"); os.IsNotExist(err) {
		os.WriteFile(cupsdConf+".mpcloud-backup", b, 0o644)
	}
	if err := os.WriteFile(cupsdConf, []byte(shareCupsdConf(string(b))), 0o644); err != nil {
		return err
	}
	fmt.Println("Opened CUPS to Tailscale addresses only:", cupsdConf)

	for _, q := range queues {
		if err := runCmd("lpadmin", "-p", q, "-o", "printer-is-shared=true"); err != nil {
			return err
		}
	}
	if ufwActive() {
		if err := runCmd("ufw", append(ufwRule, "comment", "mpcloud")...); err != nil {
			return err
		}
		fmt.Println("Allowed port 631 on tailscale0 in the firewall")
	}
	if err := runCmd("systemctl", "restart", "cups"); err != nil {
		return err
	}

	profile, err := writeProfileForSudoUser(ip, queues)
	if err != nil {
		return err
	}
	host, _ := os.Hostname()
	fmt.Printf(`
Shared over Tailscale at %s:
`, ip)
	for _, q := range queues {
		fmt.Printf("  ipp://%s:631/printers/%s\n", ip, q)
	}
	fmt.Printf(`
iPhone / iPad: send the AirPrint profile to the device, e.g. with Taildrop:
    tailscale file cp %s <device-name>:
  then open it from the Files app, and install it in Settings (Profile
  Downloaded). The printers then appear in every Print menu whenever
  Tailscale is connected.
Mac: double-click the same profile and install it in System Settings >
  Privacy & Security > Profiles (or add an IP printer: %s, IPP,
  queue printers/<name>).
Windows / Linux: add a printer by URL, e.g. http://%s:631/printers/%s

Jobs from any of your devices print through this machine (%s), using your
PaperCut login, so it has to be on and connected to Tailscale.
Undo with: sudo mpcloud unshare
`, profile, ip, ip, queues[0], host)
	return nil
}

func unshareTailscale() error {
	if err := requireRoot("unshare"); err != nil {
		return err
	}
	b, err := os.ReadFile(cupsdConf)
	if err != nil {
		return err
	}
	if err := os.WriteFile(cupsdConf, []byte(unshareCupsdConf(string(b))), 0o644); err != nil {
		return err
	}
	for _, q := range mpcloudQueues() {
		runCmd("lpadmin", "-p", q, "-o", "printer-is-shared=false")
	}
	if ufwActive() {
		runCmd("ufw", append([]string{"delete"}, ufwRule...)...)
	}
	if err := runCmd("systemctl", "restart", "cups"); err != nil {
		return err
	}
	fmt.Println("Stopped sharing. CUPS is back to local-only access.")
	fmt.Println("Remove the AirPrint profile from your devices in Settings > General > VPN & Device Management.")
	return nil
}

// writeProfileForSudoUser saves the AirPrint profile in the invoking user's
// home directory, even when run via sudo.
func writeProfileForSudoUser(ip string, queues []string) (string, error) {
	dir, uid, gid := ".", -1, -1
	if home, err := os.UserHomeDir(); err == nil {
		dir = home
	}
	if su := os.Getenv("SUDO_USER"); su != "" {
		if u, err := user.Lookup(su); err == nil {
			dir = u.HomeDir
			uid, _ = strconv.Atoi(u.Uid)
			gid, _ = strconv.Atoi(u.Gid)
		}
	}
	host, _ := os.Hostname()
	path := filepath.Join(dir, profileName)
	if err := os.WriteFile(path, airPrintProfile(host, ip, queues), 0o644); err != nil {
		return "", err
	}
	if uid >= 0 {
		os.Chown(path, uid, gid)
	}
	return path, nil
}

// stableUUID derives a UUID from a name so re-generating the profile
// replaces the installed one instead of adding a duplicate.
func stableUUID(name string) string {
	h := sha1.Sum([]byte("mpcloud:" + name))
	h[6] = (h[6] & 0x0f) | 0x50
	h[8] = (h[8] & 0x3f) | 0x80
	return fmt.Sprintf("%X-%X-%X-%X-%X", h[0:4], h[4:6], h[6:8], h[8:10], h[10:16])
}

// airPrintProfile builds an Apple configuration profile with a
// com.apple.airprint payload listing each queue.
func airPrintProfile(host, ip string, queues []string) []byte {
	esc := func(s string) string {
		var b bytes.Buffer
		xml.EscapeText(&b, []byte(s))
		return b.String()
	}
	var printers strings.Builder
	for _, q := range queues {
		fmt.Fprintf(&printers, `				<dict>
					<key>IPAddress</key>
					<string>%s</string>
					<key>Port</key>
					<integer>631</integer>
					<key>ResourcePath</key>
					<string>printers/%s</string>
					<key>ForceTLS</key>
					<false/>
				</dict>
`, esc(ip), esc(q))
	}
	id := "io.github.mattsoh.mpcloud." + strings.ToLower(host)
	return []byte(fmt.Sprintf(`<?xml version="1.0" encoding="UTF-8"?>
<!DOCTYPE plist PUBLIC "-//Apple//DTD PLIST 1.0//EN" "http://www.apple.com/DTDs/PropertyList-1.0.dtd">
<plist version="1.0">
<dict>
	<key>PayloadContent</key>
	<array>
		<dict>
			<key>AirPrint</key>
			<array>
%s			</array>
			<key>PayloadDisplayName</key>
			<string>AirPrint printers</string>
			<key>PayloadIdentifier</key>
			<string>%s.airprint</string>
			<key>PayloadType</key>
			<string>com.apple.airprint</string>
			<key>PayloadUUID</key>
			<string>%s</string>
			<key>PayloadVersion</key>
			<integer>1</integer>
		</dict>
	</array>
	<key>PayloadDescription</key>
	<string>Adds the Mobility Print printers shared by %s over Tailscale (mpcloud).</string>
	<key>PayloadDisplayName</key>
	<string>Printers on %s (mpcloud)</string>
	<key>PayloadIdentifier</key>
	<string>%s</string>
	<key>PayloadRemovalDisallowed</key>
	<false/>
	<key>PayloadType</key>
	<string>Configuration</string>
	<key>PayloadUUID</key>
	<string>%s</string>
	<key>PayloadVersion</key>
	<integer>1</integer>
</dict>
</plist>
`, printers.String(), esc(id), stableUUID(id+".airprint"), esc(host), esc(host), esc(id), stableUUID(id)))
}
