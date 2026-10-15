package web

import (
	"context"
	"crypto/aes"
	"crypto/cipher"
	"crypto/ecdh"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"encoding/base64"
	"encoding/binary"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/anchoo2kewl/llm-brain/hub/internal/store"
)

// RFC 8291, appendix A: the one worked example of aes128gcm Web Push encryption.
func TestEncryptRFC8291Vector(t *testing.T) {
	asPriv, _ := b64.DecodeString("yfWPiYE-n46HLnH0KqZOF1fJJU3MYrct3AELtAQ-oRw")
	as, err := ecdh.P256().NewPrivateKey(asPriv)
	if err != nil {
		t.Fatal(err)
	}
	salt, _ := b64.DecodeString("DGv6ra1nlYgDCS1FRnbzlw")
	got, err := encrypt([]byte("When I grow up, I want to be a watermelon"),
		"BCVxsr7N_eNgVRqvHtD0zTZsEc6-VV-JvLexhqUzORcxaOzi6-AYWXvTBHm4bjyPjs7Vd8pZGH6SRpkNtoIAiw4", "BTBZMqHH6r4Tts7J_aSIgg", as, salt)
	if err != nil {
		t.Fatal(err)
	}
	want := "DGv6ra1nlYgDCS1FRnbzlwAAEABBBP4z9KsN6nGRTbVYI_c7VJSPQTBtkgcy27mlmlMoZIIgDll6e3vCYLocInmYWAmS6TlzAC8wEqKK6PBru3jl7A_yl95bQpu6cVPTpK4Mqgkf1CXztLVBSt2Ks3oZwbuwXPXLWyouBWLVWGNWQexSgSxsj_Qulcy4a-fN"
	if b64.EncodeToString(got) != want {
		t.Fatalf("got  %s\nwant %s", b64.EncodeToString(got), want)
	}
}

// browser is a fake browser's push keys, able to decrypt what the hub sends it.
type browser struct {
	key  *ecdh.PrivateKey
	auth []byte
}

func newBrowser(t *testing.T) browser {
	k, err := ecdh.P256().GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	a := make([]byte, 16)
	_, _ = rand.Read(a)
	return browser{k, a}
}

func (b browser) sub(endpoint string) PushSub {
	var s PushSub
	s.Endpoint = endpoint
	s.Keys.P256dh = b64.EncodeToString(b.key.PublicKey().Bytes())
	s.Keys.Auth = b64.EncodeToString(b.auth)
	return s
}

// decrypt is the receiving side of RFC 8291.
func (b browser) decrypt(t *testing.T, body []byte) []byte {
	t.Helper()
	salt, rs, idlen := body[:16], binary.BigEndian.Uint32(body[16:20]), int(body[20])
	if rs != 4096 || idlen != 65 {
		t.Fatalf("header rs=%d idlen=%d", rs, idlen)
	}
	asPub, ct := body[21:21+idlen], body[21+idlen:]
	as, err := ecdh.P256().NewPublicKey(asPub)
	if err != nil {
		t.Fatal(err)
	}
	secret, _ := b.key.ECDH(as)
	ua := b.key.PublicKey().Bytes()
	ikm := hmacSHA256(hmacSHA256(b.auth, secret), []byte("WebPush: info\x00"), ua, asPub, []byte{1})
	prk := hmacSHA256(salt, ikm)
	cek := hmacSHA256(prk, []byte("Content-Encoding: aes128gcm\x00"), []byte{1})[:16]
	nonce := hmacSHA256(prk, []byte("Content-Encoding: nonce\x00"), []byte{1})[:12]
	blk, _ := aes.NewCipher(cek)
	gcm, _ := cipher.NewGCM(blk)
	pt, err := gcm.Open(nil, nonce, ct, nil)
	if err != nil {
		t.Fatal(err)
	}
	if pt[len(pt)-1] != 2 {
		t.Fatalf("missing last-record delimiter")
	}
	return pt[:len(pt)-1]
}

