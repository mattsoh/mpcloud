package main

import (
	"encoding/xml"
	"os"
	"strings"
	"testing"
)

const sampleConf = `LogLevel warn
Listen localhost:631
Listen /run/cups/cups.sock
Browsing No
<Location />
  Order allow,deny
</Location>
<Location /admin>
  Order allow,deny
</Location>
`

func TestShareCupsdConf(t *testing.T) {
	confs := []string{sampleConf}
	if b, err := os.ReadFile(cupsdConf); err == nil {
		// Also check this machine's real config, as it is before sharing
		// (it may be shared right now).
		confs = append(confs, unshareCupsdConf(string(b)))
	}
	for _, conf := range confs {
		shared := shareCupsdConf(conf)
		for _, want := range []string{"Port 631", "ServerAlias *", "Allow from 100.64.0.0/10", "Allow from fd7a:115c:a1e0::/48", "#mpcloud# Listen localhost:631", "Listen /run/cups/cups.sock"} {
			if !strings.Contains(shared, want) {
				t.Errorf("shared config missing %q", want)
			}
		}
		// Only the root location gets the tailnet allows.
		admin := shared[strings.Index(shared, "<Location /admin>"):]
		admin = admin[:strings.Index(admin, "</Location>")]
		if strings.Contains(admin, "100.64.0.0/10") {
			t.Error("tailnet access leaked into /admin")
		}
		if again := shareCupsdConf(shared); again != shared {
			t.Error("share is not idempotent")
		}
		if back := unshareCupsdConf(shared); back != conf {
			t.Errorf("unshare didn't restore the original:\n%s", back)
		}
	}
}

func TestAirPrintProfileIsXML(t *testing.T) {
	p := airPrintProfile("box & co", "100.1.2.3", []string{"Office-Printer", "Color-Printer"})
	if err := xml.Unmarshal(p, new(struct{})); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(p), "<string>printers/Color-Printer</string>") {
		t.Error("missing queue")
	}
	if stableUUID("a") != stableUUID("a") || stableUUID("a") == stableUUID("b") {
		t.Error("UUIDs not stable/distinct")
	}
}
