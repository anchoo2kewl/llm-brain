package llm

import (
	"bytes"
	"context"
	"encoding/base64"
	"errors"
	"fmt"
	"log"
	"mime"
	"os"
	"path/filepath"
	"strings"
	"unicode/utf8"
	"sync"
	"time"

	"github.com/anchoo2kewl/llm-brain/hub/internal/store"
)

// Chats runs plain chats: it stores each message, asks the gateway for the reply in the background (a
// closed tab doesn't lose it), and keeps the reply-so-far in memory for the page to stream.
type Chats struct {
	S       *store.Store
	C       *Client
	Log     *log.Logger
	Timeout time.Duration // per reply; default 10 minutes

	AssetDir string // where hub assets live (<state>/assets): attached files are read from here

	mu   sync.Mutex
	live map[int64]*reply
	wg   sync.WaitGroup
}

type reply struct {
	mu   sync.Mutex
	text strings.Builder
	done bool
	err  string
}

const maxMessage = 256 << 10

var modelOK = func(m string) bool {
	return m != "" && len(m) <= 200 && !strings.ContainsAny(m, " \t\n\"'<>")
}

// Start makes a chat and sends its first message.
func (c *Chats) Start(ctx context.Context, model, text string, attach ...int64) (int64, int64, error) {
	if err := check(model, text, len(attach)); err != nil {
		return 0, 0, err
	}
	t := strings.TrimSpace(text)
	if t == "" {
		t = "Attached files"
	}
	if i := strings.IndexByte(t, '\n'); i >= 0 {
		t = t[:i]
	}
	if len(t) > 100 {
		t = t[:100] + "…"
	}
	id, err := c.S.CreateLLMChat(ctx, t, model)
	if err != nil {
		return 0, 0, err
	}
	rid, err := c.Send(ctx, id, model, text, attach...)
	return id, rid, err
}

func check(model, text string, files int) error {
	if !modelOK(model) {
		return InputError("pick a model")
	}
	if files > 20 {
		return InputError("attach at most 20 files")
	}
	if strings.TrimSpace(text) == "" && files == 0 {
		return InputError("write a message")
	}
	if len(text) > maxMessage {
		return InputError("the message is too long")
	}
	return nil
}

// InputError is a problem with what was asked; pages show it as is.
type InputError string

func (e InputError) Error() string { return string(e) }

// Send adds the person's message and starts the reply from model (which may differ from the last one).
func (c *Chats) Send(ctx context.Context, chatID int64, model, text string, attach ...int64) (int64, error) {
	if err := check(model, text, len(attach)); err != nil {
		return 0, err
	}
	if _, err := c.S.LLMChat(ctx, chatID); err != nil {
		return 0, err
	}
	for _, a := range attach { // only the hub's own files, by id
		if _, err := c.S.Asset(ctx, a); err != nil {
			return 0, InputError("an attached file is gone; attach it again")
		}
	}
	rid, err := c.S.AddLLMTurn(ctx, chatID, model, text, attach...)
	if errors.Is(err, store.ErrBusy) {
		return 0, InputError("the last reply is still coming")
	}
	if err != nil {
		return 0, err
	}
	r := &reply{}
	c.mu.Lock()
	if c.live == nil {
		c.live = map[int64]*reply{}
	}
	c.live[rid] = r
	c.mu.Unlock()
	c.wg.Add(1)
	go func() {
		defer c.wg.Done()
		c.generate(chatID, rid, model, r)
	}()
	return rid, nil
}

// Wait waits for the replies in flight (tests, shutdown).
func (c *Chats) Wait() { c.wg.Wait() }

