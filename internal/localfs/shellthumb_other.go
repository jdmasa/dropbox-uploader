//go:build !windows

package localfs

import (
	"errors"
	"image"
)

func shellThumbnailAvailable() bool { return false }

func shellThumb(string, int) (image.Image, error) {
	return nil, errors.New("system thumbnails are only available on Windows")
}
