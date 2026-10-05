package main

// CUPS backend mode. Installed as /usr/lib/cups/backend/mpcloud and invoked by
// CUPS as:
//
//	mpcloud job-id user title copies options [file]

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/url"
	"os"
	"strconv"
	"strings"
	"time"

	"github.com/mattsoh/mpcloud/internal/cloudprint"
)

// CUPS backend exit codes.
const (
	backendOK           = 0
	backendFailed       = 1
	backendAuthRequired = 2
	backendRetry        = 6
)

// askForLogin makes CUPS hold the job and ask the user for their PaperCut
// login, then retry the job with it.
func askForLogin(why string) int {
	fmt.Fprintln(os.Stderr, "ATTR: auth-info-required=username,password")
	fmt.Fprintln(os.Stderr, "INFO:", why)
	return backendAuthRequired
}

func isBackend() bool {
	return os.Getenv("DEVICE_URI") != "" || strings.Contains(os.Args[0], "/cups/backend/")
}

// parseCUPSOptions splits the CUPS options argument (space separated
// name=value pairs, values possibly quoted or backslash-escaped).
func parseCUPSOptions(s string) map[string]string {
	opts := map[string]string{}
	var name, val strings.Builder
	inVal := false
	var quote rune
	flush := func() {
		if name.Len() > 0 {
			v := val.String()
			if !inVal {
				v = "true"
			}
			opts[name.String()] = v
		}
		name.Reset()
		val.Reset()
		inVal = false
	}
	rs := []rune(s)
	for i := 0; i < len(rs); i++ {
		r := rs[i]
		switch {
		case r == '\\' && i+1 < len(rs):
			i++
			if inVal {
				val.WriteRune(rs[i])
			} else {
				name.WriteRune(rs[i])
			}
		case quote != 0:
			if r == quote {
				quote = 0
			} else {
				val.WriteRune(r)
			}
		case inVal && (r == '\'' || r == '"'):
			quote = r
		case r == ' ' || r == '\t':
			flush()
		case r == '=' && !inVal:
			inVal = true
		case inVal:
			val.WriteRune(r)
		default:
			name.WriteRune(r)
		}
	}
	flush()
	return opts
}

// PPD PageSize / IPP media names -> Mobility Print media names.
var cupsMedia = map[string]string{
	"a4": "ISO_A4", "iso_a4_210x297mm": "ISO_A4",
	"letter": "NA_LETTER", "na_letter_8.5x11in": "NA_LETTER",
	"a3": "ISO_A3", "iso_a3_297x420mm": "ISO_A3",
	"a5": "ISO_A5", "iso_a5_148x210mm": "ISO_A5",
	"legal": "NA_LEGAL", "na_legal_8.5x14in": "NA_LEGAL",
	"executive": "NA_EXECUTIVE", "na_executive_7.25x10.5in": "NA_EXECUTIVE",
	"tabloid": "NA_LEDGER", "na_ledger_11x17in": "NA_LEDGER",
	"b4": "JIS_B4", "jis_b4_257x364mm": "JIS_B4",
	"b5": "JIS_B5", "jis_b5_182x257mm": "JIS_B5",
	"envdl": "ISO_DL", "iso_dl_110x220mm": "ISO_DL",
	"env10": "EnvelopeNo10", "na_number-10_4.125x9.5in": "EnvelopeNo10",
}

