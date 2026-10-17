// tsproxy puts a local web app on the tailnet under a name of its own, with HTTPS on port 443.
//
//	tsproxy -name hub -target http://127.0.0.1:8787
//
// This machine already uses its own Tailscale name (and port 443) for another app, so the proxy joins the
// tailnet as a separate device (tsnet) called -name, serves https://<name>.<tailnet>.ts.net with a Let's
// Encrypt certificate, redirects plain http to it, and forwards to -target on loopback. It passes the
// caller's Tailscale login in Tailscale-User-Login, as tailscale serve does. The first run logs a login
// link; open it once to add the device.
package main

import (
	"context"
	"flag"
	"net"
	"net/http"
	"net/http/httputil"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/rs/zerolog"
	"tailscale.com/tsnet"
)

func main() {
	home, _ := os.UserHomeDir()
	name := flag.String("name", "hub", "device name on the tailnet: the site is https://<name>.<tailnet>.ts.net")
	target := flag.String("target", "http://127.0.0.1:8787", "local app to forward to")
	state := flag.String("state", "", "where the device keeps its Tailscale state (default ~/.local/state/llm-brain/tsproxy-<name>)")
	flag.Parse()
	log := zerolog.New(os.Stderr).With().Timestamp().Str("app", "tsproxy").Str("name", *name).Logger()

	if *state == "" {
		*state = filepath.Join(home, ".local", "state", "llm-brain", "tsproxy-"+*name)
	}
	if err := os.MkdirAll(*state, 0o700); err != nil {
		log.Fatal().Err(err).Msg("state dir")
	}
	to, err := url.Parse(*target)
	if err != nil || to.Host == "" {
		log.Fatal().Str("target", *target).Msg("bad -target")
	}

	srv := &tsnet.Server{
		Hostname: *name,
		Dir:      *state,
		Logf:     func(string, ...any) {}, // tailscaled's chatter
		UserLogf: func(f string, a ...any) { log.Info().Msgf(f, a...) }, // includes the login link
	}
	defer srv.Close()
	ctx := context.Background()
	st, err := srv.Up(ctx)
	if err != nil {
		log.Fatal().Err(err).Msg("joining the tailnet")
	}
	lc, err := srv.LocalClient()
	if err != nil {
		log.Fatal().Err(err).Msg("local client")
	}
	host := strings.TrimSuffix(st.Self.DNSName, ".")

	proxy := httputil.NewSingleHostReverseProxy(to)
	proxy.FlushInterval = -1 // stream server-sent events and long responses as they come
	base := proxy.Director
	proxy.Director = func(r *http.Request) {
		base(r)
		r.Host = to.Host
	}
	proxy.ErrorHandler = func(w http.ResponseWriter, r *http.Request, err error) {
		log.Warn().Err(err).Str("path", r.URL.Path).Msg("upstream")
		http.Error(w, "the app isn't answering", http.StatusBadGateway)
	}
	handler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		for k := range r.Header {
			if strings.HasPrefix(strings.ToLower(k), "tailscale-") {
				r.Header.Del(k) // never trust these from the caller
			}
		}
		if who, err := lc.WhoIs(r.Context(), r.RemoteAddr); err == nil && who.UserProfile != nil {
			r.Header.Set("Tailscale-User-Login", who.UserProfile.LoginName)
			r.Header.Set("Tailscale-User-Name", who.UserProfile.DisplayName)
		}
		r.Header.Set("X-Forwarded-Proto", "https")
		r.Header.Set("X-Forwarded-Host", host)
		proxy.ServeHTTP(w, r)
	})

	tlsLn, err := srv.ListenTLS("tcp", ":443")
	if err != nil {
		log.Fatal().Err(err).Msg("listen :443")
	}
	plain, err := srv.Listen("tcp", ":80")
	if err != nil {
		log.Fatal().Err(err).Msg("listen :80")
	}
	go func() {
		redirect := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			http.Redirect(w, r, "https://"+host+r.URL.RequestURI(), http.StatusMovedPermanently)
		})
		_ = (&http.Server{Handler: redirect, ReadHeaderTimeout: 10 * time.Second}).Serve(plain)
	}()
	log.Info().Str("url", "https://"+host).Str("target", *target).Msg("serving")
	hs := &http.Server{Handler: handler, ReadHeaderTimeout: 10 * time.Second,
		BaseContext: func(net.Listener) context.Context { return ctx }}
	log.Fatal().Err(hs.Serve(tlsLn)).Msg("stopped")
}
