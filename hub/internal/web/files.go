package web

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"mime"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/anchoo2kewl/llm-brain/hub/internal/agent"
	"github.com/anchoo2kewl/llm-brain/hub/internal/store"
)

// Files on a host (agent/files.go): browse a folder, download a file, drop a file in. The browser asks the
// hub; the hub queues a command with a one-time token; the host agent calls /api/agent/file/{token}, and
// the hub hands that request to the waiting browser request (or serves it the upload).

func (s *Server) fileRoutes(g, p func(string, http.HandlerFunc), mux *http.ServeMux) {
	// sameSite on these GETs too: another site's link must not make a Mac send a file (and the hub keep it).
	p("GET /hosts/{host}/ls", s.hostList)
	p("GET /hosts/{host}/download", s.hostDownload)
	p("POST /hosts/{host}/upload", s.hostUpload)
	p("POST /hosts/{host}/keep", s.hostKeep)
	p("POST /llms/upload", s.chatUpload)
	g("GET /files", s.assetsPage)
	g("GET /browse", s.browsePage)
	g("GET /files/{id}", s.assetGet)
	mux.HandleFunc("POST /api/agent/file/{token}", s.tailnetOnly(s.agentFilePost))
	mux.HandleFunc("GET /api/agent/file/{token}", s.tailnetOnly(s.agentFileGet))
}

// MaxUpload caps a dropped file.
const MaxUpload = 200 << 20

type handoff struct {
	r    *http.Request
	w    http.ResponseWriter
	done chan struct{} // closed by the browser side when it has read the body
}

// body is the agent's upload, read with an idle deadline: a transfer that stalls for a minute, or whose
// browser went away, ends instead of holding both requests open.
func (h handoff) body(ctx context.Context) io.Reader {
	rc := http.NewResponseController(h.w)
	stop := context.AfterFunc(ctx, func() { _ = rc.SetReadDeadline(time.Now()) })
	return &idleReader{r: h.r.Body, rc: rc, stop: stop}
}

type idleReader struct {
	r    io.Reader
	rc   *http.ResponseController
	stop func() bool
}

func (i *idleReader) Read(p []byte) (int, error) {
	_ = i.rc.SetReadDeadline(time.Now().Add(time.Minute))
	n, err := i.r.Read(p)
	if err != nil {
		i.stop()
	}
	return n, err
}

type xfer struct {
	in   chan handoff  // list/get: the agent's POST
	file string        // put: the upload, served to the agent once
	gone chan struct{} // closed when the browser side stops waiting
}

type xfers struct {
	mu sync.Mutex
	m  map[string]*xfer
}

func (x *xfers) add(tok string, file string) *xfer {
	x.mu.Lock()
	defer x.mu.Unlock()
	if x.m == nil {
		x.m = map[string]*xfer{}
	}
	t := &xfer{in: make(chan handoff), file: file, gone: make(chan struct{})}
	x.m[tok] = t
	return t
}

func (x *xfers) take(tok string) *xfer {
	x.mu.Lock()
	defer x.mu.Unlock()
	t := x.m[tok]
	delete(x.m, tok)
	return t
}

// start registers a transfer and queues its command; finish must be deferred.
func (s *Server) startXfer(ctx context.Context, host, kind string, req agent.FileReq, file string) (*xfer, int64, func(), error) {
	tok := token()
	t := s.Agents.xfers.add(tok, file)
	finish := func() {
		s.Agents.xfers.take(tok)
		close(t.gone)
	}
	req.Token = tok
	cid, err := s.Agents.Ctl.QueueFile(ctx, host, kind, req)
	if err != nil {
		finish()
		return nil, 0, nil, err
	}
	return t, cid, finish, nil
}

// await waits for the agent's request (list/get) or for the command to finish (put).
func (s *Server) await(ctx context.Context, t *xfer, cid int64, limit time.Duration) (handoff, error) {
	tick := time.NewTicker(400 * time.Millisecond)
	defer tick.Stop()
	deadline := time.After(limit)
	for {
		select {
		case h := <-t.in:
			return h, nil
		case <-ctx.Done():
			return handoff{}, ctx.Err()
		case <-deadline:
			return handoff{}, errors.New("the Mac's agent didn't answer in time")
		case <-tick.C:
			c, err := s.Store.Command(ctx, cid)
			if err != nil {
				continue
			}
			switch c.Status {
			case "failed", "cancelled":
				if e := reportError(c.Result); e != "" {
					return handoff{}, errors.New(e)
				}
				return handoff{}, errors.New("the Mac's agent couldn't do it")
			case "done":
				if t.file != "" { // put: finished
					return handoff{}, nil
				}
			}
		}
	}
}