func backendMain() int {
	if len(os.Args) == 1 {
		// Device discovery: queues are created by `mpcloud install-cups`.
		return backendOK
	}
	if len(os.Args) < 6 || len(os.Args) > 7 {
		fmt.Fprintln(os.Stderr, "Usage: mpcloud job-id user title copies options [file]")
		return backendFailed
	}
	title, opts := os.Args[3], parseCUPSOptions(os.Args[5])
	copies := 1
	var in io.Reader = os.Stdin
	if len(os.Args) == 7 {
		// Only when given a file directly do we have to produce copies;
		// otherwise the filters already did.
		copies, _ = strconv.Atoi(os.Args[4])
		f, err := os.Open(os.Args[6])
		if err != nil {
			fmt.Fprintln(os.Stderr, "ERROR: unable to open print file:", err)
			return backendFailed
		}
		defer f.Close()
		in = f
	}

	u, err := url.Parse(os.Getenv("DEVICE_URI"))
	if err != nil || u.Scheme != "mpcloud" {
		fmt.Fprintln(os.Stderr, "ERROR: bad device URI", os.Getenv("DEVICE_URI"))
		return backendFailed
	}
	printerName := strings.TrimPrefix(u.Path, "/")

	doc, err := io.ReadAll(in)
	if err != nil {
		fmt.Fprintln(os.Stderr, "ERROR: reading job:", err)
		return backendFailed
	}
	if len(doc) == 0 {
		fmt.Fprintln(os.Stderr, "INFO: empty job, nothing to print")
		return backendOK
	}

	if os.Getenv("MPCLOUD_CONFIG") == "" {
		os.Setenv("MPCLOUD_CONFIG", systemConfigPath)
	}
	cfg, err := loadConfig()
	if err != nil {
		fmt.Fprintln(os.Stderr, "ERROR: mpcloud is not set up:", err)
		return backendFailed
	}

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Minute)
	defer cancel()
	fmt.Fprintln(os.Stderr, "STATE: +connecting-to-device")
	fmt.Fprintln(os.Stderr, "INFO: Connecting to Mobility Print")
	// The connection sometimes fails once and works right after, so try a
	// few times before handing the job back to CUPS.
	s, err := openSession(ctx, cfg, false)
	for wait := 5 * time.Second; err != nil && !errors.Is(err, cloudprint.ErrInvalidLink) && wait <= 20*time.Second; wait *= 2 {
		fmt.Fprintf(os.Stderr, "INFO: Couldn't connect (%v), trying again in %v\n", err, wait)
		time.Sleep(wait)
		s, err = openSession(ctx, cfg, false)
	}
	if errors.Is(err, cloudprint.ErrInvalidLink) {
		fmt.Fprintln(os.Stderr, "ERROR:", err, "- run `mpcloud` in a terminal and paste a new link")
		return backendFailed
	}
	if err != nil {
		fmt.Fprintln(os.Stderr, "ERROR: unable to connect to Mobility Print:", err)
		return backendRetry
	}
	defer s.close()
	fmt.Fprintln(os.Stderr, "STATE: -connecting-to-device")

	p := s.find(printerName)
	if p == nil {
		fmt.Fprintf(os.Stderr, "ERROR: Mobility Print has no printer named %q\n", printerName)
		return backendFailed
	}

	duplex := "NO_DUPLEX"
	switch strings.ToLower(opts["Duplex"] + opts["sides"]) {
	case "duplexnotumble", "two-sided-long-edge":
		duplex = "LONG_EDGE"
	case "duplextumble", "two-sided-short-edge":
		duplex = "SHORT_EDGE"
	}
	supported := false
	for _, d := range p.Capabilities.Duplex {
		supported = supported || d == duplex
	}
	if !supported {
		duplex = "NO_DUPLEX"
	}

	color := defaultColor(p)
	switch strings.ToLower(opts["print-color-mode"] + opts["ColorModel"]) {
	case "monochrome", "gray", "grey":
		color = "STANDARD_MONOCHROME"
	}

	var want string
	for _, k := range []string{"PageSize", "media"} {
		if v, ok := cupsMedia[strings.ToLower(opts[k])]; ok {
			want = v
			break
		}
	}
	media, ok := findMedia(p, want)
	if !ok {
		media, _ = findMedia(p, "")
	}

	// A login typed into the print dialog, if CUPS asked for one.
	creds := cloudprint.Credentials{Username: os.Getenv("AUTH_USERNAME"), Password: os.Getenv("AUTH_PASSWORD")}
	if creds.Password == "" {
		creds = cloudprint.Credentials{}
	}
	if p.RequiresAuth() && creds.Username == "" && cfg.RememberedToken == "" {
		return askForLogin("Waiting for your PaperCut login. Run `mpcloud login` or print from an app")
	}

	details := s.newJob(p, jobSpec{
		Title: title, Color: color, Duplex: duplex, Media: media,
		Copies: copies, Pages: opts["page-ranges"], ContentType: guessType("", doc),
	})
	fmt.Fprintf(os.Stderr, "INFO: Sending %d KB to %s\n", (len(doc)+1023)/1024, p.Name)
	if err := s.send(ctx, p, details, doc, creds); err != nil {
		if p.RequiresAuth() && cloudprint.IsAuthError(err) {
			if creds.Username != "" {
				return askForLogin("Wrong PaperCut username or password. Run `mpcloud login` to try again")
			}
			return askForLogin("Your PaperCut login expired. Run `mpcloud login` or print from an app")
		}
		fmt.Fprintln(os.Stderr, "ERROR:", err)
		return backendFailed
	}
	if p.RequiresAuth() {
		// Signed in and the login is saved: stop asking for it.
		fmt.Fprintln(os.Stderr, "ATTR: auth-info-required=none")
	}
	fmt.Fprintln(os.Stderr, "INFO: Sent to", p.Name)
	return backendOK
}