// Snapshot is a reply so far. ok is false when it isn't streaming in this process (finished long ago, or
// the hub restarted): read the store instead.
func (c *Chats) Snapshot(id int64) (text string, done bool, errMsg string, ok bool) {
	c.mu.Lock()
	r := c.live[id]
	c.mu.Unlock()
	if r == nil {
		return "", false, "", false
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.text.String(), r.done, r.err, true
}

func (c *Chats) generate(chatID, rid int64, model string, r *reply) {
	timeout := c.Timeout
	if timeout <= 0 {
		timeout = 10 * time.Minute
	}
	ctx, cancel := context.WithTimeout(context.Background(), timeout)
	defer cancel()
	finish := func(text, status, msg string) {
		// A fresh context: ctx may be the one that just expired, and the reply must not stay "streaming".
		fctx, fcancel := context.WithTimeout(context.Background(), 15*time.Second)
		defer fcancel()
		if err := c.S.SetLLMReply(fctx, rid, text, status, msg); err != nil && c.Log != nil {
			c.Log.Printf("chat %d: save reply: %v", chatID, err)
		}
		r.mu.Lock()
		r.done, r.err = true, msg
		r.mu.Unlock()
		// Keep it in memory a little while so a page that is streaming sees the end.
		time.AfterFunc(time.Minute, func() {
			c.mu.Lock()
			delete(c.live, rid)
			c.mu.Unlock()
		})
	}
	ms, err := c.S.LLMMessages(ctx, chatID)
	if err != nil {
		finish("", "error", err.Error())
		return
	}
	atts, _ := c.S.LLMAttachments(ctx, chatID)
	var hist []Message
	for _, m := range ms {
		if m.ID == rid || (m.Role == "assistant" && (m.Status != "done" || m.Content == "")) {
			continue
		}
		if m.Role == "user" && len(atts[m.ID]) > 0 {
			hist = append(hist, Message{Role: m.Role, Content: c.parts(m.Content, atts[m.ID])})
			continue
		}
		hist = append(hist, Message{Role: m.Role, Content: m.Content})
	}
	lastSave := time.Now()
	text, err := c.C.Stream(ctx, model, hist, func(d string) {
		r.mu.Lock()
		r.text.WriteString(d)
		snap := ""
		if time.Since(lastSave) > 2*time.Second {
			snap, lastSave = r.text.String(), time.Now()
		}
		r.mu.Unlock()
		if snap != "" {
			_ = c.S.SetLLMReply(ctx, rid, snap, "streaming", "")
		}
	})
	if err != nil {
		msg := err.Error()
		if errors.Is(ctx.Err(), context.DeadlineExceeded) {
			msg = fmt.Sprintf("no complete reply within %s; this is what arrived", timeout)
		} else if errors.Is(err, ErrNoGateway) || strings.Contains(msg, "unreachable") {
			msg = fmt.Sprintf("The account gateway isn't answering (%v). Start it, or use an agent chat, which falls back to the CLI's own login.", err)
		}
		if c.Log != nil {
			c.Log.Printf("chat %d: %s: %v", chatID, model, err)
		}
		finish(text, "error", msg)
		return
	}
	finish(text, "done", "")
}

// Attachment limits: images go to the model as data URLs, text files inline.
const (
	maxImage    = 8 << 20
	maxTextFile = 256 << 10
)

// parts turns a message with attachments into content parts: the text, each image, each text file.
func (c *Chats) parts(text string, atts []store.Asset) []Part {
	var out []Part
	if strings.TrimSpace(text) != "" {
		out = append(out, Part{Type: "text", Text: text})
	}
	for _, a := range atts {
		ctype := mime.TypeByExtension(strings.ToLower(filepath.Ext(a.Name)))
		mt, _, _ := mime.ParseMediaType(ctype)
		b, err := os.ReadFile(filepath.Join(c.AssetDir, a.SHA256))
		switch {
		case err != nil:
			out = append(out, Part{Type: "text", Text: fmt.Sprintf("[attached file %s is no longer on the hub]", a.Name)})
		case (mt == "image/png" || mt == "image/jpeg" || mt == "image/gif" || mt == "image/webp") && len(b) <= maxImage:
			out = append(out, Part{Type: "image_url", ImageURL: &ImageURL{URL: "data:" + mt + ";base64," + base64.StdEncoding.EncodeToString(b)}})
		case len(b) <= maxTextFile && utf8.Valid(b) && !bytes.ContainsRune(b, 0):
			out = append(out, Part{Type: "text", Text: fmt.Sprintf("Attached file %s:\n```\n%s\n```", a.Name, b)})
		default:
			out = append(out, Part{Type: "text", Text: fmt.Sprintf("[attached file %s (%s, %d bytes) can't be read in a plain chat; open it in a terminal to work on it]", a.Name, mt, len(b))})
		}
	}
	if len(out) == 0 {
		out = append(out, Part{Type: "text", Text: "(no text)"})
	}
	return out
}
