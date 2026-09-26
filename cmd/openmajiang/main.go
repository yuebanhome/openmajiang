package main

import (
	"context"
	"encoding/base64"
	"errors"
	"flag"
	"fmt"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"strings"
	"syscall"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/yuebanhome/openmajiang/internal/auth"
	"github.com/yuebanhome/openmajiang/internal/platform"
	"github.com/yuebanhome/openmajiang/rules/registry"
	"github.com/yuebanhome/openmajiang/web"
)

var Version = "development"
var Commit = "unknown"

func main() {
	if e := run(); e != nil {
		slog.Error("service stopped", "error", e)
		os.Exit(1)
	}
}
func env(key, fallback string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return fallback
}
func key(name string) ([]byte, error) {
	b, e := base64.StdEncoding.DecodeString(os.Getenv(name))
	if e != nil || len(b) != 32 {
		return nil, fmt.Errorf("%s must be base64 encoded 32 bytes", name)
	}
	return b, nil
}
func run() error {
	command := "serve"
	if len(os.Args) > 1 {
		command = os.Args[1]
	}
	if command == "version" {
		fmt.Printf("openmajiang %s (%s)\n", Version, Commit)
		return nil
	}
	if command == "healthcheck" {
		addr := env("HTTP_ADDR", ":8080")
		if strings.HasPrefix(addr, ":") {
			addr = "127.0.0.1" + addr
		}
		c := http.Client{Timeout: 3 * time.Second}
		r, e := c.Get("http://" + addr + "/health/ready")
		if e != nil {
			return errors.New("service is not ready")
		}
		defer r.Body.Close()
		if r.StatusCode != 200 {
			return errors.New("service is not ready")
		}
		return nil
	}
	if command != "serve" && command != "migrate" && command != "admin" {
		return errors.New("usage: openmajiang serve|migrate|healthcheck|version|admin grant --email <email> --reason <reason>")
	}
	if os.Getenv("DATABASE_URL") == "" {
		return errors.New("DATABASE_URL is required")
	}
	ctx, cancel := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer cancel()
	pool, e := pgxpool.New(ctx, os.Getenv("DATABASE_URL"))
	if e != nil {
		return errors.New("invalid database configuration")
	}
	defer pool.Close()
	if e = pool.Ping(ctx); e != nil {
		return errors.New("database unavailable")
	}
	if command == "admin" {
		if len(os.Args) < 3 || os.Args[2] != "grant" {
			return errors.New("usage: openmajiang admin grant --email <email> --reason <reason>")
		}
		f := flag.NewFlagSet("admin grant", flag.ContinueOnError)
		email := f.String("email", "", "verified operator email")
		reason := f.String("reason", "", "audit reason")
		if e = f.Parse(os.Args[3:]); e != nil {
			return e
		}
		return platform.GrantOperator(ctx, pool, *email, *reason)
	}
	if command == "migrate" {
		if e = auth.Migrate(ctx, pool); e != nil {
			return e
		}
		return platform.Migrate(ctx, pool)
	}
	if e = platform.CheckSchema(ctx, pool); e != nil {
		return e
	}
	token, e := key("TOKEN_HASH_KEY")
	if e != nil {
		return e
	}
	mailKey, e := key("AUTH_MAIL_KEY")
	if e != nil {
		return e
	}
	if string(token) == string(mailKey) {
		return errors.New("TOKEN_HASH_KEY and AUTH_MAIL_KEY must be independent")
	}
	base := os.Getenv("PUBLIC_BASE_URL")
	if base == "" {
		return errors.New("PUBLIC_BASE_URL is required")
	}
	rules, e := registry.Default()
	if e != nil {
		return e
	}
	ruleMap := rules.RuleMap()
	if enabled := os.Getenv("ENABLED_RULESETS"); enabled != "" {
		wanted := map[string]bool{}
		for _, v := range strings.Split(enabled, ",") {
			wanted[strings.TrimSpace(v)] = true
		}
		for k, r := range ruleMap {
			if !wanted[k] && !wanted[r.Manifest().ID] {
				delete(ruleMap, k)
			}
		}
	}
	p, e := platform.New(pool, platform.Config{Rules: ruleMap, BaseURL: base, TokenHashKey: token})
	if e != nil {
		return e
	}
	a, e := auth.New(pool, auth.Config{BaseURL: base, CookieSecure: strings.HasPrefix(base, "https://"), TrustedProxyCIDRs: splitNonempty(os.Getenv("TRUSTED_PROXY_CIDRS")), MailEncryptionKey: mailKey, SMTP: auth.SMTPConfig{Address: os.Getenv("SMTP_ADDR"), Username: os.Getenv("SMTP_USER"), Password: os.Getenv("SMTP_PASSWORD"), From: os.Getenv("SMTP_FROM"), StartTLS: env("SMTP_STARTTLS", "true") != "false"}, OnRevoke: p.OnRevoke, BeforeDeleteTx: p.BeforeDeleteTx, OnDelete: p.OnDelete})
	if e != nil {
		return e
	}
	p.SetAuth(a)
	mux := http.NewServeMux()
	a.RegisterRoutes(mux)
	p.RegisterRoutes(mux)
	mux.HandleFunc("GET /health/live", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.Write([]byte(`{"status":"alive"}`))
	})
	mux.HandleFunc("GET /health/ready", func(w http.ResponseWriter, r *http.Request) {
		ctx, c := context.WithTimeout(r.Context(), time.Second)
		defer c()
		if pool.Ping(ctx) != nil || platform.CheckSchema(ctx, pool) != nil {
			http.Error(w, "not ready", 503)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		w.Write([]byte(`{"status":"ready"}`))
	})
	mux.HandleFunc("/v1/", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(404)
		w.Write([]byte(`{"error":{"code":"NOT_FOUND","message":"API route not found"}}`))
	})
	mux.Handle("/", web.Handler())
	handler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("X-Content-Type-Options", "nosniff")
		w.Header().Set("Referrer-Policy", "no-referrer")
		w.Header().Set("X-Frame-Options", "DENY")
		w.Header().Set("Content-Security-Policy", "default-src 'self'; script-src 'self'; style-src 'self' 'unsafe-inline'; connect-src 'self'; img-src 'self' data:; object-src 'none'; frame-ancestors 'none'; base-uri 'self'")
		p.Middleware(mux).ServeHTTP(w, r)
	})
	server := http.Server{Addr: env("HTTP_ADDR", ":8080"), Handler: handler, ReadHeaderTimeout: 5 * time.Second, IdleTimeout: 60 * time.Second, MaxHeaderBytes: 16 << 10}
	go p.Run(ctx)
	go a.StartMailWorker(ctx)
	errCh := make(chan error, 1)
	go func() {
		slog.Info("service listening", "address", server.Addr, "version", Version)
		errCh <- server.ListenAndServe()
	}()
	select {
	case <-ctx.Done():
		shutdown, c := context.WithTimeout(context.Background(), 10*time.Second)
		defer c()
		return server.Shutdown(shutdown)
	case e := <-errCh:
		if errors.Is(e, http.ErrServerClosed) {
			return nil
		}
		return e
	}
}

func splitNonempty(raw string) []string {
	var r []string
	for _, v := range strings.Split(raw, ",") {
		if t := strings.TrimSpace(v); t != "" {
			r = append(r, t)
		}
	}
	return r
}
