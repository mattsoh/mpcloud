// Package cloudprint is a client for PaperCut Mobility Print "Cloud Print",
// reimplemented from the Windows client (mobility-print-client 2024-09-02).
// See docs/PROTOCOL.md for details. Protocol summary:
//
//  1. Signalling over REST at https://<host>/client/v1 with the share token
//     from the mobilityprint:// link as a Bearer token:
//     POST   /session                      -> {id, iceConfig{servers, maxChunkSize}}
//     PUT    /session/{id}/offer           <- {iceOffer: base64(json(SessionDescription))}
//     GET    /session/{id}/answer          -> {iceAnswer: ...} (polled)
//     POST   /session/{id}/candidate       <- {iceCandidates: [json(ICECandidateInit)]}
//     GET    /session/{id}/servercandidates?since=N -> {iceCandidates, updated}
//     DELETE /session/{id}
//  2. A WebRTC peer connection to the organization's Mobility Print server
//     with seven data channels created by the client.
//  3. Requests are text messages; replies are binary chunks (optionally
//     gzipped per message) terminated by a "FINISH" text message. Errors are
//     signalled with an "ERROR:" prefix.
package cloudprint

import (
	"bytes"
	"compress/gzip"
	"context"
	"crypto/rand"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	"math/big"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"time"

	"github.com/pion/webrtc/v4"
)

const userAgent = "PaperCutMobilityPrintCloudClientGo/1.0.0"

// ClientVersion is the Windows client version we identify as in job details.
const ClientVersion = "2024-09-02-1417"

var channelLabels = []string{"SERVERINFO", "TOKEN", "PRINTER", "CAPABILITIES", "JOBDETAILS", "JOB", "PING"}

type iceServer struct {
	URLs       []string `json:"urls"`
	Username   string   `json:"username"`
	Credential string   `json:"credential"`
}

type createSessionResponse struct {
	ID        string `json:"id"`
	ICEConfig struct {
		Servers      []iceServer `json:"servers"`
		MaxChunkSize int         `json:"maxChunkSize"`
	} `json:"iceConfig"`
}

type Printer struct {
	Name         string `json:"name"`
	Description  string `json:"description"`
	AuthMode     string `json:"authMode"`
	Capabilities struct {
		MediaSizes  []MediaSize `json:"mediaSizes"`
		Resolutions []struct {
			HorizontalDpi int32 `json:"horizontalDpi"`
			VerticalDpi   int32 `json:"verticalDpi"`
		} `json:"resolutions"`
		Color  []string `json:"color"`
		Duplex []string `json:"duplex"`
	} `json:"capabilities"`
}

type MediaSize struct {
	Name          string `json:"name"`
	DisplayName   string `json:"customDisplayName"`
	WidthMicrons  int    `json:"widthMicrons"`
	HeightMicrons int    `json:"heightMicrons"`
	IsDefault     bool   `json:"isDefault"`
}

// RequiresAuth reports whether jobs to this printer need user credentials.
func (p *Printer) RequiresAuth() bool {
	return p.AuthMode != "" && p.AuthMode != "none"
}

type ServerInfo struct {
	Version          string `json:"version"`
	ChromeEncryption bool   `json:"chromeEncryption"`
	SignInUserPass   bool   `json:"signInUserPass"`
	SignInWithGoogle bool   `json:"signInWithGoogle"`
}

type Credentials struct {
	Username string `json:"username,omitempty"`
	Password string `json:"password,omitempty"`
	Provider string `json:"provider,omitempty"`
	UserID   string `json:"userID,omitempty"`
	Token    string `json:"token,omitempty"`
}

type JobParams struct {
	ID                 string      `json:"id"`
	PrinterName        string      `json:"printerName"`
	DocumentName       string      `json:"documentName"`
	Duplex             string      `json:"duplex"`
	Color              string      `json:"color"`
	Copies             string      `json:"copies"`
	MediaSize          string      `json:"mediaSize"`
	MediaHeightMicrons string      `json:"mediaHeightMicrons"`
	MediaWidthMicrons  string      `json:"mediaWidthMicrons"`
	PageRange          string      `json:"pageRange"`
	ContentType        string      `json:"contentType"`
	Credentials        Credentials `json:"credentials"`
	Remember           string      `json:"remember"`
	RememberedToken    string      `json:"rememberedToken"`
}

