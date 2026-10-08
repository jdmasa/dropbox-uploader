package dbx

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"strings"
	"time"
)

type Account struct {
	Name  string
	Email string
}

func (c *Client) CurrentAccount(ctx context.Context) (*Account, error) {
	var out struct {
		Name struct {
			DisplayName string `json:"display_name"`
		} `json:"name"`
		Email string `json:"email"`
	}
	if err := c.rpc(ctx, "users/get_current_account", nil, &out); err != nil {
		return nil, err
	}
	return &Account{Name: out.Name.DisplayName, Email: out.Email}, nil
}

// Entry is a file or folder in Dropbox.
type Entry struct {
	Tag            string    `json:".tag"` // "file" | "folder" | "deleted"
	Name           string    `json:"name"`
	PathLower      string    `json:"path_lower"`
	PathDisplay    string    `json:"path_display"`
	ID             string    `json:"id"`
	Size           int64     `json:"size"`
	ContentHash    string    `json:"content_hash"`
	ServerModified time.Time `json:"server_modified"`
}

func (e *Entry) IsFolder() bool { return e.Tag == "folder" }

// ListFolder returns every entry in folder ("" is the Dropbox root).
func (c *Client) ListFolder(ctx context.Context, folder string) ([]Entry, error) {
	if folder == "/" {
		folder = ""
	}
	type page struct {
		Entries []Entry `json:"entries"`
		Cursor  string  `json:"cursor"`
		HasMore bool    `json:"has_more"`
	}
	var all []Entry
	var p page
	err := Retry(ctx, 4, func() error {
		return c.rpc(ctx, "files/list_folder", map[string]any{"path": folder, "limit": 2000}, &p)
	})
	for err == nil {
		all = append(all, p.Entries...)
		if !p.HasMore {
			break
		}
		cursor := p.Cursor
		p = page{}
		err = Retry(ctx, 4, func() error {
			return c.rpc(ctx, "files/list_folder/continue", map[string]any{"cursor": cursor}, &p)
		})
	}
	return all, err
}

// CreateFolder creates path. An existing folder at path is not an error.
func (c *Client) CreateFolder(ctx context.Context, path string) error {
	err := Retry(ctx, 4, func() error {
		return c.rpc(ctx, "files/create_folder_v2", map[string]any{"path": path, "autorename": false}, nil)
	})
	var ae *APIError
	if errors.As(err, &ae) && strings.Contains(ae.Summary, "path/conflict/folder") {
		return nil
	}
	return err
}

