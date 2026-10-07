package main

import (
	"bytes"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/cookiejar"
	"net/http/httptest"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/hammondus/mailer"
)

type harness struct {
	t    *testing.T
	srv  *httptest.Server
	st   *store
	mail *mailer.MemorySender
}

// newHarness runs the real handler over TLS, so the Secure session cookie
// behaves as it does in production.
func newHarness(t *testing.T) *harness {
	t.Helper()
	st, err := openStore(filepath.Join(t.TempDir(), "test.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { st.close() })
	mail := &mailer.MemorySender{}
	h, err := newHandler(st, mail, slog.New(slog.NewTextHandler(io.Discard, nil)), false)
	if err != nil {
		t.Fatal(err)
	}
	srv := httptest.NewTLSServer(h)
	t.Cleanup(srv.Close)
	return &harness{t: t, srv: srv, st: st, mail: mail}
}

// client returns a fresh browser: its own cookie jar.
// srv.Client returns one shared *http.Client, so copy it: setting Jar on
// the shared one would give every "browser" the same cookies.
func (h *harness) client() *http.Client {
	c := *h.srv.Client()
	c.Jar, _ = cookiejar.New(nil)
	return &c
}

func (h *harness) do(c *http.Client, method, path string, body any) (int, map[string]any, http.Header) {
	h.t.Helper()
	var r io.Reader
	if body != nil {
		b, _ := json.Marshal(body)
		r = bytes.NewReader(b)
	}
	req, _ := http.NewRequestWithContext(h.t.Context(), method, h.srv.URL+path, r)
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	res, err := c.Do(req)
	if err != nil {
		h.t.Fatal(err)
	}
	defer res.Body.Close()
	raw, _ := io.ReadAll(res.Body)
	var out map[string]any
	_ = json.Unmarshal(raw, &out)
	return res.StatusCode, out, res.Header
}

// waitMail waits for the asynchronous send to land.
func (h *harness) waitMail(n int) []*mailer.Message {
	h.t.Helper()
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		if sent := h.mail.Sent(); len(sent) >= n {
			return sent
		}
		time.Sleep(10 * time.Millisecond)
	}
	h.t.Fatalf("expected %d emails, got %d", n, len(h.mail.Sent()))
	return nil
}

var codeRE = regexp.MustCompile(`\b(\d{6})\b`)

// signIn invites email and signs c in through the real code flow.
func (h *harness) signIn(c *http.Client, email string) {
	h.t.Helper()
	if err := h.st.invite(h.t.Context(), email); err != nil {
		h.t.Fatal(err)
	}
	before := len(h.mail.Sent())
	if s, _, _ := h.do(c, "POST", "/api/auth/code", map[string]string{"email": email}); s != http.StatusAccepted {
		h.t.Fatalf("code request: %d", s)
	}
	msg := h.waitMail(before + 1)[before]
	code := codeRE.FindString(msg.Subject)
	if s, body, _ := h.do(c, "POST", "/api/auth/verify", map[string]string{"email": email, "code": code}); s != http.StatusOK {
		h.t.Fatalf("verify: %d %v", s, body)
	}
}

func TestShellAndCaching(t *testing.T) {
	h := newHarness(t)
	c := h.client()

	res, err := c.Get(h.srv.URL + "/")
	if err != nil {
		t.Fatal(err)
	}
	body, _ := io.ReadAll(res.Body)
	res.Body.Close()
	if cc := res.Header.Get("Cache-Control"); !strings.Contains(cc, "no-cache") {
		t.Errorf("shell Cache-Control = %q, want no-cache", cc)
	}
	m := regexp.MustCompile(`/js/app\.js\?v=[0-9a-f]+`).Find(body)
	if m == nil {
		t.Fatal("shell does not reference a hashed app.js")
	}

	get := func(path string) http.Header {
		res, err := c.Get(h.srv.URL + path)
		if err != nil {
			t.Fatal(err)
		}
		res.Body.Close()
		if res.StatusCode != 200 {
			t.Fatalf("%s: %d", path, res.StatusCode)
		}
		return res.Header
	}
	if cc := get(string(m)).Get("Cache-Control"); !strings.Contains(cc, "immutable") {
		t.Errorf("hashed app.js Cache-Control = %q, want immutable", cc)
	}
	// Unversioned URLs: the modules app.js imports, the worker, and the
	// manifest. Each must revalidate, and needs an ETag to make that a 304.
	for _, path := range []string{"/js/game.js", "/sw.js", "/manifest.webmanifest", "/icons/icon-192.png"} {
		hdr := get(path)
		if cc := hdr.Get("Cache-Control"); cc != "no-cache" {
			t.Errorf("%s Cache-Control = %q, want no-cache", path, cc)
		}
		if hdr.Get("ETag") == "" {
			t.Errorf("%s has no ETag, so no-cache costs a full transfer", path)
		}
	}
	if !strings.Contains(get("/js/version.js").Get("Content-Type"), "javascript") {
		t.Error("version.js is not served as JavaScript")
	}
	if res, _ := c.Get(h.srv.URL + "/no-such-file"); res.StatusCode != http.StatusNotFound {
		t.Errorf("unknown path = %d, want 404", res.StatusCode)
	}
}

func TestAuthFlow(t *testing.T) {
	h := newHarness(t)
	c := h.client()

	if s, _, _ := h.do(c, "GET", "/api/me", nil); s != http.StatusUnauthorized {
		t.Fatalf("guest /api/me = %d, want 401", s)
	}

	// An uninvited address gets the same answer and no email.
	if s, _, _ := h.do(c, "POST", "/api/auth/code", map[string]string{"email": "stranger@example.com"}); s != http.StatusAccepted {
		t.Fatalf("uninvited code request = %d, want 202", s)
	}
	time.Sleep(50 * time.Millisecond)
	if n := len(h.mail.Sent()); n != 0 {
		t.Fatalf("uninvited address was sent %d emails", n)
	}

	h.signIn(c, "Player@Example.com")
	s, body, _ := h.do(c, "GET", "/api/me", nil)
	if s != http.StatusOK || body["email"] != "player@example.com" {
		t.Fatalf("/api/me = %d %v", s, body)
	}

	if s, _, _ := h.do(c, "POST", "/api/auth/logout", nil); s != http.StatusNoContent {
		t.Fatalf("logout = %d", s)
	}
	if s, _, _ := h.do(c, "GET", "/api/me", nil); s != http.StatusUnauthorized {
		t.Fatalf("after logout /api/me = %d, want 401", s)
	}
}

func TestCodeDiesAfterWrongGuesses(t *testing.T) {
	h := newHarness(t)
	c := h.client()
	email := "p@example.com"
	if err := h.st.invite(t.Context(), email); err != nil {
		t.Fatal(err)
	}
	h.do(c, "POST", "/api/auth/code", map[string]string{"email": email})
	code := codeRE.FindString(h.waitMail(1)[0].Subject)
	wrong := "000000"
	if code == wrong {
		wrong = "111111"
	}
	for range codeAttempts {
		if s, _, _ := h.do(c, "POST", "/api/auth/verify", map[string]string{"email": email, "code": wrong}); s != http.StatusUnauthorized {
			t.Fatalf("wrong code = %d, want 401", s)
		}
	}
	if s, _, _ := h.do(c, "POST", "/api/auth/verify", map[string]string{"email": email, "code": code}); s != http.StatusUnauthorized {
		t.Fatalf("right code after %d wrong guesses = %d, want 401", codeAttempts, s)
	}
}

func TestSyncConflict(t *testing.T) {
	h := newHarness(t)
	phone, laptop := h.client(), h.client()
	h.signIn(phone, "p@example.com")
	h.signIn(laptop, "p@example.com")

	id := "0b7f9a52-3c2e-4d39-9a3c-6f1c2f1b5e11"
	put := func(c *http.Client, base int, moves int) (int, map[string]any) {
		s, body, _ := h.do(c, "PUT", "/api/games/"+id, map[string]any{"base_rev": base, "doc": map[string]int{"moves": moves}})
		return s, body
	}

	if s, body := put(phone, 0, 1); s != 200 || body["rev"] != float64(1) {
		t.Fatalf("first put = %d %v", s, body)
	}
	// The laptop pulls revision 1.
	_, pulled, _ := h.do(laptop, "GET", "/api/sync?since=0", nil)
	if games := pulled["games"].([]any); len(games) != 1 {
		t.Fatalf("pull returned %d games", len(games))
	}
	cursor := int(pulled["cursor"].(float64))

	// Both play on from revision 1. The phone syncs first and wins.
	if s, _ := put(phone, 1, 2); s != 200 {
		t.Fatalf("phone put = %d", s)
	}
	s, body := put(laptop, 1, 5)
	if s != http.StatusConflict {
		t.Fatalf("stale laptop put = %d, want 409", s)
	}
	server := body["game"].(map[string]any)
	if server["rev"] != float64(2) || !strings.Contains(server["doc"].(string), `"moves":2`) {
		t.Fatalf("409 carries %v, want the phone's revision 2", server)
	}

	// The laptop's next pull sees exactly the phone's change.
	_, pulled, _ = h.do(laptop, "GET", "/api/sync?since="+itoa(cursor), nil)
	if games := pulled["games"].([]any); len(games) != 1 {
		t.Fatalf("incremental pull returned %d games, want 1", len(games))
	}

	// Another account can't see it.
	other := h.client()
	h.signIn(other, "q@example.com")
	_, pulled, _ = h.do(other, "GET", "/api/sync?since=0", nil)
	if games := pulled["games"].([]any); len(games) != 0 {
		t.Fatalf("other account sees %d games", len(games))
	}

	if s, _, _ := h.do(h.client(), "GET", "/api/sync", nil); s != http.StatusUnauthorized {
		t.Fatalf("guest sync = %d, want 401", s)
	}
	if s, b, _ := h.do(phone, "PUT", "/api/games/not-a-uuid", map[string]any{"base_rev": 0, "doc": 1}); s != http.StatusBadRequest {
		t.Fatalf("bad id = %d %v, want 400", s, b)
	}
}

func TestSettingsSync(t *testing.T) {
	h := newHarness(t)
	c := h.client()
	h.signIn(c, "p@example.com")
	doc := map[string]bool{"showMistakes": true}
	if s, body, _ := h.do(c, "PUT", "/api/settings", map[string]any{"base_rev": 0, "doc": doc}); s != 200 || body["rev"] != float64(1) {
		t.Fatalf("settings put = %d %v", s, body)
	}
	if s, _, _ := h.do(c, "PUT", "/api/settings", map[string]any{"base_rev": 0, "doc": doc}); s != http.StatusConflict {
		t.Fatalf("stale settings put = %d, want 409", s)
	}
	_, pulled, _ := h.do(c, "GET", "/api/sync?since=0", nil)
	if pulled["settings"] == nil {
		t.Fatal("sync did not return changed settings")
	}
}

func TestPuzzleAndHint(t *testing.T) {
	h := newHarness(t)
	c := h.client()
	s, p, _ := h.do(c, "GET", "/api/puzzle?level=medium", nil)
	if s != 200 || p["level"] != "medium" || len(p["givens"].(string)) != 81 || len(p["steps"].([]any)) == 0 {
		t.Fatalf("puzzle = %d %v", s, p)
	}
	if s, _, _ := h.do(c, "GET", "/api/puzzle?level=impossible", nil); s != http.StatusBadRequest {
		t.Fatalf("bad level = %d, want 400", s)
	}

	givens := p["givens"].(string)
	s, hint, _ := h.do(c, "POST", "/api/hint", map[string]string{"givens": givens, "board": givens})
	if s != 200 || len(hint["steps"].([]any)) == 0 {
		t.Fatalf("hint = %d %v", s, hint)
	}
	last := hint["steps"].([]any)
	if last[len(last)-1].(map[string]any)["place"] == nil {
		t.Fatal("hint does not end in a placement")
	}
}

func itoa(n int) string { return strconv.Itoa(n) }
