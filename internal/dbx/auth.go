package dbx

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"time"

	"dropboxuploader/internal/i18n"
)

const (
	// RedirectPort must match the redirect URI registered in the Dropbox App Console:
	// http://localhost:53682/callback
	RedirectPort = 53682
	RedirectURI  = "http://localhost:53682/callback"

	authorizeURL = "https://www.dropbox.com/oauth2/authorize"
	tokenURL     = "https://api.dropboxapi.com/oauth2/token"
)

// Tokens keeps a long-lived refresh token and a cached short-lived access token.
type Tokens struct {
	AppKey       string
	RefreshToken string

	mu      sync.Mutex
	access  string
	expires time.Time
	http    *http.Client
}

func NewTokens(appKey, refreshToken string) *Tokens {
	return &Tokens{AppKey: appKey, RefreshToken: refreshToken, http: &http.Client{Timeout: 60 * time.Second}}
}

// Access returns a valid access token, refreshing it when it is about to expire.
func (t *Tokens) Access(ctx context.Context) (string, error) {
	t.mu.Lock()
	defer t.mu.Unlock()
	if t.access != "" && time.Until(t.expires) > 2*time.Minute {
		return t.access, nil
	}
	if t.RefreshToken == "" {
		return "", ErrNotLoggedIn
	}
	form := url.Values{
		"grant_type":    {"refresh_token"},
		"refresh_token": {t.RefreshToken},
		"client_id":     {t.AppKey},
	}
	tr, err := postToken(ctx, t.http, form)
	if err != nil {
		return "", err
	}
	t.access = tr.AccessToken
	t.expires = time.Now().Add(time.Duration(tr.ExpiresIn) * time.Second)
	return t.access, nil
}

// Invalidate forgets the cached access token so the next call refreshes it.
func (t *Tokens) Invalidate() {
	t.mu.Lock()
	t.access = ""
	t.mu.Unlock()
}

// ErrNotLoggedIn and the other "err.*" messages are translation keys; the UI
// shows them in the user's language ("err.key|detail" carries a parameter).
var ErrNotLoggedIn = errors.New("err.notConnected")

type tokenResponse struct {
	AccessToken  string `json:"access_token"`
	RefreshToken string `json:"refresh_token"`
	ExpiresIn    int    `json:"expires_in"`
	AccountID    string `json:"account_id"`
	Error        string `json:"error"`
	ErrorDesc    string `json:"error_description"`
}

func postToken(ctx context.Context, hc *http.Client, form url.Values) (*tokenResponse, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, tokenURL, strings.NewReader(form.Encode()))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	resp, err := hc.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	var tr tokenResponse
	if err := json.NewDecoder(resp.Body).Decode(&tr); err != nil {
		return nil, fmt.Errorf("token response: %w", err)
	}
	if resp.StatusCode != http.StatusOK || tr.AccessToken == "" {
		if tr.Error == "invalid_grant" {
			return nil, ErrNotLoggedIn
		}
		return nil, fmt.Errorf("err.loginFailed|%s %s", tr.Error, tr.ErrorDesc)
	}
	return &tr, nil
}

// Login runs the OAuth2 PKCE flow: it starts a temporary listener on localhost,
// calls openURL with Dropbox's sign-in page and waits for the redirect.
// It returns the refresh token.
func Login(ctx context.Context, appKey, lang string, openURL func(string)) (string, error) {
	verifier := randomString(64)
	sum := sha256.Sum256([]byte(verifier))
	challenge := base64.RawURLEncoding.EncodeToString(sum[:])
	state := randomString(24)

	type result struct {
		code string
		err  error
	}
	done := make(chan result, 1)
	mux := http.NewServeMux()
	mux.HandleFunc("/callback", func(w http.ResponseWriter, r *http.Request) {
		q := r.URL.Query()
		var res result
		switch {
		case q.Get("state") != state:
			res.err = errors.New("err.loginFailed|state mismatch")
		case q.Get("error") != "":
			res.err = errors.New("err.loginDenied")
		default:
			res.code = q.Get("code")
		}
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		if res.err != nil {
			fmt.Fprint(w, loginPage(lang, i18n.T(lang, "login.errTitle"), i18n.T(lang, "login.errText")))
		} else {
			fmt.Fprint(w, loginPage(lang, i18n.T(lang, "login.okTitle"), i18n.T(lang, "login.okText")))
		}
		select {
		case done <- res:
		default:
		}
	})
	srv := &http.Server{Handler: mux, ReadHeaderTimeout: 10 * time.Second}
	// Browsers may resolve "localhost" to IPv4 or IPv6, so listen on both.
	var listeners []net.Listener
	for _, addr := range []string{"127.0.0.1", "[::1]"} {
		if l, err := net.Listen("tcp", fmt.Sprintf("%s:%d", addr, RedirectPort)); err == nil {
			listeners = append(listeners, l)
			go srv.Serve(l)
		}
	}
	if len(listeners) == 0 {
		return "", fmt.Errorf("err.loginPort|%d", RedirectPort)
	}
	defer srv.Close()

	q := url.Values{
		"client_id":             {appKey},
		"response_type":         {"code"},
		"code_challenge":        {challenge},
		"code_challenge_method": {"S256"},
		"token_access_type":     {"offline"},
		"redirect_uri":          {RedirectURI},
		"state":                 {state},
	}
	openURL(authorizeURL + "?" + q.Encode())

	var res result
	select {
	case res = <-done:
	case <-ctx.Done():
		return "", ctx.Err()
	case <-time.After(10 * time.Minute):
		return "", errors.New("err.loginTimeout")
	}
	if res.err != nil {
		return "", res.err
	}
	tr, err := postToken(ctx, &http.Client{Timeout: 60 * time.Second}, url.Values{
		"grant_type":    {"authorization_code"},
		"code":          {res.code},
		"code_verifier": {verifier},
		"client_id":     {appKey},
		"redirect_uri":  {RedirectURI},
	})
	if err != nil {
		return "", err
	}
	if tr.RefreshToken == "" {
		return "", errors.New("err.loginFailed|no refresh token")
	}
	return tr.RefreshToken, nil
}

func randomString(n int) string {
	b := make([]byte, n)
	_, _ = rand.Read(b)
	return base64.RawURLEncoding.EncodeToString(b)[:n]
}

func loginPage(lang, title, msg string) string {
	return `<!doctype html><html lang="` + lang + `"><head><meta charset="utf-8"><title>Dropbox Uploader</title>
<style>body{font-family:Segoe UI,system-ui,sans-serif;display:flex;align-items:center;justify-content:center;height:100vh;margin:0;background:#f4f6fa;color:#1d2433}
.card{background:#fff;padding:40px 56px;border-radius:12px;box-shadow:0 4px 24px rgba(0,0,0,.08);text-align:center}h1{font-size:28px;margin:0 0 12px}p{font-size:18px;margin:0}</style>
</head><body><div class="card"><h1>` + title + `</h1><p>` + msg + `</p></div></body></html>`
}
