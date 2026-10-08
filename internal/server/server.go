// Package server runs the dashboard: the v1 API and the browser UI on one
// HTTP listener backed by one store.
package server

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/DanBradbury/firekeeper/internal/server/api"
	"github.com/DanBradbury/firekeeper/internal/server/auth"
	"github.com/DanBradbury/firekeeper/internal/server/store"
	"github.com/DanBradbury/firekeeper/internal/server/web"
)

// DefaultListen is the address the dashboard listens on by default.
const DefaultListen = "127.0.0.1:7777"

// ShutdownGrace is how long in-flight requests get to finish once Run's
// context is cancelled.
const ShutdownGrace = 5 * time.Second

// ErrNotLoopback marks a listen address refused because it is reachable
// from other machines and Config.Insecure is not set.
var ErrNotLoopback = errors.New("refusing to listen on a non-loopback address without a token or --insecure")

// Config configures Run.
type Config struct {
	// Listen is the TCP address. Empty means DefaultListen.
	Listen string
	// DB is the dashboard database path. Its directory is created with
	// mode 0700 if missing. Empty means DefaultDBPath.
	DB string
	// Insecure allows a non-loopback Listen address when no token or account
	// exists. The API is then open: anyone who can reach it can read and
	// upload.
	Insecure bool
	// Signup says who may create an account. Empty means closed.
	Signup auth.SignupMode
	// Out receives the startup URL; Err receives warnings. Nil discards.
	Out, Err io.Writer
	// Prices is the per-model price table for usage cost, per million
	// tokens. Nil means usage shows tokens only.
	Prices map[string]store.Price
	// FileLink is the template linking a session's changed files to their
	// repository host, such as
	// "https://github.com/me/{project}/blob/{ref}/{path}". Empty means no
	// links. See api.ValidateFileLink.
	FileLink string
	// OnListen, if set, is called with the base URL once the listener is
	// bound and before requests are served.
	OnListen func(url string)
}

// DefaultDBPath returns ~/.firekeeper/dashboard.db.
func DefaultDBPath() (string, error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return "", fmt.Errorf("locate home directory: %w", err)
	}
	return filepath.Join(home, ".firekeeper", "dashboard.db"), nil
}

// Loopback reports whether the host part of a listen address only accepts
// local connections. An empty host listens on every interface.
func Loopback(addr string) bool {
	host, _, err := net.SplitHostPort(addr)
	if err != nil {
		return false
	}
	if strings.EqualFold(host, "localhost") {
		return true
	}
	ip := net.ParseIP(host)
	return ip != nil && ip.IsLoopback()
}

// Run serves the dashboard until ctx is cancelled, then stops accepting
// connections, gives in-flight requests ShutdownGrace to finish, and closes
// the store. It returns nil after a clean shutdown.
func Run(ctx context.Context, cfg Config) error {
	out, errOut := writerOr(cfg.Out), writerOr(cfg.Err)
	listen := cfg.Listen
	if listen == "" {
		listen = DefaultListen
	}
	if _, _, err := net.SplitHostPort(listen); err != nil {
		return fmt.Errorf("invalid listen address %q: %w", listen, err)
	}
	if cfg.FileLink != "" {
		if err := api.ValidateFileLink(cfg.FileLink); err != nil {
			return err
		}
	}
	dbPath := cfg.DB
	if dbPath == "" {
		var err error
		if dbPath, err = DefaultDBPath(); err != nil {
			return err
		}
	}
	loopback := Loopback(listen)
	// Without a database there cannot be a token; refuse before creating one.
	if !loopback && !cfg.Insecure {
		if _, err := os.Stat(dbPath); err != nil {
			return fmt.Errorf("%w: %s", ErrNotLoopback, listen)
		}
	}
	if err := os.MkdirAll(filepath.Dir(dbPath), 0o700); err != nil {
		return fmt.Errorf("create database directory: %w", err)
	}
	s, err := store.Open(ctx, dbPath)
	if err != nil {
		return fmt.Errorf("open database: %w", err)
	}
	defer s.Close()

	tokens, err := s.ListTokens(ctx, true)
	if err != nil {
		return fmt.Errorf("list tokens: %w", err)
	}
	accounts, err := s.HasAccounts(ctx)
	if err != nil {
		return fmt.Errorf("list accounts: %w", err)
	}
	signup := cfg.Signup
	if signup == "" {
		signup = auth.SignupClosed
	}
	if !loopback && len(tokens) == 0 && !accounts {
		if !cfg.Insecure {
			return fmt.Errorf("%w: %s", ErrNotLoopback, listen)
		}
		fmt.Fprintf(errOut, "warning: listening on %s with no authentication; anyone who can reach this address can read and upload transcripts\n", listen)
	}

	ln, err := net.Listen("tcp", listen)
	if err != nil {
		return fmt.Errorf("listen: %w", err)
	}

	// Streams never finish on their own, so they would hold Shutdown for
	// the whole grace period. End them as soon as shutdown begins.
	stopping, stopStreams := context.WithCancel(context.Background())
	defer stopStreams()
	// The middleware resolves every request to an account. It leaves the API
	// open to the default account only while no token and no account exists.
	am := auth.New(s, auth.WithSignup(signup))
	apiHandler := am.Wrap(api.Handler(s, api.WithPrices(cfg.Prices), api.WithFileLink(cfg.FileLink)))
	mux := http.NewServeMux()
	mux.Handle("POST /v1/auth/login", am.Login())
	mux.Handle("POST /v1/auth/signup", am.Signup())
	mux.Handle("POST /v1/link/start", am.LinkStart())
	mux.Handle("POST /v1/link/poll", am.LinkPoll())
	mux.Handle("POST /v1/link/approve", am.Wrap(am.LinkApprove()))
	mux.Handle("/v1/", apiHandler)
	mux.Handle("/v1/stream", endWith(stopping, apiHandler))
	mux.Handle("/", web.Handler(web.WithGate(am.Page)))

	// No WriteTimeout: it would cut /v1/stream off.
	srv := &http.Server{
		Handler:           mux,
		ReadHeaderTimeout: 10 * time.Second,
		IdleTimeout:       2 * time.Minute,
	}

	url := "http://" + ln.Addr().String() + "/"
	fmt.Fprintf(out, "firekeeper dashboard at %s\n", url)
	fmt.Fprintf(out, "mode: %s\n", am.Mode(ctx))
	if cfg.OnListen != nil {
		cfg.OnListen(url)
	}

	served := make(chan error, 1)
	go func() { served <- srv.Serve(ln) }()

	select {
	case err := <-served:
		return fmt.Errorf("serve: %w", err)
	case <-ctx.Done():
	}

	stopStreams()
	shutdownCtx, cancel := context.WithTimeout(context.Background(), ShutdownGrace)
	defer cancel()
	if err := srv.Shutdown(shutdownCtx); err != nil {
		// Grace period over: drop whatever is left.
		srv.Close()
	}
	<-served
	if err := s.Close(); err != nil {
		return fmt.Errorf("close database: %w", err)
	}
	return nil
}

// endWith cancels each request's context when stop is done.
func endWith(stop context.Context, h http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		ctx, cancel := context.WithCancel(r.Context())
		defer cancel()
		defer context.AfterFunc(stop, cancel)()
		h.ServeHTTP(w, r.WithContext(ctx))
	})
}

func writerOr(w io.Writer) io.Writer {
	if w == nil {
		return io.Discard
	}
	return w
}
