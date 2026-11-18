//go:build replacement

package server

import (
	"testing"

	"audiobookshelf-compat/tests/replacement/harness"
)

func TestReplacement(t *testing.T) { harness.Run(t, Packet()) }
