package catalog

import (
	"audiobookshelf-compat/internal/access"
	"audiobookshelf-compat/internal/dbtime"
	"audiobookshelf-compat/internal/filekind"
	"audiobookshelf-compat/internal/httputil"
	"audiobookshelf-compat/internal/items"
	"audiobookshelf-compat/internal/jsonvalue"
	"audiobookshelf-compat/internal/logging"
	"audiobookshelf-compat/internal/mediafiles"
	"audiobookshelf-compat/internal/mediatools"
	"audiobookshelf-compat/internal/store"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"sort"
	"strings"
)

func (h *Handler) updateChapters(w http.ResponseWriter, r *http.Request) error {
	user := store.RequestUser(r)
	itemID := r.PathValue("id")
	scope, err := h.itemFor(r.Context(), user, itemID, true)
	if err != nil {
		return err
	}
	if scope.mediaType != "book" {
		logging.RequestDebug(r, "responding 500: the library item is not a book")
		return httputil.Status(http.StatusInternalServerError, "")
	}
	var missing, audioCount int
	var currentRaw []byte
	if err := h.db.QueryRowContext(r.Context(), `SELECT li.isMissing,(SELECT COUNT(*) FROM json_each(COALESCE(b.audioFiles,'[]')) WHERE COALESCE(json_extract(json_each.value,'$.exclude'),0)=0),b.chapters FROM libraryItems li JOIN books b ON b.id=li.mediaId WHERE li.id=?`, itemID).Scan(&missing, &audioCount, &currentRaw); err != nil {
		return httputil.Internal("database read failed", err)
	}
	if missing != 0 || audioCount == 0 {
		logging.RequestDebug(r, fmt.Sprintf("responding 500: library item has %d missing and %d audio files", missing, audioCount))
		return httputil.Status(http.StatusInternalServerError, "")
	}
	var request struct {
		Chapters json.RawMessage `json:"chapters"`
	}
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 2<<20)).Decode(&request); err != nil {
		return httputil.Status(http.StatusBadRequest, "Invalid payload")
	}
	chapterJSON := strings.TrimSpace(string(request.Chapters))
	if chapterJSON == "" || chapterJSON == "null" || chapterJSON[0] != '[' {
		return httputil.Status(http.StatusBadRequest, "Invalid payload")
	}
	var chapterRows []struct {
		Title string  `json:"title"`
		Start float64 `json:"start"`
		End   float64 `json:"end"`
	}
	if err := json.Unmarshal(request.Chapters, &chapterRows); err != nil {
		return httputil.Status(http.StatusBadRequest, "Invalid payload")
	}
	for _, chapter := range chapterRows {
		if chapter.Title == "" {
			return httputil.Status(http.StatusBadRequest, "Invalid payload")
		}
	}
	var previous []map[string]any
	if err := json.Unmarshal(currentRaw, &previous); err != nil {
		previous = nil
	}
	chapters := make([]map[string]any, len(chapterRows))
	updated := len(previous) != len(chapterRows)
	for i, chapter := range chapterRows {
		id := i
		if len(previous) == len(chapterRows) {
			if old, ok := previous[i]["id"]; ok {
				chapters[i] = map[string]any{"id": old, "title": chapter.Title, "start": chapter.Start, "end": chapter.End}
			}
		}
		if chapters[i] == nil {
			chapters[i] = map[string]any{"id": id, "title": chapter.Title, "start": chapter.Start, "end": chapter.End}
		}
		if i >= len(previous) || previous[i]["title"] != chapter.Title || jsonvalue.Float(previous[i]["start"]) != chapter.Start || jsonvalue.Float(previous[i]["end"]) != chapter.End {
			updated = true
		}
	}
	if updated {
		encoded, _ := json.Marshal(chapters)
		now := dbtime.Now()
		if _, err := h.db.ExecContext(r.Context(), `UPDATE books SET chapters=?,updatedAt=? WHERE id=?`, string(encoded), now, scope.mediaID); err != nil {
			return httputil.Internal("database write failed", err)
		}
		if err := h.ItemChanged(r.Context(), itemID); err != nil {
			return httputil.Internal("metadata file write failed", err)
		}
	}
	return httputil.JSON(w, r, map[string]any{"success": true, "updated": updated})
}

