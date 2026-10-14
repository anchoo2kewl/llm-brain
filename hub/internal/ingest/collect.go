package ingest

import (
	"bytes"
	"context"
	"errors"
	"io"
	"io/fs"
	"log"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"time"

	"github.com/anchoo2kewl/llm-brain/hub/internal/parse"
)

// Collector finds this machine's source files and feeds new bytes to a Sink. It keeps no state of its own
// between cycles except which prompts it already sent: the Sink's offsets are the truth.
type Collector struct {
	Host      string
	Role      string // serve | ship
	ClaudeDir string // ~/.claude/projects
	CodexDir  string // ~/.codex/sessions
	StateDir  string // ~/.local/state/llm-brain
	TZ        string
	Sink      Sink
	ChunkMax  int // bytes per chunk; a single longer line grows the chunk up to MaxLine
	Log       *log.Logger

	promptSent map[string]time.Time
}

const MaxLine = 64 << 20

type source struct {
	path, kind string
}

// Discover lists the files this machine contributes.
func (c *Collector) Discover() []source {
	var out []source
	add := func(kind string, paths []string) {
		for _, p := range paths {
			if strings.Contains(p, string(filepath.Separator)+"memory"+string(filepath.Separator)) {
				continue
			}
			out = append(out, source{p, kind})
		}
	}
	if c.ClaudeDir != "" {
		top, _ := filepath.Glob(filepath.Join(c.ClaudeDir, "*", "*.jsonl"))
		sub, _ := filepath.Glob(filepath.Join(c.ClaudeDir, "*", "*", "subagents", "*.jsonl"))
		add(KindClaude, top)
		add(KindClaude, sub)
	}
	if c.CodexDir != "" {
		var cx []string
		_ = filepath.WalkDir(c.CodexDir, func(p string, d fs.DirEntry, err error) error {
			if err == nil && !d.IsDir() && strings.HasSuffix(p, ".jsonl") {
				cx = append(cx, p)
			}
			return nil
		})
		add(KindCodex, cx)
	}
	if c.StateDir != "" {
		logs, _ := filepath.Glob(filepath.Join(c.StateDir, "logs", "*.log"))
		for _, p := range logs {
			base := filepath.Base(p)
			if base == "fleet.log" {
				out = append(out, source{p, KindFleetLog})
			} else if _, ok := parse.ParseRunLogName(base, time.UTC, "", ""); ok {
				out = append(out, source{p, KindRunLog})
			}
		}
	}
	return out
}

// Cycle ships everything new once, then sends a heartbeat.
func (c *Collector) Cycle(ctx context.Context) error {
	if c.promptSent == nil {
		c.promptSent = map[string]time.Time{}
	}
	if c.ChunkMax <= 0 {
		c.ChunkMax = 4 << 20
	}
	offsets, err := c.Sink.Offsets(ctx, c.Host)
	if err != nil {
		return err
	}
	var firstErr error
	for _, s := range c.Discover() {
		if ctx.Err() != nil {
			return ctx.Err()
		}
		if err := c.syncFile(ctx, s, offsets[s.path]); err != nil {
			c.logf("sync %s: %v", s.path, err)
			if firstErr == nil {
				firstErr = err
			}
		}
	}
	hb := Heartbeat{Host: c.Host, Role: c.Role, TZ: c.TZ, Running: c.runningKeys()}
	prompts := c.prompts()
	hb.Prompts = prompts
	if err := c.Sink.Heartbeat(ctx, hb); err != nil {
		return err
	}
	for _, p := range prompts {
		c.promptSent[p.Key] = p.Mtime
	}
	return firstErr
}

