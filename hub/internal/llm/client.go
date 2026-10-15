// Package llm is plain chat: the hub talks to a model through the account gateway's OpenAI-compatible API
// (GET /v1/models, POST /v1/chat/completions with stream: true). No tools, no folder.
package llm

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"sort"
	"strings"
	"time"
)

// Client calls the gateway. KeyFile holds CPA_API_KEY; it is read on every call, so a new key needs no
// restart.
type Client struct {
	URL     string
	KeyFile string
	Key     string // set directly in tests
	HTTP    *http.Client
}

// ErrNoGateway: no gateway configured, or no client key for it.
var ErrNoGateway = errors.New("no account gateway configured")

func (c *Client) key() string {
	if c.Key != "" {
		return c.Key
	}
	b, err := os.ReadFile(c.KeyFile)
	if err != nil {
		return ""
	}
	sc := bufio.NewScanner(bytes.NewReader(b))
	for sc.Scan() {
		t := strings.TrimPrefix(strings.TrimSpace(sc.Text()), "export ")
		if k, v, ok := strings.Cut(t, "="); ok && strings.TrimSpace(k) == "CPA_API_KEY" {
			return strings.Trim(strings.TrimSpace(v), `"'`)
		}
	}
	return ""
}

func (c *Client) req(ctx context.Context, method, path string, body io.Reader) (*http.Request, error) {
	if c == nil || c.URL == "" {
		return nil, ErrNoGateway
	}
	k := c.key()
	if k == "" {
		return nil, fmt.Errorf("%w: no CPA_API_KEY in %s", ErrNoGateway, c.KeyFile)
	}
	r, err := http.NewRequestWithContext(ctx, method, strings.TrimRight(c.URL, "/")+path, body)
	if err != nil {
		return nil, err
	}
	r.Header.Set("Authorization", "Bearer "+k)
	if body != nil {
		r.Header.Set("Content-Type", "application/json")
	}
	return r, nil
}

func (c *Client) http() *http.Client {
	if c.HTTP != nil {
		return c.HTTP
	}
	return http.DefaultClient
}

type Model struct {
	ID      string `json:"id"`
	OwnedBy string `json:"owned_by"`
}

// Models lists what the gateway serves.
func (c *Client) Models(ctx context.Context) ([]Model, error) {
	ctx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	r, err := c.req(ctx, http.MethodGet, "/v1/models", nil)
	if err != nil {
		return nil, err
	}
	resp, err := c.http().Do(r)
	if err != nil {
		return nil, fmt.Errorf("gateway unreachable: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		b, _ := io.ReadAll(io.LimitReader(resp.Body, 512))
		return nil, fmt.Errorf("gateway models: %s: %s", resp.Status, strings.TrimSpace(string(b)))
	}
	var out struct {
		Data []Model `json:"data"`
	}
	if err := json.NewDecoder(io.LimitReader(resp.Body, 8<<20)).Decode(&out); err != nil {
		return nil, err
	}
	sort.Slice(out.Data, func(i, j int) bool { return out.Data[i].ID < out.Data[j].ID })
	return out.Data, nil
}

// Vendor groups a model for the picker.
func Vendor(m Model) string {
	id := strings.ToLower(m.ID)
	owner := strings.ToLower(m.OwnedBy)
	switch {
	case strings.Contains(id, "claude") || owner == "anthropic":
		return "Claude"
	case strings.HasPrefix(id, "gpt") || strings.HasPrefix(id, "o1") || strings.HasPrefix(id, "o3") || strings.HasPrefix(id, "o4") ||
		strings.Contains(id, "codex") || strings.HasPrefix(id, "chatgpt") || owner == "openai":
		return "OpenAI"
	case strings.Contains(id, "gemini") || owner == "google":
		return "Gemini"
	}
	return "Other"
}

type Group struct {
	Vendor string
	Models []Model
}

// Grouped orders models Claude, OpenAI, Gemini, Other.
func Grouped(ms []Model) []Group {
	order := []string{"Claude", "OpenAI", "Gemini", "Other"}
	by := map[string][]Model{}
	for _, m := range ms {
		v := Vendor(m)
		by[v] = append(by[v], m)
	}
	var out []Group
	for _, v := range order {
		if len(by[v]) > 0 {
			out = append(out, Group{Vendor: v, Models: by[v]})
		}
	}
	return out
}

type Message struct {
	Role    string `json:"role"`
	Content string `json:"content"`
}

// Stream asks model for the next reply and calls onDelta with each piece as it arrives. It returns the whole
// reply.
func (c *Client) Stream(ctx context.Context, model string, msgs []Message, onDelta func(string)) (string, error) {
	body, _ := json.Marshal(map[string]any{"model": model, "messages": msgs, "stream": true})
	r, err := c.req(ctx, http.MethodPost, "/v1/chat/completions", bytes.NewReader(body))
	if err != nil {
		return "", err
	}
	r.Header.Set("Accept", "text/event-stream")
	resp, err := c.http().Do(r)
	if err != nil {
		return "", fmt.Errorf("gateway unreachable: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		b, _ := io.ReadAll(io.LimitReader(resp.Body, 2048))
		return "", fmt.Errorf("gateway: %s: %s", resp.Status, strings.TrimSpace(string(b)))
	}
	var full strings.Builder
	br := bufio.NewReaderSize(resp.Body, 64<<10)
	for {
		line, err := br.ReadString('\n')
		if t := strings.TrimSpace(line); strings.HasPrefix(t, "data:") {
			data := strings.TrimSpace(strings.TrimPrefix(t, "data:"))
			if data == "[DONE]" {
				return full.String(), nil
			}
			var ev struct {
				Choices []struct {
					Delta struct {
						Content string `json:"content"`
					} `json:"delta"`
					Message struct {
						Content string `json:"content"`
					} `json:"message"`
				} `json:"choices"`
				Error *struct {
					Message string `json:"message"`
				} `json:"error"`
			}
			if json.Unmarshal([]byte(data), &ev) == nil {
				if ev.Error != nil {
					return full.String(), errors.New(ev.Error.Message)
				}
				for _, ch := range ev.Choices {
					d := ch.Delta.Content
					if d == "" {
						d = ch.Message.Content
					}
					if d != "" {
						full.WriteString(d)
						if onDelta != nil {
							onDelta(d)
						}
					}
				}
			}
		}
		if err != nil {
			if errors.Is(err, io.EOF) {
				return full.String(), nil
			}
			return full.String(), err
		}
	}
}
