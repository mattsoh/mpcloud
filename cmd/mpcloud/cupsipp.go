package main

// A minimal IPP client for the local CUPS server, just enough for
// `mpcloud login`: list jobs waiting for a login and give CUPS the login.

import (
	"bytes"
	"context"
	"encoding/binary"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"os/user"
	"strconv"
	"time"
)

const (
	ippGetJobAttributes = 0x0009
	ippGetJobs          = 0x000A
	cupsAuthenticateJob = 0x400E

	tagOperation = 0x01
	tagJob       = 0x02
	tagEnd       = 0x03

	tagInteger  = 0x21
	tagBoolean  = 0x22
	tagEnum     = 0x23
	tagText     = 0x41
	tagName     = 0x42
	tagKeyword  = 0x44
	tagURI      = 0x45
	tagCharset  = 0x47
	tagLanguage = 0x48

	jobHeld      = 4
	jobCanceled  = 7
	jobAborted   = 8
	jobCompleted = 9
)

type ippAttr struct {
	tag    byte
	name   string
	values []string // integers and enums as decimal strings
}

// cupsJob holds a job's attributes from a CUPS response.
type cupsJob map[string][]string

func (j cupsJob) get(name string) string {
	if v := j[name]; len(v) > 0 {
		return v[0]
	}
	return ""
}

func (j cupsJob) id() int    { n, _ := strconv.Atoi(j.get("job-id")); return n }
func (j cupsJob) state() int { n, _ := strconv.Atoi(j.get("job-state")); return n }

func (j cupsJob) has(name, value string) bool {
	for _, v := range j[name] {
		if v == value {
			return true
		}
	}
	return false
}

// waitingForLogin reports whether CUPS is holding the job until someone
// gives it a login.
func (j cupsJob) waitingForLogin() bool {
	return j.state() == jobHeld && j.has("job-state-reasons", "cups-held-for-authentication")
}

func encodeIPP(op uint16, attrs []ippAttr) []byte {
	var b bytes.Buffer
	b.Write([]byte{2, 0})
	binary.Write(&b, binary.BigEndian, op)
	binary.Write(&b, binary.BigEndian, uint32(1))
	b.WriteByte(tagOperation)
	for _, a := range attrs {
		for i, v := range a.values {
			name := a.name
			if i > 0 {
				name = "" // additional value of the same attribute
			}
			var val []byte
			switch a.tag {
			case tagInteger, tagEnum:
				n, _ := strconv.Atoi(v)
				val = binary.BigEndian.AppendUint32(nil, uint32(n))
			case tagBoolean:
				val = []byte{0}
				if v == "true" {
					val[0] = 1
				}
			default:
				val = []byte(v)
			}
			b.WriteByte(a.tag)
			binary.Write(&b, binary.BigEndian, uint16(len(name)))
			b.WriteString(name)
			binary.Write(&b, binary.BigEndian, uint16(len(val)))
			b.Write(val)
		}
	}
	b.WriteByte(tagEnd)
	return b.Bytes()
}

// decodeIPP returns the response status and its job attribute groups.
func decodeIPP(b []byte) (status int, jobs []cupsJob, err error) {
	if len(b) < 9 {
		return 0, nil, errors.New("short IPP response")
	}
	status = int(binary.BigEndian.Uint16(b[2:4]))
	var cur cupsJob
	var last string
	for p := 8; p < len(b); {
		tag := b[p]
		p++
		if tag == tagEnd {
			break
		}
		if tag < 0x10 { // start of a new attribute group
			cur = nil
			if tag == tagJob {
				cur = cupsJob{}
				jobs = append(jobs, cur)
			}
			continue
		}
		if p+2 > len(b) {
			return status, jobs, errors.New("bad IPP response")
		}
		nl := int(binary.BigEndian.Uint16(b[p:]))
		p += 2
		if p+nl+2 > len(b) {
			return status, jobs, errors.New("bad IPP response")
		}
		if nl > 0 {
			last = string(b[p : p+nl])
		}
		p += nl
		vl := int(binary.BigEndian.Uint16(b[p:]))
		p += 2
		if p+vl > len(b) {
			return status, jobs, errors.New("bad IPP response")
		}
		val := b[p : p+vl]
		p += vl
		if cur == nil {
			continue
		}
		s := string(val)
		if (tag == tagInteger || tag == tagEnum) && vl == 4 {
			s = strconv.Itoa(int(int32(binary.BigEndian.Uint32(val))))
		}
		cur[last] = append(cur[last], s)
	}
	return status, jobs, nil
}

