package daemon

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"
)

func TestTail(t *testing.T) {
	tests := []struct {
		content string
		n       int
		want    string
	}{
		{"a\nb\nc\n", 2, "b\nc\n"},
		{"a\nb\nc\n", 3, "a\nb\nc\n"},
		{"a\nb\nc\n", 10, "a\nb\nc\n"},
		{"a\nb\nc", 1, "c"},
		{"a\nb\nc\n", 0, ""},
		{"", 5, ""},
	}
	for _, tt := range tests {
		path := filepath.Join(t.TempDir(), "daemon.log")
		if err := os.WriteFile(path, []byte(tt.content), 0o600); err != nil {
			t.Fatal(err)
		}
		var out bytes.Buffer
		offset, err := Tail(path, tt.n, &out)
		if err != nil {
			t.Fatal(err)
		}
		if out.String() != tt.want || offset != int64(len(tt.content)) {
			t.Fatalf("Tail(%q, %d) = %q, %d", tt.content, tt.n, out.String(), offset)
		}
	}
}

func TestTailLargeLogDropsPartialLine(t *testing.T) {
	path := filepath.Join(t.TempDir(), "daemon.log")
	var b strings.Builder
	for i := 0; b.Len() < tailWindow+4096; i++ {
		fmt.Fprintf(&b, "line %06d %s\n", i, strings.Repeat("x", 50))
	}
	if err := os.WriteFile(path, []byte(b.String()), 0o600); err != nil {
		t.Fatal(err)
	}
	var out bytes.Buffer
	if _, err := Tail(path, 1<<30, &out); err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(out.String(), "line ") || !strings.HasSuffix(b.String(), out.String()) {
		t.Fatalf("tail starts mid-line: %q", out.String()[:40])
	}
}

func TestTailMissing(t *testing.T) {
	_, err := Tail(filepath.Join(t.TempDir(), "daemon.log"), 5, &bytes.Buffer{})
	if !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("err = %v", err)
	}
	if strings.Contains(err.Error(), "daemon.log") {
		t.Fatalf("error leaks path: %v", err)
	}
}

type syncBuffer struct {
	mu sync.Mutex
	b  bytes.Buffer
}

func (s *syncBuffer) Write(p []byte) (int, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.b.Write(p)
}

func (s *syncBuffer) String() string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.b.String()
}

func TestFollowAcrossRotation(t *testing.T) {
	path := filepath.Join(t.TempDir(), "daemon.log")
	if err := os.WriteFile(path, []byte("old\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	out := &syncBuffer{}
	done := make(chan error, 1)
	go func() { done <- Follow(ctx, path, 4, time.Millisecond, out) }()

	appendTo := func(p, s string) {
		f, err := os.OpenFile(p, os.O_WRONLY|os.O_APPEND|os.O_CREATE, 0o600)
		if err != nil {
			t.Fatal(err)
		}
		f.WriteString(s)
		f.Close()
	}
	waitFor := func(want string) {
		t.Helper()
		deadline := time.Now().Add(5 * time.Second)
		for out.String() != want {
			if time.Now().After(deadline) {
				t.Fatalf("output = %q, want %q", out.String(), want)
			}
			time.Sleep(time.Millisecond)
		}
	}

	appendTo(path, "one\n")
	waitFor("one\n")
	// Rotate the way rotatingFile does: rename, then create anew.
	appendTo(path, "two\n")
	if err := os.Rename(path, path+".1"); err != nil {
		t.Fatal(err)
	}
	appendTo(path, "three\n")
	waitFor("one\ntwo\nthree\n")

	cancel()
	if err := <-done; err != nil {
		t.Fatal(err)
	}
}
