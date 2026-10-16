package web

import (
	"bytes"
	"context"
	"crypto/aes"
	"crypto/cipher"
	"crypto/ecdh"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"crypto/x509"
	"encoding/base64"
	"encoding/binary"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	"math/big"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"time"

	"github.com/anchoo2kewl/llm-brain/hub/internal/store"
)

// Web Push: the installed app (or any browser that allowed it) is told when an agent is waiting for you, a PR
// needs a review, or something failed. Payloads are encrypted for each browser (RFC 8291, aes128gcm) and
// signed with the hub's own VAPID key (RFC 8292), so the push service only relays ciphertext. Browsers need
// the hub on HTTPS for this: `tailscale serve --bg 8787` gives https://<mac>.<tailnet>.ts.net.

const keyVAPID = "push_vapid" // the hub's VAPID private key, PKCS#8, base64

var b64 = base64.RawURLEncoding

// Push sends notifications to every subscribed browser.
type Push struct {
	S      *store.Store
	Log    *log.Logger
	Client *http.Client
	Sub    string // the VAPID subject: a URL or mailto: that identifies this sender

	once sync.Once
	key  *ecdsa.PrivateKey
	err  error
}

// PushSub is a browser's PushSubscription.
type PushSub struct {
	Endpoint string `json:"endpoint"`
	Keys     struct {
		P256dh string `json:"p256dh"`
		Auth   string `json:"auth"`
	} `json:"keys"`
	Label string `json:"label,omitempty"`
}

// Notice is what the service worker shows.
type Notice struct {
	Title string `json:"title"`
	Body  string `json:"body"`
	URL   string `json:"url"`
	Tag   string `json:"tag,omitempty"`
}

// vapid loads the hub's key, making one the first time.
func (p *Push) vapid(ctx context.Context) (*ecdsa.PrivateKey, error) {
	p.once.Do(func() {
		v, err := p.S.Setting(ctx, keyVAPID)
		if err != nil {
			p.err = err
			return
		}
		if v != "" {
			der, err := base64.StdEncoding.DecodeString(v)
			if err != nil {
				p.err = err
				return
			}
			k, err := x509.ParsePKCS8PrivateKey(der)
			if err != nil {
				p.err = err
				return
			}
			ek, ok := k.(*ecdsa.PrivateKey)
			if !ok {
				p.err = errors.New("stored VAPID key isn't ECDSA")
				return
			}
			p.key = ek
			return
		}
		k, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
		if err != nil {
			p.err = err
			return
		}
		der, _ := x509.MarshalPKCS8PrivateKey(k)
		if err := p.S.SetSettings(ctx, map[string]string{keyVAPID: base64.StdEncoding.EncodeToString(der)}); err != nil {
			p.err = err
			return
		}
		p.key = k
	})
	return p.key, p.err
}

// PublicKey is the applicationServerKey browsers subscribe with (uncompressed P-256 point, base64url).
func (p *Push) PublicKey(ctx context.Context) (string, error) {
	k, err := p.vapid(ctx)
	if err != nil {
		return "", err
	}
	pub, err := k.PublicKey.ECDH()
	if err != nil {
		return "", err
	}
	return b64.EncodeToString(pub.Bytes()), nil
}

// Subscribe stores a browser's subscription.
func (p *Push) Subscribe(ctx context.Context, s PushSub) error {
	u, err := url.Parse(s.Endpoint)
	if err != nil || u.Scheme != "https" || u.Host == "" {
		return errors.New("the endpoint must be an https URL")
	}
	if pk, err := b64.DecodeString(strings.TrimRight(s.Keys.P256dh, "=")); err != nil || len(pk) != 65 {
		return errors.New("bad p256dh key")
	}
	if a, err := b64.DecodeString(strings.TrimRight(s.Keys.Auth, "=")); err != nil || len(a) != 16 {
		return errors.New("bad auth secret")
	}
	_, err = p.S.DB.ExecContext(ctx, `INSERT INTO push_subs (endpoint, p256dh, auth, label, created_at) VALUES (?, ?, ?, ?, ?)
		ON CONFLICT (endpoint) DO UPDATE SET p256dh = excluded.p256dh, auth = excluded.auth, label = excluded.label`,
		s.Endpoint, s.Keys.P256dh, s.Keys.Auth, trunc(80, s.Label), store.Now())
	return err
}

