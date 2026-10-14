// hub — one place to see every agent the llm-brain fleet runs, and every Claude/Codex conversation, on every
// machine, with full history. Turso (Rust SQLite) storage, server-rendered UI, no login: it listens only on
// loopback and the machine's Tailscale address.
//
//	hub serve [flags]            ingest this machine's files and serve the UI (the Studio)
//	hub ship --to URL [flags]    send this machine's files to a hub (the laptop)
package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"log"
	"net"
	"net/http"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"time"

	"github.com/anchoo2kewl/llm-brain/hub/internal/gateway"
	"github.com/anchoo2kewl/llm-brain/hub/internal/ingest"
	"github.com/anchoo2kewl/llm-brain/hub/internal/search"
	"github.com/anchoo2kewl/llm-brain/hub/internal/store"
	"github.com/anchoo2kewl/llm-brain/hub/internal/web"
)

var version = "dev"

func main() {
	if len(os.Args) < 2 {
		usage()
	}
	lg := log.New(os.Stderr, "", log.LstdFlags)
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	var err error
	switch os.Args[1] {
	case "serve":
		err = serve(ctx, lg, os.Args[2:])
	case "ship":
		err = ship(ctx, lg, os.Args[2:])
	case "version":
		fmt.Println(version)
	default:
		usage()
	}
	if err != nil && !errors.Is(err, context.Canceled) {
		lg.Fatal(err)
	}
}

func usage() {
	fmt.Fprintln(os.Stderr, "usage: hub serve [flags] | hub ship --to http://<hub>:8787 [flags] | hub version")
	os.Exit(2)
}

type common struct {
	state, claude, codex, host string
	interval                   time.Duration
}

func commonFlags(fs *flag.FlagSet) *common {
	home, _ := os.UserHomeDir()
	c := &common{}
	fs.StringVar(&c.state, "state", filepath.Join(home, ".local/state/llm-brain"), "llm-brain state dir (logs/, runs/, status.json)")
	fs.StringVar(&c.claude, "claude", filepath.Join(home, ".claude/projects"), "Claude Code transcripts dir ('' to skip)")
	fs.StringVar(&c.codex, "codex", filepath.Join(home, ".codex/sessions"), "Codex sessions dir ('' to skip)")
	fs.StringVar(&c.host, "host", ingest.ShortHostname(), "this machine's name, as `hostname -s`")
	fs.DurationVar(&c.interval, "interval", 10*time.Second, "re-scan interval")
	return c
}

func (c *common) collector(role string, sink ingest.Sink, lg *log.Logger) *ingest.Collector {
	return &ingest.Collector{Host: c.host, Role: role, ClaudeDir: c.claude, CodexDir: c.codex, StateDir: c.state,
		TZ: ingest.LocalTZ(), Sink: sink, Log: lg}
}