func jsonErr(w http.ResponseWriter, code int, err error) {
	writeJSON(w, code, map[string]string{"error": err.Error()})
}

func (s *Server) hostList(w http.ResponseWriter, r *http.Request) {
	t, cid, finish, err := s.startXfer(r.Context(), r.PathValue("host"), agent.KindFileList, agent.FileReq{Path: r.URL.Query().Get("path")}, "")
	if err != nil {
		jsonErr(w, http.StatusBadRequest, err)
		return
	}
	defer finish()
	h, err := s.await(r.Context(), t, cid, 30*time.Second)
	if err != nil {
		jsonErr(w, http.StatusBadGateway, err)
		return
	}
	defer close(h.done)
	var l agent.Listing
	if err := json.NewDecoder(io.LimitReader(h.body(r.Context()), 8<<20)).Decode(&l); err != nil {
		jsonErr(w, http.StatusBadGateway, err)
		return
	}
	writeJSON(w, http.StatusOK, l)
}

func (s *Server) hostDownload(w http.ResponseWriter, r *http.Request) {
	t, cid, finish, err := s.startXfer(r.Context(), r.PathValue("host"), agent.KindFileGet, agent.FileReq{Path: r.URL.Query().Get("path")}, "")
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	defer finish()
	h, err := s.await(r.Context(), t, cid, 60*time.Second)
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadGateway)
		return
	}
	defer close(h.done)
	name := agent.SafeName(h.r.Header.Get("X-File-Name"))
	disp := "attachment"
	if r.URL.Query().Get("view") == "1" {
		disp = "inline"
	}
	w.Header().Set("Content-Disposition", mime.FormatMediaType(disp, map[string]string{"filename": name}))
	ctype := mime.TypeByExtension(filepath.Ext(name))
	if ctype == "" || disp == "inline" && !inlineSafe(ctype) {
		ctype = "application/octet-stream"
	}
	w.Header().Set("Content-Type", ctype)
	w.Header().Set("Content-Security-Policy", "default-src 'none'; img-src 'self'; style-src 'unsafe-inline'; sandbox")
	if h.r.ContentLength > 0 {
		w.Header().Set("Content-Length", strconv.FormatInt(h.r.ContentLength, 10))
	}
	// The hub keeps a copy of everything it hands out, so it stays downloadable from Files.
	if _, err := s.keepAsset(r.Context(), r.PathValue("host"), h, w, "download"); err != nil {
		s.Log.Printf("files: keep %s: %v", name, err)
	}
}

// keepAsset copies the agent's file to dst (nil: nowhere) and into the hub's assets, and records it.
func (s *Server) keepAsset(ctx context.Context, host string, h handoff, dst io.Writer, source string) (int64, error) {
	name := agent.SafeName(h.r.Header.Get("X-File-Name"))
	path := h.r.Header.Get("X-File-Path")
	if path == "" {
		path = name
	}
	return s.storeAsset(ctx, host, path, name, h.body(ctx), dst, source)
}

func (s *Server) assetsDir() string { return filepath.Join(s.StateDir, "assets") }

// storeAsset streams src to dst (if any) and into <state>/assets/<sha256>, then records the row.
func (s *Server) storeAsset(ctx context.Context, host, path, name string, src io.Reader, dst io.Writer, source string) (int64, error) {
	dir := s.assetsDir()
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return 0, err
	}
	tmp, err := os.CreateTemp(dir, ".in-*")
	if err != nil {
		return 0, err
	}
	defer os.Remove(tmp.Name())
	h := sha256.New()
	ws := []io.Writer{tmp, h}
	if dst != nil {
		ws = append(ws, dst)
	}
	n, err := io.Copy(io.MultiWriter(ws...), src)
	if cerr := tmp.Close(); err == nil {
		err = cerr
	}
	if err != nil {
		return 0, err
	}
	sum := hex.EncodeToString(h.Sum(nil))
	if err := os.Rename(tmp.Name(), filepath.Join(dir, sum)); err != nil {
		return 0, err
	}
	return s.Store.AddAsset(ctx, store.Asset{Host: host, Path: path, Name: name, Size: n, SHA256: sum, Source: source})
}

