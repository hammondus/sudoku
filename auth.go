package main

import (
	"context"
	"crypto/rand"
	"encoding/base64"
	"errors"
	"fmt"
	"log/slog"
	"math/big"
	"net/http"
	"net/mail"
	"time"

	"github.com/hammondus/mailer"
	"github.com/hammondus/nitrokit"
)

// Sign-in is by emailed code, for invited addresses only. A code rather than
// a link: on iOS a link in Mail opens Safari, not the installed PWA, so the
// session would land in a browser the player isn't using. A typed code signs
// in whichever window asked for it.

const (
	sessionCookie = "sudoku_session"
	// A year, fixed. Players sign in once per device; a game is not worth a
	// sliding window's extra write on every request.
	sessionTTL = 365 * 24 * time.Hour

	codeTTL         = 10 * time.Minute
	codeAttempts    = 5               // wrong guesses before a code dies
	codeResendAfter = 1 * time.Minute // per account, between emails
	sendTimeout     = 30 * time.Second
)

type auth struct {
	st     *store
	mail   mailer.Sender
	log    *slog.Logger
	trust  *nitrokit.ProxyTrust
	secure bool // Secure cookie flag; off only for -dev over plain HTTP

	// codeLimit caps code requests per client address, so one address can't
	// make the server send mail to every invited player in turn.
	codeLimit *nitrokit.Limiter
	// verifyFails locks out an address that guesses wrong too often, across
	// accounts; codeAttempts covers the per-code side.
	verifyFails *nitrokit.FailLimiter
}

func newAuth(st *store, m mailer.Sender, log *slog.Logger, trust *nitrokit.ProxyTrust, secure bool) *auth {
	return &auth{
		st: st, mail: m, log: log, trust: trust, secure: secure,
		codeLimit:   nitrokit.NewLimiter(1.0/30, 5), // 5 at once, then one per 30 s
		verifyFails: nitrokit.NewFailLimiter(10, 15*time.Minute),
	}
}

func (a *auth) ip(r *http.Request) string { return a.trust.ClientIP(r).String() }

// handleCode emails a sign-in code. The response is the same whether or not
// the address is invited, and the email goes out after the response, so
// neither the body nor the timing tells a stranger who has an account.
func (a *auth) handleCode(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Email string `json:"email"`
	}
	if !nitrokit.ReadJSON(w, r, &req, 4<<10) {
		return
	}
	email := normEmail(req.Email)
	if _, err := mail.ParseAddress(email); err != nil {
		nitrokit.JSONError(w, http.StatusBadRequest, "Enter a valid email address.")
		return
	}
	if ok, retry := a.codeLimit.Allow(a.ip(r)); !ok {
		tooMany(w, retry)
		return
	}

	nitrokit.NoCachePrivate(w)
	w.WriteHeader(http.StatusAccepted)

	userID, found, err := a.st.userByEmail(r.Context(), email)
	if err != nil {
		a.log.Error("code: look up user", "err", err)
		return
	}
	if !found {
		a.log.Info("code: address not invited", "email", email)
		return
	}
	last, err := a.st.lastCodeSent(r.Context(), userID)
	if err != nil {
		a.log.Error("code: last sent", "err", err)
		return
	}
	if time.Since(last) < codeResendAfter {
		return
	}
	code, err := newCode()
	if err != nil {
		a.log.Error("code: generate", "err", err)
		return
	}
	if err := a.st.putCode(r.Context(), userID, code, codeTTL); err != nil {
		a.log.Error("code: store", "err", err)
		return
	}
	// Detached from the request: the response is already sent, and a slow
	// SMTP server must not hold the connection open.
	go func() {
		ctx, cancel := context.WithTimeout(context.Background(), sendTimeout)
		defer cancel()
		if err := a.mail.Send(ctx, codeMessage(email, code)); err != nil {
			a.log.Error("code: send", "email", email, "err", err)
		}
	}()
}

func codeMessage(to, code string) *mailer.Message {
	text := fmt.Sprintf(`Your Sudoku sign-in code is %s.

To sign in, enter this code in the app. The code expires in %d minutes and works once.

If you didn't ask to sign in, ignore this email. Nobody can sign in without the code.
`, code, int(codeTTL.Minutes()))
	return &mailer.Message{
		To:      []string{to},
		Subject: "Your Sudoku sign-in code: " + code,
		Text:    text,
	}
}

// newCode returns six random decimal digits.
func newCode() (string, error) {
	n, err := rand.Int(rand.Reader, big.NewInt(1_000_000))
	if err != nil {
		return "", err
	}
	return fmt.Sprintf("%06d", n.Int64()), nil
}

