package localfs

import (
	"bytes"
	"crypto/sha1"
	"encoding/hex"
	"fmt"
	"image"
	"image/jpeg"
	"os"
	"path/filepath"
	"runtime"
	"strings"

	_ "image/gif"
	_ "image/png"

	"github.com/disintegration/imaging"
	"github.com/rwcarlsen/goexif/exif"
	_ "golang.org/x/image/bmp"
	_ "golang.org/x/image/webp"
)

const thumbSize = 320

// Thumbnailer makes small JPEG previews of local images and caches them on disk.
type Thumbnailer struct {
	cacheDir string
	sem      chan struct{}
}

func NewThumbnailer(cacheDir string) *Thumbnailer {
	_ = os.MkdirAll(cacheDir, 0o700)
	n := runtime.NumCPU()
	if n > 6 {
		n = 6
	}
	return &Thumbnailer{cacheDir: cacheDir, sem: make(chan struct{}, n)}
}

// Thumb returns JPEG bytes for the image at path.
func (t *Thumbnailer) Thumb(path string) ([]byte, error) {
	info, err := os.Stat(path)
	if err != nil {
		return nil, err
	}
	if !HasThumbnail(path) {
		return nil, fmt.Errorf("no preview for %s", filepath.Ext(path))
	}
	sum := sha1.Sum([]byte(fmt.Sprintf("%s|%d|%d", path, info.Size(), info.ModTime().UnixNano())))
	cached := filepath.Join(t.cacheDir, hex.EncodeToString(sum[:])+".jpg")
	if b, err := os.ReadFile(cached); err == nil {
		return b, nil
	}

	t.sem <- struct{}{}
	defer func() { <-t.sem }()

	img, err := t.render(path)
	if err != nil {
		return nil, err
	}
	var buf bytes.Buffer
	if err := jpeg.Encode(&buf, img, &jpeg.Options{Quality: 80}); err != nil {
		return nil, err
	}
	_ = os.WriteFile(cached, buf.Bytes(), 0o600)
	return buf.Bytes(), nil
}

func (t *Thumbnailer) render(path string) (image.Image, error) {
	if !CanThumbnail(path) {
		// HEIC, RAW, video: ask Windows (uses the same codecs as Explorer).
		img, err := shellThumb(path, thumbSize)
		if err != nil {
			return nil, err
		}
		return imaging.Fit(img, thumbSize, thumbSize, imaging.Linear), nil
	}
	ext := strings.ToLower(filepath.Ext(path))
	if ext == ".jpg" || ext == ".jpeg" {
		// Many cameras embed a 320px+ preview in their EXIF data, which is much
		// faster than decoding a 12-50 megapixel image (phones embed ~160px ones, too blurry).
		if img := exifThumb(path); img != nil {
			return img, nil
		}
	}
	img, err := imaging.Open(path, imaging.AutoOrientation(true))
	if err != nil {
		return nil, err
	}
	return imaging.Fit(img, thumbSize, thumbSize, imaging.Linear), nil
}

func exifThumb(path string) image.Image {
	f, err := os.Open(path)
	if err != nil {
		return nil
	}
	defer f.Close()
	x, err := exif.Decode(f)
	if err != nil {
		return nil
	}
	data, err := x.JpegThumbnail()
	if err != nil {
		return nil
	}
	img, err := jpeg.Decode(bytes.NewReader(data))
	if err != nil || max(img.Bounds().Dx(), img.Bounds().Dy()) < 240 {
		return nil
	}
	// The embedded preview is stored unrotated; apply the photo's orientation.
	if tag, err := x.Get(exif.Orientation); err == nil {
		if o, err := tag.Int(0); err == nil {
			img = orient(img, o)
		}
	}
	return img
}

func orient(img image.Image, o int) image.Image {
	switch o {
	case 2:
		return imaging.FlipH(img)
	case 3:
		return imaging.Rotate180(img)
	case 4:
		return imaging.FlipV(img)
	case 5:
		return imaging.Transpose(img)
	case 6:
		return imaging.Rotate270(img)
	case 7:
		return imaging.Transverse(img)
	case 8:
		return imaging.Rotate90(img)
	}
	return img
}
