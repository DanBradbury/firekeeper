package otelrecv

import (
	"bufio"
	"bytes"
	"compress/gzip"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"mime"
	"net"
	"net/http"
	"os"
	"path"
	"path/filepath"
	"strings"
	"sync"
	"time"
)

const (
	// DefaultPort is the OTLP/HTTP port.
	DefaultPort = 4318
	// maxBody is the largest export body accepted, compressed or not.
	maxBody = 8 << 20
)

var (
	errTooLarge    = errors.New("body too large")
	errBadEncoding = errors.New("unsupported content encoding")
)

// Options configures Listen.
type Options struct {
	// Port is the TCP port on 127.0.0.1. Zero asks the kernel for a free port.
	Port int
	// Path is the JSONL file. Records are appended.
	Path string
	// Stdout receives the per-session summary when Listen returns.
	Stdout io.Writer
	// Stderr receives the listen address, the Claude Code environment, and
	// counts of recorded events. Event fields are not written.
	Stderr io.Writer
	// OnListen, when set, is called with the bound 127.0.0.1 address before
	// the server accepts a connection.
	OnListen func(addr string)
	// Quiet skips the Claude Code environment banner. The daemon sets it.
	Quiet bool
}

// Listen serves OTLP/HTTP on 127.0.0.1 until ctx is cancelled, then writes
// the per-session summary. It refuses any other address.
func Listen(ctx context.Context, opt Options) error {
	if opt.Stdout == nil {
		opt.Stdout = io.Discard
	}
	if opt.Stderr == nil {
		opt.Stderr = io.Discard
	}
	addr, err := LoopbackHostPort(opt.Port)
	if err != nil {
		return err
	}
	if opt.Path == "" {
		return errors.New("otel: no output path")
	}
	if err := os.MkdirAll(filepath.Dir(opt.Path), 0o700); err != nil {
		return errors.New("otel: create output directory")
	}
	file, err := os.OpenFile(opt.Path, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o600)
	if err != nil {
		return errors.New("otel: open output file")
	}
	defer file.Close()

	journal := newJournal(file, existingKeys(opt.Path))
	ln, err := net.Listen("tcp", addr)
	if err != nil {
		return errors.New("otel: listen on 127.0.0.1 failed")
	}
	if err := assertLoopback(ln.Addr()); err != nil {
		ln.Close()
		return err
	}

	srv := &http.Server{
		Handler:           newHandler(journal, opt.Stderr),
		ReadHeaderTimeout: 5 * time.Second,
		ReadTimeout:       30 * time.Second,
		WriteTimeout:      10 * time.Second,
		IdleTimeout:       60 * time.Second,
		MaxHeaderBytes:    1 << 20,
	}
	bound := ln.Addr().String()
	fmt.Fprintf(opt.Stderr, "listening on %s\nappending to %s\n", bound, opt.Path)
	if !opt.Quiet {
		writeSetup(opt.Stderr, bound)
	}
	if opt.OnListen != nil {
		opt.OnListen(bound)
	}

	errCh := make(chan error, 1)
	go func() { errCh <- srv.Serve(ln) }()

	var serveErr error
	select {
	case <-ctx.Done():
		shut, cancel := context.WithTimeout(context.Background(), 2*time.Second)
		serveErr = srv.Shutdown(shut)
		cancel()
		<-errCh
	case serveErr = <-errCh:
	}
	if err := journal.sync(); err != nil {
		return err
	}
	if err := WriteSummary(opt.Stdout, journal.records()); err != nil {
		return err
	}
	if serveErr != nil && !errors.Is(serveErr, http.ErrServerClosed) {
		return errors.New("otel: server stopped")
	}
	return nil
}

// DefaultPath is ~/.firekeeper/telemetry.jsonl, the store shared by
// otel-listen and the daemon. It is created when a listener starts.
func DefaultPath() (string, error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return "", errors.New("otel: find home directory")
	}
	return filepath.Join(home, ".firekeeper", "telemetry.jsonl"), nil
}

// LoopbackHostPort returns 127.0.0.1:port. Any other host is not representable
// here: the only input is a port number.
func LoopbackHostPort(port int) (string, error) {
	if port < 0 || port > 65535 {
		return "", fmt.Errorf("otel: port %d is outside 0-65535", port)
	}
	return fmt.Sprintf("127.0.0.1:%d", port), nil
}

