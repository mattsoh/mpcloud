package main

import (
	"strings"
	"testing"
)

func TestParseCUPSOptions(t *testing.T) {
	o := parseCUPSOptions(`job-uuid=urn:uuid:1 Duplex=DuplexNoTumble PageSize=A4 job-name='my doc' x=a\ b collate`)
	want := map[string]string{"job-uuid": "urn:uuid:1", "Duplex": "DuplexNoTumble", "PageSize": "A4", "job-name": "my doc", "x": "a b", "collate": "true"}
	for k, v := range want {
		if o[k] != v {
			t.Errorf("%s = %q, want %q", k, o[k], v)
		}
	}
	if q := queueName("2nd Floor Color Printer Mobility Queue"); q != "2nd-Floor-Color-Printer" {
		t.Errorf("queueName = %q", q)
	}
}

func TestNormalizeLink(t *testing.T) {
	const tok = "eyJhbGciOiJSUzI1NiJ9.eyJleHAiOjF9.c2ln"
	const want = "mobilityprint://mp.cloud.papercut.com?token=" + tok
	ok := []string{
		"mobilityprint://mp.cloud.papercut.com?token=" + tok,
		"http://mp.cloud.papercut.com/?token=" + tok,
		"https://mp.cloud.papercut.com/?token=" + tok,
		"  '" + "https://mp.cloud.papercut.com/?token=" + tok + "'  ",
		"<mobilityprint://mp.cloud.papercut.com?token=" + tok + ">",
		tok,
		"token=" + tok,
	}
	for _, in := range ok {
		got, err := normalizeLink(in)
		if err != nil || got != want {
			t.Errorf("normalizeLink(%q) = %q, %v; want %q", in, got, err, want)
		}
	}
	bad := []string{"", "   ", "https://example.com/", "ftp://mp.cloud.papercut.com/?token=" + tok, "mobilityprint://mp.cloud.papercut.com"}
	for _, in := range bad {
		if got, err := normalizeLink(in); err == nil {
			t.Errorf("normalizeLink(%q) = %q, want error", in, got)
		}
	}
}

func TestTestPagePDF(t *testing.T) {
	pdf := string(testPagePDF())
	if !strings.HasPrefix(pdf, "%PDF-1.4") || !strings.HasSuffix(pdf, "%%EOF\n") || !strings.Contains(pdf, "Printer Test Page") {
		t.Fatal("test page is not a well-formed PDF")
	}
}

func TestQueuePPDName(t *testing.T) {
	got := string(queuePPD(`Office "Laser" Printer (2) Mobility Queue`))
	for _, want := range []string{
		`*NickName: "Office Laser Printer 2"`,
		`*ModelName: "Office Laser Printer 2"`,
		`*Product: "(Office Laser Printer 2)"`,
	} {
		if !strings.Contains(got, want) {
			t.Errorf("PPD missing %s", want)
		}
	}
	if strings.Contains(got, "Mobility Print Cloud") {
		t.Error("PPD still names the generic model")
	}
}

func TestNewerVersion(t *testing.T) {
	cases := []struct {
		a, b string
		want bool
	}{
		{"0.3.1", "v0.4.0", true},
		{"0.4.0", "v0.4.0", false},
		{"0.10.0", "v0.9.9", false},
		{"0.4.0-SNAPSHOT-abc", "v0.4.0", false},
		{"0.4.0", "v1.0.0", true},
		{"dev", "v9.9.9", false},
		{"6fd69a3-dirty", "v0.4.0", false},
	}
	for _, c := range cases {
		if got := newerVersion(c.a, c.b); got != c.want {
			t.Errorf("newerVersion(%q, %q) = %t, want %t", c.a, c.b, got, c.want)
		}
	}
}