// cupsRequest sends an IPP request to the local CUPS server. It uses CUPS's
// local socket when there is one, where CUPS can check which user is asking,
// as the job owner must for CUPS-Authenticate-Job.
func cupsRequest(ctx context.Context, op uint16, attrs []ippAttr) (int, []cupsJob, error) {
	me, err := user.Current()
	if err != nil {
		return 0, nil, err
	}
	attrs = append([]ippAttr{
		{tagCharset, "attributes-charset", []string{"utf-8"}},
		{tagLanguage, "attributes-natural-language", []string{"en"}},
	}, append(attrs, ippAttr{tagName, "requesting-user-name", []string{me.Username}})...)

	tr := &http.Transport{}
	for _, sock := range []string{"/run/cups/cups.sock", "/var/run/cups/cups.sock"} {
		if _, err := os.Stat(sock); err == nil {
			tr.DialContext = func(ctx context.Context, _, _ string) (net.Conn, error) {
				return (&net.Dialer{}).DialContext(ctx, "unix", sock)
			}
			break
		}
	}
	client := &http.Client{Transport: tr, Timeout: 30 * time.Second}
	req, err := http.NewRequestWithContext(ctx, "POST", "http://localhost:631/", bytes.NewReader(encodeIPP(op, attrs)))
	if err != nil {
		return 0, nil, err
	}
	req.Header.Set("Content-Type", "application/ipp")
	if tr.DialContext != nil {
		req.Header.Set("Authorization", "PeerCred "+me.Username)
	}
	resp, err := client.Do(req)
	if err != nil {
		return 0, nil, fmt.Errorf("couldn't reach CUPS (is it running?): %w", err)
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return 0, nil, err
	}
	if resp.StatusCode != http.StatusOK {
		return 0, nil, fmt.Errorf("CUPS refused the request: %s", resp.Status)
	}
	return decodeIPP(body)
}

var jobAttrs = []string{"job-id", "job-name", "job-state", "job-state-reasons", "job-printer-state-message", "job-printer-uri"}

// myUnfinishedJobs lists the current user's jobs that haven't finished.
func myUnfinishedJobs(ctx context.Context) ([]cupsJob, error) {
	_, jobs, err := cupsRequest(ctx, ippGetJobs, []ippAttr{
		{tagURI, "printer-uri", []string{"ipp://localhost/"}},
		{tagBoolean, "my-jobs", []string{"true"}},
		{tagKeyword, "which-jobs", []string{"not-completed"}},
		{tagKeyword, "requested-attributes", jobAttrs},
	})
	return jobs, err
}

func cupsJobInfo(ctx context.Context, id int) (cupsJob, error) {
	_, jobs, err := cupsRequest(ctx, ippGetJobAttributes, []ippAttr{
		{tagURI, "job-uri", []string{"ipp://localhost/jobs/" + strconv.Itoa(id)}},
		{tagKeyword, "requested-attributes", jobAttrs},
	})
	if err != nil {
		return nil, err
	}
	if len(jobs) == 0 {
		return nil, fmt.Errorf("job %d not found", id)
	}
	return jobs[0], nil
}

// authenticateJob gives CUPS the login for a job that's waiting for one, and
// CUPS retries the job with it.
func authenticateJob(ctx context.Context, id int, username, password string) error {
	status, _, err := cupsRequest(ctx, cupsAuthenticateJob, []ippAttr{
		{tagURI, "job-uri", []string{"ipp://localhost/jobs/" + strconv.Itoa(id)}},
		{tagText, "auth-info", []string{username, password}},
	})
	if err != nil {
		return err
	}
	if status >= 0x0100 {
		return fmt.Errorf("CUPS didn't accept the login for job %d (IPP status 0x%04x)", id, status)
	}
	return nil
}
