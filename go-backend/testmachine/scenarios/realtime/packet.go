package realtime

import (
	"slices"

	"audiobookshelf-compat/testmachine/scenarios/harness"
)

// Packet is the realtime owner packet: the Socket.IO transport, socket
// authentication, dispatch of the client actions this slice owns, and the
// events they emit. The shared server needs no special options: the fixture
// install plus the harness' prefixed socket path are what clients use.
func Packet() harness.Packet {
	return harness.Packet{
		Name: "realtime",
		Scenarios: slices.Concat(
			transportScenarios(),
			authScenarios(),
			authFailedScenarios(),
			initScenarios(),
			pingScenarios(),
			pongScenarios(),
			disconnectScenarios(),
			userOnlineScenarios(),
			userOfflineScenarios(),
			messageScenarios(),
			searchCoversScenarios(),
			cancelCoversScenarios(),
			setLogListenerScenarios(),
			removeLogListenerScenarios(),
			socketioScenarios(),
			// Last: the cancel_scan group stages folders into the fixture
			// library and leaves them behind.
			cancelScanScenarios(),
		),
	}
}
