package events

import (
	"encoding/json"
	"testing"
)

func TestEmitterAudiencesAndNilEmitter(t *testing.T) {
	var audiences, names []string
	e := Emitter(func(audience, event string, _ any) {
		audiences = append(audiences, audience)
		names = append(names, event)
	})
	e.ToUser("u1", "a", nil)
	e.ToAdmins("b", nil)
	e.ToAll("c", nil)
	e.TaskStarted(NewTask("t", "x", nil))
	e.TaskFinished(NewTask("t", "x", nil))
	wantAud := []string{"u1", Admins, AllUsers, AllUsers, AllUsers}
	wantNames := []string{"a", "b", "c", "task_started", "task_finished"}
	for i := range wantAud {
		if audiences[i] != wantAud[i] || names[i] != wantNames[i] {
			t.Fatalf("delivery %d = %s/%s, want %s/%s", i, audiences[i], names[i], wantAud[i], wantNames[i])
		}
	}
	var none Emitter
	none.ToAll("ignored", nil) // a nil emitter discards
	none.TaskStarted(Task{})
}

// The task record the client renders (server/objects/Task.js toJSON).
func TestTaskLifecycleJSON(t *testing.T) {
	task := NewTask("id1", "scan", map[string]any{"libraryId": "l1"}).Titled("Scanning", "MessageTaskScan", []string{"Books"})
	started := task
	task = task.Described("working", "MessageDesc", 3).Finished()
	failed := task.Failed("boom", "MessageErr")

	if started.IsFinished || started.FinishedAt != nil || started.Description != nil {
		t.Fatalf("an emitted task is a snapshot; later changes must not reach it: %+v", started)
	}
	var doc map[string]any
	raw, _ := json.Marshal(failed)
	if err := json.Unmarshal(raw, &doc); err != nil {
		t.Fatal(err)
	}
	for key, want := range map[string]any{
		"id": "id1", "action": "scan", "title": "Scanning", "titleKey": "MessageTaskScan",
		"description": "working", "descriptionKey": "MessageDesc", "error": "boom", "errorKey": "MessageErr",
		"isFinished": true, "isFailed": true, "showSuccess": false,
	} {
		if doc[key] != want {
			t.Errorf("%s = %#v, want %#v", key, doc[key], want)
		}
	}
	if doc["finishedAt"] == nil || doc["startedAt"] == nil {
		t.Errorf("startedAt/finishedAt missing: %v", doc)
	}
	if subs, _ := doc["descriptionSubs"].([]any); len(subs) != 1 || subs[0] != float64(3) {
		t.Errorf("descriptionSubs = %v", doc["descriptionSubs"])
	}
}
