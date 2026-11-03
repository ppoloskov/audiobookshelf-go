// Package events defines how feature packages publish Socket.IO events. It is
// a leaf package: the composition root supplies the implementation (the
// realtime hub), and packages only see this function type.
package events

// Emitter publishes event with payload to the sockets selected by userID: a
// user id, "*" for every authenticated user, or "@admins" for administrators.
// The hub decides which connected sockets match.
type Emitter func(userID, event string, payload any)
