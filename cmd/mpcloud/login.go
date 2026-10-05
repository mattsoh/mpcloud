package main

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path"
	"time"
)

// login signs in to PaperCut for print jobs waiting in CUPS. PaperCut only
// accepts a login together with a print job, so `lp` jobs that need one wait
// in CUPS until this (or a desktop print dialog) gives CUPS the login.
func login(ctx context.Context) error {
	jobs, err := myUnfinishedJobs(ctx)
	if err != nil {
		return err
	}
	var waiting []cupsJob
	for _, j := range jobs {
		if j.waitingForLogin() {
			waiting = append(waiting, j)
		}
	}
	if len(waiting) == 0 {
		fmt.Println("No print jobs are waiting for a login.")
		fmt.Println("Print something first (for example: lp -d PRINTER file.pdf). If it needs")
		fmt.Println("your PaperCut login, it waits until you run `mpcloud login`.")
		return nil
	}

	fmt.Println("Waiting for your PaperCut login:")
	for _, j := range waiting {
		fmt.Printf("  - %s (on %s)\n", j.get("job-name"), path.Base(j.get("job-printer-uri")))
	}
	fmt.Println("\nSign in with your PaperCut account. This is usually your network")
	fmt.Println("username, often without the @domain part.")

	username := ""
	if cfg, err := loadConfig(); err == nil {
		username = cfg.Username
	}
	first := waiting[0]
	for attempt := 1; ; attempt++ {
		username = ask("Username", username)
		password, err := promptPassword()
		if err != nil {
			return err
		}
		if err := authenticateJob(ctx, first.id(), username, password); err != nil {
			return err
		}
		fmt.Println("Checking your login...")
		err = waitForJob(ctx, first.id())
		if errors.Is(err, errWrongLogin) && attempt < 3 {
			fmt.Println("That username or password didn't work. Try again.")
			continue
		}
		if err != nil {
			return err
		}
		// Signed in: the rest can go too.
		for _, j := range waiting[1:] {
			authenticateJob(ctx, j.id(), username, password)
		}
		fmt.Println("Signed in. Your print jobs have been sent, and you won't be asked")
		fmt.Println("again until the login expires.")
		return nil
	}
}

var errWrongLogin = errors.New("wrong PaperCut username or password")

// waitForJob waits until CUPS has sent the job, or it needs a login again.
func waitForJob(ctx context.Context, id int) error {
	start := time.Now()
	for time.Since(start) < 5*time.Minute {
		time.Sleep(2 * time.Second)
		j, err := cupsJobInfo(ctx, id)
		if err != nil {
			return err
		}
		switch {
		case j.state() == jobCompleted:
			return nil
		case j.waitingForLogin():
			// Right after the login is handed over, the job may not have
			// been released yet.
			if time.Since(start) > 5*time.Second {
				return errWrongLogin
			}
		case j.state() == jobHeld, j.state() == jobCanceled, j.state() == jobAborted:
			msg := j.get("job-printer-state-message")
			if msg == "" {
				msg = "check it with: lpstat -l -o"
			}
			return fmt.Errorf("the job didn't print: %s", msg)
		}
	}
	return fmt.Errorf("job %d is still waiting to print; check it with: lpstat -l -o", id)
}

// logout forgets the saved PaperCut login, both for `mpcloud` and for the
// printers in CUPS.
func logout() error {
	done := false
	for _, p := range []string{systemConfigPath, userConfigPath()} {
		os.Setenv("MPCLOUD_CONFIG", p)
		cfg, err := loadConfig()
		if err != nil {
			continue
		}
		cfg.RememberedToken = ""
		if err := cfg.save(); err != nil {
			if p == systemConfigPath {
				return fmt.Errorf("couldn't log out the printers: %w", err)
			}
			continue
		}
		done = true
	}
	os.Unsetenv("MPCLOUD_CONFIG")
	if !done {
		return errNotConfigured
	}
	fmt.Println("Logged out. You'll be asked for your PaperCut login next time you print.")
	return nil
}
