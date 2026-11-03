// Package ids generates the identifiers the server stores. It is a leaf
// package: standard library only.
package ids

import (
	"crypto/rand"
	"encoding/hex"
)

// New returns a random (version 4) UUID in its canonical 36-character form,
// the same shape upstream's uuid.v4() stores. It fails only when the system
// random source does, and never substitutes a weaker id.
func New() (string, error) {
	var b [16]byte
	if _, err := rand.Read(b[:]); err != nil {
		return "", err
	}
	b[6] = (b[6] & 0x0f) | 0x40
	b[8] = (b[8] & 0x3f) | 0x80
	encoded := hex.EncodeToString(b[:])
	return encoded[:8] + "-" + encoded[8:12] + "-" + encoded[12:16] + "-" + encoded[16:20] + "-" + encoded[20:], nil
}
