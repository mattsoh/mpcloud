// Command mpcloud prints to PaperCut Mobility Print "Cloud Print" queues from
// Linux: interactively, from scripts, or through CUPS (lp and print dialogs).
package main

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

	"github.com/mattsoh/mpcloud/internal/cloudprint"
)

// version is set at build time with -ldflags "-X main.version=...".
var version = "dev"

func usage() {
	fmt.Fprint(os.Stderr, `mpcloud - print to PaperCut Mobility Print (Cloud Print) from Linux

usage:
  mpcloud                               interactive mode (prompts for everything)
  mpcloud setup [--no-verify] [LINK]    check and save your organization's link
                                        (asks for it if not given)
                                        (the mobilityprint:// link, the browser
                                        address with ?token=..., or just the token)
  mpcloud printers [-json]              list printers
  mpcloud print -p PRINTER [options] FILE|-
      -duplex NO_DUPLEX|LONG_EDGE|SHORT_EDGE
      -color  STANDARD_MONOCHROME|STANDARD_COLOR   (default: from queue name)
      -media  NAME                                 (default: A4; e.g. NA_LETTER)
      -copies N   -pages RANGE   -title TITLE   -type MIME
      -user USER  (password from $MPCLOUD_PASSWORD or prompted)
  mpcloud logout                        forget the saved PaperCut login
  mpcloud info                          show Mobility Print server info
  sudo mpcloud install-cups             add the printers to CUPS (lp, print dialogs)
  sudo mpcloud uninstall-cups           remove them again
  mpcloud version

global flags: -v  verbose protocol logging
`)
	os.Exit(2)
}

func main() {
	if isBackend() {
		os.Exit(backendMain())
	}
	verbose := false
	args := os.Args[1:]
	if len(args) > 0 && (args[0] == "-v" || args[0] == "--verbose") {
		verbose, args = true, args[1:]
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Minute)
	defer cancel()
	var err error
	if len(args) == 0 {
		err = interactive(ctx, verbose)
	} else {
		err = run(ctx, args[0], args[1:], verbose)
	}
	if err != nil {
		fmt.Fprintln(os.Stderr, "error:", err)
		os.Exit(1)
	}
}

func run(ctx context.Context, cmd string, args []string, verbose bool) error {
	switch cmd {
	case "help", "-h", "--help":
		usage()

	case "version", "--version":
		fmt.Println("mpcloud", version)
		return nil

	case "setup":
		verify := true
		if len(args) == 2 && args[0] == "--no-verify" {
			verify, args = false, args[1:]
		}
		switch len(args) {
		case 0:
			// No link given: ask for one (used by the install script, so the
			// instructions always match this binary).
			cfg, err := loadConfig()
			if err != nil {
				cfg = &config{}
			}
			if err := promptLink(ctx, cfg, true); err == errSkipped {
				fmt.Println("Skipped. Run `mpcloud` any time to connect.")
			} else if err != nil {
				return err
			}
			return nil
		case 1:
			return setup(ctx, args[0], verify)
		}
		usage()

	case "logout":
		cfg, err := loadConfig()
		if err != nil {
			return err
		}
		cfg.RememberedToken = ""
		if err := cfg.save(); err != nil {
			return err
		}
		fmt.Println("Forgot the saved PaperCut login.")
		return nil

	case "install-cups":
		return installCUPS(ctx)

	case "uninstall-cups":
		return uninstallCUPS()

	case "info":
		cfg, err := loadConfig()
		if err != nil {
			return err
		}
		s := &session{cfg: cfg, verbose: verbose}
		if err := s.connect(ctx); err != nil {
			return err
		}
		defer s.close()
		si, err := s.client.ServerInfo(ctx)
		if err != nil {
			return err
		}
		fmt.Printf("Mobility Print server %s (username/password sign-in: %t, Google sign-in: %t)\n",
			si.Version, si.SignInUserPass, si.SignInWithGoogle)
		return nil

	case "printers":
		cfg, err := loadConfig()
		if err != nil {
			return err
		}
		s, err := openSession(ctx, cfg, verbose)
		if err != nil {
			return err
		}
		defer s.close()
		if len(args) > 0 && args[0] == "-json" {
			b, _ := json.MarshalIndent(s.printers, "", "  ")
			fmt.Println(string(b))
			return nil
		}
		for _, p := range s.printers {
			fmt.Printf("%s\n    sign-in: %t  color: %v  sides: %v\n",
				p.Name, p.RequiresAuth(), p.Capabilities.Color, p.Capabilities.Duplex)
		}
		return nil

	case "print":
		return printCmd(ctx, args, verbose)
	}
	usage()
	return nil
}