// Metadata returns the entry at path, or nil if nothing exists there.
func (c *Client) Metadata(ctx context.Context, path string) (*Entry, error) {
	var e Entry
	err := Retry(ctx, 4, func() error {
		return c.rpc(ctx, "files/get_metadata", map[string]any{"path": path}, &e)
	})
	var ae *APIError
	if errors.As(err, &ae) && strings.Contains(ae.Summary, "not_found") {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	return &e, nil
}

// Thumbnail returns a JPEG thumbnail (about 256px) of an image or video in Dropbox.
func (c *Client) Thumbnail(ctx context.Context, path string) ([]byte, error) {
	return c.download(ctx, "files/get_thumbnail_v2", map[string]any{
		"resource": map[string]any{".tag": "path", "path": path},
		"format":   "jpeg",
		"size":     "w256h256",
		"mode":     "bestfit",
	})
}

// ---- Upload sessions ----

type Cursor struct {
	SessionID string `json:"session_id"`
	Offset    int64  `json:"offset"`
}

// StartSession opens an upload session with the first chunk. close=true means data is the whole file.
func (c *Client) StartSession(ctx context.Context, data []byte, close bool, progress ProgressFunc) (string, error) {
	var out struct {
		SessionID string `json:"session_id"`
	}
	err := c.upload(ctx, "files/upload_session/start", map[string]any{"close": close}, data, progress, &out)
	return out.SessionID, err
}

// AppendSession uploads the next chunk at cur.Offset.
func (c *Client) AppendSession(ctx context.Context, cur Cursor, data []byte, close bool, progress ProgressFunc) error {
	return c.upload(ctx, "files/upload_session/append_v2", map[string]any{"cursor": cur, "close": close}, data, progress, nil)
}

// IncorrectOffset extracts the offset Dropbox expects when an append was sent at the wrong position.
func IncorrectOffset(err error) (int64, bool) {
	var ae *APIError
	if !errors.As(err, &ae) || !strings.Contains(ae.Summary, "incorrect_offset") {
		return 0, false
	}
	var body struct {
		CorrectOffset *int64 `json:"correct_offset"`
	}
	if json.Unmarshal(ae.Raw, &body) == nil && body.CorrectOffset != nil {
		return *body.CorrectOffset, true
	}
	return 0, false
}

// SessionGone reports whether the upload session no longer exists (expired or already used).
func SessionGone(err error) bool {
	var ae *APIError
	return errors.As(err, &ae) && (strings.Contains(ae.Summary, "not_found") ||
		strings.Contains(ae.Summary, "closed") || strings.Contains(ae.Summary, "not_closed"))
}

type Commit struct {
	Path           string    `json:"path"`
	ClientModified time.Time `json:"-"`
}

type FinishEntry struct {
	Cursor Cursor
	Commit Commit
}

type FinishResult struct {
	Entry *Entry
	Err   error
}

// FinishBatch commits up to 1000 closed upload sessions in one call, which avoids the
// "too_many_write_operations" lock contention of finishing files one at a time.
// Name clashes are auto-renamed ("photo (1).jpg").
func (c *Client) FinishBatch(ctx context.Context, entries []FinishEntry) ([]FinishResult, error) {
	type commitInfo struct {
		Path           string `json:"path"`
		Mode           string `json:"mode"`
		Autorename     bool   `json:"autorename"`
		ClientModified string `json:"client_modified,omitempty"`
	}
	type entry struct {
		Cursor Cursor     `json:"cursor"`
		Commit commitInfo `json:"commit"`
	}
	arg := struct {
		Entries []entry `json:"entries"`
	}{}
	for _, e := range entries {
		ci := commitInfo{Path: e.Commit.Path, Mode: "add", Autorename: true}
		if !e.Commit.ClientModified.IsZero() {
			ci.ClientModified = e.Commit.ClientModified.UTC().Truncate(time.Second).Format("2006-01-02T15:04:05Z")
		}
		arg.Entries = append(arg.Entries, entry{Cursor: e.Cursor, Commit: ci})
	}
	var out struct {
		Entries []json.RawMessage `json:"entries"`
	}
	if err := c.rpc(ctx, "files/upload_session/finish_batch_v2", arg, &out); err != nil {
		return nil, err
	}
	res := make([]FinishResult, len(entries))
	for i := range res {
		if i >= len(out.Entries) {
			res[i].Err = errors.New("missing result from Dropbox")
			continue
		}
		var head struct {
			Tag     string          `json:".tag"`
			Failure json.RawMessage `json:"failure"`
		}
		raw := out.Entries[i]
		_ = json.Unmarshal(raw, &head)
		if head.Tag == "success" {
			var e Entry
			_ = json.Unmarshal(raw, &e)
			e.Tag = "file"
			res[i].Entry = &e
			continue
		}
		res[i].Err = &APIError{Status: 409, Summary: failureSummary(head.Failure), Raw: head.Failure}
	}
	return res, nil
}

// failureSummary turns a nested union like {".tag":"path","path":{".tag":"insufficient_space"}}
// into "path/insufficient_space".
func failureSummary(raw json.RawMessage) string {
	var parts []string
	for len(raw) > 0 && len(parts) < 5 {
		var m map[string]json.RawMessage
		if json.Unmarshal(raw, &m) != nil {
			break
		}
		var tag string
		if json.Unmarshal(m[".tag"], &tag) != nil || tag == "" {
			break
		}
		parts = append(parts, tag)
		raw = m[tag]
	}
	if len(parts) == 0 {
		return string(bytes.TrimSpace(raw))
	}
	return strings.Join(parts, "/")
}
