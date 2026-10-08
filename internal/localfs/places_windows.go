//go:build windows

package localfs

import (
	"io/fs"
	"strings"
	"syscall"

	"golang.org/x/sys/windows"
)

func init() {
	// Don't let Windows pop up "There is no disk in the drive" for empty card readers.
	windows.SetErrorMode(windows.SEM_FAILCRITICALERRORS | windows.SEM_NOOPENFILEERRORBOX)
}

func knownFolders() []Place {
	ids := []struct {
		id   *windows.KNOWNFOLDERID
		name string
		icon string
	}{
		{windows.FOLDERID_Pictures, "Pictures", "pictures"},
		{windows.FOLDERID_Videos, "Videos", "videos"},
		{windows.FOLDERID_Desktop, "Desktop", "desktop"},
		{windows.FOLDERID_Downloads, "Downloads", "downloads"},
		{windows.FOLDERID_Documents, "Documents", "documents"},
		{windows.FOLDERID_Profile, "Home", "home"},
	}
	var out []Place
	for _, k := range ids {
		// KnownFolderPath follows OneDrive redirection of Pictures/Desktop/Documents.
		if p, err := windows.KnownFolderPath(k.id, 0); err == nil && p != "" {
			out = append(out, Place{Name: k.name, Path: p, Icon: k.icon})
		}
	}
	return out
}

func drives() []Place {
	var out []Place
	mask, err := windows.GetLogicalDrives()
	if err != nil {
		return out
	}
	for i := 0; i < 26; i++ {
		if mask&(1<<uint(i)) == 0 {
			continue
		}
		letter := string(rune('A' + i))
		root := letter + `:\`
		rootPtr, _ := windows.UTF16PtrFromString(root)
		icon := "drive"
		kind := "Local Disk"
		switch windows.GetDriveType(rootPtr) {
		case windows.DRIVE_REMOVABLE:
			icon, kind = "usb", "USB Drive"
		case windows.DRIVE_REMOTE:
			icon, kind = "network", "Network Drive"
		case windows.DRIVE_CDROM:
			continue
		case windows.DRIVE_NO_ROOT_DIR, windows.DRIVE_UNKNOWN:
			continue
		}
		label := volumeLabel(rootPtr)
		if label == "" {
			label = kind
		}
		out = append(out, Place{Name: label + " (" + letter + ":)", Path: root, Icon: icon})
	}
	return out
}

func volumeLabel(root *uint16) string {
	buf := make([]uint16, windows.MAX_PATH+1)
	// Fails quickly for empty card readers; that's fine.
	if err := windows.GetVolumeInformation(root, &buf[0], uint32(len(buf)), nil, nil, nil, nil, 0); err != nil {
		return ""
	}
	return strings.TrimSpace(windows.UTF16ToString(buf))
}

func isHidden(_ string, d fs.DirEntry) bool {
	info, err := d.Info()
	if err != nil {
		return false
	}
	if a, ok := info.Sys().(*syscall.Win32FileAttributeData); ok {
		return a.FileAttributes&(windows.FILE_ATTRIBUTE_HIDDEN|windows.FILE_ATTRIBUTE_SYSTEM) != 0
	}
	return false
}