func serve(ctx context.Context, lg *log.Logger, args []string) error {
	fs := flag.NewFlagSet("serve", flag.ExitOnError)
	c := commonFlags(fs)
	port := fs.Int("port", 8787, "listen port")
	dbPath := fs.String("db", "", "database file (default <state>/hub.db)")
	indexPath := fs.String("index", "", "full-text index directory (default <state>/hub.bleve; 'off' to search the database instead)")
	listen := fs.String("listen", "", "comma-separated addresses to bind (default: 127.0.0.1 + this machine's Tailscale IPv4)")
	org := fs.String("org", "elastio", "GitHub org of pr-/review- worker keys")
	issueRepo := fs.String("issue-repo", "elastio/blue-stack", "owner/repo of issue- worker keys")
	noScan := fs.Bool("no-scan", false, "serve only; don't ingest this machine's files")
	gwURL := fs.String("gateway", "http://127.0.0.1:8317", "account gateway (CLIProxyAPI) base URL; '' disables")
	gwEnv := fs.String("gateway-env", filepath.Join(os.Getenv("HOME"), ".config/llm-brain/cpa.env"), "file holding CPA_MGMT_KEY")
	_ = fs.Parse(args)
	if *dbPath == "" {
		*dbPath = filepath.Join(c.state, "hub.db")
	}
	if err := os.MkdirAll(filepath.Dir(*dbPath), 0o755); err != nil {
		return err
	}
	st, err := store.Open(*dbPath)
	if err != nil {
		return fmt.Errorf("open %s: %w", *dbPath, err)
	}
	defer st.Close()
	eng := ingest.NewEngine(st, *org, *issueRepo)
	srv, err := web.New(st, eng, c.state, c.host, lg)
	if err != nil {
		return err
	}
	if *indexPath == "" {
		*indexPath = filepath.Join(c.state, "hub.bleve")
	}
	if *indexPath != "off" {
		idx, err := search.Open(*indexPath, st, lg)
		if err != nil {
			return fmt.Errorf("search index: %w", err)
		}
		defer idx.Close()
		srv.Index = idx
		eng.OnWrite = func(ctx context.Context) {
			if err := idx.Sync(ctx); err != nil && ctx.Err() == nil {
				lg.Printf("search index: %v", err)
			}
		}
		go idx.Run(ctx, 30*time.Second)
	}
	h := srv.Handler()

	var wg sync.WaitGroup
	var mu sync.Mutex
	var servers []*http.Server
	bind := func(addr string) error {
		ln, err := net.Listen("tcp", net.JoinHostPort(addr, strconv.Itoa(*port)))
		if err != nil {
			return err
		}
		hs := &http.Server{Handler: h, ReadHeaderTimeout: 10 * time.Second}
		mu.Lock()
		servers = append(servers, hs)
		mu.Unlock()
		lg.Printf("hub %s serving on http://%s", version, ln.Addr())
		wg.Add(1)
		go func() {
			defer wg.Done()
			if err := hs.Serve(ln); err != nil && !errors.Is(err, http.ErrServerClosed) {
				lg.Printf("serve %s: %v", addr, err)
			}
		}()
		return nil
	}
	if *listen != "" {
		for _, a := range strings.Split(*listen, ",") {
			if err := bind(strings.TrimSpace(a)); err != nil {
				return err
			}
		}
	} else {
		if err := bind("127.0.0.1"); err != nil {
			return err
		}
		// Tailscale may come up after us (login at boot): keep looking until it does.
		go func() {
			for {
				if ip := tailscaleIP(); ip != "" {
					if err := bind(ip); err != nil {
						lg.Printf("bind tailscale %s: %v", ip, err)
					} else {
						return
					}
				} else {
					lg.Printf("no Tailscale IP yet; serving on localhost only, retrying in 30s")
				}
				select {
				case <-ctx.Done():
					return
				case <-time.After(30 * time.Second):
				}
			}
		}()
	}
	if !*noScan {
		col := c.collector("serve", eng, lg)
		go col.Run(ctx, c.interval)
	}
	if *gwURL != "" {
		if key := gateway.KeyFromEnvFile(*gwEnv); key != "" {
			gp := &gateway.Poller{Store: st, URL: *gwURL, Key: key, Log: lg}
			go gp.Run(ctx, 20*time.Second)
		} else {
			lg.Printf("gateway: no CPA_MGMT_KEY in %s; account quota/routing disabled", *gwEnv)
		}
	}
	<-ctx.Done()
	sctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	mu.Lock()
	for _, s := range servers {
		_ = s.Shutdown(sctx)
	}
	mu.Unlock()
	wg.Wait()
	return nil
}

func ship(ctx context.Context, lg *log.Logger, args []string) error {
	fs := flag.NewFlagSet("ship", flag.ExitOnError)
	c := commonFlags(fs)
	to := fs.String("to", "", "hub base URL, e.g. http://100.74.108.114:8787")
	once := fs.Bool("once", false, "ship once and exit")
	_ = fs.Parse(args)
	if *to == "" {
		return errors.New("ship needs --to")
	}
	col := c.collector("ship", ingest.NewHTTPSink(strings.TrimRight(*to, "/")), lg)
	lg.Printf("hub %s shipping %s to %s every %s", version, c.host, *to, c.interval)
	if *once {
		return col.Cycle(ctx)
	}
	col.Run(ctx, c.interval)
	return nil
}

// tailscaleIP is this machine's Tailscale IPv4, or "" when Tailscale isn't installed or isn't up.
func tailscaleIP() string {
	for _, exe := range []string{"/usr/local/bin/tailscale", "/opt/homebrew/bin/tailscale", "/Applications/Tailscale.app/Contents/MacOS/Tailscale"} {
		if _, err := os.Stat(exe); err != nil {
			continue
		}
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		out, err := exec.CommandContext(ctx, exe, "ip", "-4").Output()
		cancel()
		if err != nil {
			continue
		}
		if f := strings.Fields(string(out)); len(f) > 0 && net.ParseIP(f[0]) != nil {
			return f[0]
		}
	}
	return ""
}
