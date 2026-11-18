package server

import (
	"net/http"
	"testing"
	"time"

	"audiobookshelf-compat/testmachine/scenarios/harness"
)

// backupEventScenarios characterize the cap-event-backup-applied requirement:
// when the event is emitted, who receives it and that a rejected apply is
// silent.
func (s *suite) backupEventScenarios() []harness.Scenario {
	return []harness.Scenario{
		{
			Requirement: "cap-event-backup-applied", Invariant: "INV-CAP-EVENT-BACKUP-APPLIED", Case: "normal", State: "initial", Assertion: "backup-applied-emitted-to-all-authorized-sockets",
			Sources: []string{"server/managers/BackupManager.js:263-266", "server/SocketAuthority.js:52-68"},
			Claim:   "A successful backup apply emits exactly one backup_applied to every authenticated socket, admin or ordinary, and none to an unauthenticated socket.",
			Run: func(t *testing.T, e *harness.Env) {
				backup := s.ensureBackup(t, e)
				rootSock := e.Socket(t, root(t, e))
				listenerSock := e.Socket(t, e.Token(t, "listener"))
				anonSock := e.Socket(t, "")
				rootMark, listenerMark, anonMark := rootSock.Mark(), listenerSock.Mark(), anonSock.Mark()
				requireStatus(t, applyBackup(t, e, backup.ID), http.StatusOK, "apply backup")
				backupAppliedArgs(t, rootSock, rootMark, "root socket")
				backupAppliedArgs(t, listenerSock, listenerMark, "ordinary socket")
				anonSock.None(t, anonMark, "backup_applied", 500*time.Millisecond, nil)
			},
		},
		{
			Requirement: "cap-event-backup-applied", Invariant: "INV-CAP-EVENT-BACKUP-APPLIED", Case: "error", State: "failure", Assertion: "backup-applied-absent-on-rejected-apply",
			Sources: []string{"server/controllers/BackupController.js:160-180", "server/managers/BackupManager.js:263-266"},
			Claim:   "A non-admin apply, an unknown backup id and a rejected upload emit no backup_applied to either an admin or an ordinary socket.",
			Run: func(t *testing.T, e *harness.Env) {
				rootSock := e.Socket(t, root(t, e))
				listenerSock := e.Socket(t, e.Token(t, "listener"))
				rootMark, listenerMark := rootSock.Mark(), listenerSock.Mark()
				requireStatus(t, e.Get(t, e.Token(t, "listener"), "/api/backups/not-a-backup/apply"), http.StatusForbidden, "ordinary apply")
				requireStatus(t, e.Get(t, root(t, e), "/api/backups/not-a-backup/apply"), http.StatusNotFound, "apply unknown id")
				requireStatus(t, uploadBackup(t, e, "broken.audiobookshelf", []byte("not a zip")), http.StatusBadRequest, "rejected upload")
				rootSock.None(t, rootMark, "backup_applied", 500*time.Millisecond, nil)
				listenerSock.None(t, listenerMark, "backup_applied", 500*time.Millisecond, nil)
			},
		},
		{
			Requirement: "cap-event-backup-applied", Invariant: "INV-CAP-EVENT-BACKUP-APPLIED", Case: "edge", State: "repeat", Assertion: "backup-applied-single-null-argument",
			Sources: []string{"server/managers/BackupManager.js:263-266", "server/SocketAuthority.js:57-67"},
			Claim:   "backup_applied carries exactly one argument serialized as JSON null: the source argument is omitted upstream and Socket.IO does not send an empty argument list or an object.",
			Run: func(t *testing.T, e *harness.Env) {
				backup := s.ensureBackup(t, e)
				socket := e.Socket(t, root(t, e))
				mark := socket.Mark()
				requireStatus(t, applyBackup(t, e, backup.ID), http.StatusOK, "apply backup")
				args := backupAppliedArgs(t, socket, mark, "argument shape")
				requireNullArgument(t, args, "argument shape")
			},
		},
		{
			Requirement: "cap-event-backup-applied", Invariant: "INV-CAP-EVENT-BACKUP-APPLIED", Case: "normal", State: "initial", Assertion: "backup-applied-first-apply-after-connect",
			Sources: []string{"server/managers/BackupManager.js:263-266", "server/SocketAuthority.js:57-67"},
			Claim:   "A socket connected after earlier applies still receives exactly one backup_applied for the first apply it observes, without backlog.",
			Run: func(t *testing.T, e *harness.Env) {
				backup := s.ensureBackup(t, e)
				socket := e.Socket(t, root(t, e))
				mark := socket.Mark()
				requireStatus(t, applyBackup(t, e, backup.ID), http.StatusOK, "apply backup")
				backupAppliedArgs(t, socket, mark, "first observed apply")
				if got := len(countEvents(socket, mark, "backup_applied")); got != 1 {
					t.Fatalf("first observed apply delivered %d events, want 1", got)
				}
			},
		},
		{
			Requirement: "cap-event-backup-applied", Invariant: "INV-CAP-EVENT-BACKUP-APPLIED", Case: "normal", State: "repeat", Assertion: "backup-applied-once-per-apply",
			Sources: []string{"server/managers/BackupManager.js:263-266"},
			Claim:   "Each successful apply emits its own backup_applied: two applies deliver exactly two events, one per apply.",
			Run: func(t *testing.T, e *harness.Env) {
				backup := s.ensureBackup(t, e)
				socket := e.Socket(t, root(t, e))
				mark := socket.Mark()
				requireStatus(t, applyBackup(t, e, backup.ID), http.StatusOK, "first apply")
				backupAppliedArgs(t, socket, mark, "first apply")
				second := socket.Mark()
				requireStatus(t, applyBackup(t, e, backup.ID), http.StatusOK, "second apply")
				backupAppliedArgs(t, socket, second, "second apply")
				if got := len(countEvents(socket, mark, "backup_applied")); got != 2 {
					t.Fatalf("two applies delivered %d events, want 2", got)
				}
			},
		},
		{
			Requirement: "cap-event-backup-applied", Invariant: "INV-CAP-EVENT-BACKUP-APPLIED", Case: "normal", State: "changed", Assertion: "backup-applied-not-replayed-to-late-socket",
			Sources: []string{"server/managers/BackupManager.js:263-266", "server/SocketAuthority.js:57-67"},
			Claim:   "A socket connected after an apply receives no replay of it, and receives the next apply's single event.",
			Run: func(t *testing.T, e *harness.Env) {
				backup := s.ensureBackup(t, e)
				requireStatus(t, applyBackup(t, e, backup.ID), http.StatusOK, "apply before connecting")
				late := e.Socket(t, root(t, e))
				late.None(t, 0, "backup_applied", 500*time.Millisecond, nil)
				mark := late.Mark()
				requireStatus(t, applyBackup(t, e, backup.ID), http.StatusOK, "apply after connecting")
				backupAppliedArgs(t, late, mark, "late socket")
			},
		},
		{
			Requirement: "cap-event-backup-applied", Invariant: "INV-CAP-EVENT-BACKUP-APPLIED", Case: "error", State: "failure", Assertion: "backup-applied-absent-on-failed-swap",
			Sources: []string{"server/managers/BackupManager.js:190-273", "server/managers/BackupManager.js:263-266"},
			Claim:   "An archive that cannot be applied (no absdatabase.sqlite) is rejected and emits no backup_applied, leaving the database and backup list unchanged.",
			Run: func(t *testing.T, e *harness.Env) {
				socket := e.Socket(t, root(t, e))
				mark := socket.Mark()
				before := len(getBackups(t, e, root(t, e)).Backups)
				hostile := zipWith(t, map[string][]byte{"details": []byte("2026-01-01T0000\nsqlite\n1\n2.37.2")})
				requireStatus(t, uploadBackup(t, e, "hostile.audiobookshelf", hostile), http.StatusInternalServerError, "upload archive without a database")
				socket.None(t, mark, "backup_applied", 500*time.Millisecond, nil)
				if after := len(getBackups(t, e, root(t, e)).Backups); after != before {
					t.Fatalf("failed upload changed backup count from %d to %d", before, after)
				}
				if got := libraryName(t, e, root(t, e), e.IDs["library"]); got != "Audiobooks" {
					t.Fatalf("failed upload changed library name to %q", got)
				}
			},
		},
	}
}

