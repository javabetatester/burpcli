// Command burpui is an intercepting HTTP/HTTPS proxy with a terminal UI, for authorized testing only.
package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"os"
	"os/signal"
	"strings"
	"syscall"
	"time"

	"github.com/javabetatester/burpcli/internal/app"
)

const (
	exitOK    = 0
	exitError = 1
	exitUsage = 2
)

func main() { os.Exit(run(os.Args[1:])) }

func run(args []string) int {
	cfg := app.Config{Stdout: os.Stdout, Stderr: os.Stderr}
	var include, exclude string
	fs := flag.NewFlagSet("burpui", flag.ContinueOnError)
	fs.StringVar(&cfg.Listen, "listen", "127.0.0.1:8080", "proxy listen address")
	fs.BoolVar(&cfg.Headless, "headless", false, "no TUI: emit JSON Lines flow records on stdout and logs on stderr")
	fs.BoolVar(&cfg.MITM, "mitm", true, "intercept HTTPS with the local CA (false tunnels CONNECT untouched)")
	fs.StringVar(&cfg.CADir, "ca-dir", "", "CA directory (default: user config dir)")
	fs.BoolVar(&cfg.TrustHelp, "trust-help", false, "print how to trust the CA and exit")
	fs.StringVar(&cfg.ExportCA, "export-ca", "", "write the CA certificate (PEM) to this path and exit")
	fs.Int64Var(&cfg.MaxBody, "max-body", 32<<20, "max bytes buffered per request or response body")
	fs.IntVar(&cfg.MaxCapture, "max-capture", 4<<20, "max body bytes kept in history per message")
	fs.StringVar(&cfg.DBPath, "db", "", "SQLite file for persistent history (default: memory only)")
	fs.IntVar(&cfg.HistorySize, "history-size", 5000, "flows kept in memory")
	fs.StringVar(&include, "scope", "", "comma-separated host globs in scope (default: all)")
	fs.StringVar(&exclude, "exclude", "", "comma-separated host globs out of scope")
	fs.DurationVar(&cfg.HoldTimeout, "hold-timeout", 60*time.Second, "drop a held request after this long")
	fs.BoolVar(&cfg.InsecureUpstream, "insecure-upstream", false, "DANGEROUS: skip upstream TLS verification")
	fs.BoolVar(&cfg.Verbose, "verbose", false, "debug logging")
	fs.StringVar(&cfg.LogFile, "log-file", "", "append JSON logs to this file")
	if err := fs.Parse(args); err != nil {
		if errors.Is(err, flag.ErrHelp) {
			return exitOK
		}
		return exitUsage
	}
	cfg.ScopeInclude, cfg.ScopeExclude = split(include), split(exclude)

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	if err := app.Run(ctx, cfg); err != nil {
		fmt.Fprintln(os.Stderr, "burpui:", err)
		return exitError
	}
	return exitOK
}

func split(s string) []string {
	var out []string
	for _, p := range strings.Split(s, ",") {
		if p = strings.TrimSpace(p); p != "" {
			out = append(out, p)
		}
	}
	return out
}
