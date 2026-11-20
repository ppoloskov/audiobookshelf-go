package realtime

func (h *Hub) handleSearchCovers(s *Socket, args []any) {
	id := s.ID()
	search, ok := decode[CoverSearch](id, "search_covers", args)
	if _, authenticated := h.userFor(id); !authenticated {
		s.emitCoverError(search.RequestID, "Unauthorized")
		return
	}
	if !ok || search.RequestID == "" || search.Title == "" {
		s.emitCoverError(search.RequestID, "Invalid request parameters")
		return
	}
	h.backend().SearchCovers(search, s.reply)
}

func (h *Hub) handleCancelCoverSearch(s *Socket, args []any) {
	if _, ok := h.userFor(s.ID()); !ok {
		return
	}
	if requestID, ok := decode[string](s.ID(), "cancel_cover_search", args); ok {
		h.backend().CancelCoverSearch(requestID, s.reply)
	}
}

// reply is the Reply that answers on this socket.
func (s *Socket) reply(event string, payload any) { s.Emit(event, payload) }

// emitCoverError tells the client its cover search failed; an empty request id
// is sent as null.
func (s *Socket) emitCoverError(requestID, message string) {
	var id *string
	if requestID != "" {
		id = &requestID
	}
	s.Emit("cover_search_error", struct {
		RequestID *string `json:"requestId"`
		Error     string  `json:"error"`
	}{id, message})
}
