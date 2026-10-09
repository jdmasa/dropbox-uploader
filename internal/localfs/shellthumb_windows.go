//go:build windows

package localfs

import (
	"errors"
	"fmt"
	"image"
	"image/color"
	"runtime"
	"syscall"
	"unsafe"

	"golang.org/x/sys/windows"
)

// Windows Explorer's thumbnail provider (IShellItemImageFactory). It previews
// formats Go can't decode: HEIC (with the HEIF extension installed), camera RAW
// files and videos, using whatever codecs Windows has.

var (
	shell32                         = windows.NewLazySystemDLL("shell32.dll")
	gdi32                           = windows.NewLazySystemDLL("gdi32.dll")
	user32                          = windows.NewLazySystemDLL("user32.dll")
	procSHCreateItemFromParsingName = shell32.NewProc("SHCreateItemFromParsingName")
	procGetObjectW                  = gdi32.NewProc("GetObjectW")
	procGetDIBits                   = gdi32.NewProc("GetDIBits")
	procDeleteObject                = gdi32.NewProc("DeleteObject")
	procGetDC                       = user32.NewProc("GetDC")
	procReleaseDC                   = user32.NewProc("ReleaseDC")

	iidShellItemImageFactory = windows.GUID{Data1: 0xbcc18b79, Data2: 0xba16, Data3: 0x442f, Data4: [8]byte{0x80, 0xc4, 0x8a, 0x59, 0xc3, 0x0c, 0x46, 0x3b}}
)

const (
	siigbfBiggerSizeOK  = 0x1
	siigbfThumbnailOnly = 0x8
)

type shellRequest struct {
	path  string
	size  int
	reply chan shellResult
}

type shellResult struct {
	img image.Image
	err error
}

var shellQueue = make(chan shellRequest)

func init() {
	// COM needs an initialised, OS-locked thread; keep a few dedicated ones.
	for i := 0; i < 3; i++ {
		go shellWorker()
	}
}

func shellWorker() {
	runtime.LockOSThread()
	if err := windows.CoInitializeEx(0, windows.COINIT_APARTMENTTHREADED); err != nil {
		var errno syscall.Errno
		// S_FALSE (already initialised) is fine.
		if !errors.As(err, &errno) || errno != 1 {
			for req := range shellQueue {
				req.reply <- shellResult{err: err}
			}
		}
	}
	for req := range shellQueue {
		img, err := shellThumbOnThread(req.path, req.size)
		req.reply <- shellResult{img, err}
	}
}

func shellThumbnailAvailable() bool { return true }

func shellThumb(path string, size int) (image.Image, error) {
	reply := make(chan shellResult, 1)
	shellQueue <- shellRequest{path: path, size: size, reply: reply}
	r := <-reply
	return r.img, r.err
}

func shellThumbOnThread(path string, size int) (image.Image, error) {
	p, err := windows.UTF16PtrFromString(path)
	if err != nil {
		return nil, err
	}
	var factory unsafe.Pointer // COM object; its first word points to the vtable
	hr, _, _ := procSHCreateItemFromParsingName.Call(
		uintptr(unsafe.Pointer(p)), 0,
		uintptr(unsafe.Pointer(&iidShellItemImageFactory)),
		uintptr(unsafe.Pointer(&factory)))
	if hr != 0 || factory == nil {
		return nil, fmt.Errorf("SHCreateItemFromParsingName: 0x%x", hr)
	}
	vtbl := *(**[4]uintptr)(factory)
	defer syscall.SyscallN(vtbl[2], uintptr(factory)) // Release

	// GetImage(SIZE size, SIIGBF flags, HBITMAP *phbm). The 8-byte SIZE struct is passed by
	// value: packed into one register on 64-bit Windows, as two stack words on 32-bit.
	var hbmp windows.Handle
	args := []uintptr{uintptr(factory)}
	if unsafe.Sizeof(uintptr(0)) == 8 {
		args = append(args, uintptr(uint64(uint32(size))|uint64(uint32(size))<<32))
	} else {
		args = append(args, uintptr(size), uintptr(size))
	}
	args = append(args, siigbfBiggerSizeOK|siigbfThumbnailOnly, uintptr(unsafe.Pointer(&hbmp)))
	hr, _, _ = syscall.SyscallN(vtbl[3], args...)
	if hr != 0 || hbmp == 0 {
		return nil, fmt.Errorf("GetImage: 0x%x", hr)
	}
	defer procDeleteObject.Call(uintptr(hbmp))
	return hbitmapToImage(hbmp)
}

type bitmap struct {
	Type       int32
	Width      int32
	Height     int32
	WidthBytes int32
	Planes     uint16
	BitsPixel  uint16
	Bits       uintptr
}

type bitmapInfoHeader struct {
	Size          uint32
	Width         int32
	Height        int32
	Planes        uint16
	BitCount      uint16
	Compression   uint32
	SizeImage     uint32
	XPelsPerMeter int32
	YPelsPerMeter int32
	ClrUsed       uint32
	ClrImportant  uint32
}

func hbitmapToImage(hbmp windows.Handle) (image.Image, error) {
	var bm bitmap
	if r, _, _ := procGetObjectW.Call(uintptr(hbmp), unsafe.Sizeof(bm), uintptr(unsafe.Pointer(&bm))); r == 0 {
		return nil, errors.New("GetObject failed")
	}
	w, h := int(bm.Width), int(bm.Height)
	if w <= 0 || h <= 0 || w > 4096 || h > 4096 {
		return nil, errors.New("bad bitmap size")
	}
	hdr := bitmapInfoHeader{Size: uint32(unsafe.Sizeof(bitmapInfoHeader{})), Width: int32(w), Height: -int32(h), Planes: 1, BitCount: 32}
	// BITMAPINFO = header + one RGBQUAD; reserve extra room to be safe.
	info := make([]byte, unsafe.Sizeof(hdr)+16)
	*(*bitmapInfoHeader)(unsafe.Pointer(&info[0])) = hdr
	pix := make([]byte, w*h*4)
	hdc, _, _ := procGetDC.Call(0)
	defer procReleaseDC.Call(0, hdc)
	if r, _, _ := procGetDIBits.Call(hdc, uintptr(hbmp), 0, uintptr(h), uintptr(unsafe.Pointer(&pix[0])), uintptr(unsafe.Pointer(&info[0])), 0); r == 0 {
		return nil, errors.New("GetDIBits failed")
	}
	img := image.NewRGBA(image.Rect(0, 0, w, h))
	for i := 0; i < w*h; i++ {
		b, g, r := pix[i*4], pix[i*4+1], pix[i*4+2]
		img.SetRGBA(i%w, i/w, color.RGBA{R: r, G: g, B: b, A: 255})
	}
	return img, nil
}
