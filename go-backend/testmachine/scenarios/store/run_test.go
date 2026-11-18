//go:build replacement

package store

import (
	"audiobookshelf-compat/testmachine/scenarios/harness"
	"testing"
)

func TestReplacement(t *testing.T) { harness.Run(t, Packet()) }