func (c *Collector) syncFile(ctx context.Context, s source, off int64) error {
	st, err := os.Stat(s.path)
	if err != nil {
		return nil // vanished between glob and stat
	}
	size := st.Size()
	if size == off {
		return nil
	}
	f, err := os.Open(s.path)
	if err != nil {
		return err
	}
	defer f.Close()
	reset := false
	if size < off { // truncated or replaced: start over
		reset, off = true, 0
	}
	retries := 0
	for off < size {
		want := int64(c.ChunkMax)
		var data []byte
		for {
			n := min(want, size-off)
			data = make([]byte, n)
			if _, err := f.ReadAt(data, off); err != nil && !errors.Is(err, io.EOF) {
				return err
			}
			// A line longer than the chunk: grow until it fits.
			if off+n < size && bytes.IndexByte(data, '\n') < 0 && want < MaxLine {
				want *= 4
				continue
			}
			break
		}
		ch := Chunk{Host: c.Host, Kind: s.kind, Path: s.path, Offset: off, Size: size, Mtime: st.ModTime(), TZ: c.TZ,
			Reset: reset, EOF: off+int64(len(data)) >= size, Data: data}
		newOff, err := c.Sink.Chunk(ctx, ch)
		if errors.Is(err, ErrOffset) {
			retries++
			if retries > 3 || newOff > size {
				return err
			}
			off, reset = newOff, false
			continue
		}
		if err != nil {
			return err
		}
		reset = false
		if newOff <= off {
			return nil // only a partial line so far
		}
		off = newOff
	}
	return nil
}

// runningKeys lists fleet workers whose pid file points at a live process.
func (c *Collector) runningKeys() []string {
	if c.StateDir == "" {
		return nil
	}
	pids, _ := filepath.Glob(filepath.Join(c.StateDir, "runs", "*.pid"))
	var out []string
	for _, p := range pids {
		b, err := os.ReadFile(p)
		if err != nil {
			continue
		}
		pid, err := strconv.Atoi(strings.TrimSpace(string(b)))
		if err != nil || pid <= 0 {
			continue
		}
		if err := syscall.Kill(pid, 0); err == nil || errors.Is(err, syscall.EPERM) {
			out = append(out, strings.TrimSuffix(filepath.Base(p), ".pid"))
		}
	}
	return out
}

// prompts returns worker prompts that changed since they were last sent: runs/<key>.prompt for implement and
// babysit workers, reviews/<key>/prompt.md for PR reviews.
func (c *Collector) prompts() []Prompt {
	if c.StateDir == "" {
		return nil
	}
	found := map[string]string{}
	a, _ := filepath.Glob(filepath.Join(c.StateDir, "runs", "*.prompt"))
	for _, p := range a {
		found[strings.TrimSuffix(filepath.Base(p), ".prompt")] = p
	}
	b, _ := filepath.Glob(filepath.Join(c.StateDir, "reviews", "*", "prompt.md"))
	for _, p := range b {
		k := filepath.Base(filepath.Dir(p))
		if _, ok := found[k]; !ok {
			found[k] = p
		}
	}
	var out []Prompt
	for k, p := range found {
		st, err := os.Stat(p)
		if err != nil || c.promptSent[k].Equal(st.ModTime()) {
			continue
		}
		body, err := os.ReadFile(p)
		if err != nil {
			continue
		}
		out = append(out, Prompt{Key: k, Mtime: st.ModTime(), Text: parse.Truncate(string(body), 256<<10)})
	}
	return out
}

func (c *Collector) logf(f string, a ...any) {
	if c.Log != nil {
		c.Log.Printf(f, a...)
	}
}

// Run cycles every interval until ctx ends.
func (c *Collector) Run(ctx context.Context, every time.Duration) {
	for {
		start := time.Now()
		if err := c.Cycle(ctx); err != nil && ctx.Err() == nil {
			c.logf("cycle: %v", err)
		}
		if d := time.Since(start); d > 5*time.Second {
			c.logf("cycle took %s", d.Round(time.Millisecond))
		}
		select {
		case <-ctx.Done():
			return
		case <-time.After(every):
		}
	}
}

// LocalTZ is this machine's IANA zone name (macOS: /etc/localtime -> .../zoneinfo/America/Toronto).
func LocalTZ() string {
	if tz := os.Getenv("TZ"); tz != "" {
		return tz
	}
	if l, err := os.Readlink("/etc/localtime"); err == nil {
		if i := strings.Index(l, "zoneinfo/"); i >= 0 {
			return l[i+len("zoneinfo/"):]
		}
	}
	return "Local"
}

// ShortHostname matches `hostname -s`, which is what the fleet scripts call the machine.
func ShortHostname() string {
	h, _ := os.Hostname()
	if i := strings.IndexByte(h, '.'); i > 0 {
		h = h[:i]
	}
	return h
}
