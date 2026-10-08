// Package dbx is a small Dropbox API v2 client covering only what the uploader needs.
package dbx

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math/rand"
	"net/http"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"
)

const (
	apiHost     = "https://api.dropboxapi.com/2/"
	contentHost = "https://content.dropboxapi.com/2/"
)

type Client struct {
	Tokens *Tokens
	http   *http.Client
}

func New(t *Tokens) *Client {
	return &Client{
		Tokens: t,
		// No overall timeout: chunk uploads can be slow. Requests are bounded by
		// their context and the transport's idle/handshake timeouts.
		http: &http.Client{Transport: &http.Transport{
			Proxy:                 http.ProxyFromEnvironment,
			TLSHandshakeTimeout:   20 * time.Second,
			ResponseHeaderTimeout: 3 * time.Minute,
			IdleConnTimeout:       90 * time.Second,
			MaxIdleConnsPerHost:   16,
		}},
	}
}

// APIError is an error response from Dropbox.
type APIError struct {
	Status     int
	Summary    string
	Raw        json.RawMessage
	RetryAfter time.Duration
}

func (e *APIError) Error() string {
	if e.Summary != "" {
		return e.Summary
	}
	return fmt.Sprintf("Dropbox error (HTTP %d)", e.Status)
}

// Retryable reports whether err is worth retrying (network failures, rate limits, server errors).
func Retryable(err error) bool {
	if err == nil || errors.Is(err, context.Canceled) || errors.Is(err, ErrNotLoggedIn) {
		return false
	}
	var ae *APIError
	if errors.As(err, &ae) {
		if ae.Status == 429 || ae.Status >= 500 {
			return true
		}
		return strings.Contains(ae.Summary, "too_many_write_operations") ||
			strings.Contains(ae.Summary, "internal_error")
	}
	// Anything else is a transport-level failure (timeout, reset, DNS...).
	return true
}

// Backoff returns how long to wait before retry number attempt (0-based).
func Backoff(err error, attempt int) time.Duration {
	var ae *APIError
	if errors.As(err, &ae) && ae.RetryAfter > 0 {
		return ae.RetryAfter
	}
	d := time.Second << attempt // 1s, 2s, 4s, 8s, 16s...
	if d > 60*time.Second {
		d = 60 * time.Second
	}
	return d/2 + time.Duration(rand.Int63n(int64(d/2)+1))
}

// Retry calls fn until it succeeds, fails with a non-retryable error or maxAttempts is reached.
func Retry(ctx context.Context, maxAttempts int, fn func() error) error {
	var err error
	for attempt := 0; attempt < maxAttempts; attempt++ {
		if err = fn(); err == nil || !Retryable(err) {
			return err
		}
		if attempt == maxAttempts-1 {
			break
		}
		select {
		case <-time.After(Backoff(err, attempt)):
		case <-ctx.Done():
			return ctx.Err()
		}
	}
	return err
}

// ProgressFunc receives the number of bytes just written to the network.
type ProgressFunc func(n int64)

// rpc calls an endpoint on api.dropboxapi.com with a JSON body.
func (c *Client) rpc(ctx context.Context, endpoint string, arg, out any) error {
	body := []byte("null")
	if arg != nil {
		var err error
		if body, err = json.Marshal(arg); err != nil {
			return err
		}
	}
	return c.do(ctx, func(token string) (*http.Request, error) {
		req, err := http.NewRequestWithContext(ctx, http.MethodPost, apiHost+endpoint, bytes.NewReader(body))
		if err != nil {
			return nil, err
		}
		req.Header.Set("Authorization", "Bearer "+token)
		req.Header.Set("Content-Type", "application/json")
		return req, nil
	}, func(resp *http.Response) error {
		if out == nil {
			return nil
		}
		return json.NewDecoder(resp.Body).Decode(out)
	})
}

