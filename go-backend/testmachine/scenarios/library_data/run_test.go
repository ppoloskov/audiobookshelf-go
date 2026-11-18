//go:build replacement

package library_data

import (
	"testing"

	"audiobookshelf-compat/testmachine/scenarios/harness"
)

func TestReplacement(t *testing.T) { harness.Run(t, Packet()) }
