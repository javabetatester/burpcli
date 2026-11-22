// Package app wires the core packages together and runs the proxy headless or with the TUI.
package app

import (
	"context"
	"crypto/tls"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net"
	"os"
	"path/filepath"
	"sync"
	"time"

	"github.com/javabetatester/burpcli/internal/ca"
	"github.com/javabetatester/burpcli/internal/flow"
	"github.com/javabetatester/burpcli/internal/har"
	"github.com/javabetatester/burpcli/internal/history"
	"github.com/javabetatester/burpcli/internal/pipeline"
	"github.com/javabetatester/burpcli/internal/proxy"
	"github.com/javabetatester/burpcli/internal/repeater"
	"github.com/javabetatester/burpcli/internal/tui"
)

// Version is reported in exported HAR files.
const Version = "0.2.0"

// RecordSchema is the version of the JSON Lines record emitted in headless mode.
const RecordSchema = 1

// Config is the full runtime configuration, filled from flags.
type Config struct {
	Listen           string
	Headless         bool
	MITM             bool
	CADir            string
	TrustHelp        bool
	ExportCA         string
	MaxBody          int64
	MaxCapture       int
	DBPath           string
	HistorySize      int
	ScopeInclude     []string
	ScopeExclude     []string
	HoldTimeout      time.Duration
	InsecureUpstream bool
	Verbose          bool
	LogFile          string
	Stdout           io.Writer
	Stderr           io.Writer
}

// Record is the stable headless output line (schema version RecordSchema).
type Record struct {
	Schema     int       `json:"schema"`
	ID         uint64    `json:"id"`
	Time       time.Time `json:"time"`
	Method     string    `json:"method"`
	URL        string    `json:"url"`
	Status     int       `json:"status"`
	Proto      string    `json:"proto,omitempty"`
	DurationMS float64   `json:"duration_ms"`
	Error      string    `json:"error,omitempty"`
	Dropped    bool      `json:"dropped,omitempty"`
	WebSocket  bool      `json:"websocket,omitempty"`
	Tunnel     bool      `json:"tunnel,omitempty"`
}

// ToRecord projects a flow onto the headless record schema.
func ToRecord(f *flow.Flow) Record {
	r := Record{Schema: RecordSchema, ID: f.ID, Time: f.Started, Method: f.Request.Method, URL: f.Request.URL,
		DurationMS: float64(f.Duration) / float64(time.Millisecond), Error: f.Error, Dropped: f.Dropped, WebSocket: f.WebSocket, Tunnel: f.Tunnel}
	if f.Response != nil {
		r.Status, r.Proto = f.Response.Status, f.Response.Proto
	}
	return r
}

// Run executes the configured action and returns when ctx ends or the user quits.
func Run(ctx context.Context, cfg Config) error {
	dir := cfg.CADir
	if dir == "" {
		var err error
		if dir, err = ca.DefaultDir(); err != nil {
			return err
		}
	}
	var authority *ca.Authority
	if cfg.MITM || cfg.TrustHelp || cfg.ExportCA != "" {
		a, created, err := ca.LoadOrCreate(dir)
		if err != nil {
			return err
		}
		authority = a
		if created || cfg.TrustHelp {
			fmt.Fprint(cfg.Stderr, ca.TrustInstructions(ca.CertPath(dir), a.Fingerprint()))
		}
		if cfg.ExportCA != "" {
			if err := os.WriteFile(cfg.ExportCA, a.CertPEM(), 0o644); err != nil {
				return fmt.Errorf("app: export CA: %w", err)
			}
		}
		if cfg.TrustHelp || cfg.ExportCA != "" {
			return nil
		}
	}
	return serve(ctx, cfg, authority)
}

func serve(ctx context.Context, cfg Config, authority *ca.Authority) error {
	ctx, stop := context.WithCancel(ctx)
	defer stop()
	log, closeLog, err := newLogger(cfg)
	if err != nil {
		return err
	}
	defer closeLog()
	var persist history.Persister
	if cfg.DBPath != "" {
		db, err := history.OpenSQLite(cfg.DBPath)
		if err != nil {
			return err
		}
		defer func() { _ = db.Close() }()
		persist = db
	}
	store, err := history.New(cfg.HistorySize, persist, log)
	if err != nil {
		return err
	}
	scope := history.Scope{Include: cfg.ScopeInclude, Exclude: cfg.ScopeExclude}
	queue := pipeline.NewQueue(cfg.HoldTimeout, scope.Match)
	upTLS := &tls.Config{MinVersion: tls.VersionTLS12, InsecureSkipVerify: cfg.InsecureUpstream}
	if cfg.InsecureUpstream {
		log.Warn("upstream TLS verification disabled")
	}
	transport := proxy.NewTransport(upTLS)
	srv, err := proxy.New(proxy.Config{Transport: transport, UpstreamTLS: upTLS, CA: authority, Chain: pipeline.Chain{queue},
		History: store, MaxBody: cfg.MaxBody, MaxCapture: cfg.MaxCapture, Log: log})
	if err != nil {
		return err
	}
	l, err := net.Listen("tcp", cfg.Listen)
	if err != nil {
		return fmt.Errorf("app: listen: %w", err)
	}
	if host, _, _ := net.SplitHostPort(l.Addr().String()); !net.ParseIP(host).IsLoopback() {
		log.Warn("proxy is reachable from the network; anyone who can connect can use it", "addr", l.Addr().String())
	}
	log.Info("proxy listening", "addr", l.Addr().String(), "mitm", authority != nil)
	errc := make(chan error, 1)
	go func() { errc <- srv.Serve(ctx, l) }()

	if cfg.Headless {
		var mu sync.Mutex
		enc := json.NewEncoder(cfg.Stdout)
		store.OnRecord(func(f *flow.Flow) {
			mu.Lock()
			defer mu.Unlock()
			if err := enc.Encode(ToRecord(f)); err != nil {
				log.Error("write record", "err", err)
			}
		})
		return <-errc
	}
	tuiErr := tui.Run(ctx, tui.Deps{History: store, Queue: queue, Repeater: repeater.New(transport), Scope: scope, Export: exporter()})
	stop()
	if err := <-errc; err != nil && tuiErr == nil {
		return err
	}
	if errors.Is(tuiErr, context.Canceled) {
		return nil
	}
	return tuiErr
}

func exporter() func([]*flow.Flow) (string, error) {
	return func(flows []*flow.Flow) (string, error) {
		path := filepath.Join(".", "burpui-"+time.Now().Format("20060102-150405")+".har")
		f, err := os.OpenFile(path, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o600)
		if err != nil {
			return "", fmt.Errorf("app: create HAR: %w", err)
		}
		if err := har.Write(f, Version, flows); err != nil {
			_ = f.Close()
			return "", err
		}
		return path, f.Close()
	}
}

func newLogger(cfg Config) (*slog.Logger, func(), error) {
	level := slog.LevelInfo
	if cfg.Verbose {
		level = slog.LevelDebug
	}
	w := io.Writer(cfg.Stderr)
	closeFn := func() {}
	switch {
	case cfg.LogFile != "":
		f, err := os.OpenFile(cfg.LogFile, os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o600)
		if err != nil {
			return nil, nil, fmt.Errorf("app: open log file: %w", err)
		}
		w, closeFn = f, func() { _ = f.Close() }
	case !cfg.Headless:
		w = io.Discard
	}
	return slog.New(slog.NewJSONHandler(w, &slog.HandlerOptions{Level: level})), closeFn, nil
}