func (a *auth) handleVerify(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Email string `json:"email"`
		Code  string `json:"code"`
	}
	if !nitrokit.ReadJSON(w, r, &req, 4<<10) {
		return
	}
	ip := a.ip(r)
	if a.verifyFails.Blocked(ip) {
		nitrokit.JSONError(w, http.StatusTooManyRequests, "Too many wrong codes. Wait 15 minutes, then try again.")
		return
	}
	userID, found, err := a.st.userByEmail(r.Context(), normEmail(req.Email))
	if err != nil {
		internalError(w, a.log, "verify: look up user", err)
		return
	}
	if found {
		err = a.st.useCode(r.Context(), userID, req.Code, codeAttempts)
	}
	if !found || errors.Is(err, errCodeInvalid) {
		a.verifyFails.Fail(ip)
		nitrokit.JSONError(w, http.StatusUnauthorized, "That code is wrong or has expired. Check the email, or ask for a new code.")
		return
	}
	if err != nil {
		internalError(w, a.log, "verify: use code", err)
		return
	}
	a.verifyFails.Pass(ip)

	token, err := newToken()
	if err != nil {
		internalError(w, a.log, "verify: token", err)
		return
	}
	if err := a.st.createSession(r.Context(), userID, token, sessionTTL); err != nil {
		internalError(w, a.log, "verify: session", err)
		return
	}
	http.SetCookie(w, &http.Cookie{
		Name:     sessionCookie,
		Value:    token,
		Path:     "/",
		MaxAge:   int(sessionTTL.Seconds()),
		HttpOnly: true,
		Secure:   a.secure,
		// Lax, plus the JSON-only API, is the CSRF defence: a cross-site
		// form can't send application/json, and a cross-site fetch that
		// could would need a CORS preflight this server never answers.
		SameSite: http.SameSiteLaxMode,
	})
	nitrokit.NoStore(w) // the response sets a credential
	nitrokit.WriteJSON(w, http.StatusOK, map[string]string{"email": normEmail(req.Email)})
}

func (a *auth) handleLogout(w http.ResponseWriter, r *http.Request) {
	if c, err := r.Cookie(sessionCookie); err == nil {
		if err := a.st.deleteSession(r.Context(), c.Value); err != nil {
			a.log.Error("logout", "err", err)
		}
	}
	http.SetCookie(w, &http.Cookie{
		Name: sessionCookie, Value: "", Path: "/", MaxAge: -1,
		HttpOnly: true, Secure: a.secure, SameSite: http.SameSiteLaxMode,
	})
	w.WriteHeader(http.StatusNoContent)
}

// handleMe tells the client who is signed in, or 401 for a guest.
func (a *auth) handleMe(w http.ResponseWriter, r *http.Request, userID int64) {
	email, err := a.st.emailOf(r.Context(), userID)
	if err != nil {
		internalError(w, a.log, "me", err)
		return
	}
	nitrokit.NoCachePrivate(w)
	nitrokit.WriteJSON(w, http.StatusOK, map[string]string{"email": email})
}

// user wraps a handler that needs a signed-in player. A missing, unknown,
// or expired session is a 401, which the client reads as "you're a guest".
func (a *auth) user(next func(http.ResponseWriter, *http.Request, int64)) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		c, err := r.Cookie(sessionCookie)
		if err != nil {
			nitrokit.JSONError(w, http.StatusUnauthorized, "Sign in to sync.")
			return
		}
		userID, ok, err := a.st.sessionUser(r.Context(), c.Value)
		if err != nil {
			internalError(w, a.log, "session lookup", err)
			return
		}
		if !ok {
			nitrokit.JSONError(w, http.StatusUnauthorized, "Sign in to sync.")
			return
		}
		next(w, r, userID)
	}
}

func newToken() (string, error) {
	b := make([]byte, 32)
	if _, err := rand.Read(b); err != nil {
		return "", err
	}
	return base64.RawURLEncoding.EncodeToString(b), nil
}

func tooMany(w http.ResponseWriter, retry time.Duration) {
	w.Header().Set("Retry-After", fmt.Sprint(int(retry.Seconds())+1))
	nitrokit.JSONError(w, http.StatusTooManyRequests, "Too many requests. Wait a minute, then try again.")
}

func internalError(w http.ResponseWriter, log *slog.Logger, what string, err error) {
	log.Error(what, "err", err)
	nitrokit.JSONError(w, http.StatusInternalServerError, "Something went wrong on the server. Try again.")
}