func TestVAPIDToken(t *testing.T) {
	k, _ := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	tok, err := vapidJWT(k, "https://push.example", "https://github.com/x", time.Now().Add(time.Hour))
	if err != nil {
		t.Fatal(err)
	}
	if !verifyJWT(&k.PublicKey, tok) {
		t.Fatal("signature doesn't verify")
	}
	parts := strings.Split(tok, ".")
	claims, _ := base64.RawURLEncoding.DecodeString(parts[1])
	var c map[string]any
	_ = json.Unmarshal(claims, &c)
	if c["aud"] != "https://push.example" || c["sub"] != "https://github.com/x" {
		t.Fatalf("claims %v", c)
	}
	other, _ := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if verifyJWT(&other.PublicKey, tok) {
		t.Fatal("verifies with another key")
	}
}

func TestPushDeliversAndForgetsGoneBrowsers(t *testing.T) {
	st, err := store.Open(filepath.Join(t.TempDir(), "hub.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	type hit struct {
		hdr  http.Header
		body []byte
	}
	var mu sync.Mutex
	var hits []hit
	ps := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		b, _ := io.ReadAll(r.Body)
		mu.Lock()
		hits = append(hits, hit{r.Header.Clone(), b})
		mu.Unlock()
		if strings.HasSuffix(r.URL.Path, "/gone") {
			w.WriteHeader(http.StatusGone)
			return
		}
		w.WriteHeader(http.StatusCreated)
	}))
	defer ps.Close()
	p := &Push{S: st, Client: ps.Client()}
	ctx := context.Background()
	b1, b2 := newBrowser(t), newBrowser(t)
	if err := p.Subscribe(ctx, b1.sub(ps.URL+"/ok")); err != nil {
		t.Fatal(err)
	}
	if err := p.Subscribe(ctx, b2.sub(ps.URL+"/gone")); err != nil {
		t.Fatal(err)
	}
	bad := b1.sub("http://insecure.example/x")
	if err := p.Subscribe(ctx, bad); err == nil {
		t.Fatal("http endpoint accepted")
	}
	bad = b1.sub(ps.URL + "/x")
	bad.Keys.Auth = "short"
	if err := p.Subscribe(ctx, bad); err == nil {
		t.Fatal("bad auth secret accepted")
	}
	p.Announce(ctx, []Item{{Key: "t:4", State: "waiting", Kind: "terminal", Title: "fix pager", Detail: "Done. Run the tests?", Link: "/terminals/4"}})
	if len(hits) != 2 {
		t.Fatalf("want 2 deliveries, got %d", len(hits))
	}
	h := hits[0]
	if h.hdr.Get("Content-Encoding") != "aes128gcm" || h.hdr.Get("TTL") == "" || !strings.HasPrefix(h.hdr.Get("Authorization"), "vapid t=") {
		t.Fatalf("headers %v", h.hdr)
	}
	var n Notice
	if err := json.Unmarshal(b1.decrypt(t, h.body), &n); err != nil {
		t.Fatal(err)
	}
	if n.Title != "fix pager — waiting for you" || n.URL != "/terminals/4" || n.Tag != "t:4" {
		t.Fatalf("notice %+v", n)
	}
	subs, _ := p.subs(ctx)
	if len(subs) != 1 || !strings.HasSuffix(subs[0].Endpoint, "/ok") || subs[0].LastOK == "" {
		t.Fatalf("the 410 browser should be gone, the other marked delivered: %+v", subs)
	}
	// Many at once: one summary.
	hits = nil
	var many []Item
	for i := 0; i < 5; i++ {
		many = append(many, Item{Key: "x" + itoa(int64(i)), State: "failed", Title: "run"})
	}
	p.Announce(ctx, many)
	if len(hits) != 1 {
		t.Fatalf("want one summary, got %d", len(hits))
	}
	_ = json.Unmarshal(b1.decrypt(t, hits[0].body), &n)
	if n.Title != "5 things need you" || n.Body != "5 failed" {
		t.Fatalf("summary %+v", n)
	}
	// The key survives a restart.
	k1, _ := p.PublicKey(ctx)
	k2, _ := (&Push{S: st}).PublicKey(ctx)
	if k1 == "" || k1 != k2 {
		t.Fatalf("VAPID key changed: %s vs %s", k1, k2)
	}
}
