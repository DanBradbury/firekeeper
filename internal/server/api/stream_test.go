package api

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/DanBradbury/firekeeper/internal/server/auth"
	"github.com/DanBradbury/firekeeper/internal/server/store"
)

type sseFrame struct {
	comment, event, data string
}

// readFrames parses server-sent events from body onto a channel.
func readFrames(body *bufio.Reader) <-chan sseFrame {
	out := make(chan sseFrame, 64)
	go func() {
		defer close(out)
		var f sseFrame
		for {
			line, err := body.ReadString('\n')
			if err != nil {
				return
			}
			line = strings.TrimSuffix(line, "\n")
			switch {
			case line == "":
				out <- f
				f = sseFrame{}
			case strings.HasPrefix(line, ":"):
				f.comment = strings.TrimSpace(line[1:])
			case strings.HasPrefix(line, "event: "):
				f.event = line[len("event: "):]
			case strings.HasPrefix(line, "data: "):
				f.data = line[len("data: "):]
			}
		}
	}()
	return out
}

// newServer registers srv.Close as a cleanup so it runs after the stream
// request is cancelled; a deferred Close would wait on the open stream.
func newServer(t *testing.T, s *store.Store) *httptest.Server {
	t.Helper()
	srv := httptest.NewServer(Handler(s))
	t.Cleanup(srv.Close)
	return srv
}

func openStream(t *testing.T, srv *httptest.Server) <-chan sseFrame {
	t.Helper()
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	req, _ := http.NewRequestWithContext(ctx, "GET", srv.URL+"/v1/stream", nil)
	resp, err := srv.Client().Do(req)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { resp.Body.Close() })
	if resp.StatusCode != 200 || resp.Header.Get("Content-Type") != "text/event-stream" {
		t.Fatalf("stream: %d %s", resp.StatusCode, resp.Header.Get("Content-Type"))
	}
	frames := readFrames(bufio.NewReader(resp.Body))
	if f := next(t, frames); f.comment != "connected" {
		t.Fatalf("first frame %+v", f)
	}
	return frames
}

func next(t *testing.T, frames <-chan sseFrame) sseFrame {
	t.Helper()
	select {
	case f, ok := <-frames:
		if !ok {
			t.Fatal("stream closed")
		}
		return f
	case <-time.After(5 * time.Second):
		t.Fatal("timed out waiting for stream frame")
	}
	return sseFrame{}
}

func TestStreamEventAppended(t *testing.T) {
	s := newStore(t)
	srv := newServer(t, s)
	frames := openStream(t, srv)

	b, _ := json.Marshal(req(3))
	resp, err := srv.Client().Post(srv.URL+"/v1/ingest", "application/json", bytes.NewReader(b))
	if err != nil || resp.StatusCode != 200 {
		t.Fatalf("ingest: %v %v", err, resp)
	}
	resp.Body.Close()

	seen := map[string]bool{}
	for !seen["event.appended"] {
		f := next(t, frames)
		seen[f.event] = true
		if f.event != "event.appended" {
			continue
		}
		var ev streamEvent
		if err := json.Unmarshal([]byte(f.data), &ev); err != nil {
			t.Fatal(err)
		}
		if ev.UID != "m:s" || ev.MachineID != "m" || ev.SessionID != "s" || ev.Seq == nil || *ev.Seq != 2 {
			t.Fatalf("event.appended data %s", f.data)
		}
	}
	if !seen["machine.status"] || !seen["session.updated"] {
		t.Fatalf("missing kinds: %v", seen)
	}
}

func TestStreamKeepalive(t *testing.T) {
	old := keepaliveInterval
	keepaliveInterval = 20 * time.Millisecond
	defer func() { keepaliveInterval = old }()

	s := newStore(t)
	srv := newServer(t, s)
	if f := next(t, openStream(t, srv)); f.comment != "keepalive" {
		t.Fatalf("frame %+v", f)
	}
}