func (h *Handler) metadataObject(w http.ResponseWriter, r *http.Request) error {
	user := store.RequestUser(r)
	itemID := r.PathValue("id")
	if !access.AdminUser(user) {
		return httputil.Status(http.StatusForbidden, "")
	}
	scope, err := h.itemFor(r.Context(), user, itemID, false)
	if err != nil {
		return err
	}
	var audioRaw []byte
	if scope.mediaType == "book" {
		if err := h.db.QueryRowContext(r.Context(), `SELECT audioFiles FROM books WHERE id=?`, scope.mediaID).Scan(&audioRaw); err != nil {
			return httputil.Internal("database read failed", err)
		}
	}
	audioCount := 0
	for _, file := range store.DecodeJSONSlice[mediafiles.File](audioRaw) {
		if !file.Excluded() {
			audioCount++
		}
	}
	if scope.missing || scope.mediaType != "book" || audioCount == 0 {
		return httputil.Status(http.StatusBadRequest, "")
	}
	item, err := items.LoadOne(r.Context(), h.db, itemID, items.Options{})
	if err != nil || item.Book == nil {
		return httputil.Internal("database read failed", errors.Join(err, fmt.Errorf("item %s is not a book", itemID)))
	}
	metadata := item.Book
	authorNames := make([]string, 0, len(metadata.Authors))
	for _, author := range metadata.Authors {
		authorNames = append(authorNames, author.Name)
	}
	values := map[string]any{
		"title": store.StringPtrValue(metadata.Title), "artist": strings.Join(authorNames, ", "), "album_artist": strings.Join(authorNames, ", "),
		"album": stringPointerValue(metadata.Title) + func() string {
			if subtitle := stringPointerValue(metadata.Subtitle); subtitle != "" {
				return ": " + subtitle
			}
			return ""
		}(),
		"TIT3": store.StringPtrValue(metadata.Subtitle), "genre": strings.Join(metadata.Genres, "; "),
		"date": store.StringPtrValue(metadata.PublishedYear), "comment": store.StringPtrValue(metadata.Description), "description": store.StringPtrValue(metadata.Description),
		"composer": strings.Join(metadata.Narrators, ", "), "copyright": store.StringPtrValue(metadata.Publisher), "publisher": store.StringPtrValue(metadata.Publisher),
		"TRACKTOTAL": fmt.Sprint(audioCount),
	}
	groups := make([]string, 0, len(metadata.Series))
	for _, entry := range metadata.Series {
		name := entry.Name
		if entry.Sequence != nil && *entry.Sequence != "" {
			name += " #" + *entry.Sequence
		}
		groups = append(groups, name)
	}
	values["grouping"] = strings.Join(groups, "; ")
	for key, value := range values {
		if value == nil || value == "" {
			delete(values, key)
		}
	}
	return httputil.JSON(w, r, values)
}

func (h *Handler) ffprobe(w http.ResponseWriter, r *http.Request) error {
	user := store.RequestUser(r)
	itemID := r.PathValue("id")
	ino := r.PathValue("ino")
	if !access.AdminUser(user) {
		return httputil.Status(http.StatusForbidden, "")
	}
	scope, err := h.itemFor(r.Context(), user, itemID, false)
	if err != nil {
		return err
	}
	var filesRaw []byte
	if scope.mediaType == "book" {
		if err := h.db.QueryRowContext(r.Context(), `SELECT audioFiles FROM books WHERE id=?`, scope.mediaID).Scan(&filesRaw); err != nil {
			return httputil.Internal("database read failed", err)
		}
	} else if scope.mediaType == "podcast" {
		if err := h.db.QueryRowContext(r.Context(), `SELECT COALESCE(json_group_array(json(audioFile)), '[]') FROM podcastEpisodes WHERE podcastId=?`, scope.mediaID).Scan(&filesRaw); err != nil {
			return httputil.Internal("database read failed", err)
		}
	}
	target := ""
	for _, file := range store.DecodeJSONSlice[mediafiles.File](filesRaw) {
		if file.Ino() == ino {
			target = file.Path()
			break
		}
		if audio, ok := file["audioFile"].(map[string]any); ok && mediafiles.File(audio).Ino() == ino {
			target = mediafiles.File(audio).Path()
			break
		}
	}
	if target == "" {
		return httputil.Status(http.StatusNotFound, "")
	}
	if !h.pathAllowed(r.Context(), target, scope.libraryID) {
		return httputil.Status(http.StatusNotFound, "")
	}
	result, err := mediatools.ProbeRaw(r.Context(), target)
	if err != nil {
		return httputil.Internal("ffprobe failed", err)
	}
	return httputil.JSON(w, r, result)
}

