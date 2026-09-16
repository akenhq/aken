// SPDX-License-Identifier: Apache-2.0
package relayserver

import (
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net"
	"net/http"
	"strings"
	"time"

	"github.com/akenhq/aken/internal/relayserver/limits"
	"github.com/akenhq/aken/internal/relayserver/r2"
	"github.com/akenhq/aken/protocol"
	"github.com/akenhq/aken/relay"
)

type Config struct {
	Listen           string
	Store            string
	DataDir          string
	BehindCloudflare bool
	Limits           limits.Config
	R2               r2.Config
	Now              func() time.Time
}

func Run(ctx context.Context, cfg Config, stdout, stderr io.Writer) error {
	if cfg.Now == nil {
		cfg.Now = func() time.Time { return time.Now().UTC() }
	}
	var store relay.Store
	switch cfg.Store {
	case "memory":
		store = relay.NewMemoryStore()
	case "dir":
		var err error
		store, err = relay.NewDirStore(cfg.DataDir)
		if err != nil {
			return err
		}
	case "r2":
		var err error
		store, err = r2.New(ctx, cfg.R2)
		if err != nil {
			return err
		}
	default:
		return fmt.Errorf("unknown store %q", cfg.Store)
	}
	host, _, err := net.SplitHostPort(cfg.Listen)
	if err != nil {
		return err
	}
	if cfg.BehindCloudflare && host != "localhost" && !net.ParseIP(host).IsLoopback() {
		_, _ = fmt.Fprintf(stderr, "aken-relay: --behind-cloudflare trusts CF-Connecting-IP; make sure nothing but the tunnel can reach %s\n", cfg.Listen)
	}
	listener, err := net.Listen("tcp", cfg.Listen)
	if err != nil {
		return err
	}
	defer func() { _ = listener.Close() }()
	cfg.Limits.BehindCloudflare = cfg.BehindCloudflare
	logger := slog.New(slog.NewJSONHandler(stdout, nil))
	cfg.Limits.Logger = logger
	limiter := limits.New(cfg.Limits, cfg.Now)
	handler := relay.NewHandler(store, relay.Options{Now: cfg.Now})
	if err := sweep(ctx, store, handler, limiter, cfg.Now()); err != nil {
		return err
	}
	mux := http.NewServeMux()
	mux.HandleFunc("GET /healthz", func(w http.ResponseWriter, _ *http.Request) { _, _ = io.WriteString(w, "ok") })
	mux.Handle("/", limiter.Middleware(handler))
	server := &http.Server{
		Addr: cfg.Listen, Handler: logging(mux, logger, cfg.BehindCloudflare),
		ReadHeaderTimeout: 10 * time.Second, ReadTimeout: 5 * time.Minute, WriteTimeout: 5 * time.Minute, MaxHeaderBytes: 16 << 10,
	}
	sweepCtx, cancelSweep := context.WithCancel(ctx)
	sweepDone := make(chan struct{})
	go func() {
		defer close(sweepDone)
		ticker := time.NewTicker(10 * time.Minute)
		defer ticker.Stop()
		for {
			select {
			case <-sweepCtx.Done():
				return
			case <-ticker.C:
				if err := sweep(sweepCtx, store, handler, limiter, cfg.Now()); err != nil && sweepCtx.Err() == nil {
					logger.Error("sweep failed", "error", err)
				}
			}
		}
	}()
	defer func() { cancelSweep(); <-sweepDone }()
	stopped := make(chan error, 1)
	_, _ = fmt.Fprintf(stdout, "aken-relay listening on http://%s (store %s)\n", listener.Addr(), cfg.Store)
	go func() { stopped <- server.Serve(listener) }()
	select {
	case err := <-stopped:
		if errors.Is(err, http.ErrServerClosed) {
			return nil
		}
		return err
	case <-ctx.Done():
		shutdownCtx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		err := server.Shutdown(shutdownCtx)
		if err != nil {
			_ = server.Close()
		}
		<-stopped
		return err
	}
}

func sweep(ctx context.Context, store relay.Store, handler *relay.Handler, limiter *limits.Limiter, now time.Time) error {
	limiter.Prune(now)
	handler.Sweep()
	switch store := store.(type) {
	case *relay.MemoryStore:
		store.Sweep(now)
	case *relay.DirStore:
		_, live, err := store.Sweep(now)
		if err != nil {
			return err
		}
		limiter.SetLiveSessions(live)
	case *r2.Store:
		_, live, err := store.Sweep(ctx, now)
		if err != nil {
			return err
		}
		limiter.SetLiveSessions(live)
	}
	return nil
}

type responseWriter struct {
	http.ResponseWriter
	status, bytes int
}

func (w *responseWriter) WriteHeader(status int) {
	if w.status != 0 {
		return
	}
	w.status = status
	w.ResponseWriter.WriteHeader(status)
}

func (w *responseWriter) Write(body []byte) (int, error) {
	if w.status == 0 {
		w.WriteHeader(http.StatusOK)
	}
	n, err := w.ResponseWriter.Write(body)
	w.bytes += n
	return n, err
}

func logRoute(path string) (pattern, sid string) {
	if path == "/healthz" || path == "/v0/info" {
		return path, ""
	}
	parts := strings.Split(strings.TrimPrefix(path, "/"), "/")
	if len(parts) >= 3 && parts[0] == "v0" && parts[1] == "sessions" {
		switch {
		case len(parts) == 3:
			pattern = "/v0/sessions/{sid}"
		case len(parts) == 4 && (parts[3] == "join" || parts[3] == "jobs" || parts[3] == "results"):
			pattern = "/v0/sessions/{sid}/" + parts[3]
		case len(parts) == 5 && parts[3] == "blob" && parts[4] == "manifest":
			pattern = "/v0/sessions/{sid}/blob/manifest"
		case len(parts) == 6 && parts[3] == "blob" && parts[4] == "chunks":
			pattern = "/v0/sessions/{sid}/blob/chunks/{index}"
		}
		if pattern != "" {
			if id, ok := protocol.ParseSessionID(parts[2]); ok {
				sid = id.String()
			}
			return pattern, sid
		}
	}
	return "/", ""
}

func logging(next http.Handler, logger *slog.Logger, behindCloudflare bool) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		start := time.Now()
		recorded := &responseWriter{ResponseWriter: w}
		next.ServeHTTP(recorded, r)
		if recorded.status == 0 {
			recorded.status = http.StatusOK
		}
		pattern, sid := logRoute(r.URL.Path)
		logger.Info("request", "method", r.Method, "route", pattern, "session_id", sid,
			"status", recorded.status, "bytes", recorded.bytes, "duration", time.Since(start), "client_ip", limits.ClientIP(r, behindCloudflare))
	})
}
