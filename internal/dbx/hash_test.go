package dbx

import (
	"crypto/sha256"
	"encoding/hex"
	"os"
	"path/filepath"
	"testing"
)

// Reference implementation written directly from Dropbox's spec.
func refHash(data []byte) string {
	var concat []byte
	for len(data) > 0 {
		n := hashBlockSize
		if n > len(data) {
			n = len(data)
		}
		s := sha256.Sum256(data[:n])
		concat = append(concat, s[:]...)
		data = data[n:]
	}
	s := sha256.Sum256(concat)
	return hex.EncodeToString(s[:])
}

func TestContentHash(t *testing.T) {
	// Empty file hash published by Dropbox.
	if got := NewContentHasher().Sum(); got != "e3b0c44298fc1c149afbf4c8996fb92427ae41e4649b934ca495991b7852b855" {
		t.Fatalf("empty hash = %s", got)
	}
	for _, size := range []int{1, hashBlockSize - 1, hashBlockSize, hashBlockSize + 1, 3*hashBlockSize + 12345} {
		data := make([]byte, size)
		for i := range data {
			data[i] = byte(i * 7)
		}
		h := NewContentHasher()
		// Write in odd-sized pieces to exercise block boundaries.
		for p := data; len(p) > 0; {
			n := 999_983
			if n > len(p) {
				n = len(p)
			}
			h.Write(p[:n])
			p = p[n:]
		}
		if got, want := h.Sum(), refHash(data); got != want {
			t.Fatalf("size %d: got %s want %s", size, got, want)
		}
		path := filepath.Join(t.TempDir(), "f")
		os.WriteFile(path, data, 0o600)
		if got, _ := FileContentHash(path); got != refHash(data) {
			t.Fatalf("file size %d mismatch", size)
		}
	}
}

func TestHeaderJSON(t *testing.T) {
	got, _ := headerJSON(map[string]string{"path": "/Fotos/año 😀.jpg"})
	want := `{"path":"/Fotos/a` + `\` + `u00f1o ` + `\` + `ud83d` + `\` + `ude00.jpg"}`
	if got != want {
		t.Fatalf("got %s want %s", got, want)
	}
}

func TestFailureSummary(t *testing.T) {
	got := failureSummary([]byte(`{".tag":"path","path":{".tag":"insufficient_space"}}`))
	if got != "path/insufficient_space" {
		t.Fatal(got)
	}
}