// keepFromHost copies a file from the host into the hub's assets.
func (s *Server) keepFromHost(ctx context.Context, host, path, source string) (int64, error) {
	t, cid, finish, err := s.startXfer(ctx, host, agent.KindFileGet, agent.FileReq{Path: path}, "")
	if err != nil {
		return 0, err
	}
	defer finish()
	h, err := s.await(ctx, t, cid, 60*time.Second)
	if err != nil {
		return 0, err
	}
	defer close(h.done)
	return s.keepAsset(ctx, host, h, nil, source)
}

// hostKeep copies a file from the host into the hub's assets without downloading it.
func (s *Server) hostKeep(w http.ResponseWriter, r *http.Request) {
	id, err := s.keepFromHost(r.Context(), r.PathValue("host"), r.FormValue("path"), "keep")
	if back := r.FormValue("back"); localPath(back) { // a plain form on a page
		s.back(w, r, back, err)
		return
	}
	if err != nil {
		jsonErr(w, http.StatusBadGateway, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"id": id, "url": fmt.Sprintf("/files/%d", id)})
}

// keyHarvest is the settings key holding the last message id the harvester looked at.
const keyHarvest = "assets_harvest_after"

// RunHarvest keeps a copy of every document a session writes (a report, a CSV, a screenshot; not source
// code, which lives in git), so it can still be downloaded after the Mac cleans up. A host that is offline
// holds the queue until it is back.
func (s *Server) RunHarvest(ctx context.Context, every time.Duration) {
	for ctx.Err() == nil {
		s.harvestOnce(ctx)
		select {
		case <-ctx.Done():
		case <-time.After(every):
		}
	}
}

func (s *Server) harvestOnce(ctx context.Context) {
	if s.Agents == nil {
		return
	}
	// 1. New documents written since the cursor go on the queue; the cursor always moves on.
	for {
		v, _ := s.Store.Setting(ctx, keyHarvest)
		after, _ := strconv.ParseInt(v, 10, 64)
		docs, last, err := s.Store.WrittenDocs(ctx, after, 500)
		if err != nil {
			s.Log.Printf("files: harvest: %v", err)
			return
		}
		if err := s.Store.QueueAssets(ctx, docs); err != nil {
			s.Log.Printf("files: harvest queue: %v", err)
			return
		}
		if last <= after {
			break
		}
		_ = s.Store.SetSettings(ctx, map[string]string{keyHarvest: strconv.FormatInt(last, 10)})
	}
	// 2. Copy what's due, per item: an offline Mac or a slow copy only delays its own files.
	hosts, err := s.Store.AgentHosts(ctx)
	if err != nil {
		return
	}
	var live []string
	for _, h := range hosts {
		if h.Live() {
			live = append(live, h.Host)
		}
	}
	due, err := s.Store.DueAssets(ctx, live, 50)
	if err != nil {
		s.Log.Printf("files: harvest: %v", err)
		return
	}
	for _, d := range due {
		if ctx.Err() != nil {
			return
		}
		if !s.Agents.Ctl.HostLive(ctx, d[0]) { // went offline mid-batch: wait, don't count it
			continue
		}
		_, err := s.keepFromHost(ctx, d[0], d[1], "auto")
		switch {
		case err == nil || permanent(err):
			_ = s.Store.DoneAsset(ctx, d[0], d[1])
		default:
			_ = s.Store.RetryAsset(ctx, d[0], d[1], s.Store.AssetTries(ctx, d[0], d[1]))
		}
	}
}

// permanent: a copy that will never work (the file is gone, outside home, a credential, a folder).
func permanent(err error) bool {
	m := err.Error()
	for _, x := range []string{"no such file", "not in your home folder", "won't serve", "that's a folder", "not a regular file", "file does not exist"} {
		if strings.Contains(m, x) {
			return true
		}
	}
	return false
}