func TestSlowClientDroppedWithoutBlockingIngest(t *testing.T) {
	h := newHub()
	changes := make(chan store.Change)
	go h.run(changes)
	slow := h.subscribe(store.DefaultAccountID)
	fast := h.subscribe(store.DefaultAccountID)

	// Nobody reads slow; sending past its buffer must not block.
	done := make(chan struct{})
	go func() {
		defer close(done)
		for i := 0; i < clientBuffer+10; i++ {
			changes <- store.Change{Kind: store.EventAppended, AccountID: store.DefaultAccountID, MachineID: "m", SessionID: "s", Seq: int64(i)}
			<-fast
		}
	}()
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("broadcast blocked on a slow client")
	}
	n := 0
	for range slow {
		n++
	}
	if n != clientBuffer {
		t.Fatalf("slow client got %d buffered changes before drop, want %d", n, clientBuffer)
	}
	h.unsubscribe(slow) // already dropped; must not double-close

	close(changes)
	if _, ok := <-fast; ok {
		t.Fatal("fast client not closed on shutdown")
	}
	if h.subscribe(store.DefaultAccountID) != nil {
		t.Fatal("subscribe after shutdown should fail")
	}
}

func TestStreamEndsWhenStoreCloses(t *testing.T) {
	s := newStore(t)
	srv := newServer(t, s)
	frames := openStream(t, srv)
	s.Close()
	select {
	case _, ok := <-frames:
		if ok {
			// Drain anything buffered, then require close.
			for range frames {
			}
		}
	case <-time.After(5 * time.Second):
		t.Fatal("stream did not end after store close")
	}
}

func TestHubDeliversOnlyToTheChangesAccount(t *testing.T) {
	h := newHub()
	changes := make(chan store.Change)
	go h.run(changes)
	alice, bob := h.subscribe("alice"), h.subscribe("bob")
	changes <- store.Change{Kind: store.SessionUpdated, AccountID: "alice", MachineID: "m", SessionID: "s"}
	changes <- store.Change{Kind: store.SessionUpdated, AccountID: "bob", MachineID: "m", SessionID: "s"}
	close(changes)
	for _, tc := range []struct {
		name string
		ch   chan store.Change
		want string
	}{{"alice", alice, "alice"}, {"bob", bob, "bob"}} {
		var got []string
		for c := range tc.ch {
			got = append(got, c.AccountID)
		}
		if len(got) != 1 || got[0] != tc.want {
			t.Fatalf("%s received %v, want only [%s]", tc.name, got, tc.want)
		}
	}
}

// A stream opened with a browser session ends once that session does, so
// signing out cannot leave a tab reading transcripts.
func TestStreamEndsWhenBrowserSessionEnds(t *testing.T) {
	old := keepaliveInterval
	keepaliveInterval = 20 * time.Millisecond
	defer func() { keepaliveInterval = old }()

	s := newStore(t)
	ctx := context.Background()
	acct, err := s.CreateAccount(ctx, "a@example.com", "$argon2id$stub", "", time.Now())
	if err != nil {
		t.Fatal(err)
	}
	cookie, ws, err := s.CreateWebSession(ctx, acct.ID, time.Hour, time.Now())
	if err != nil {
		t.Fatal(err)
	}
	srv := httptest.NewServer(auth.New(s).Wrap(Handler(s)))
	t.Cleanup(srv.Close)

	rctx, cancel := context.WithCancel(ctx)
	t.Cleanup(cancel)
	req, _ := http.NewRequestWithContext(rctx, "GET", srv.URL+"/v1/stream", nil)
	req.AddCookie(&http.Cookie{Name: auth.CookieName, Value: cookie})
	resp, err := srv.Client().Do(req)
	if err != nil || resp.StatusCode != 200 {
		t.Fatalf("stream: %v %v", resp, err)
	}
	t.Cleanup(func() { resp.Body.Close() })
	frames := readFrames(bufio.NewReader(resp.Body))
	next(t, frames) // connected

	out, _ := http.NewRequest("POST", srv.URL+"/v1/auth/logout", nil)
	out.AddCookie(&http.Cookie{Name: auth.CookieName, Value: cookie})
	out.Header.Set(auth.CSRFHeader, ws.CSRFToken)
	lr, err := srv.Client().Do(out)
	if err != nil || lr.StatusCode != 200 {
		t.Fatalf("logout: %v %v", lr, err)
	}
	lr.Body.Close()

	done := make(chan struct{})
	go func() {
		for range frames {
		}
		close(done)
	}()
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("stream outlived its browser session")
	}
}