// socketEventScenarios characterize the socket-outgoing-backup-applied
// requirement: the recipient decision, argument list and repeat semantics of
// the same event from the socket contract's point of view.
func (s *suite) socketEventScenarios() []harness.Scenario {
	return []harness.Scenario{
		{
			Requirement: "socket-outgoing-backup-applied", Invariant: "INV-SOCKET-OUTGOING-BACKUP-APPLIED", Case: "normal", State: "initial", Assertion: "socket-backup-applied-audiences",
			Sources: []string{"server/managers/BackupManager.js:263-266", "server/SocketAuthority.js:52-68", "client/layouts/default.vue"},
			Claim:   "backup_applied is a broadcast to all authenticated sockets: two admin sockets and an ordinary socket each receive one, and an unauthenticated socket receives none.",
			Run: func(t *testing.T, e *harness.Env) {
				backup := s.ensureBackup(t, e)
				adminOne := e.Socket(t, root(t, e))
				adminTwo := e.Socket(t, root(t, e))
				ordinary := e.Socket(t, e.Token(t, "listener"))
				anonymous := e.Socket(t, "")
				marks := []int{adminOne.Mark(), adminTwo.Mark(), ordinary.Mark(), anonymous.Mark()}
				requireStatus(t, applyBackup(t, e, backup.ID), http.StatusOK, "apply backup")
				for i, socket := range []*harness.Socket{adminOne, adminTwo, ordinary} {
					backupAppliedArgs(t, socket, marks[i], "authenticated socket audience")
				}
				anonymous.None(t, marks[3], "backup_applied", 500*time.Millisecond, nil)
			},
		},
		{
			Requirement: "socket-outgoing-backup-applied", Invariant: "INV-SOCKET-OUTGOING-BACKUP-APPLIED", Case: "error", State: "failure", Assertion: "socket-backup-applied-non-admin-apply-silent",
			Sources: []string{"server/controllers/BackupController.js:160-180", "server/managers/BackupManager.js:263-266"},
			Claim:   "An ordinary user's apply attempt is forbidden and emits no backup_applied to an admin socket, an ordinary socket or an anonymous socket.",
			Run: func(t *testing.T, e *harness.Env) {
				admin := e.Socket(t, root(t, e))
				ordinary := e.Socket(t, e.Token(t, "listener"))
				anonymous := e.Socket(t, "")
				marks := []int{admin.Mark(), ordinary.Mark(), anonymous.Mark()}
				requireStatus(t, e.Get(t, e.Token(t, "listener"), "/api/backups/not-a-backup/apply"), http.StatusForbidden, "ordinary apply")
				for i, socket := range []*harness.Socket{admin, ordinary, anonymous} {
					socket.None(t, marks[i], "backup_applied", 500*time.Millisecond, nil)
				}
			},
		},
		{
			Requirement: "socket-outgoing-backup-applied", Invariant: "INV-SOCKET-OUTGOING-BACKUP-APPLIED", Case: "edge", State: "repeat", Assertion: "socket-backup-applied-argument-list",
			Sources: []string{"server/managers/BackupManager.js:263-266", "server/SocketAuthority.js:57-67", "client/layouts/default.vue"},
			Claim:   "The event's argument list is exactly one JSON null for both an admin and a non-admin recipient, matching upstream's emit without a data argument.",
			Run: func(t *testing.T, e *harness.Env) {
				backup := s.ensureBackup(t, e)
				admin := e.Socket(t, root(t, e))
				ordinary := e.Socket(t, e.Token(t, "listener"))
				adminMark, ordinaryMark := admin.Mark(), ordinary.Mark()
				requireStatus(t, applyBackup(t, e, backup.ID), http.StatusOK, "apply backup")
				requireNullArgument(t, backupAppliedArgs(t, admin, adminMark, "admin"), "admin")
				requireNullArgument(t, backupAppliedArgs(t, ordinary, ordinaryMark, "ordinary"), "ordinary")
			},
		},
		{
			Requirement: "socket-outgoing-backup-applied", Invariant: "INV-SOCKET-OUTGOING-BACKUP-APPLIED", Case: "normal", State: "initial", Assertion: "socket-backup-applied-after-connect",
			Sources: []string{"server/managers/BackupManager.js:263-266", "server/SocketAuthority.js:57-67"},
			Claim:   "A freshly connected admin socket receives the next apply's single event at its current position, with no earlier event replayed.",
			Run: func(t *testing.T, e *harness.Env) {
				backup := s.ensureBackup(t, e)
				socket := e.Socket(t, root(t, e))
				socket.None(t, 0, "backup_applied", 300*time.Millisecond, nil)
				mark := socket.Mark()
				requireStatus(t, applyBackup(t, e, backup.ID), http.StatusOK, "apply backup")
				backupAppliedArgs(t, socket, mark, "fresh socket")
			},
		},
		{
			Requirement: "socket-outgoing-backup-applied", Invariant: "INV-SOCKET-OUTGOING-BACKUP-APPLIED", Case: "normal", State: "repeat", Assertion: "socket-backup-applied-each-apply-one-event",
			Sources: []string{"server/managers/BackupManager.js:263-266"},
			Claim:   "Two applies in a row deliver one event each to the same socket, in the order the applies completed.",
			Run: func(t *testing.T, e *harness.Env) {
				backup := s.ensureBackup(t, e)
				socket := e.Socket(t, root(t, e))
				first := socket.Mark()
				requireStatus(t, applyBackup(t, e, backup.ID), http.StatusOK, "first apply")
				backupAppliedArgs(t, socket, first, "first of two")
				second := socket.Mark()
				requireStatus(t, applyBackup(t, e, backup.ID), http.StatusOK, "second apply")
				backupAppliedArgs(t, socket, second, "second of two")
				if got := len(countEvents(socket, first, "backup_applied")); got != 2 {
					t.Fatalf("two applies delivered %d events, want 2", got)
				}
			},
		},
		{
			Requirement: "socket-outgoing-backup-applied", Invariant: "INV-SOCKET-OUTGOING-BACKUP-APPLIED", Case: "normal", State: "changed", Assertion: "socket-backup-applied-late-socket-no-replay",
			Sources: []string{"server/managers/BackupManager.js:263-266", "server/SocketAuthority.js:57-67"},
			Claim:   "A socket connected after an apply does not receive that past event, but does receive the next apply's event.",
			Run: func(t *testing.T, e *harness.Env) {
				backup := s.ensureBackup(t, e)
				requireStatus(t, applyBackup(t, e, backup.ID), http.StatusOK, "apply before connect")
				late := e.Socket(t, root(t, e))
				late.None(t, 0, "backup_applied", 500*time.Millisecond, nil)
				mark := late.Mark()
				requireStatus(t, applyBackup(t, e, backup.ID), http.StatusOK, "apply after connect")
				backupAppliedArgs(t, late, mark, "late socket")
			},
		},
		{
			Requirement: "socket-outgoing-backup-applied", Invariant: "INV-SOCKET-OUTGOING-BACKUP-APPLIED", Case: "error", State: "failure", Assertion: "socket-backup-applied-absent-on-failure",
			Sources: []string{"server/managers/BackupManager.js:190-273", "server/managers/BackupManager.js:263-266"},
			Claim:   "A rejected archive and an unknown backup id emit no backup_applied to admin, ordinary or anonymous sockets.",
			Run: func(t *testing.T, e *harness.Env) {
				admin := e.Socket(t, root(t, e))
				ordinary := e.Socket(t, e.Token(t, "listener"))
				anonymous := e.Socket(t, "")
				marks := []int{admin.Mark(), ordinary.Mark(), anonymous.Mark()}
				hostile := zipWith(t, map[string][]byte{"details": []byte("2026-01-01T0000\nsqlite\n1\n2.37.2")})
				requireStatus(t, uploadBackup(t, e, "hostile.audiobookshelf", hostile), http.StatusInternalServerError, "rejected archive")
				requireStatus(t, e.Get(t, root(t, e), "/api/backups/not-a-backup/apply"), http.StatusNotFound, "unknown backup id")
				for i, socket := range []*harness.Socket{admin, ordinary, anonymous} {
					socket.None(t, marks[i], "backup_applied", 500*time.Millisecond, nil)
				}
			},
		},
	}
}