func (s *Server) assetsPage(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query().Get("q")
	as, err := s.Store.Assets(r.Context(), q, 500)
	if err != nil {
		s.fail(w, err)
		return
	}
	hosts, _ := s.Store.AgentHosts(r.Context())
	s.render(w, "files", "layout", struct {
		page
		Assets []store.Asset
		Hosts  []store.AgentHost
	}{page{Title: "Files", Nav: "files", Q: q}, as, hosts})
}

// browsePage: a Mac's home folder, listed by files.js.
func (s *Server) browsePage(w http.ResponseWriter, r *http.Request) {
	hosts, err := s.Store.AgentHosts(r.Context())
	if err != nil {
		s.fail(w, err)
		return
	}
	type hostOpt struct {
		store.AgentHost
		Live bool
	}
	sel := r.URL.Query().Get("host")
	var hs []hostOpt
	for _, h := range hosts {
		hs = append(hs, hostOpt{h, h.Live()})
		if sel == "" && h.Live() && h.Host == s.Host {
			sel = h.Host
		}
	}
	path := r.URL.Query().Get("path")
	if path == "" {
		path = "~"
	}
	s.render(w, "browse", "layout", struct {
		page
		Hosts   []hostOpt
		HostSel string
		Path    string
	}{page{Title: "Browse", Nav: "files"}, hs, sel, path})
}

// assetGet serves the hub's copy: inline for images, PDFs and text with ?view=1, else as a download.
func (s *Server) assetGet(w http.ResponseWriter, r *http.Request) {
	id, ok := pathID(r)
	if !ok {
		http.NotFound(w, r)
		return
	}
	a, err := s.Store.Asset(r.Context(), id)
	if err != nil {
		http.NotFound(w, r)
		return
	}
	f, err := os.Open(filepath.Join(s.assetsDir(), a.SHA256))
	if err != nil {
		http.Error(w, "the hub's copy is missing", http.StatusGone)
		return
	}
	defer f.Close()
	ctype := mime.TypeByExtension(filepath.Ext(a.Name))
	disp := "attachment"
	if r.URL.Query().Get("view") == "1" && inlineSafe(ctype) {
		disp = "inline"
	}
	if disp == "attachment" || ctype == "" {
		ctype = "application/octet-stream"
	}
	w.Header().Set("Content-Type", ctype)
	w.Header().Set("Content-Disposition", mime.FormatMediaType(disp, map[string]string{"filename": a.Name}))
	w.Header().Set("Content-Security-Policy", "default-src 'none'; img-src 'self'; style-src 'unsafe-inline'; sandbox")
	w.Header().Set("Content-Length", strconv.FormatInt(a.Size, 10))
	_, _ = io.Copy(w, f)
}

// inlineSafe: what a browser may show in place (no HTML or SVG: they could run script on the hub's origin).
func inlineSafe(ctype string) bool {
	mt, _, _ := mime.ParseMediaType(ctype)
	switch mt {
	case "image/png", "image/jpeg", "image/gif", "image/webp", "application/pdf", "text/plain", "text/markdown", "text/csv", "application/json":
		return true
	}
	return false
}

// hostUpload saves a dropped file on the host under <state>/uploads/<into>/<time>/ and returns its path.
func (s *Server) hostUpload(w http.ResponseWriter, r *http.Request) {
	into := r.URL.Query().Get("into")
	if !agent.ValidSlug(into) {
		jsonErr(w, http.StatusBadRequest, errors.New("bad upload folder"))
		return
	}
	r.Body = http.MaxBytesReader(w, r.Body, MaxUpload+1<<20)
	f, hdr, err := r.FormFile("file")
	if err != nil {
		jsonErr(w, http.StatusBadRequest, fmt.Errorf("no file (max %d MB): %w", MaxUpload>>20, err))
		return
	}
	defer f.Close()
	dir := filepath.Join(s.StateDir, "upload-tmp")
	_ = os.MkdirAll(dir, 0o700)
	tmp, err := os.CreateTemp(dir, "up-*")
	if err != nil {
		jsonErr(w, http.StatusInternalServerError, err)
		return
	}
	defer os.Remove(tmp.Name())
	if _, err := io.Copy(tmp, f); err != nil {
		tmp.Close()
		jsonErr(w, http.StatusBadRequest, err)
		return
	}
	tmp.Close()
	t, cid, finish, err := s.startXfer(r.Context(), r.PathValue("host"), agent.KindFilePut, agent.FileReq{Into: into, Name: hdr.Filename}, tmp.Name())
	if err != nil {
		jsonErr(w, http.StatusBadRequest, err)
		return
	}
	defer finish()
	if _, err := s.await(r.Context(), t, cid, 5*time.Minute); err != nil {
		jsonErr(w, http.StatusBadGateway, err)
		return
	}
	c, err := s.Store.Command(r.Context(), cid)
	if err != nil {
		jsonErr(w, http.StatusInternalServerError, err)
		return
	}
	var rep agent.Report
	_ = json.Unmarshal([]byte(c.Result), &rep)
	if in, err := os.Open(tmp.Name()); err == nil { // the hub keeps uploads too
		if _, err := s.storeAsset(r.Context(), r.PathValue("host"), rep.Result, agent.SafeName(hdr.Filename), in, nil, "upload"); err != nil {
			s.Log.Printf("files: keep upload: %v", err)
		}
		in.Close()
	}
	writeJSON(w, http.StatusOK, map[string]string{"path": rep.Result, "name": agent.SafeName(hdr.Filename)})
}