type JobDetails struct {
	ClientVersion string    `json:"clientVersion"`
	PrintToken    string    `json:"printToken"`
	Params        JobParams `json:"params"`
	FileSize      int64     `json:"fileSize"`
}

type Client struct {
	host       string
	shareToken string
	clientID   string
	http       *http.Client
	sessionID  string
	chunkSize  int
	pc         *webrtc.PeerConnection
	channels   map[string]*channel
	Verbose    bool
}

type channel struct {
	dc      *webrtc.DataChannel
	open    chan struct{}
	mu      sync.Mutex
	buf     bytes.Buffer
	replies chan reply
}

type reply struct {
	data []byte
	err  error
}

func randString(set string, n int) string {
	b := make([]byte, n)
	for i := range b {
		x, _ := rand.Int(rand.Reader, big.NewInt(int64(len(set))))
		b[i] = set[x.Int64()]
	}
	return string(b)
}

// NewJobID returns a random job identifier.
func NewJobID() string {
	return randString("0123456789abcdef", 32)
}

// IsAuthError reports whether err is the server rejecting credentials or a
// remember-me token.
func IsAuthError(err error) bool {
	m := strings.ToLower(err.Error())
	return strings.Contains(m, "authentication") || strings.Contains(m, "unauthori") || strings.Contains(m, "credential")
}

func NewClient(host, shareToken string) *Client {
	letters := "ABCDEFGHIJKLMNOPQRSTUVWXYZabcdefghijklmnopqrstuvwxyz"
	return &Client{
		host:       host,
		shareToken: shareToken,
		clientID:   randString(letters, 8) + "-" + randString(letters, 8),
		http:       &http.Client{Timeout: 30 * time.Second},
		channels:   map[string]*channel{},
	}
}

func (c *Client) logf(format string, args ...any) {
	if c.Verbose {
		log.Printf(format, args...)
	}
}

func (c *Client) do(ctx context.Context, method, path string, query string, in, out any) (int, error) {
	var body io.Reader
	if in != nil {
		b, err := json.Marshal(in)
		if err != nil {
			return 0, err
		}
		body = bytes.NewReader(b)
	}
	u := "https://" + c.host + "/client/v1" + path
	if query != "" {
		u += "?" + query
	}
	req, err := http.NewRequestWithContext(ctx, method, u, body)
	if err != nil {
		return 0, err
	}
	req.Header.Set("Accept", "application/json")
	req.Header.Set("Authorization", "Bearer "+c.shareToken)
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("User-Agent", userAgent)
	req.Header.Set("X-PaperCut-Client-Id", c.clientID)
	resp, err := c.http.Do(req)
	if err != nil {
		return 0, err
	}
	defer resp.Body.Close()
	data, _ := io.ReadAll(resp.Body)
	c.logf("%s %s -> %d %s", method, path, resp.StatusCode, truncate(string(data), 300))
	if resp.StatusCode >= 300 {
		return resp.StatusCode, fmt.Errorf("%s %s: HTTP %d: %s", method, path, resp.StatusCode, strings.TrimSpace(string(data)))
	}
	if out != nil && len(data) > 0 {
		if err := json.Unmarshal(data, out); err != nil {
			return resp.StatusCode, fmt.Errorf("%s %s: bad response: %w", method, path, err)
		}
	}
	return resp.StatusCode, nil
}

func truncate(s string, n int) string {
	if len(s) > n {
		return s[:n] + "..."
	}
	return s
}

func encodeSD(sd webrtc.SessionDescription) (string, error) {
	b, err := json.Marshal(sd)
	if err != nil {
		return "", err
	}
	return base64.StdEncoding.EncodeToString(b), nil
}

func decodeSD(s string) (webrtc.SessionDescription, error) {
	var sd webrtc.SessionDescription
	b, err := base64.StdEncoding.DecodeString(s)
	if err != nil {
		if b, err = base64.URLEncoding.DecodeString(s); err != nil {
			return sd, err
		}
	}
	return sd, json.Unmarshal(b, &sd)
}

// ErrInvalidLink means the share token from the mobilityprint:// link is
// malformed, expired or was rejected by the cloud service.
var ErrInvalidLink = errors.New("the Cloud Print link is invalid or has expired")

