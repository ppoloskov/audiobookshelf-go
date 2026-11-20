// Package ids generates the identifiers the server stores. It is a leaf
// package: standard library only.
package ids

import (
	"crypto/rand"
	"encoding/hex"
	"regexp"
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

// Must is New for callers with no error path. crypto/rand cannot fail since
// Go 1.24 (it terminates the program instead), so the panic is unreachable.
func Must() string {
	id, err := New()
	if err != nil {
		panic(err)
	}
	return id
}

var validID = regexp.MustCompile(`(?i)^(?:[0-9a-f]{8}-[0-9a-f]{4}-[1-5][0-9a-f]{3}-[89ab][0-9a-f]{3}-[0-9a-f]{12}|00000000-0000-0000-0000-000000000000)$`)

// Valid is uuid.validate of the uuid package v8.3.2 the server uses
// (node_modules/uuid/dist/regex.js:7): versions 1-5 and the nil UUID.
func Valid(s string) bool { return validID.MatchString(s) }

// ValidV4 is uuid.validate(s) && uuid.version(s) === 4.
func ValidV4(s string) bool { return Valid(s) && s[14] == '4' }
