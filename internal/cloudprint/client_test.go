package cloudprint

import (
	"bytes"
	"compress/gzip"
	"encoding/base64"
	"errors"
	"fmt"
	"net/url"
	"strings"
	"testing"
	"time"

	"github.com/pion/webrtc/v4"
)

func TestSessionDescriptionRoundTrip(t *testing.T) {
	in := webrtc.SessionDescription{Type: webrtc.SDPTypeOffer, SDP: "v=0\r\n"}
	enc, err := encodeSD(in)
	if err != nil {
		t.Fatal(err)
	}
	out, err := decodeSD(enc)
	if err != nil {
		t.Fatal(err)
	}
	if out.Type != in.Type || out.SDP != in.SDP {
		t.Fatalf("got %+v", out)
	}
}

func newTestChannel() *channel {
	return &channel{replies: make(chan reply, 4)}
}

func TestChunkedReply(t *testing.T) {
	ch := newTestChannel()
	var gz bytes.Buffer
	w := gzip.NewWriter(&gz)
	w.Write([]byte(`"b"]`))
	w.Close()
	ch.onMessage(webrtc.DataChannelMessage{Data: []byte(`["a",`)})
	ch.onMessage(webrtc.DataChannelMessage{Data: gz.Bytes()}) // gzipped chunk
	ch.onMessage(webrtc.DataChannelMessage{IsString: true, Data: []byte("FINISH")})
	r := <-ch.replies
	if r.err != nil || string(r.data) != `["a","b"]` {
		t.Fatalf("got %q, %v", r.data, r.err)
	}
}

func TestErrorReply(t *testing.T) {
	ch := newTestChannel()
	ch.onMessage(webrtc.DataChannelMessage{Data: []byte("ERROR:invalid printer name")})
	ch.onMessage(webrtc.DataChannelMessage{IsString: true, Data: []byte("FINISH")})
	r := <-ch.replies
	if r.err == nil || !strings.Contains(r.err.Error(), "invalid printer name") {
		t.Fatalf("got %q, %v", r.data, r.err)
	}
}

// The server recovers the name with url.Parse(...).Path split on "/printers/".
func TestPrinterURL(t *testing.T) {
	name := "Color Printer #2 / Floor 3"
	u, err := url.Parse(PrinterURL(name))
	if err != nil {
		t.Fatal(err)
	}
	parts := strings.Split(u.Path, "/printers/")
	if len(parts) != 2 || parts[1] != name {
		t.Fatalf("round trip gave %q", u.Path)
	}
}

func jwt(payload string) string {
	enc := base64.RawURLEncoding.EncodeToString
	return enc([]byte(`{"alg":"RS256"}`)) + "." + enc([]byte(payload)) + ".sig"
}

func TestCheckShareToken(t *testing.T) {
	future := time.Now().Add(time.Hour).Unix()
	cases := map[string]bool{
		jwt(fmt.Sprintf(`{"exp":%d}`, future)): true,
		jwt(`{"sub":"tokenCreation"}`):         true,
		jwt(`{"exp":1}`):                       false,
		"garbage":                              false,
		"a.!!!.c":                              false,
	}
	for tok, ok := range cases {
		err := CheckShareToken(tok)
		if (err == nil) != ok || (err != nil && !errors.Is(err, ErrInvalidLink)) {
			t.Errorf("CheckShareToken(%q) = %v, want ok=%t", tok, err, ok)
		}
	}
}
