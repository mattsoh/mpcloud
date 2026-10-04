package main

import (
	"context"
	"fmt"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/mattsoh/mpcloud/internal/cloudprint"
)

// session is a connected client plus the print token and printer list that
// every operation needs.
type session struct {
	cfg      *config
	verbose  bool
	client   *cloudprint.Client
	token    string
	printers []cloudprint.Printer
}

func (s *session) connect(ctx context.Context) error {
	host, share, err := parseLink(s.cfg.Link)
	if err != nil {
		return err
	}
	c := cloudprint.NewClient(host, share)
	c.Verbose = s.verbose
	if err := c.Connect(ctx); err != nil {
		c.Close()
		return err
	}
	s.client = c
	return nil
}

func openSession(ctx context.Context, cfg *config, verbose bool) (*session, error) {
	s := &session{cfg: cfg, verbose: verbose}
	if err := s.connect(ctx); err != nil {
		return nil, err
	}
	var err error
	if s.token, err = s.client.PrintToken(ctx); err != nil {
		s.close()
		return nil, fmt.Errorf("exchanging token: %w", err)
	}
	if s.printers, err = s.client.Printers(ctx, s.token); err != nil {
		s.close()
		return nil, err
	}
	return s, nil
}

func (s *session) close() {
	if s.client != nil {
		s.client.Close()
	}
}

func (s *session) find(name string) *cloudprint.Printer {
	for i := range s.printers {
		if strings.EqualFold(s.printers[i].Name, name) {
			return &s.printers[i]
		}
	}
	return nil
}

// defaultColor picks color mode from the queue itself: queues generally
// advertise both modes, so "mono" queues print black & white and everything
// else prints color.
func defaultColor(p *cloudprint.Printer) string {
	if strings.Contains(strings.ToLower(p.Name), "mono") {
		return "STANDARD_MONOCHROME"
	}
	return "STANDARD_COLOR"
}

// sortedMedia returns the printer's sizes with common ones first; the server
// returns them in arbitrary order.
func sortedMedia(p *cloudprint.Printer) []cloudprint.MediaSize {
	rank := map[string]int{"ISO_A4": 1, "NA_LETTER": 2, "ISO_A3": 3, "ISO_A5": 4, "NA_LEGAL": 5}
	r := func(n string) int {
		if v, ok := rank[n]; ok {
			return v
		}
		return 99
	}
	sizes := append([]cloudprint.MediaSize(nil), p.Capabilities.MediaSizes...)
	sort.SliceStable(sizes, func(i, j int) bool {
		if ri, rj := r(sizes[i].Name), r(sizes[j].Name); ri != rj {
			return ri < rj
		}
		return sizes[i].DisplayName < sizes[j].DisplayName
	})
	return sizes
}

// findMedia looks a size up by server or display name. An empty name means
// A4, falling back to the printer's default.
func findMedia(p *cloudprint.Printer, name string) (cloudprint.MediaSize, bool) {
	sizes := p.Capabilities.MediaSizes
	if name == "" {
		name = "ISO_A4"
		for _, m := range sizes {
			if m.Name == name {
				return m, true
			}
		}
		for _, m := range sizes {
			if m.IsDefault {
				return m, true
			}
		}
		if len(sizes) > 0 {
			return sizes[0], true
		}
		return cloudprint.MediaSize{}, false
	}
	for _, m := range sizes {
		if strings.EqualFold(m.Name, name) || strings.EqualFold(m.DisplayName, name) {
			return m, true
		}
	}
	return cloudprint.MediaSize{}, false
}

type jobSpec struct {
	Title       string
	Color       string
	Duplex      string
	Media       cloudprint.MediaSize
	Copies      int
	Pages       string
	ContentType string
}

func (s *session) newJob(p *cloudprint.Printer, spec jobSpec) cloudprint.JobDetails {
	if spec.Copies < 1 {
		spec.Copies = 1
	}
	return cloudprint.JobDetails{
		ClientVersion: cloudprint.ClientVersion,
		PrintToken:    s.token,
		Params: cloudprint.JobParams{
			ID:                 cloudprint.NewJobID(),
			PrinterName:        cloudprint.PrinterURL(p.Name),
			DocumentName:       spec.Title,
			Duplex:             spec.Duplex,
			Color:              spec.Color,
			Copies:             strconv.Itoa(spec.Copies),
			MediaSize:          spec.Media.Name,
			MediaWidthMicrons:  strconv.Itoa(spec.Media.WidthMicrons),
			MediaHeightMicrons: strconv.Itoa(spec.Media.HeightMicrons),
			PageRange:          spec.Pages,
			ContentType:        spec.ContentType,
		},
	}
}

// send submits a job. For printers that need sign-in it uses creds when given
// and the saved remember-me token otherwise, and saves the fresh token the
// server returns.
func (s *session) send(ctx context.Context, p *cloudprint.Printer, details cloudprint.JobDetails, doc []byte, creds cloudprint.Credentials) error {
	if p.RequiresAuth() {
		details.Params.Credentials = creds
		details.Params.Remember = "true"
		details.Params.RememberedToken = ""
		if creds.Username == "" {
			if s.cfg.RememberedToken == "" {
				return fmt.Errorf("%s requires sign-in: run `mpcloud` once to log in", p.Name)
			}
			details.Params.RememberedToken = s.cfg.RememberedToken
		}
	}
	// The peer connection may have dropped while the user was prompted.
	if !s.client.Connected() {
		s.client.Close()
		if err := s.connect(ctx); err != nil {
			return err
		}
	}
	reply, err := s.client.SendJob(ctx, details, doc)
	if err != nil {
		return err
	}
	if p.RequiresAuth() && strings.Count(reply, ".") == 2 {
		s.cfg.RememberedToken = reply
		if creds.Username != "" {
			s.cfg.Username = creds.Username
		}
		s.cfg.save()
	}
	// Give the server a moment to take the last chunks before tearing down.
	time.Sleep(2 * time.Second)
	return nil
}

func guessType(name string, doc []byte) string {
	switch {
	case strings.HasPrefix(string(doc), "%PDF"):
		return "application/pdf"
	case strings.HasPrefix(string(doc), "%!"):
		return "application/postscript"
	}
	switch strings.ToLower(filepath.Ext(name)) {
	case ".pdf":
		return "application/pdf"
	case ".ps":
		return "application/postscript"
	case ".jpg", ".jpeg":
		return "image/jpeg"
	case ".png":
		return "image/png"
	}
	return "application/octet-stream"
}
