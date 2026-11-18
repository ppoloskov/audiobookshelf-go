//go:build replacement

package launch

import (
	"testing"

	"audiobookshelf-compat/testmachine/scenarios/harness"
)

// TestReplacement runs the launch packet's black-box scenarios against the
// configured target through the shared harness.
func TestReplacement(t *testing.T) {
	harness.Run(t, Packet())
}
