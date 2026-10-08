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

// friendlyError turns technical errors into short messages for the queue panel.
func friendlyError(err error) string {
	if err == nil {
		return ""
	}
	if errors.Is(err, dbx.ErrNotLoggedIn) {
		return "Not connected to Dropbox. Please reconnect and press Retry."
	}
	if errors.Is(err, fs.ErrNotExist) {
		return "The file is no longer on this computer."
	}
	if errors.Is(err, fs.ErrPermission) {
		return "Windows did not allow reading this file."
	}
	var pe *os.PathError
	if errors.As(err, &pe) {
		return "Could not read the file: " + pe.Err.Error()
	}
	var ae *dbx.APIError
	if errors.As(err, &ae) {
		s := ae.Summary
		switch {
		case strings.Contains(s, "insufficient_space"):
			return "Your Dropbox is full."
		case strings.Contains(s, "disallowed_name"), strings.Contains(s, "malformed_path"):
			return "Dropbox does not accept this file name."
		case strings.Contains(s, "too_large"):
			return "The file is too large for Dropbox."
		case strings.Contains(s, "no_write_permission"):
			return "You don't have permission to upload into this Dropbox folder."
		case strings.Contains(s, "too_many_write_operations"), ae.Status == 429:
			return "Dropbox is busy. Press Retry in a minute."
		case ae.Status >= 500:
			return "Dropbox is having problems. Press Retry later."
		}
		return "Dropbox error: " + s
	}
	if errors.Is(err, context.DeadlineExceeded) || dbx.Retryable(err) {
		return "Network problem. Check the internet connection and press Retry."
	}
	return err.Error()
}