// CheckShareToken validates a share token locally: it must be a JWT whose
// expiry, if present, is in the future. It does not verify the signature.
func CheckShareToken(token string) error {
	parts := strings.Split(token, ".")
	if len(parts) != 3 {
		return fmt.Errorf("%w (token is not a JWT)", ErrInvalidLink)
	}
	payload, err := base64.RawURLEncoding.DecodeString(strings.TrimRight(parts[1], "="))
	if err != nil {
		return fmt.Errorf("%w (token is not a JWT)", ErrInvalidLink)
	}
	var claims struct {
		Exp int64 `json:"exp"`
	}
	if err := json.Unmarshal(payload, &claims); err != nil {
		return fmt.Errorf("%w (token is not a JWT)", ErrInvalidLink)
	}
	if claims.Exp != 0 && time.Unix(claims.Exp, 0).Before(time.Now()) {
		return fmt.Errorf("%w (expired %s)", ErrInvalidLink, time.Unix(claims.Exp, 0).Format("2006-01-02"))
	}
	return nil
}

func (c *Client) createSession(ctx context.Context) (*createSessionResponse, error) {
	if err := CheckShareToken(c.shareToken); err != nil {
		return nil, err
	}
	var sess createSessionResponse
	code, err := c.do(ctx, "POST", "/session", "", struct{}{}, &sess)
	if code == http.StatusUnauthorized || code == http.StatusForbidden {
		return nil, fmt.Errorf("%w (rejected by %s)", ErrInvalidLink, c.host)
	}
	if err != nil {
		return nil, fmt.Errorf("creating session: %w", err)
	}
	return &sess, nil
}

// Verify checks with the cloud service that the share token is accepted,
// without connecting to the Mobility Print server.
func (c *Client) Verify(ctx context.Context) error {
	sess, err := c.createSession(ctx)
	if err != nil {
		return err
	}
	c.do(ctx, "DELETE", "/session/"+sess.ID, "", nil, nil)
	return nil
}

