package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"log"
	"os"
	"path/filepath"
	"strconv"

	"github.com/anchoo2kewl/llm-brain/hub/internal/ingest"
	"github.com/anchoo2kewl/llm-brain/hub/internal/memory"
	"github.com/anchoo2kewl/llm-brain/hub/internal/store"
)

// memoryFlags are serve's shared-memory flags.
type memoryFlags struct{ url, env, project *string }

func addMemoryFlags(fs *flag.FlagSet) memoryFlags {
	home, _ := os.UserHomeDir()
	return memoryFlags{
		url:     fs.String("taskai", "https://taskai.cc", "TaskAI base URL that shared agent memories are copied to; '' keeps them on the hub"),
		env:     fs.String("taskai-env", filepath.Join(home, ".config/llm-brain/taskai.env"), "file holding TASKAI_API_KEY (secrets.env next to it is tried too)"),
		project: fs.String("memory-project", "Agent memory", "TaskAI project memories go to: a name (found or made) or a project id"),
	}
}

// service is the shared memory, synced to TaskAI when there's a key.
func (f memoryFlags) service(st *store.Store, lg *log.Logger) *memory.Service {
	svc := &memory.Service{S: st, Log: lg}
	if *f.url == "" {
		return svc
	}
	key := memory.KeyFromEnvFile(*f.env)
	if key == "" {
		key = memory.KeyFromEnvFile(filepath.Join(filepath.Dir(*f.env), "secrets.env"))
	}
	if key == "" {
		lg.Printf("memory: no TASKAI_API_KEY in %s; memories stay on the hub", *f.env)
		return svc
	}
	t := &memory.TaskAI{URL: *f.url, Key: key, Project: *f.project}
	if id, err := strconv.ParseInt(*f.project, 10, 64); err == nil && id > 0 {
		t.ProjectID = id
	}
	svc.TaskAI = t
	return svc
}

// memoryCmd: hub memory import [--to URL] [--dir ~/.claude/projects] [--host NAME]
func memoryCmd(ctx context.Context, lg *log.Logger, args []string) error {
	if len(args) == 0 || args[0] != "import" {
		return errors.New("usage: hub memory import [--to http://127.0.0.1:8787] [--dir ~/.claude/projects] [--host NAME]")
	}
	home, _ := os.UserHomeDir()
	fs := flag.NewFlagSet("memory import", flag.ExitOnError)
	to := fs.String("to", "http://127.0.0.1:8787", "hub base URL")
	dir := fs.String("dir", filepath.Join(home, ".claude/projects"), "Claude Code projects dir")
	host := fs.String("host", ingest.ShortHostname(), "this machine's name")
	verbose := fs.Bool("v", false, "list every file that was refused or failed")
	_ = fs.Parse(args[1:])
	st, err := memory.ImportClaude(ctx, *dir, *host, memory.HTTPPoster(*to))
	if err != nil {
		return err
	}
	fmt.Printf("%d files: %d new, %d updated, %d unchanged, %d refused, %d failed\n",
		st.Files, st.Created, st.Superseded, st.Duplicate, st.Refused, st.Failed)
	if *verbose || st.Failed > 0 {
		for _, e := range st.Errors {
			fmt.Println("  " + e)
		}
	} else if st.Refused > 0 {
		fmt.Println("  (-v lists the refused files)")
	}
	if st.Failed > 0 {
		return fmt.Errorf("%d of %d failed", st.Failed, st.Files)
	}
	return nil
}
