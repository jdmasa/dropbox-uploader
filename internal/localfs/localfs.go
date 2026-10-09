// Package localfs lists drives and folders on this computer for the left panel.
package localfs

import (
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"
)

type Kind string

const (
	Folder Kind = "folder"
	Image  Kind = "image"
	Video  Kind = "video"
	Other  Kind = "other"
)

var (
	// Images the app can make thumbnails of.
	imageExt = map[string]bool{".jpg": true, ".jpeg": true, ".png": true, ".gif": true, ".webp": true, ".bmp": true}
	// Photos we recognise but cannot preview yet (shown with a photo icon).
	rawPhotoExt = map[string]bool{".heic": true, ".heif": true, ".cr2": true, ".nef": true, ".arw": true, ".dng": true, ".raf": true, ".orf": true, ".tif": true, ".tiff": true}
	videoExt    = map[string]bool{".mp4": true, ".m4v": true, ".mov": true, ".webm": true, ".avi": true, ".mkv": true, ".3gp": true, ".mts": true, ".m2ts": true, ".wmv": true, ".mpg": true, ".mpeg": true}
)

// KindOf classifies a file by extension.
func KindOf(name string) Kind {
	ext := strings.ToLower(filepath.Ext(name))
	switch {
	case imageExt[ext] || rawPhotoExt[ext]:
		return Image
	case videoExt[ext]:
		return Video
	}
	return Other
}

// HasThumbnail reports whether a preview image can be produced for this file,
// either by our own decoder or by Windows' thumbnail provider.
func HasThumbnail(name string) bool {
	return CanThumbnail(name) || (shellThumbnailAvailable() && KindOf(name) != Other)
}

// CanThumbnail reports whether the app can decode this image itself.
func CanThumbnail(name string) bool {
	return imageExt[strings.ToLower(filepath.Ext(name))]
}

type Entry struct {
	Name    string    `json:"name"`
	Path    string    `json:"path"`
	Kind    Kind      `json:"kind"`
	Size    int64     `json:"size"`
	ModTime time.Time `json:"modTime"`
	Thumb   bool      `json:"thumb"` // a thumbnail can be generated
}

type Listing struct {
	Path    string  `json:"path"`
	Parent  string  `json:"parent"` // "" at a drive root
	Crumbs  []Crumb `json:"crumbs"`
	Entries []Entry `json:"entries"`
}

type Crumb struct {
	Name string `json:"name"`
	Path string `json:"path"`
}

// List returns the visible contents of dir, folders first, then files by name.
func List(dir string) (*Listing, error) {
	dir = filepath.Clean(dir)
	des, err := os.ReadDir(dir)
	if err != nil {
		return nil, err
	}
	l := &Listing{Path: dir, Crumbs: crumbs(dir)}
	if parent := filepath.Dir(dir); parent != dir {
		l.Parent = parent
	}
	for _, d := range des {
		name := d.Name()
		full := filepath.Join(dir, name)
		if skipName(name) || isHidden(full, d) {
			continue
		}
		info, err := d.Info()
		if err != nil {
			continue
		}
		if info.Mode()&os.ModeSymlink != 0 {
			if info, err = os.Stat(full); err != nil {
				continue
			}
		}
		e := Entry{Name: name, Path: full, Size: info.Size(), ModTime: info.ModTime()}
		if info.IsDir() {
			e.Kind, e.Size = Folder, 0
		} else if info.Mode().IsRegular() {
			e.Kind = KindOf(name)
			e.Thumb = HasThumbnail(name)
		} else {
			continue
		}
		l.Entries = append(l.Entries, e)
	}
	sort.SliceStable(l.Entries, func(i, j int) bool {
		a, b := l.Entries[i], l.Entries[j]
		if (a.Kind == Folder) != (b.Kind == Folder) {
			return a.Kind == Folder
		}
		return NaturalLess(strings.ToLower(a.Name), strings.ToLower(b.Name))
	})
	return l, nil
}

func skipName(name string) bool {
	l := strings.ToLower(name)
	switch l {
	case "desktop.ini", "thumbs.db", ".ds_store", "$recycle.bin", "system volume information", "$windows.~ws", "$winreagent", "config.msi":
		return true
	}
	return strings.HasPrefix(l, ".") || strings.HasPrefix(l, "~$") || strings.HasPrefix(l, "$")
}

func crumbs(dir string) []Crumb {
	var out []Crumb
	for p := dir; ; {
		name := filepath.Base(p)
		parent := filepath.Dir(p)
		if parent == p {
			// Volume root: "C:\" on Windows, "/" elsewhere.
			name = strings.TrimRight(p, `\/`)
			if name == "" {
				name = "/"
			}
		}
		out = append([]Crumb{{Name: name, Path: p}}, out...)
		if parent == p {
			return out
		}
		p = parent
	}
}

// NaturalLess orders "IMG_2.jpg" before "IMG_10.jpg".
func NaturalLess(a, b string) bool {
	for a != "" && b != "" {
		ad, bd := isDigit(a[0]), isDigit(b[0])
		if ad && bd {
			na, ra := splitNum(a)
			nb, rb := splitNum(b)
			ta, tb := strings.TrimLeft(na, "0"), strings.TrimLeft(nb, "0")
			if len(ta) != len(tb) {
				return len(ta) < len(tb)
			}
			if ta != tb {
				return ta < tb
			}
			a, b = ra, rb
			continue
		}
		if a[0] != b[0] {
			return a[0] < b[0]
		}
		a, b = a[1:], b[1:]
	}
	return len(a) < len(b)
}

func isDigit(c byte) bool { return c >= '0' && c <= '9' }

func splitNum(s string) (string, string) {
	i := 0
	for i < len(s) && isDigit(s[i]) {
		i++
	}
	return s[:i], s[i:]
}

// Place is a shortcut shown at the top of the left panel (drive or known folder).
// For known folders the UI shows a translated name based on Icon; for drives
// it shows Name (the volume label, may be empty) and Letter.
type Place struct {
	Name   string `json:"name"`
	Path   string `json:"path"`
	Icon   string `json:"icon"` // home, desktop, pictures, videos, downloads, documents, drive, usb, network
	Drive  bool   `json:"drive"`
	Letter string `json:"letter,omitempty"`
}

// Places returns the user's common folders followed by the drives.
func Places() []Place {
	var out []Place
	for _, kf := range knownFolders() {
		if st, err := os.Stat(kf.Path); err == nil && st.IsDir() {
			out = append(out, kf)
		}
	}
	return append(out, drives()...)
}