func (p *Push) Unsubscribe(ctx context.Context, endpoint string) error {
	_, err := p.S.DB.ExecContext(ctx, `DELETE FROM push_subs WHERE endpoint = ?`, endpoint)
	return err
}

type pushRow struct {
	PushSub
	CreatedAt, LastOK, LastErr string
}

func (p *Push) subs(ctx context.Context) ([]pushRow, error) {
	rows, err := p.S.DB.QueryContext(ctx, `SELECT endpoint, p256dh, auth, label, created_at, last_ok, last_err FROM push_subs ORDER BY created_at`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []pushRow
	for rows.Next() {
		var r pushRow
		if err := rows.Scan(&r.Endpoint, &r.Keys.P256dh, &r.Keys.Auth, &r.Label, &r.CreatedAt, &r.LastOK, &r.LastErr); err != nil {
			return nil, err
		}
		out = append(out, r)
	}
	return out, rows.Err()
}

// Announce tells every browser about new inbox items: one notification each, or a summary for many.
func (p *Push) Announce(ctx context.Context, items []Item) {
	var ns []Notice
	for _, it := range items {
		ns = append(ns, noticeFor(it))
	}
	if len(ns) > 3 {
		ns = []Notice{{Title: fmt.Sprintf("%d things need you", len(items)), Body: summarize(items), URL: "/inbox", Tag: "inbox"}}
	}
	for _, n := range ns {
		if err := p.SendAll(ctx, n); err != nil && p.Log != nil {
			p.Log.Printf("push: %v", err)
		}
	}
}

func noticeFor(it Item) Notice {
	verb := map[string]string{"waiting": "waiting for you", "review": "needs a review", "blocked": "blocked", "failed": "failed"}[it.State]
	title := it.Title
	switch {
	case strings.HasPrefix(it.Key, "tb:"): // a frozen terminal: the title says it all
	case it.Kind == "chat":
		title = it.Title + " · " + it.Where
	case it.Kind == "terminal" || it.Kind == "task" || it.Kind == "session":
		title = it.Title + " — " + verb
	default:
		if verb != "" {
			title = it.Title + " — " + verb
		}
	}
	link := it.Link
	if it.Ext {
		link = "/inbox"
	}
	return Notice{Title: trunc(120, title), Body: trunc(240, it.Detail), URL: link, Tag: it.Key}
}

func summarize(items []Item) string {
	n := map[string]int{}
	for _, it := range items {
		n[it.State]++
	}
	var parts []string
	for _, s := range []string{"waiting", "review", "blocked", "failed"} {
		if n[s] > 0 {
			parts = append(parts, fmt.Sprintf("%d %s", n[s], map[string]string{"waiting": "waiting", "review": "to review", "blocked": "blocked", "failed": "failed"}[s]))
		}
	}
	return strings.Join(parts, " · ")
}

// SendAll delivers n to every subscription; ones the push service says are gone are dropped.
func (p *Push) SendAll(ctx context.Context, n Notice) error {
	subs, err := p.subs(ctx)
	if err != nil || len(subs) == 0 {
		return err
	}
	body, _ := json.Marshal(n)
	var errs []error
	for _, s := range subs {
		code, err := p.send(ctx, s.PushSub, body)
		switch {
		case code == http.StatusNotFound || code == http.StatusGone:
			_ = p.Unsubscribe(ctx, s.Endpoint)
		case err != nil:
			_, _ = p.S.DB.ExecContext(ctx, `UPDATE push_subs SET last_err = ? WHERE endpoint = ?`, trunc(300, err.Error()), s.Endpoint)
			errs = append(errs, err)
		default:
			_, _ = p.S.DB.ExecContext(ctx, `UPDATE push_subs SET last_ok = ?, last_err = '' WHERE endpoint = ?`, store.Now(), s.Endpoint)
		}
	}
	return errors.Join(errs...)
}

func (p *Push) send(ctx context.Context, s PushSub, payload []byte) (int, error) {
	key, err := p.vapid(ctx)
	if err != nil {
		return 0, err
	}
	body, err := encrypt(payload, s.Keys.P256dh, s.Keys.Auth, nil, nil)
	if err != nil {
		return 0, err
	}
	u, err := url.Parse(s.Endpoint)
	if err != nil {
		return 0, err
	}
	sub := p.Sub
	if sub == "" {
		sub = "https://github.com/anchoo2kewl/llm-brain"
	}
	jwt, err := vapidJWT(key, u.Scheme+"://"+u.Host, sub, time.Now().Add(12*time.Hour))
	if err != nil {
		return 0, err
	}
	pub, _ := key.PublicKey.ECDH()
	req, _ := http.NewRequestWithContext(ctx, http.MethodPost, s.Endpoint, bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/octet-stream")
	req.Header.Set("Content-Encoding", "aes128gcm")
	req.Header.Set("TTL", "86400")
	req.Header.Set("Urgency", "high")
	req.Header.Set("Authorization", "vapid t="+jwt+", k="+b64.EncodeToString(pub.Bytes()))
	c := p.Client
	if c == nil {
		c = &http.Client{Timeout: 15 * time.Second}
	}
	resp, err := c.Do(req)
	if err != nil {
		return 0, err
	}
	defer resp.Body.Close()
	if resp.StatusCode/100 != 2 {
		msg, _ := io.ReadAll(io.LimitReader(resp.Body, 300))
		return resp.StatusCode, fmt.Errorf("%s: %s %s", u.Host, resp.Status, strings.TrimSpace(string(msg)))
	}
	return resp.StatusCode, nil
}

// vapidJWT signs the RFC 8292 token: ES256 over {aud, exp, sub}.
func vapidJWT(key *ecdsa.PrivateKey, aud, sub string, exp time.Time) (string, error) {
	head := b64.EncodeToString([]byte(`{"typ":"JWT","alg":"ES256"}`))
	claims, _ := json.Marshal(map[string]any{"aud": aud, "exp": exp.Unix(), "sub": sub})
	signing := head + "." + b64.EncodeToString(claims)
	h := sha256.Sum256([]byte(signing))
	r, s, err := ecdsa.Sign(rand.Reader, key, h[:])
	if err != nil {
		return "", err
	}
	sig := make([]byte, 64)
	r.FillBytes(sig[:32])
	s.FillBytes(sig[32:])
	return signing + "." + b64.EncodeToString(sig), nil
}

// verifyJWT checks a token signed by vapidJWT (tests).
func verifyJWT(pub *ecdsa.PublicKey, tok string) bool {
	i := strings.LastIndexByte(tok, '.')
	if i < 0 {
		return false
	}
	sig, err := b64.DecodeString(tok[i+1:])
	if err != nil || len(sig) != 64 {
		return false
	}
	h := sha256.Sum256([]byte(tok[:i]))
	return ecdsa.Verify(pub, h[:], new(big.Int).SetBytes(sig[:32]), new(big.Int).SetBytes(sig[32:]))
}

func hmacSHA256(key []byte, parts ...[]byte) []byte {
	m := hmac.New(sha256.New, key)
	for _, p := range parts {
		m.Write(p)
	}
	return m.Sum(nil)
}

// encrypt is RFC 8291 with one aes128gcm record. asKey and salt are random unless given (the RFC's test
// vector gives both).
func encrypt(plaintext []byte, p256dh, authSecret string, asKey *ecdh.PrivateKey, salt []byte) ([]byte, error) {
	uaPub, err := b64.DecodeString(strings.TrimRight(p256dh, "="))
	if err != nil {
		return nil, fmt.Errorf("p256dh: %w", err)
	}
	auth, err := b64.DecodeString(strings.TrimRight(authSecret, "="))
	if err != nil {
		return nil, fmt.Errorf("auth: %w", err)
	}
	ua, err := ecdh.P256().NewPublicKey(uaPub)
	if err != nil {
		return nil, fmt.Errorf("p256dh: %w", err)
	}
	if asKey == nil {
		if asKey, err = ecdh.P256().GenerateKey(rand.Reader); err != nil {
			return nil, err
		}
	}
	if salt == nil {
		salt = make([]byte, 16)
		if _, err := rand.Read(salt); err != nil {
			return nil, err
		}
	}
	secret, err := asKey.ECDH(ua)
	if err != nil {
		return nil, err
	}
	asPub := asKey.PublicKey().Bytes()
	// IKM = HKDF(auth, ecdh secret, "WebPush: info\0" || ua_public || as_public, 32)
	prkKey := hmacSHA256(auth, secret)
	ikm := hmacSHA256(prkKey, []byte("WebPush: info\x00"), uaPub, asPub, []byte{1})
	// CEK and nonce = HKDF(salt, IKM, ...)
	prk := hmacSHA256(salt, ikm)
	cek := hmacSHA256(prk, []byte("Content-Encoding: aes128gcm\x00"), []byte{1})[:16]
	nonce := hmacSHA256(prk, []byte("Content-Encoding: nonce\x00"), []byte{1})[:12]
	block, err := aes.NewCipher(cek)
	if err != nil {
		return nil, err
	}
	gcm, err := cipher.NewGCM(block)
	if err != nil {
		return nil, err
	}
	ct := gcm.Seal(nil, nonce, append(append([]byte{}, plaintext...), 2), nil) // 0x02: the last (only) record
	var out bytes.Buffer
	out.Write(salt)
	_ = binary.Write(&out, binary.BigEndian, uint32(4096))
	out.WriteByte(byte(len(asPub)))
	out.Write(asPub)
	out.Write(ct)
	return out.Bytes(), nil
}

// ---------------------------------------------------------------- routes

func (s *Server) pushRoutes(mux *http.ServeMux) {
	g := func(pattern string, h http.HandlerFunc) { mux.HandleFunc(pattern, s.tailnetOnly(h)) }
	p := func(pattern string, h http.HandlerFunc) { mux.HandleFunc(pattern, s.tailnetOnly(s.sameSite(h))) }
	g("GET /api/push/key", s.pushKey)
	p("POST /api/push/subscribe", s.pushSubscribe)
	p("POST /api/push/unsubscribe", s.pushUnsubscribe)
	p("POST /api/push/test", s.pushTest)
}

func (s *Server) pushKey(w http.ResponseWriter, r *http.Request) {
	if s.Push == nil {
		writeJSON(w, http.StatusServiceUnavailable, map[string]string{"error": "push is off"})
		return
	}
	k, err := s.Push.PublicKey(r.Context())
	if err != nil {
		s.fail(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]string{"key": k})
}

func (s *Server) pushSubscribe(w http.ResponseWriter, r *http.Request) {
	var sub PushSub
	if s.Push == nil || !jsonBody(w, r, &sub) {
		if s.Push == nil {
			writeJSON(w, http.StatusServiceUnavailable, map[string]string{"error": "push is off"})
		}
		return
	}
	if err := s.Push.Subscribe(r.Context(), sub); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": err.Error()})
		return
	}
	writeJSON(w, http.StatusOK, map[string]bool{"ok": true})
}

func (s *Server) pushUnsubscribe(w http.ResponseWriter, r *http.Request) {
	var sub PushSub
	if s.Push == nil || !jsonBody(w, r, &sub) {
		if s.Push == nil {
			writeJSON(w, http.StatusServiceUnavailable, map[string]string{"error": "push is off"})
		}
		return
	}
	if err := s.Push.Unsubscribe(r.Context(), sub.Endpoint); err != nil {
		s.fail(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]bool{"ok": true})
}

func (s *Server) pushTest(w http.ResponseWriter, r *http.Request) {
	if s.Push == nil {
		writeJSON(w, http.StatusServiceUnavailable, map[string]string{"error": "push is off"})
		return
	}
	err := s.Push.SendAll(r.Context(), Notice{Title: "llm-brain", Body: "Notifications work. You'll hear when an agent is waiting for you.", URL: "/inbox", Tag: "test"})
	if err != nil {
		writeJSON(w, http.StatusBadGateway, map[string]string{"error": err.Error()})
		return
	}
	writeJSON(w, http.StatusOK, map[string]bool{"ok": true})
}
