package dbx

import (
	"crypto/sha256"
	"encoding"
	"encoding/hex"
	"hash"
	"io"
	"os"
)

const hashBlockSize = 4 * 1024 * 1024

// ContentHasher computes Dropbox's content_hash: SHA-256 of the concatenated
// SHA-256 digests of each 4 MiB block.
// https://www.dropbox.com/developers/reference/content-hash
type ContentHasher struct {
	overall hash.Hash
	block   hash.Hash
	inBlock int
}

func NewContentHasher() *ContentHasher {
	return &ContentHasher{overall: sha256.New(), block: sha256.New()}
}

func (h *ContentHasher) Write(p []byte) (int, error) {
	n := len(p)
	for len(p) > 0 {
		take := hashBlockSize - h.inBlock
		if take > len(p) {
			take = len(p)
		}
		h.block.Write(p[:take])
		h.inBlock += take
		p = p[take:]
		if h.inBlock == hashBlockSize {
			h.overall.Write(h.block.Sum(nil))
			h.block.Reset()
			h.inBlock = 0
		}
	}
	return n, nil
}

// Sum returns the hex content hash of everything written so far.
func (h *ContentHasher) Sum() string {
	if h.inBlock == 0 {
		return hex.EncodeToString(h.overall.Sum(nil))
	}
	// Fold the partial last block into a copy so further writes stay valid.
	state, _ := h.overall.(encoding.BinaryMarshaler).MarshalBinary()
	overall := sha256.New()
	_ = overall.(encoding.BinaryUnmarshaler).UnmarshalBinary(state)
	overall.Write(h.block.Sum(nil))
	return hex.EncodeToString(overall.Sum(nil))
}

// FileContentHash computes the Dropbox content hash of a local file.
func FileContentHash(path string) (string, error) {
	f, err := os.Open(path)
	if err != nil {
		return "", err
	}
	defer f.Close()
	h := NewContentHasher()
	if _, err := io.CopyBuffer(h, f, make([]byte, 1<<20)); err != nil {
		return "", err
	}
	return h.Sum(), nil
}
