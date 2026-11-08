// Package events defines how feature packages publish Socket.IO events. It is
// a leaf package: the composition root supplies the delivery function (the
// realtime hub), and packages only see Emitter and Task.
package events

import "time"

// Audience values understood by the delivery function behind an Emitter.
const (
	AllUsers = "*"
	Admins   = "@admins"
)

// Emitter publishes events to an audience through a delivery function (the
// realtime hub in production). The nil Emitter discards every event, so a
// feature never checks for a missing emitter. The hub decides which connected
// sockets match the audience.
type Emitter func(audience, event string, payload any)

func (e Emitter) emit(audience, event string, payload any) {
	if e != nil {
		e(audience, event, payload)
	}
}

func (e Emitter) ToUser(userID, event string, payload any) { e.emit(userID, event, payload) }

// ToAdmins sends the event to administrators and root.
func (e Emitter) ToAdmins(event string, payload any) { e.emit(Admins, event, payload) }

func (e Emitter) ToAll(event string, payload any) { e.emit(AllUsers, event, payload) }

func (e Emitter) TaskStarted(t Task) { e.ToAll("task_started", t) }

func (e Emitter) TaskFinished(t Task) { e.ToAll("task_finished", t) }

// Task is the client's background-task record (server/objects/Task.js
// toJSON), shared by the scanner, audio tools, library match and podcasts.
// It is a value: an emitted copy is a snapshot that later changes do not reach.
type Task struct {
	ID              string   `json:"id"`
	Action          string   `json:"action"`
	Data            any      `json:"data"`
	Title           string   `json:"title"`
	TitleKey        string   `json:"titleKey"`
	TitleSubs       []string `json:"titleSubs"`
	Description     *string  `json:"description"`
	DescriptionKey  *string  `json:"descriptionKey"`
	DescriptionSubs []any    `json:"descriptionSubs"`
	Error           *string  `json:"error"`
	ErrorKey        *string  `json:"errorKey"`
	ErrorSubs       []string `json:"errorSubs"`
	ShowSuccess     bool     `json:"showSuccess"`
	IsFailed        bool     `json:"isFailed"`
	IsFinished      bool     `json:"isFinished"`
	StartedAt       int64    `json:"startedAt"`
	FinishedAt      *int64   `json:"finishedAt"`
}

func NewTask(id, action string, data any) Task {
	return Task{ID: id, Action: action, Data: data, StartedAt: time.Now().UnixMilli()}
}

// Titled sets the title and its translation key and substitutions.
func (t Task) Titled(title, key string, subs []string) Task {
	t.Title, t.TitleKey, t.TitleSubs = title, key, subs
	return t
}

// Described sets the description, replacing an earlier one.
func (t Task) Described(text, key string, subs ...any) Task {
	t.Description, t.DescriptionKey, t.DescriptionSubs = &text, &key, subs
	return t
}

func (t Task) Finished() Task {
	now := time.Now().UnixMilli()
	t.IsFinished, t.FinishedAt = true, &now
	return t
}

// Failed marks the task failed with an error message and its key.
func (t Task) Failed(text, key string) Task {
	t.IsFailed, t.Error, t.ErrorKey = true, &text, &key
	return t
}
