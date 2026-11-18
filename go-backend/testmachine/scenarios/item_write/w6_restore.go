package item_write

import (
	"net/http"
	"testing"

	"audiobookshelf-compat/testmachine/scenarios/harness"
)

// w6RestoreScenarios covers the backup restore window of the destructive
// cascades requirement: a live socket survives the database swap and receives
// backup_applied, and still receives later cascade events.
func w6RestoreScenarios(_ *setup) []harness.Scenario {
	const req = "sub-destructive-cascades-w6"
	const inv = "INV-W6-RESTORE-SOCKET-001"
	return []harness.Scenario{
		scenario(req, inv, "edge", "changed", "w6-restore-keeps-live-socket",
			"Applying a backup answers 200 and keeps the authenticated sockets connected; backup_applied with one null argument reaches every authenticated socket before a later item_updated on the same connection, while a non-admin or unknown backup request is rejected with 403 or 404 and emits nothing.",
			srcs("server/controllers/BackupController.js:154-156", "server/managers/BackupManager.js:200-267", "server/managers/BackupManager.js:263-266", "server/SocketAuthority.js:57-71"),
			func(t *testing.T, _ *harness.Env) {
				e, _ := freshServer(t)
				root := e.Token(t, "root")
				a := connect(t, e, map[string]string{"root": root, "listener": e.Token(t, "listener"), "anonymous": ""})
				wantStatus(t, "non-admin apply", e.Get(t, e.Token(t, "listener"), "/api/backups/00000000-0000-4000-8000-000000000000/apply"), http.StatusForbidden)
				wantStatus(t, "unknown backup", e.Get(t, root, "/api/backups/00000000-0000-4000-8000-000000000000/apply"), http.StatusNotFound)
				for _, name := range []string{"root", "listener"} {
					a.sockets[name].None(t, a.marks[name], "backup_applied", absentWindow, nil)
				}
				created := e.Post(t, root, "/api/backups", nil)
				wantStatus(t, "create backup", created, http.StatusOK)
				backups, _ := created.Object(t)["backups"].([]any)
				if len(backups) == 0 {
					t.Fatalf("no backup created: %.300s", created.Body)
				}
				id, _ := harness.Dig(backups, 0, "id").(string)
				wantStatus(t, "apply backup", e.Get(t, root, "/api/backups/"+id+"/apply"), http.StatusOK)
				for _, name := range []string{"root", "listener"} {
					ev := a.sockets[name].Wait(t, a.marks[name], "backup_applied", eventWait, nil)
					if len(ev.Args) != 1 || string(ev.Args[0]) != "null" {
						t.Fatalf("%s backup_applied arguments = %q, want one null", name, ev.Args)
					}
				}
				a.sockets["anonymous"].None(t, a.marks["anonymous"], "backup_applied", absentWindow, nil)
				a.reset()
				wantStatus(t, "edit after restore", patchMedia(t, e, root, e.IDs["novel"], map[string]any{"metadata": map[string]any{"subtitle": "After restore"}}), http.StatusOK)
				a.receive(t, "item_updated", idOf(e.IDs["novel"]), []string{"root", "listener"}, []string{"anonymous"})
			}),
	}
}