// Connect establishes the session and peer connection and waits for all data
// channels to open.
func (c *Client) Connect(ctx context.Context) error {
	sess, err := c.createSession(ctx)
	if err != nil {
		return err
	}
	c.sessionID = sess.ID
	c.chunkSize = sess.ICEConfig.MaxChunkSize
	if c.chunkSize <= 0 {
		c.chunkSize = 16 * 1024
	}

	var servers []webrtc.ICEServer
	for _, s := range sess.ICEConfig.Servers {
		srv := webrtc.ICEServer{URLs: s.URLs, Username: s.Username}
		if s.Credential != "" {
			srv.Credential = s.Credential
		}
		servers = append(servers, srv)
	}
	pc, err := webrtc.NewPeerConnection(webrtc.Configuration{ICEServers: servers})
	if err != nil {
		return err
	}
	c.pc = pc

	connected := make(chan struct{})
	failed := make(chan struct{})
	var once sync.Once
	pc.OnConnectionStateChange(func(s webrtc.PeerConnectionState) {
		c.logf("peer connection state: %s", s)
		switch s {
		case webrtc.PeerConnectionStateConnected:
			once.Do(func() { close(connected) })
		case webrtc.PeerConnectionStateFailed:
			select {
			case <-failed:
			default:
				close(failed)
			}
		}
	})

	for _, label := range channelLabels {
		dc, err := pc.CreateDataChannel(label, nil)
		if err != nil {
			return err
		}
		ch := &channel{dc: dc, open: make(chan struct{}), replies: make(chan reply, 4)}
		c.channels[label] = ch
		dc.OnOpen(func() { close(ch.open) })
		dc.OnMessage(func(m webrtc.DataChannelMessage) { ch.onMessage(m) })
	}

	// Trickle our candidates to the cloud as they are gathered.
	pc.OnICECandidate(func(cand *webrtc.ICECandidate) {
		if cand == nil {
			return
		}
		b, _ := json.Marshal(cand.ToJSON())
		go func() {
			req := map[string][]string{"iceCandidates": {string(b)}}
			if _, err := c.do(ctx, "POST", "/session/"+c.sessionID+"/candidate", "", req, nil); err != nil {
				c.logf("notify candidate: %v", err)
			}
		}()
	})

	offer, err := pc.CreateOffer(nil)
	if err != nil {
		return err
	}
	if err := pc.SetLocalDescription(offer); err != nil {
		return err
	}
	enc, err := encodeSD(offer)
	if err != nil {
		return err
	}
	if _, err := c.do(ctx, "PUT", "/session/"+c.sessionID+"/offer", "", map[string]string{"iceOffer": enc}, nil); err != nil {
		return fmt.Errorf("sending offer: %w", err)
	}

	// Poll for the answer.
	deadline := time.Now().Add(45 * time.Second)
	for {
		var ans struct {
			ICEAnswer string `json:"iceAnswer"`
		}
		code, err := c.do(ctx, "GET", "/session/"+c.sessionID+"/answer", "", nil, &ans)
		if err == nil && ans.ICEAnswer != "" {
			sd, err := decodeSD(ans.ICEAnswer)
			if err != nil {
				return fmt.Errorf("decoding answer: %w", err)
			}
			if err := pc.SetRemoteDescription(sd); err != nil {
				return fmt.Errorf("applying answer: %w", err)
			}
			break
		}
		if err != nil && code != http.StatusNotFound && code != http.StatusNoContent && code != http.StatusAccepted {
			return fmt.Errorf("fetching answer: %w", err)
		}
		if time.Now().After(deadline) {
			return errors.New("timed out waiting for the Mobility Print server to answer (is it online?)")
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(time.Second):
		}
	}

	// Poll for server candidates until connected.
	pollCtx, stopPoll := context.WithCancel(ctx)
	defer stopPoll()
	go func() {
		var since int64
		for {
			var r struct {
				ICECandidates []string `json:"iceCandidates"`
				Updated       int64    `json:"updated"`
			}
			if _, err := c.do(pollCtx, "GET", "/session/"+c.sessionID+"/servercandidates", fmt.Sprintf("since=%d", since), nil, &r); err == nil {
				for _, s := range r.ICECandidates {
					var ci webrtc.ICECandidateInit
					if err := json.Unmarshal([]byte(s), &ci); err != nil {
						c.logf("bad server candidate %q: %v", s, err)
						continue
					}
					if err := pc.AddICECandidate(ci); err != nil {
						c.logf("add candidate: %v", err)
					}
				}
				if r.Updated > since {
					since = r.Updated
				}
			}
			select {
			case <-pollCtx.Done():
				return
			case <-time.After(time.Second):
			}
		}
	}()

	select {
	case <-connected:
	case <-failed:
		return errors.New("WebRTC connection to the Mobility Print server failed")
	case <-time.After(45 * time.Second):
		return errors.New("timed out establishing WebRTC connection")
	case <-ctx.Done():
		return ctx.Err()
	}
	for label, ch := range c.channels {
		select {
		case <-ch.open:
		case <-time.After(20 * time.Second):
			return fmt.Errorf("data channel %s did not open", label)
		}
	}
	return nil
}

func gunzipMaybe(b []byte) []byte {
	if len(b) < 2 || b[0] != 0x1f || b[1] != 0x8b {
		return b
	}
	r, err := gzip.NewReader(bytes.NewReader(b))
	if err != nil {
		return b
	}
	out, err := io.ReadAll(r)
	if err != nil {
		return b
	}
	return out
}

func (ch *channel) onMessage(m webrtc.DataChannelMessage) {
	ch.mu.Lock()
	defer ch.mu.Unlock()
	if !m.IsString {
		ch.buf.Write(gunzipMaybe(m.Data))
		return
	}
	s := string(m.Data)
	switch {
	case s == "FINISH":
		data := append([]byte(nil), ch.buf.Bytes()...)
		ch.buf.Reset()
		ch.deliver(reply{data: data})
	case strings.HasPrefix(s, "ERROR:"):
		ch.buf.Reset()
		ch.deliver(reply{err: errors.New(strings.TrimSpace(strings.TrimPrefix(s, "ERROR:")))})
	default:
		ch.deliver(reply{data: m.Data})
	}
}

func (ch *channel) deliver(r reply) {
	// Errors may also be sent as a chunked binary "ERROR:..." payload.
	if r.err == nil && bytes.HasPrefix(r.data, []byte("ERROR:")) {
		r = reply{err: errors.New(strings.TrimSpace(string(r.data[6:])))}
	}
	select {
	case ch.replies <- r:
	default:
	}
}

