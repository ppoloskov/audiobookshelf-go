//go:build replacement

package user_me

import (
	"testing"

	"audiobookshelf-compat/tests/replacement/harness"
)

func TestReplacement(t *testing.T) { harness.Run(t, Packet()) }
