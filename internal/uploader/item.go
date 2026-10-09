package uploader

import (
	"context"
	"errors"
	"io/fs"
	"os"
	"strings"
	"time"

	"dropboxuploader/internal/dbx"
)

type Status string

const (
	Queued    Status = "queued"
	Checking  Status = "checking"
	Uploading Status = "uploading"
	Finishing Status = "finishing"
	Done      Status = "done"
	Skipped   Status = "skipped"
	Failed    Status = "failed"
)

func (s Status) active() bool { return s == Checking || s == Uploading || s == Finishing }

// Item is one file in the upload queue. Exported fields are sent to the UI and saved to disk.
type Item struct {
	ID      int64     `json:"id"`
	Local   string    `json:"local"`
	Dest    string    `json:"dest"` // full Dropbox path, e.g. /Photos/2024/IMG_0001.jpg
	Name    string    `json:"name"`
	Size    int64     `json:"size"`
	ModTime time.Time `json:"modTime"`

	Status     Status  `json:"status"`
	Sent       int64   `json:"sent"`
	Speed      float64 `json:"speed"` // bytes/s, only while uploading
	Error      string  `json:"error,omitempty"`
	ResultPath string  `json:"resultPath,omitempty"`

	// Upload session state for resuming.
	SessionID      string    `json:"sessionId,omitempty"`
	Offset         int64     `json:"offset,omitempty"` // bytes confirmed by Dropbox
	SessionStarted time.Time `json:"sessionStarted,omitempty"`

	lastSent int64
}

func (it *Item) resetSession() {
	it.SessionID = ""
	it.Offset = 0
	it.SessionStarted = time.Time{}
}

// friendlyError turns technical errors into translation keys ("err.code" or
// "err.code|detail") that the queue panel shows in the user's language.
func friendlyError(err error) string {
	if err == nil {
		return ""
	}
	if errors.Is(err, dbx.ErrNotLoggedIn) {
		return "err.notConnected"
	}
	if errors.Is(err, fs.ErrNotExist) {
		return "err.fileGone"
	}
	if errors.Is(err, fs.ErrPermission) {
		return "err.permission"
	}
	var pe *os.PathError
	if errors.As(err, &pe) {
		return "err.readFile|" + pe.Err.Error()
	}
	var ae *dbx.APIError
	if errors.As(err, &ae) {
		s := ae.Summary
		switch {
		case strings.Contains(s, "insufficient_space"):
			return "err.dropboxFull"
		case strings.Contains(s, "disallowed_name"), strings.Contains(s, "malformed_path"):
			return "err.badName"
		case strings.Contains(s, "too_large"):
			return "err.tooLarge"
		case strings.Contains(s, "no_write_permission"):
			return "err.noWritePermission"
		case strings.Contains(s, "too_many_write_operations"), ae.Status == 429:
			return "err.dropboxBusy"
		case ae.Status >= 500:
			return "err.dropboxDown"
		}
		return "err.dropbox|" + s
	}
	if errors.Is(err, context.DeadlineExceeded) || dbx.Retryable(err) {
		return "err.network"
	}
	return err.Error()
}
