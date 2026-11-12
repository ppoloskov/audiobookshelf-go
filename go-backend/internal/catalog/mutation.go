package catalog

import (
	"audiobookshelf-compat/internal/itemwrite"
)

func (h *Handler) env() itemwrite.Env {
	return itemwrite.Env{DB: h.db, Filters: h.filters, Emit: h.emit}
}