// agentFilePost: the agent's listing or file, handed to the waiting browser request.
func (s *Server) agentFilePost(w http.ResponseWriter, r *http.Request) {
	t := s.Agents.xfers.take(r.PathValue("token"))
	if t == nil || t.file != "" {
		http.NotFound(w, r)
		return
	}
	h := handoff{r: r, w: w, done: make(chan struct{})}
	select {
	case t.in <- h:
	case <-t.gone:
		http.Error(w, "the browser stopped waiting", http.StatusGone)
		return
	case <-time.After(10 * time.Second):
		http.Error(w, "nobody waiting", http.StatusGone)
		return
	}
	select {
	case <-h.done:
	case <-r.Context().Done():
	}
}

// agentFileGet: the upload, served to the agent once.
func (s *Server) agentFileGet(w http.ResponseWriter, r *http.Request) {
	s.Agents.xfers.mu.Lock()
	t := s.Agents.xfers.m[r.PathValue("token")]
	s.Agents.xfers.mu.Unlock()
	if t == nil || t.file == "" {
		http.NotFound(w, r)
		return
	}
	w.Header().Set("Content-Type", "application/octet-stream")
	http.ServeFile(w, r, t.file)
}

// localPath: a path on this hub, safe to redirect to (no scheme, no host, no backslashes browsers read as /).
func localPath(p string) bool {
	if !strings.HasPrefix(p, "/") || strings.HasPrefix(p, "//") || strings.ContainsAny(p, "\\\r\n") {
		return false
	}
	u, err := url.Parse(p)
	return err == nil && u.Scheme == "" && u.Host == "" && u.User == nil
}

// chatUpload stores files for a model chat on the hub itself (no Mac involved) and returns their ids.
func (s *Server) chatUpload(w http.ResponseWriter, r *http.Request) {
	r.Body = http.MaxBytesReader(w, r.Body, MaxUpload+1<<20)
	if err := r.ParseMultipartForm(32 << 20); err != nil {
		jsonErr(w, http.StatusBadRequest, fmt.Errorf("upload (max %d MB): %w", MaxUpload>>20, err))
		return
	}
	type up struct {
		ID    int64  `json:"id"`
		Name  string `json:"name"`
		Image bool   `json:"image"`
		URL   string `json:"url"`
	}
	out := []up{}
	for _, fh := range r.MultipartForm.File["file"] {
		f, err := fh.Open()
		if err != nil {
			jsonErr(w, http.StatusBadRequest, err)
			return
		}
		name := agent.SafeName(fh.Filename)
		id, err := s.storeAsset(r.Context(), "hub", "chat/"+name, name, f, nil, "upload")
		f.Close()
		if err != nil {
			jsonErr(w, http.StatusInternalServerError, err)
			return
		}
		mt, _, _ := mime.ParseMediaType(mime.TypeByExtension(filepath.Ext(name)))
		out = append(out, up{id, name, strings.HasPrefix(mt, "image/") && mt != "image/svg+xml", fmt.Sprintf("/files/%d", id)})
	}
	writeJSON(w, http.StatusOK, out)
}
