//go:build replacement

package playback_sessions

import (
	"testing"

	"audiobookshelf-compat/testmachine/scenarios/harness"
)

func TestReplacement(t *testing.T) { harness.Run(t, Packet()) }