func assertLoopback(addr net.Addr) error {
	tcp, ok := addr.(*net.TCPAddr)
	if !ok || tcp.IP == nil || !tcp.IP.Equal(net.ParseIP("127.0.0.1")) {
		return errors.New("otel: refusing to listen: bound address is not 127.0.0.1")
	}
	return nil
}

func writeSetup(w io.Writer, addr string) {
	fmt.Fprintf(w, `
Point Claude Code at this listener from the shell that starts it.
This process records api_request usage only. Leave the content switches unset
so prompts and responses are not sent here either.

  export CLAUDE_CODE_ENABLE_TELEMETRY=1
  export OTEL_LOGS_EXPORTER=otlp
  export OTEL_METRICS_EXPORTER=none
  export OTEL_EXPORTER_OTLP_PROTOCOL=http/protobuf
  export OTEL_EXPORTER_OTLP_ENDPOINT=http://%s
  export OTEL_LOGS_EXPORT_INTERVAL=5000

http/json is also accepted. gRPC is not. Use 127.0.0.1, not localhost.
Stop this command with Ctrl-C to print per-session totals.
`, addr)
}

// fileSyncer is the output file. *os.File satisfies it; tests use a buffer.
type fileSyncer interface {
	io.Writer
	Sync() error
}

type journal struct {
	mu   sync.Mutex
	file fileSyncer
	enc  *json.Encoder
	seen map[string]struct{}
	recs []Request
}

func newJournal(file fileSyncer, seen map[string]struct{}) *journal {
	if seen == nil {
		seen = map[string]struct{}{}
	}
	enc := json.NewEncoder(file)
	enc.SetEscapeHTML(false)
	return &journal{file: file, enc: enc, seen: seen}
}

func existingKeys(path string) map[string]struct{} {
	recs, _, err := ReadFile(path)
	seen := map[string]struct{}{}
	if err != nil {
		return seen
	}
	for _, rec := range recs {
		seen[rec.dedupeKey()] = struct{}{}
	}
	return seen
}

// add appends rec unless it repeats a request already written in this process.
func (j *journal) add(rec Request) (added bool, err error) {
	j.mu.Lock()
	defer j.mu.Unlock()
	rec = rec.clean()
	key := rec.dedupeKey()
	if _, ok := j.seen[key]; ok {
		return false, nil
	}
	if err := j.enc.Encode(rec); err != nil {
		return false, errors.New("otel: write output file")
	}
	j.seen[key] = struct{}{}
	j.recs = append(j.recs, rec)
	return true, nil
}

func (j *journal) records() []Request {
	j.mu.Lock()
	defer j.mu.Unlock()
	out := make([]Request, len(j.recs))
	copy(out, j.recs)
	return out
}

func (j *journal) sync() error {
	j.mu.Lock()
	defer j.mu.Unlock()
	if err := j.file.Sync(); err != nil {
		return errors.New("otel: write output file")
	}
	return nil
}

func newHandler(j *journal, stderr io.Writer) http.Handler {
	if stderr == nil {
		stderr = io.Discard
	}
	return &handler{journal: j, stderr: stderr}
}

type handler struct {
	journal *journal
	stderr  io.Writer
}

func (h *handler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	if len(r.URL.Path) > 64 || len(r.URL.RequestURI()) > 256 {
		http.Error(w, "not found", http.StatusNotFound)
		return
	}
	switch path.Clean(r.URL.Path) {
	case "/v1/logs":
		h.logs(w, r)
	case "/v1/metrics", "/v1/traces":
		h.discard(w, r)
	default:
		http.Error(w, "not found", http.StatusNotFound)
	}
}

func (h *handler) logs(w http.ResponseWriter, r *http.Request) {
	body, err := readBody(r)
	if err != nil {
		writeStatus(w, err)
		fmt.Fprintf(h.stderr, "rejected logs payload (%d bytes)\n", len(body))
		return
	}
	mediaType := mediaTypeOf(r.Header.Get("Content-Type"))
	recs, err := DecodeLogs(mediaType, body)
	if err != nil {
		writeStatus(w, err)
		fmt.Fprintf(h.stderr, "rejected logs payload (%d bytes)\n", len(body))
		return
	}
	added, dup := 0, 0
	for _, rec := range recs {
		ok, err := h.journal.add(rec)
		if err != nil {
			http.Error(w, "write failed", http.StatusInternalServerError)
			fmt.Fprintln(h.stderr, "rejected logs payload (write failed)")
			return
		}
		if ok {
			added++
		} else {
			dup++
		}
	}
	if added > 0 || dup > 0 {
		fmt.Fprintf(h.stderr, "recorded %d api_request event(s)", added)
		if dup > 0 {
			fmt.Fprintf(h.stderr, ", %d duplicate ignored", dup)
		}
		fmt.Fprintln(h.stderr)
	}
	writeOK(w, protobufMedia(mediaType))
}