func (c *Client) request(ctx context.Context, label string, send func(*webrtc.DataChannel) error, timeout time.Duration) ([]byte, error) {
	ch := c.channels[label]
	// Drain stale replies.
	for len(ch.replies) > 0 {
		<-ch.replies
	}
	if err := send(ch.dc); err != nil {
		return nil, err
	}
	select {
	case r := <-ch.replies:
		if r.err != nil {
			return nil, fmt.Errorf("server error on %s: %w", label, r.err)
		}
		return r.data, nil
	case <-time.After(timeout):
		return nil, fmt.Errorf("timed out waiting for %s reply", label)
	case <-ctx.Done():
		return nil, ctx.Err()
	}
}

func (c *Client) requestText(ctx context.Context, label, text string) ([]byte, error) {
	return c.request(ctx, label, func(dc *webrtc.DataChannel) error { return dc.SendText(text) }, 30*time.Second)
}

func (c *Client) ServerInfo(ctx context.Context) (*ServerInfo, error) {
	b, err := c.requestText(ctx, "SERVERINFO", "")
	if err != nil {
		return nil, err
	}
	var si ServerInfo
	if err := json.Unmarshal(b, &si); err != nil {
		return nil, fmt.Errorf("parsing server info %q: %w", truncate(string(b), 200), err)
	}
	return &si, nil
}

// PrintToken exchanges the share token from the link for a print token.
func (c *Client) PrintToken(ctx context.Context) (string, error) {
	b, err := c.requestText(ctx, "TOKEN", c.shareToken)
	if err != nil {
		return "", err
	}
	return strings.TrimSpace(string(b)), nil
}

func (c *Client) Printers(ctx context.Context, printToken string) ([]Printer, error) {
	b, err := c.requestText(ctx, "PRINTER", printToken)
	if err != nil {
		return nil, err
	}
	var ps []Printer
	if err := json.Unmarshal(b, &ps); err != nil {
		return nil, fmt.Errorf("parsing printers %q: %w", truncate(string(b), 200), err)
	}
	return ps, nil
}

// SendJob sends the job details, waits for acceptance and then streams the
// document in chunks over the JOB channel. Returns the details reply (a
// remember-me token when the server issues one).
func (c *Client) SendJob(ctx context.Context, details JobDetails, doc []byte) (string, error) {
	details.FileSize = int64(len(doc))
	b, err := json.Marshal(details)
	if err != nil {
		return "", err
	}
	c.logf("job details: %s", b)
	resp, err := c.request(ctx, "JOBDETAILS", func(dc *webrtc.DataChannel) error { return dc.Send(b) }, 60*time.Second)
	if err != nil {
		return "", err
	}
	c.logf("job details reply: %q", truncate(string(resp), 200))

	job := c.channels["JOB"].dc
	job.SetBufferedAmountLowThreshold(uint64(c.chunkSize) * 8)
	low := make(chan struct{}, 1)
	job.OnBufferedAmountLow(func() {
		select {
		case low <- struct{}{}:
		default:
		}
	})
	for off := 0; off < len(doc); off += c.chunkSize {
		end := min(off+c.chunkSize, len(doc))
		for job.BufferedAmount() > uint64(c.chunkSize)*64 {
			select {
			case <-low:
			case <-time.After(5 * time.Second):
			case <-ctx.Done():
				return "", ctx.Err()
			}
		}
		if err := job.Send(doc[off:end]); err != nil {
			return "", fmt.Errorf("sending chunk at %d: %w", off, err)
		}
	}
	// Let the buffer drain before returning so the caller doesn't close early.
	for job.BufferedAmount() > 0 {
		select {
		case <-low:
		case <-time.After(200 * time.Millisecond):
		case <-ctx.Done():
			return "", ctx.Err()
		}
	}
	return strings.TrimSpace(string(resp)), nil
}

func (c *Client) Connected() bool {
	return c.pc != nil && c.pc.ConnectionState() == webrtc.PeerConnectionStateConnected
}

func (c *Client) Close() {
	if c.pc != nil {
		c.pc.Close()
	}
	if c.sessionID != "" {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		c.do(ctx, "DELETE", "/session/"+c.sessionID, "", nil, nil)
	}
}

// PrinterURL builds the value the server expects in JobParams.PrinterName:
// it parses it as a URL and takes the path segment after "/printers/"
// (mobility.JobParams.PrinterNameOnly in the original client).
func PrinterURL(name string) string {
	return "https://localhost:9164/printers/" + url.PathEscape(name)
}
