//go:build replacement

package backup

import (
	"testing"

	"audiobookshelf-compat/testmachine/scenarios/harness"
)

func TestReplacement(t *testing.T) { harness.Run(t, Packet()) }
