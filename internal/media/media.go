// Package media serves thumbnails and local files to the app window through
// Wails' asset server (no network port is opened).
package media

import (
	"context"
	"net/http"
	"os"
	"sync"
	"time"

	"dropboxuploader/internal/dbx"
	"dropboxuploader/internal/localfs"
)

type Handler struct {
	Thumbs *localfs.Thumbnailer
	Client func() *dbx.Client

	mu       sync.Mutex
	dbxCache map[string][]byte
	dbxOrder []string
	dbxSem   chan struct{}
}

func New(thumbs *localfs.Thumbnailer, client func() *dbx.Client) *Handler {
	return &Handler{Thumbs: thumbs, Client: client, dbxCache: map[string][]byte{}, dbxSem: make(chan struct{}, 4)}
}

func (h *Handler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	p := r.URL.Query().Get("p")
	switch r.URL.Path {
	case "/local/thumb":
		b, err := h.Thumbs.Thumb(p)
		if err != nil {
			http.Error(w, "no preview", http.StatusNotFound)
			return
		}
		writeJPEG(w, b)
	case "/local/file":
		// Full file for the preview overlay; ServeContent supports Range requests for video seeking.
		f, err := os.Open(p)
		if err != nil {
			http.NotFound(w, r)
			return
		}
		defer f.Close()
		st, err := f.Stat()
		if err != nil || st.IsDir() {
			http.NotFound(w, r)
			return
		}
		http.ServeContent(w, r, st.Name(), st.ModTime(), f)
	case "/dropbox/thumb":
		b, err := h.dropboxThumb(r.Context(), p)
		if err != nil {
			http.Error(w, "no preview", http.StatusNotFound)
			return
		}
		writeJPEG(w, b)
	default:
		http.NotFound(w, r)
	}
}

func writeJPEG(w http.ResponseWriter, b []byte) {
	w.Header().Set("Content-Type", "image/jpeg")
	w.Header().Set("Cache-Control", "max-age=3600")
	_, _ = w.Write(b)
}

func (h *Handler) dropboxThumb(ctx context.Context, path string) ([]byte, error) {
	h.mu.Lock()
	if b, ok := h.dbxCache[path]; ok {
		h.mu.Unlock()
		return b, nil
	}
	h.mu.Unlock()
	c := h.Client()
	if c == nil {
		return nil, dbx.ErrNotLoggedIn
	}
	select {
	case h.dbxSem <- struct{}{}:
	case <-ctx.Done():
		return nil, ctx.Err()
	}
	defer func() { <-h.dbxSem }()
	ctx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	b, err := c.Thumbnail(ctx, path)
	if err != nil {
		return nil, err
	}
	h.mu.Lock()
	defer h.mu.Unlock()
	h.dbxCache[path] = b
	h.dbxOrder = append(h.dbxOrder, path)
	if len(h.dbxOrder) > 1000 {
		delete(h.dbxCache, h.dbxOrder[0])
		h.dbxOrder = h.dbxOrder[1:]
	}
	return b, nil
}
