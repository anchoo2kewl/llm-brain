package ingest

import (
	"bytes"
	"compress/gzip"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"time"
)

// HTTPSink ships chunks to a hub (`hub ship --to http://<studio>:8787`).
type HTTPSink struct {
	Base   string
	Client *http.Client
}

func NewHTTPSink(base string) *HTTPSink {
	return &HTTPSink{Base: base, Client: &http.Client{Timeout: 2 * time.Minute}}
}

// Wire headers for POST /api/ship/chunk (body: gzip of the raw bytes).
const (
	HHost   = "X-Hub-Host"
	HKind   = "X-Hub-Kind"
	HPath   = "X-Hub-Path"
	HOffset = "X-Hub-Offset"
	HSize   = "X-Hub-Size"
	HMtime  = "X-Hub-Mtime"
	HTZ     = "X-Hub-Tz"
	HReset  = "X-Hub-Reset"
	HEOF    = "X-Hub-Eof"
)

type OffsetReply struct {
	Offset int64  `json:"offset"`
	Error  string `json:"error,omitempty"`
}

func (h *HTTPSink) Offsets(ctx context.Context, host string) (map[string]int64, error) {
	req, _ := http.NewRequestWithContext(ctx, http.MethodGet, h.Base+"/api/ship/offsets?host="+url.QueryEscape(host), nil)
	resp, err := h.Client.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		b, _ := io.ReadAll(io.LimitReader(resp.Body, 1024))
		return nil, fmt.Errorf("offsets: %s: %s", resp.Status, b)
	}
	out := map[string]int64{}
	return out, json.NewDecoder(resp.Body).Decode(&out)
}

func (h *HTTPSink) Chunk(ctx context.Context, c Chunk) (int64, error) {
	var buf bytes.Buffer
	zw := gzip.NewWriter(&buf)
	_, _ = zw.Write(c.Data)
	_ = zw.Close()
	req, _ := http.NewRequestWithContext(ctx, http.MethodPost, h.Base+"/api/ship/chunk", &buf)
	req.Header.Set("Content-Encoding", "gzip")
	req.Header.Set("Content-Type", "application/octet-stream")
	req.Header.Set(HHost, c.Host)
	req.Header.Set(HKind, c.Kind)
	req.Header.Set(HPath, url.PathEscape(c.Path))
	req.Header.Set(HOffset, strconv.FormatInt(c.Offset, 10))
	req.Header.Set(HSize, strconv.FormatInt(c.Size, 10))
	req.Header.Set(HMtime, c.Mtime.UTC().Format(time.RFC3339Nano))
	req.Header.Set(HTZ, c.TZ)
	req.Header.Set(HReset, strconv.FormatBool(c.Reset))
	req.Header.Set(HEOF, strconv.FormatBool(c.EOF))
	resp, err := h.Client.Do(req)
	if err != nil {
		return 0, err
	}
	defer resp.Body.Close()
	var r OffsetReply
	_ = json.NewDecoder(io.LimitReader(resp.Body, 1<<16)).Decode(&r)
	switch resp.StatusCode {
	case http.StatusOK:
		return r.Offset, nil
	case http.StatusConflict:
		return r.Offset, ErrOffset
	default:
		return 0, fmt.Errorf("chunk: %s: %s", resp.Status, r.Error)
	}
}

func (h *HTTPSink) Heartbeat(ctx context.Context, hb Heartbeat) error {
	body, _ := json.Marshal(hb)
	req, _ := http.NewRequestWithContext(ctx, http.MethodPost, h.Base+"/api/ship/heartbeat", bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	resp, err := h.Client.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		b, _ := io.ReadAll(io.LimitReader(resp.Body, 1024))
		return fmt.Errorf("heartbeat: %s: %s", resp.Status, b)
	}
	return nil
}

// DecodeChunk is the server half of Chunk: the request back into a Chunk.
func DecodeChunk(r *http.Request, maxBytes int64) (Chunk, error) {
	var c Chunk
	p, err := url.PathUnescape(r.Header.Get(HPath))
	if err != nil {
		return c, err
	}
	c.Host, c.Kind, c.Path, c.TZ = r.Header.Get(HHost), r.Header.Get(HKind), p, r.Header.Get(HTZ)
	if c.Offset, err = strconv.ParseInt(r.Header.Get(HOffset), 10, 64); err != nil {
		return c, fmt.Errorf("bad offset")
	}
	c.Size, _ = strconv.ParseInt(r.Header.Get(HSize), 10, 64)
	c.Mtime, _ = time.Parse(time.RFC3339Nano, r.Header.Get(HMtime))
	c.Reset, _ = strconv.ParseBool(r.Header.Get(HReset))
	c.EOF, _ = strconv.ParseBool(r.Header.Get(HEOF))
	var body io.Reader = io.LimitReader(r.Body, maxBytes)
	if r.Header.Get("Content-Encoding") == "gzip" {
		zr, err := gzip.NewReader(body)
		if err != nil {
			return c, err
		}
		defer zr.Close()
		body = io.LimitReader(zr, maxBytes)
	}
	c.Data, err = io.ReadAll(body)
	return c, err
}
