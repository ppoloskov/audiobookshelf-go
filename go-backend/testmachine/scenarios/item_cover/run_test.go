//go:build replacement

package item_cover

import (
	"testing"

	"audiobookshelf-compat/testmachine/scenarios/harness"
)

func TestReplacement(t *testing.T) { harness.Run(t, Packet()) }