func setup(ctx context.Context, input string, verify bool) error {
	link, err := normalizeLink(input)
	if err != nil {
		return err
	}
	if verify {
		if err := verifyLink(ctx, link); err != nil {
			return err
		}
	}
	cfg, err := loadConfig()
	if err != nil {
		cfg = &config{}
	}
	if cfg.Link != link {
		// A different link may mean a different organization.
		cfg.RememberedToken = ""
	}
	cfg.Link = link
	if err := cfg.save(); err != nil {
		return err
	}
	fmt.Println("Saved your Cloud Print link to", configPath())
	fmt.Println("Run `mpcloud` to print.")
	return nil
}

func printCmd(ctx context.Context, args []string, verbose bool) error {
	fs := flag.NewFlagSet("print", flag.ExitOnError)
	printer := fs.String("p", "", "printer name")
	duplex := fs.String("duplex", "NO_DUPLEX", "")
	color := fs.String("color", "", "")
	copies := fs.Int("copies", 1, "")
	pages := fs.String("pages", "", "")
	ctype := fs.String("type", "", "")
	title := fs.String("title", "", "")
	user := fs.String("user", "", "")
	media := fs.String("media", "", "")
	fs.Usage = usage
	fs.Parse(args)
	if *printer == "" || fs.NArg() != 1 {
		usage()
	}
	file := fs.Arg(0)
	var doc []byte
	var err error
	if file == "-" {
		doc, err = io.ReadAll(os.Stdin)
	} else {
		doc, err = os.ReadFile(file)
	}
	if err != nil {
		return err
	}
	if *ctype == "" {
		*ctype = guessType(file, doc)
	}
	if *title == "" {
		*title = filepath.Base(file)
	}

	cfg, err := loadConfig()
	if err != nil {
		return err
	}
	var creds cloudprint.Credentials
	if *user != "" {
		creds.Username = *user
		if creds.Password = os.Getenv("MPCLOUD_PASSWORD"); creds.Password == "" {
			if creds.Password, err = promptPassword(); err != nil {
				return err
			}
		}
	}
	s, err := openSession(ctx, cfg, verbose)
	if err != nil {
		return err
	}
	defer s.close()
	p := s.find(*printer)
	if p == nil {
		return fmt.Errorf("no printer named %q (see: mpcloud printers)", *printer)
	}
	m, ok := findMedia(p, *media)
	if !ok {
		return fmt.Errorf("printer %q has no paper size %q (see: mpcloud printers -json)", p.Name, *media)
	}
	if *color == "" {
		*color = defaultColor(p)
	}
	details := s.newJob(p, jobSpec{
		Title: *title, Color: *color, Duplex: *duplex, Media: m,
		Copies: *copies, Pages: *pages, ContentType: *ctype,
	})
	if err := s.send(ctx, p, details, doc, creds); err != nil {
		return err
	}
	fmt.Printf("Sent %q (%d KB) to %s\n", *title, (len(doc)+1023)/1024, p.Name)
	return nil
}

// promptPassword reads a password from the terminal without echoing it.
func promptPassword() (string, error) {
	tty, err := os.OpenFile("/dev/tty", os.O_RDWR, 0)
	if err != nil {
		return "", errors.New("password required: set MPCLOUD_PASSWORD")
	}
	defer tty.Close()
	fmt.Fprint(tty, "PaperCut password: ")
	stty := func(arg string) {
		cmd := exec.Command("stty", arg)
		cmd.Stdin = tty
		cmd.Run()
	}
	stty("-echo")
	defer stty("echo")
	line, err := bufio.NewReader(tty).ReadString('\n')
	fmt.Fprintln(tty)
	return strings.TrimRight(line, "\r\n"), err
}