func (h *Handler) updateTracks(w http.ResponseWriter, r *http.Request) error {
	user := store.RequestUser(r)
	itemID := r.PathValue("id")
	scope, err := h.itemFor(r.Context(), user, itemID, true)
	if err != nil {
		return err
	}
	if scope.mediaType != "book" {
		return httputil.Status(http.StatusBadRequest, "")
	}
	var request struct {
		Ordered []map[string]any `json:"orderedFileData"`
	}
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 2<<20)).Decode(&request); err != nil {
		return httputil.Status(http.StatusBadRequest, "")
	}
	if len(request.Ordered) == 0 {
		return httputil.Status(http.StatusBadRequest, "")
	}
	var raw []byte
	if err := h.db.QueryRowContext(r.Context(), `SELECT audioFiles FROM books WHERE id=?`, scope.mediaID).Scan(&raw); err != nil {
		return httputil.Internal("database read failed", err)
	}
	files := store.DecodeJSONSlice[mediafiles.File](raw)
	byIno := make(map[string]mediafiles.File, len(files))
	for _, file := range files {
		byIno[file.Ino()] = file
	}
	for _, ordered := range request.Ordered {
		ino := mediafiles.File(ordered).Ino()
		if ino == "" || byIno[ino] == nil {
			return httputil.Status(http.StatusBadRequest, "")
		}
	}
	orderedFiles := make([]mediafiles.File, 0, len(request.Ordered))
	index := 1
	for _, ordered := range request.Ordered {
		file := byIno[mediafiles.File(ordered).Ino()]
		file["manuallyVerified"] = true
		excluded := jsonvalue.Truthy(ordered["exclude"])
		file["exclude"] = excluded
		if excluded {
			file["index"] = -1
		} else {
			file["index"] = index
			index++
		}
		orderedFiles = append(orderedFiles, file)
	}
	sort.SliceStable(orderedFiles, func(i, j int) bool {
		return jsonvalue.Float(orderedFiles[i]["index"]) < jsonvalue.Float(orderedFiles[j]["index"])
	})
	encoded, _ := json.Marshal(orderedFiles)
	if _, err := h.db.ExecContext(r.Context(), `UPDATE books SET audioFiles=?,updatedAt=? WHERE id=?`, string(encoded), dbtime.Now(), scope.mediaID); err != nil {
		return httputil.Internal("database write failed", err)
	}
	item, err := h.expandedItem(r.Context(), itemID)
	if err != nil {
		return httputil.Internal("database read failed", err)
	}
	h.emit.ToAll("item_updated", item)
	return httputil.JSON(w, r, item)
}

func (h *Handler) updateEbookStatus(w http.ResponseWriter, r *http.Request) error {
	user := store.RequestUser(r)
	itemID := r.PathValue("id")
	ino := r.PathValue("ino")
	scope, err := h.itemFor(r.Context(), user, itemID, true)
	if err != nil {
		return err
	}
	if scope.mediaType != "book" {
		return httputil.Status(http.StatusBadRequest, "")
	}
	var rawFiles, rawAudio []byte
	if err := h.db.QueryRowContext(r.Context(), `SELECT li.libraryFiles,b.audioFiles FROM libraryItems li JOIN books b ON b.id=li.mediaId WHERE li.id=?`, itemID).Scan(&rawFiles, &rawAudio); err != nil {
		return httputil.Internal("database read failed", err)
	}
	files := store.DecodeJSONSlice[mediafiles.File](rawFiles)
	selected := -1
	for i, file := range files {
		if file.Ino() == ino {
			selected = i
			break
		}
	}
	if selected < 0 || files[selected].Kind() != filekind.Ebook {
		return httputil.Status(http.StatusBadRequest, "Invalid ebook file id")
	}
	var primary any
	if jsonvalue.Truthy(files[selected]["isSupplementary"]) {
		primaryFile := make(mediafiles.File, len(files[selected]))
		for key, value := range files[selected] {
			if key != "isSupplementary" && key != "fileType" {
				primaryFile[key] = value
			}
		}
		// TS FileMetadata.format: the extension without its dot, lower-cased; "" without an extension.
		primaryFile["ebookFormat"] = strings.ToLower(strings.TrimPrefix(primaryFile.Ext(), "."))
		primary = primaryFile
	}
	primaryIno := ""
	if m, ok := primary.(mediafiles.File); ok {
		primaryIno = m.Ino()
	}
	for _, file := range files {
		if file.Kind() == filekind.Ebook {
			file["isSupplementary"] = file.Ino() != primaryIno
		}
	}
	filesJSON, _ := json.Marshal(files)
	encodedPrimary, _ := json.Marshal(primary)
	missing := 1
	for _, audio := range store.DecodeJSONSlice[mediafiles.File](rawAudio) {
		if !audio.Excluded() {
			missing = 0
			break
		}
	}
	if primary != nil {
		missing = 0
	}
	if _, err := h.db.ExecContext(r.Context(), `UPDATE books SET ebookFile=?,updatedAt=? WHERE id=?`, string(encodedPrimary), dbtime.Now(), scope.mediaID); err != nil {
		return httputil.Internal("database write failed", err)
	}
	if _, err := h.db.ExecContext(r.Context(), `UPDATE libraryItems SET libraryFiles=?,isMissing=?,updatedAt=? WHERE id=?`, string(filesJSON), missing, dbtime.Now(), itemID); err != nil {
		return httputil.Internal("database write failed", err)
	}
	if err := h.itemUpdated(r.Context(), itemID); err != nil {
		return httputil.Internal("database read failed", err)
	}
	w.WriteHeader(http.StatusOK)
	return nil
}

func stringPointerValue(value *string) string {
	if value == nil {
		return ""
	}
	return *value
}
