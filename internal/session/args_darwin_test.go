//go:build darwin

package session

import (
	"encoding/binary"
	"os"
	"path/filepath"
	"slices"
	"testing"
	"time"
)

func procargs2Block(argc int32, exe string, args ...string) []byte {
	b := binary.NativeEndian.AppendUint32(nil, uint32(argc))
	b = append(b, exe...)
	b = append(b, 0, 0, 0)
	for _, a := range args {
		b = append(b, a...)
		b = append(b, 0)
	}
	b = append(b, "HOME=/secret\x00TOKEN=x\x00"...)
	return b
}

func TestParseProcargs2(t *testing.T) {
	got, err := parseProcargs2(procargs2Block(3, "/opt/my dir/kimi", "/opt/my dir/kimi", "--foo", "a b"))
	if err != nil || !slices.Equal(got, []string{"/opt/my dir/kimi", "--foo", "a b"}) {
		t.Fatalf("parseProcargs2 = %q, %v", got, err)
	}
	for name, block := range map[string][]byte{
		"short":     {1, 0},
		"zero argc": procargs2Block(0, "/x"),
		"negative":  procargs2Block(-1, "/x"),
		"truncated": procargs2Block(5, "/x", "a")[:20],
	} {
		if args, err := parseProcargs2(block); err == nil {
			t.Errorf("%s: parseProcargs2 = %q, want error", name, args)
		}
	}
}

// The real sysctl returns a stand-in process's argument list, including a
// space in the program path, and never its environment.
func TestReadProcessArgsRealProcessDarwin(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "my dir")
	cmd := startHelper(t, dir, "kimi", "--foo", "a b", "web")
	want := []string{filepath.Join(dir, "kimi"), "--foo", "a b", "web"}
	deadline := time.Now().Add(3 * time.Second)
	for {
		got, err := readProcessArgs(cmd.Process.Pid)
		if err == nil && slices.Equal(got, want) {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("readProcessArgs = %q, %v; want %q", got, err, want)
		}
		time.Sleep(10 * time.Millisecond)
	}
	if _, err := readProcessArgs(-1); err == nil {
		t.Fatal("readProcessArgs(-1) succeeded")
	}
}

// BenchmarkReadProcessArgs measures one kern.procargs2 read, the cost per
// candidate process on each refresh.
func BenchmarkReadProcessArgs(b *testing.B) {
	pid := os.Getpid()
	for b.Loop() {
		if _, err := readProcessArgs(pid); err != nil {
			b.Fatal(err)
		}
	}
}