// upload calls a content-upload endpoint (argument in the Dropbox-API-Arg header).
func (c *Client) upload(ctx context.Context, endpoint string, arg any, data []byte, progress ProgressFunc, out any) error {
	argJSON, err := headerJSON(arg)
	if err != nil {
		return err
	}
	return c.do(ctx, func(token string) (*http.Request, error) {
		var r io.Reader = bytes.NewReader(data)
		if progress != nil {
			r = &countingReader{r: r, fn: progress}
		}
		req, err := http.NewRequestWithContext(ctx, http.MethodPost, contentHost+endpoint, r)
		if err != nil {
			return nil, err
		}
		req.ContentLength = int64(len(data))
		req.Header.Set("Authorization", "Bearer "+token)
		req.Header.Set("Content-Type", "application/octet-stream")
		req.Header.Set("Dropbox-API-Arg", argJSON)
		return req, nil
	}, func(resp *http.Response) error {
		if out == nil {
			return nil
		}
		return json.NewDecoder(resp.Body).Decode(out)
	})
}

// download calls a content-download endpoint and returns the response body bytes.
func (c *Client) download(ctx context.Context, endpoint string, arg any) ([]byte, error) {
	argJSON, err := headerJSON(arg)
	if err != nil {
		return nil, err
	}
	var data []byte
	err = c.do(ctx, func(token string) (*http.Request, error) {
		req, err := http.NewRequestWithContext(ctx, http.MethodPost, contentHost+endpoint, nil)
		if err != nil {
			return nil, err
		}
		req.Header.Set("Authorization", "Bearer "+token)
		req.Header.Set("Dropbox-API-Arg", argJSON)
		return req, nil
	}, func(resp *http.Response) error {
		var err error
		data, err = io.ReadAll(resp.Body)
		return err
	})
	return data, err
}

// do sends a request, refreshing the access token once if Dropbox says it expired.
func (c *Client) do(ctx context.Context, build func(token string) (*http.Request, error), handle func(*http.Response) error) error {
	for attempt := 0; ; attempt++ {
		token, err := c.Tokens.Access(ctx)
		if err != nil {
			return err
		}
		req, err := build(token)
		if err != nil {
			return err
		}
		resp, err := c.http.Do(req)
		if err != nil {
			return err
		}
		if resp.StatusCode == http.StatusOK {
			err = handle(resp)
			resp.Body.Close()
			return err
		}
		apiErr := readError(resp)
		resp.Body.Close()
		if resp.StatusCode == http.StatusUnauthorized && attempt == 0 && strings.Contains(apiErr.Summary, "expired_access_token") {
			c.Tokens.Invalidate()
			continue
		}
		if resp.StatusCode == http.StatusUnauthorized {
			return fmt.Errorf("%w (%s)", ErrNotLoggedIn, apiErr.Summary)
		}
		return apiErr
	}
}

func readError(resp *http.Response) *APIError {
	b, _ := io.ReadAll(io.LimitReader(resp.Body, 64<<10))
	e := &APIError{Status: resp.StatusCode}
	var parsed struct {
		Summary string          `json:"error_summary"`
		Error   json.RawMessage `json:"error"`
	}
	if json.Unmarshal(b, &parsed) == nil && parsed.Summary != "" {
		e.Summary = parsed.Summary
		e.Raw = parsed.Error
	} else {
		e.Summary = strings.TrimSpace(string(b))
	}
	if ra := resp.Header.Get("Retry-After"); ra != "" {
		if secs, err := strconv.Atoi(ra); err == nil {
			e.RetryAfter = time.Duration(secs) * time.Second
		}
	}
	return e
}

// headerJSON marshals v for the Dropbox-API-Arg header, which must be ASCII:
// non-ASCII characters (é, ñ, emoji...) are escaped as \uXXXX.
func headerJSON(v any) (string, error) {
	b, err := json.Marshal(v)
	if err != nil {
		return "", err
	}
	var sb strings.Builder
	for len(b) > 0 {
		r, size := utf8.DecodeRune(b)
		b = b[size:]
		if r < 0x80 {
			sb.WriteRune(r)
			continue
		}
		if r > 0xFFFF {
			r -= 0x10000
			fmt.Fprintf(&sb, "\\u%04x\\u%04x", 0xD800+(r>>10), 0xDC00+(r&0x3FF))
			continue
		}
		fmt.Fprintf(&sb, "\\u%04x", r)
	}
	return sb.String(), nil
}

type countingReader struct {
	r  io.Reader
	fn ProgressFunc
}

func (c *countingReader) Read(p []byte) (int, error) {
	n, err := c.r.Read(p)
	if n > 0 {
		c.fn(int64(n))
	}
	return n, err
}