func (h *handler) discard(w http.ResponseWriter, r *http.Request) {
	if _, err := readBody(r); err != nil {
		writeStatus(w, err)
		return
	}
	writeOK(w, protobufMedia(mediaTypeOf(r.Header.Get("Content-Type"))))
}

func mediaTypeOf(header string) string {
	mediaType, _, err := mime.ParseMediaType(header)
	if err != nil {
		return ""
	}
	return mediaType
}

func protobufMedia(mediaType string) bool {
	return mediaType == "application/x-protobuf" || mediaType == "application/protobuf"
}

func readBody(r *http.Request) ([]byte, error) {
	if r.ContentLength > maxBody {
		return nil, errTooLarge
	}
	compressed, err := io.ReadAll(io.LimitReader(r.Body, maxBody+1))
	if err != nil {
		return nil, errInvalidPayload
	}
	if len(compressed) > maxBody {
		return nil, errTooLarge
	}
	enc := strings.TrimSpace(r.Header.Get("Content-Encoding"))
	switch {
	case enc == "", strings.EqualFold(enc, "identity"):
		return compressed, nil
	case strings.EqualFold(enc, "gzip"):
		zr, err := gzip.NewReader(bytes.NewReader(compressed))
		if err != nil {
			return nil, errInvalidPayload
		}
		defer zr.Close()
		plain, err := io.ReadAll(io.LimitReader(zr, maxBody+1))
		if err != nil {
			return nil, errInvalidPayload
		}
		if len(plain) > maxBody {
			return nil, errTooLarge
		}
		return plain, nil
	default:
		return nil, errBadEncoding
	}
}

func writeStatus(w http.ResponseWriter, err error) {
	switch {
	case errors.Is(err, errTooLarge):
		http.Error(w, "body too large", http.StatusRequestEntityTooLarge)
	case errors.Is(err, ErrUnsupportedType), errors.Is(err, errBadEncoding):
		http.Error(w, "unsupported media type", http.StatusUnsupportedMediaType)
	default:
		http.Error(w, "invalid otlp logs payload", http.StatusBadRequest)
	}
}

func writeOK(w http.ResponseWriter, protobuf bool) {
	if protobuf {
		w.Header().Set("Content-Type", "application/x-protobuf")
		w.WriteHeader(http.StatusOK)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusOK)
	_, _ = w.Write([]byte("{}"))
}

// ReadFile reads a JSONL file written by Listen. Lines that are not a
// Request are counted and skipped. Duplicate request ids are collapsed.
// Line text is never returned.
func ReadFile(name string) ([]Request, int, error) {
	file, err := os.Open(name)
	if err != nil {
		return nil, 0, errors.New("otel: open jsonl file")
	}
	defer file.Close()
	return Read(file)
}

// Read parses JSONL from r. See ReadFile.
func Read(r io.Reader) ([]Request, int, error) {
	scanner := bufio.NewScanner(r)
	scanner.Buffer(make([]byte, 64<<10), 1<<20)
	var out []Request
	seen := map[string]struct{}{}
	skipped := 0
	for scanner.Scan() {
		line := scanner.Bytes()
		rec, ok := decodeRequestLine(line)
		if !ok {
			if strings.TrimSpace(string(line)) != "" {
				skipped++
			}
			continue
		}
		key := rec.dedupeKey()
		if _, dup := seen[key]; dup {
			continue
		}
		seen[key] = struct{}{}
		out = append(out, rec)
	}
	if err := scanner.Err(); err != nil {
		if errors.Is(err, bufio.ErrTooLong) {
			return out, skipped + 1, nil
		}
		return nil, skipped, errors.New("otel: read jsonl file")
	}
	return out, skipped, nil
}
