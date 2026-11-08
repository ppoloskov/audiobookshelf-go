package realtime

func (h *Hub) handleSearchCovers(s *Socket, args []any, ack Ack) {
	id := s.ID()
	user, authenticated := h.userFor(id)
	raw := first(args)
	payload := object(raw)
	if !authenticated {
		h.emitCoverError(s, payload["requestId"], "Unauthorized")
		return
	}
	requestID, _ := payload["requestId"].(string)
	title, _ := payload["title"].(string)
	if requestID == "" || title == "" {
		h.emitCoverError(s, payload["requestId"], "Invalid request parameters")
		return
	}
	h.dispatchAction(s, user, "search_covers", payload, func(event string, value any) {
		s.Emit(event, value)
	}, ack)
}

func (h *Hub) handleCancelCoverSearch(s *Socket, args []any, ack Ack) {
	user, ok := h.userFor(s.ID())
	if !ok {
		return
	}
	h.dispatchAction(s, user, "cancel_cover_search", first(args), func(event string, value any) {
		s.Emit(event, value)
	}, ack)
}

func (h *Hub) emitCoverError(s *Socket, requestID any, message string) {
	s.Emit("cover_search_error", map[string]any{"requestId": requestID, "error": message})
}
