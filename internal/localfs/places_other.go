//go:build !windows

package localfs

import (
	"io/fs"
	"os"
	"path/filepath"
)

func knownFolders() []Place {
	home, err := os.UserHomeDir()
	if err != nil {
		return nil
	}
	return []Place{
		{Name: "Pictures", Path: filepath.Join(home, "Pictures"), Icon: "pictures"},
		{Name: "Videos", Path: filepath.Join(home, "Movies"), Icon: "videos"},
		{Name: "Desktop", Path: filepath.Join(home, "Desktop"), Icon: "desktop"},
		{Name: "Downloads", Path: filepath.Join(home, "Downloads"), Icon: "downloads"},
		{Name: "Documents", Path: filepath.Join(home, "Documents"), Icon: "documents"},
		{Name: "Home", Path: home, Icon: "home"},
	}
}

func drives() []Place {
	out := []Place{{Path: "/", Icon: "drive", Drive: true}}
	if des, err := os.ReadDir("/Volumes"); err == nil {
		for _, d := range des {
			out = append(out, Place{Name: d.Name(), Path: filepath.Join("/Volumes", d.Name()), Icon: "usb", Drive: true})
		}
	}
	return out
}

func isHidden(string, fs.DirEntry) bool { return false }
