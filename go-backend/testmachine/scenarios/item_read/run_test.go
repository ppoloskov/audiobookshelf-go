//go:build replacement

package item_read

import (
	"testing"

	"audiobookshelf-compat/testmachine/scenarios/harness"
)

func TestReplacement(t *testing.T) { harness.Run(t, Packet()) }
