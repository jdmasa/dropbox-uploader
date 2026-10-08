package localfs

import (
	"bytes"
	"image"
	"image/color"
	"image/jpeg"
	"os"
	"path/filepath"
	"testing"
)

func TestNaturalSortAndList(t *testing.T) {
	dir := t.TempDir()
	for _, n := range []string{"IMG_10.jpg", "IMG_2.jpg", "clip.MOV", "notes.txt", ".hidden", "desktop.ini"} {
		os.WriteFile(filepath.Join(dir, n), []byte("x"), 0o644)
	}
	os.Mkdir(filepath.Join(dir, "Zoo"), 0o755)
	l, err := List(dir)
	if err != nil {
		t.Fatal(err)
	}
	var names []string
	for _, e := range l.Entries {
		names = append(names, e.Name+":"+string(e.Kind))
	}
	want := "[Zoo:folder clip.MOV:video IMG_2.jpg:image IMG_10.jpg:image notes.txt:other]"
	if got := formatList(names); got != want {
		t.Fatalf("got %s want %s", got, want)
	}
}

func formatList(s []string) string {
	out := "["
	for i, x := range s {
		if i > 0 {
			out += " "
		}
		out += x
	}
	return out + "]"
}

func TestThumbnail(t *testing.T) {
	dir := t.TempDir()
	img := image.NewRGBA(image.Rect(0, 0, 1200, 800))
	for x := 0; x < 1200; x++ {
		img.Set(x, x%800, color.RGBA{255, 0, 0, 255})
	}
	p := filepath.Join(dir, "photo.jpg")
	f, _ := os.Create(p)
	jpeg.Encode(f, img, nil)
	f.Close()

	th := NewThumbnailer(filepath.Join(dir, "cache"))
	b, err := th.Thumb(p)
	if err != nil {
		t.Fatal(err)
	}
	out, err := jpeg.Decode(bytesReader(b))
	if err != nil {
		t.Fatal(err)
	}
	if out.Bounds().Dx() != thumbSize || out.Bounds().Dy() != 213 {
		t.Fatalf("thumb size %v", out.Bounds())
	}
	if b2, _ := th.Thumb(p); len(b2) != len(b) {
		t.Fatal("cache miss")
	}
}

func bytesReader(b []byte) *bytes.Reader { return bytes.NewReader(b) }
