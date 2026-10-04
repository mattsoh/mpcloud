package main

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"

	"github.com/mattsoh/mpcloud/internal/cloudprint"
)

var stdin = bufio.NewReader(os.Stdin)

func ask(prompt, def string) string {
	if def != "" {
		fmt.Printf("%s [%s]: ", prompt, def)
	} else {
		fmt.Printf("%s: ", prompt)
	}
	line, err := stdin.ReadString('\n')
	if err != nil && line == "" {
		fmt.Println("\nCanceled.")
		os.Exit(1)
	}
	line = strings.TrimSpace(line)
	if line == "" {
		return def
	}
	return line
}

// choose shows a numbered menu and returns the index picked.
func choose(prompt string, options []string, def int) int {
	fmt.Println(prompt)
	for i, o := range options {
		fmt.Printf("  %d) %s\n", i+1, o)
	}
	for {
		n, err := strconv.Atoi(ask("Choose", strconv.Itoa(def+1)))
		if err == nil && n >= 1 && n <= len(options) {
			return n - 1
		}
		fmt.Println("  Please enter a number from the list.")
	}
}

func expandHome(p string) string {
	p = strings.Trim(p, `"'`)
	if strings.HasPrefix(p, "~/") {
		home, _ := os.UserHomeDir()
		p = filepath.Join(home, p[2:])
	}
	return p
}

var friendly = map[string]string{
	"STANDARD_MONOCHROME": "Black & white",
	"STANDARD_COLOR":      "Color",
	"NO_DUPLEX":           "Single-sided",
	"LONG_EDGE":           "Double-sided (flip on long edge)",
	"SHORT_EDGE":          "Double-sided (flip on short edge)",
}

func label(v string) string {
	if f, ok := friendly[v]; ok {
		return f
	}
	return v
}

func askCredentials(cfg *config) (cloudprint.Credentials, error) {
	var creds cloudprint.Credentials
	fmt.Println("\nThis printer requires you to sign in with your PaperCut account.")
	fmt.Println("(Usually your organization's network username, often without an @domain part.)")
	creds.Username = ask("Username", cfg.Username)
	if creds.Password = os.Getenv("MPCLOUD_PASSWORD"); creds.Password == "" {
		var err error
		if creds.Password, err = promptPassword(); err != nil {
			return creds, err
		}
	}
	return creds, nil
}

// promptLink asks for a mobilityprint:// link until the cloud service accepts
// one, then saves it.
// errSkipped is returned by promptLink when skipping is allowed and the user
// pressed Enter without pasting anything.
var errSkipped = errors.New("skipped")

func promptLink(ctx context.Context, cfg *config, allowSkip bool) error {
	fmt.Println("To connect, mpcloud needs your organization's Mobility Print link.")
	fmt.Println("Open the Cloud Print setup link your organization gave you, then copy")
	fmt.Println("the address from your browser's address bar (it contains \"?token=\").")
	fmt.Println("A mobilityprint:// link or just the token works too.")
	prompt := "\nPaste it here"
	if allowSkip {
		prompt += " (or press Enter to skip)"
	}
	for {
		input := ask(prompt, "")
		if input == "" && allowSkip {
			return errSkipped
		}
		link, err := normalizeLink(input)
		if err == nil {
			err = verifyLink(ctx, link)
		}
		if err != nil {
			fmt.Println("  That didn't work:", err)
			continue
		}
		if cfg.Link != link {
			cfg.RememberedToken = ""
		}
		cfg.Link = link
		if err := cfg.save(); err != nil {
			return err
		}
		fmt.Println("  Connected! Link saved.")
		return nil
	}
}

func interactive(ctx context.Context, verbose bool) error {
	cfg, err := loadConfig()
	if err == errNotConfigured {
		cfg = &config{}
		if err := promptLink(ctx, cfg, false); err != nil {
			return err
		}
	} else if err != nil {
		return err
	}

	fmt.Println("Connecting to Mobility Print...")
	s, err := openSession(ctx, cfg, verbose)
	for errors.Is(err, cloudprint.ErrInvalidLink) {
		fmt.Println("\nYour saved link no longer works:", err)
		if err := promptLink(ctx, cfg, false); err != nil {
			return err
		}
		fmt.Println("Connecting to Mobility Print...")
		s, err = openSession(ctx, cfg, verbose)
	}
	if err != nil {
		return err
	}
	defer s.close()
	if len(s.printers) == 0 {
		return fmt.Errorf("the server has no printers published")
	}

	var names []string
	for _, p := range s.printers {
		names = append(names, p.Name)
	}
	fmt.Println()
	p := &s.printers[choose("Printer:", names, 0)]

	// Sign in up front so a typo doesn't cost the rest of the prompts.
	var creds cloudprint.Credentials
	if p.RequiresAuth() {
		if cfg.RememberedToken != "" {
			fmt.Printf("\nSigning in as %s (saved login; `mpcloud logout` to change).\n", cfg.Username)
		} else if creds, err = askCredentials(cfg); err != nil {
			return err
		}
	}

	var file string
	var doc []byte
	for {
		file = expandHome(ask("\nFile to print (PDF)", ""))
		if doc, err = os.ReadFile(file); err == nil {
			break
		}
		fmt.Println("  Can't read that file:", err)
	}

	color := defaultColor(p)
	duplex := "NO_DUPLEX"
	if d := p.Capabilities.Duplex; len(d) > 1 {
		var labels []string
		def := 0
		for i, v := range d {
			labels = append(labels, label(v))
			if v == duplex {
				def = i
			}
		}
		fmt.Println()
		duplex = d[choose("Sides:", labels, def)]
	}

	sizes := sortedMedia(p)
	if len(sizes) == 0 {
		return fmt.Errorf("%s reports no paper sizes", p.Name)
	}
	var mediaNames []string
	mediaDef := 0
	for i, m := range sizes {
		mediaNames = append(mediaNames, m.DisplayName)
		if m.Name == "ISO_A4" {
			mediaDef = i
		}
	}
	fmt.Println()
	media := sizes[choose("Paper size:", mediaNames, mediaDef)]

	copies := 0
	for copies < 1 {
		copies, _ = strconv.Atoi(ask("\nCopies", "1"))
	}

	plural := "ies"
	if copies == 1 {
		plural = "y"
	}
	fmt.Printf("\nAbout to print %q (%d KB) to %s: %s, %s, %s, %d cop%s.\n",
		filepath.Base(file), (len(doc)+1023)/1024, p.Name, label(color), label(duplex), media.DisplayName, copies, plural)
	if a := strings.ToLower(ask("Send? (y/n)", "y")); a != "y" && a != "yes" {
		fmt.Println("Canceled.")
		return nil
	}

	details := s.newJob(p, jobSpec{
		Title: filepath.Base(file), Color: color, Duplex: duplex, Media: media,
		Copies: copies, ContentType: guessType(file, doc),
	})
	for attempt := 1; ; attempt++ {
		fmt.Println("Sending...")
		err := s.send(ctx, p, details, doc, creds)
		if err == nil {
			break
		}
		if !p.RequiresAuth() || !cloudprint.IsAuthError(err) || attempt >= 3 {
			return err
		}
		if creds.Username == "" {
			fmt.Println("\nYour saved login has expired. Please sign in again.")
		} else {
			fmt.Println("\nSign-in failed: the username or password was not accepted.")
		}
		cfg.RememberedToken = ""
		cfg.save()
		if creds, err = askCredentials(cfg); err != nil {
			return err
		}
	}
	fmt.Println("Done! Your job was sent to", p.Name+".")
	fmt.Println("If your organization uses release stations, release it at the printer.")
	return nil
}
