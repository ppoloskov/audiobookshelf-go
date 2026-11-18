//go:build replacement

package admin_settings

import (
	"testing"

	"audiobookshelf-compat/tests/replacement/harness"
)

func TestReplacement(t *testing.T) { harness.Run(t, Packet()) }
