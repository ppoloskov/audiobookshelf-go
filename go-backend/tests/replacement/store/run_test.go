//go:build replacement

package store

import (
	"audiobookshelf-compat/tests/replacement/harness"
	"testing"
)

func TestReplacement(t *testing.T) { harness.Run(t, Packet()) }
