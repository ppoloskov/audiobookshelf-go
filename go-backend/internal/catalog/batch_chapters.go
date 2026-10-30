package catalog

import (
	"audiobookshelf-compat/internal/dbtime"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"os"
	"os/exec"
	"sort"
	"strings"

	"audiobookshelf-compat/internal/store"
)

func canUpdateCatalog(user store.User) bool {
	var permissions map[string]any
	_ = json.Unmarshal(user.Permissions, &permissions)
	return user.IsActive && permissions["update"] == true
}

func (h *Handler) batchGet(w http.ResponseWriter, r *http.Request, user store.User) {
	var request struct {
		IDs []string `json:"libraryItemIds"`
	}
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 1<<20)).Decode(&request); err != nil || len(request.IDs) == 0 {
		writeError(w, http.StatusForbidden, "Invalid payload")
		return
	}
	ids := make([]string, 0, len(request.IDs))
	seen := map[string]bool{}
	for _, id := range request.IDs {
		if id != "" && !seen[id] {
			ids = append(ids, id)
			seen[id] = true
		}
	}
	if len(ids) == 0 {
		writeError(w, http.StatusForbidden, "Invalid payload")
		return
	}
	args := make([]any, len(ids))
	for i, id := range ids {
		args[i] = id
	}
	rows, err := h.db.QueryContext(r.Context(), `SELECT id,libraryId,mediaType FROM libraryItems WHERE id IN (`+placeholders(len(ids))+`)`, args...)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "database read failed")
		return
	}
	type access struct{ id, libraryID, mediaType string }
	items := make(map[string]access, len(ids))
	for rows.Next() {
		var item access
		if err := rows.Scan(&item.id, &item.libraryID, &item.mediaType); err != nil {
			rows.Close()
			writeError(w, http.StatusInternalServerError, "database read failed")
			return
		}
		items[item.id] = item
	}
	if err := rows.Err(); err != nil {
		rows.Close()
		writeError(w, http.StatusInternalServerError, "database read failed")
		return
	}
	rows.Close()
	selected := make([]map[string]any, 0, len(items))
	for _, id := range ids {
		ref, ok := items[id]
		if !ok {
			continue
		}
		if !userCanAccessLibrary(user, ref.libraryID) {
			writeError(w, http.StatusForbidden, "")
			return
		}
		mediaAlias := "b"
		if ref.mediaType == "podcast" {
			mediaAlias = "p"
		}
		permission, permissionArgs := userMediaClause(user, ref.mediaType)
		var visible int
		query := `SELECT COUNT(*) FROM libraryItems li JOIN ` + map[string]string{"book": "books", "podcast": "podcasts"}[ref.mediaType] + ` ` + mediaAlias + ` ON ` + mediaAlias + `.id=li.mediaId WHERE li.id=?` + permission
		if _, exists := map[string]string{"book": "books", "podcast": "podcasts"}[ref.mediaType]; !exists {
			writeError(w, http.StatusForbidden, "")
			return
		}
		queryArgs := append([]any{id}, permissionArgs...)
		if err := h.db.QueryRowContext(r.Context(), query, queryArgs...).Scan(&visible); err != nil {
			writeError(w, http.StatusInternalServerError, "database read failed")
			return
		}
		if visible == 0 {
			writeError(w, http.StatusForbidden, "")
			return
		}
		item, err := h.ItemJSON(r.Context(), id, true)
		if err != nil {
			writeError(w, http.StatusInternalServerError, "database read failed")
			return
		}
		selected = append(selected, item)
	}
	writeJSON(w, r, http.StatusOK, map[string]any{"libraryItems": selected})
}

func (h *Handler) updateChapters(w http.ResponseWriter, r *http.Request, user store.User, itemID string) {
	scope, _, err := h.writableItem(r.Context(), itemID, user)
	if errors.Is(err, sql.ErrNoRows) {
		writeError(w, http.StatusNotFound, "")
		return
	}
	if err != nil {
		writeError(w, http.StatusInternalServerError, "database read failed")
		return
	}
	if !userCanAccessLibrary(user, scope.libraryID) {
		writeError(w, http.StatusForbidden, "")
		return
	}
	if !canUpdateCatalog(user) {
		writeError(w, http.StatusForbidden, "")
		return
	}
	if scope.mediaType != "book" {
		writeError(w, http.StatusInternalServerError, "")
		return
	}
	var missing, audioCount int
	var currentRaw []byte
	if err := h.db.QueryRowContext(r.Context(), `SELECT li.isMissing,(SELECT COUNT(*) FROM json_each(COALESCE(b.audioFiles,'[]')) WHERE COALESCE(json_extract(json_each.value,'$.exclude'),0)=0),b.chapters FROM libraryItems li JOIN books b ON b.id=li.mediaId WHERE li.id=?`, itemID).Scan(&missing, &audioCount, &currentRaw); err != nil {
		writeError(w, http.StatusInternalServerError, "database read failed")
		return
	}
	if missing != 0 || audioCount == 0 {
		writeError(w, http.StatusInternalServerError, "")
		return
	}
	var request struct {
		Chapters json.RawMessage `json:"chapters"`
	}
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 2<<20)).Decode(&request); err != nil {
		writeError(w, http.StatusBadRequest, "Invalid payload")
		return
	}
	chapterJSON := strings.TrimSpace(string(request.Chapters))
	if chapterJSON == "" || chapterJSON == "null" || chapterJSON[0] != '[' {
		writeError(w, http.StatusBadRequest, "Invalid payload")
		return
	}
	var chapterRows []struct {
		Title string  `json:"title"`
		Start float64 `json:"start"`
		End   float64 `json:"end"`
	}
	if err := json.Unmarshal(request.Chapters, &chapterRows); err != nil {
		writeError(w, http.StatusBadRequest, "Invalid payload")
		return
	}
	for _, chapter := range chapterRows {
		if chapter.Title == "" {
			writeError(w, http.StatusBadRequest, "Invalid payload")
			return
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
		if i >= len(previous) || previous[i]["title"] != chapter.Title || floatValue(previous[i]["start"]) != chapter.Start || floatValue(previous[i]["end"]) != chapter.End {
			updated = true
		}
	}
	if updated {
		encoded, _ := json.Marshal(chapters)
		now := dbtime.Now()
		if _, err := h.db.ExecContext(r.Context(), `UPDATE books SET chapters=?,updatedAt=? WHERE id=?`, string(encoded), now, scope.mediaID); err != nil {
			writeError(w, http.StatusInternalServerError, "database write failed")
			return
		}
		if h.itemChanged != nil {
			if err := h.itemChanged(r.Context(), itemID); err != nil {
				writeError(w, http.StatusInternalServerError, "metadata file write failed")
				return
			}
		}
		if h.emit != nil {
			if item, err := h.ItemJSON(r.Context(), itemID, true); err == nil {
				h.emit("*", "item_updated", item)
			}
		}
	}
	writeJSON(w, r, http.StatusOK, map[string]any{"success": true, "updated": updated})
}

func (h *Handler) metadataObject(w http.ResponseWriter, r *http.Request, user store.User, itemID string) {
	if user.Type != "root" && user.Type != "admin" {
		writeError(w, http.StatusForbidden, "")
		return
	}
	var libraryID, mediaID, mediaType string
	var missing int
	err := h.db.QueryRowContext(r.Context(), `SELECT libraryId,mediaId,mediaType,isMissing FROM libraryItems WHERE id=?`, itemID).Scan(&libraryID, &mediaID, &mediaType, &missing)
	if errors.Is(err, sql.ErrNoRows) {
		writeError(w, http.StatusNotFound, "")
		return
	}
	if err != nil {
		writeError(w, http.StatusInternalServerError, "database read failed")
		return
	}
	if !userCanAccessLibrary(user, libraryID) {
		writeError(w, http.StatusForbidden, "")
		return
	}
	var audioRaw []byte
	if mediaType == "book" {
		if err := h.db.QueryRowContext(r.Context(), `SELECT audioFiles FROM books WHERE id=?`, mediaID).Scan(&audioRaw); err != nil {
			writeError(w, http.StatusInternalServerError, "database read failed")
			return
		}
	}
	audioCount := 0
	for _, file := range decodeArray(audioRaw) {
		if excluded, _ := file["exclude"].(bool); !excluded {
			audioCount++
		}
	}
	if missing != 0 || mediaType != "book" || audioCount == 0 {
		writeError(w, http.StatusBadRequest, "")
		return
	}
	item, err := h.ItemJSON(r.Context(), itemID, false)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "database read failed")
		return
	}
	media, _ := item["media"].(map[string]any)
	metadata, _ := media["metadata"].(map[string]any)
	authors, _ := metadata["authors"].([]map[string]any)
	authorNames := make([]string, 0, len(authors))
	for _, author := range authors {
		authorNames = append(authorNames, stringValue(author["name"]))
	}
	values := map[string]any{
		"title": metadata["title"], "artist": strings.Join(authorNames, ", "), "album_artist": strings.Join(authorNames, ", "),
		"album": stringValue(metadata["title"]) + func() string {
			if subtitle := stringValue(metadata["subtitle"]); subtitle != "" {
				return ": " + subtitle
			}
			return ""
		}(),
		"TIT3": metadata["subtitle"], "genre": strings.Join(anyStrings(metadata["genres"]), "; "),
		"date": metadata["publishedYear"], "comment": metadata["description"], "description": metadata["description"],
		"composer": strings.Join(anyStrings(metadata["narrators"]), ", "), "copyright": metadata["publisher"], "publisher": metadata["publisher"],
		"TRACKTOTAL": fmt.Sprint(audioCount),
	}
	series, _ := metadata["series"].([]map[string]any)
	groups := make([]string, 0, len(series))
	for _, row := range series {
		name := stringValue(row["name"])
		if sequence := stringValue(row["sequence"]); sequence != "" {
			name += " #" + sequence
		}
		groups = append(groups, name)
	}
	values["grouping"] = strings.Join(groups, "; ")
	for key, value := range values {
		if value == nil || value == "" {
			delete(values, key)
		}
	}
	writeJSON(w, r, http.StatusOK, values)
}

func (h *Handler) ffprobe(w http.ResponseWriter, r *http.Request, user store.User, itemID, ino string) {
	if user.Type != "root" && user.Type != "admin" {
		writeError(w, http.StatusForbidden, "")
		return
	}
	var libraryID, mediaType, mediaID string
	err := h.db.QueryRowContext(r.Context(), `SELECT libraryId,mediaType,mediaId FROM libraryItems WHERE id=?`, itemID).Scan(&libraryID, &mediaType, &mediaID)
	if errors.Is(err, sql.ErrNoRows) {
		writeError(w, http.StatusNotFound, "")
		return
	}
	if err != nil {
		writeError(w, http.StatusInternalServerError, "database read failed")
		return
	}
	if !userCanAccessLibrary(user, libraryID) {
		writeError(w, http.StatusForbidden, "")
		return
	}
	var filesRaw []byte
	if mediaType == "book" {
		if err := h.db.QueryRowContext(r.Context(), `SELECT audioFiles FROM books WHERE id=?`, mediaID).Scan(&filesRaw); err != nil {
			writeError(w, http.StatusInternalServerError, "database read failed")
			return
		}
	} else if mediaType == "podcast" {
		if err := h.db.QueryRowContext(r.Context(), `SELECT COALESCE(json_group_array(json(audioFile)), '[]') FROM podcastEpisodes WHERE podcastId=?`, mediaID).Scan(&filesRaw); err != nil {
			writeError(w, http.StatusInternalServerError, "database read failed")
			return
		}
	}
	target := ""
	for _, file := range decodeArray(filesRaw) {
		if stringValue(file["ino"]) == ino {
			target = filePath(file)
			break
		}
		if audio, ok := file["audioFile"].(map[string]any); ok && stringValue(audio["ino"]) == ino {
			target = filePath(audio)
			break
		}
	}
	if target == "" {
		writeError(w, http.StatusNotFound, "")
		return
	}
	if !h.pathAllowed(r.Context(), target, libraryID, false) {
		writeError(w, http.StatusNotFound, "")
		return
	}
	probe := strings.TrimSpace(os.Getenv("FFPROBE_PATH"))
	if probe == "" {
		probe = "ffprobe"
	}
	cmd := exec.CommandContext(r.Context(), probe, "-hide_banner", "-loglevel", "fatal", "-show_error", "-show_format", "-show_streams", "-show_programs", "-show_chapters", "-show_private_data", "-print_format", "json", target)
	output, err := cmd.Output()
	if err != nil {
		writeError(w, http.StatusInternalServerError, "ffprobe failed")
		return
	}
	var result any
	if err := json.Unmarshal(output, &result); err != nil {
		writeError(w, http.StatusInternalServerError, "ffprobe returned invalid data")
		return
	}
	writeJSON(w, r, http.StatusOK, result)
}

func (h *Handler) updateTracks(w http.ResponseWriter, r *http.Request, user store.User, itemID string) {
	scope, _, err := h.writableItem(r.Context(), itemID, user)
	if errors.Is(err, sql.ErrNoRows) {
		writeError(w, http.StatusNotFound, "")
		return
	}
	if err != nil {
		writeError(w, http.StatusInternalServerError, "database read failed")
		return
	}
	if !userCanAccessLibrary(user, scope.libraryID) {
		writeError(w, http.StatusForbidden, "")
		return
	}
	if !canUpdateCatalog(user) {
		writeError(w, http.StatusForbidden, "")
		return
	}
	if scope.mediaType != "book" {
		writeError(w, http.StatusBadRequest, "")
		return
	}
	var request struct {
		Ordered []map[string]any `json:"orderedFileData"`
	}
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 2<<20)).Decode(&request); err != nil {
		writeError(w, http.StatusBadRequest, "")
		return
	}
	if len(request.Ordered) == 0 {
		writeError(w, http.StatusBadRequest, "")
		return
	}
	var raw []byte
	if err := h.db.QueryRowContext(r.Context(), `SELECT audioFiles FROM books WHERE id=?`, scope.mediaID).Scan(&raw); err != nil {
		writeError(w, http.StatusInternalServerError, "database read failed")
		return
	}
	files := decodeArray(raw)
	byIno := make(map[string]map[string]any, len(files))
	for _, file := range files {
		byIno[stringValue(file["ino"])] = file
	}
	for _, ordered := range request.Ordered {
		ino := stringValue(ordered["ino"])
		if ino == "" || byIno[ino] == nil {
			writeError(w, http.StatusBadRequest, "")
			return
		}
	}
	orderedFiles := make([]map[string]any, 0, len(request.Ordered))
	index := 1
	for _, ordered := range request.Ordered {
		file := byIno[stringValue(ordered["ino"])]
		file["manuallyVerified"] = true
		excluded := truthy(ordered["exclude"])
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
		return floatValue(orderedFiles[i]["index"]) < floatValue(orderedFiles[j]["index"])
	})
	encoded, _ := json.Marshal(orderedFiles)
	if _, err := h.db.ExecContext(r.Context(), `UPDATE books SET audioFiles=?,updatedAt=? WHERE id=?`, string(encoded), dbNow(), scope.mediaID); err != nil {
		writeError(w, http.StatusInternalServerError, "database write failed")
		return
	}
	item, err := h.ItemJSON(r.Context(), itemID, true)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "database read failed")
		return
	}
	if h.emit != nil {
		h.emit("*", "item_updated", item)
	}
	writeJSON(w, r, http.StatusOK, item)
}

func (h *Handler) updateEbookStatus(w http.ResponseWriter, r *http.Request, user store.User, itemID, ino string) {
	scope, _, err := h.writableItem(r.Context(), itemID, user)
	if errors.Is(err, sql.ErrNoRows) {
		writeError(w, http.StatusNotFound, "")
		return
	}
	if err != nil {
		writeError(w, http.StatusInternalServerError, "database read failed")
		return
	}
	if !userCanAccessLibrary(user, scope.libraryID) {
		writeError(w, http.StatusForbidden, "")
		return
	}
	if !canUpdateCatalog(user) {
		writeError(w, http.StatusForbidden, "")
		return
	}
	if scope.mediaType != "book" {
		writeError(w, http.StatusBadRequest, "")
		return
	}
	var rawFiles, rawAudio []byte
	if err := h.db.QueryRowContext(r.Context(), `SELECT li.libraryFiles,b.audioFiles FROM libraryItems li JOIN books b ON b.id=li.mediaId WHERE li.id=?`, itemID).Scan(&rawFiles, &rawAudio); err != nil {
		writeError(w, http.StatusInternalServerError, "database read failed")
		return
	}
	files := decodeArray(rawFiles)
	selected := -1
	for i, file := range files {
		if stringValue(file["ino"]) == ino {
			selected = i
			break
		}
	}
	if selected < 0 || fileType(fileMetadataExt(files[selected])) != "ebook" {
		writeError(w, http.StatusBadRequest, "Invalid ebook file id")
		return
	}
	var primary any
	if truthy(files[selected]["isSupplementary"]) {
		primaryFile := make(map[string]any, len(files[selected]))
		for key, value := range files[selected] {
			if key != "isSupplementary" && key != "fileType" {
				primaryFile[key] = value
			}
		}
		metadata, _ := primaryFile["metadata"].(map[string]any)
		if metadata != nil {
			primaryFile["ebookFormat"] = metadata["format"]
		}
		primary = primaryFile
	}
	primaryIno := ""
	if m, ok := primary.(map[string]any); ok {
		primaryIno = stringValue(m["ino"])
	}
	for _, file := range files {
		if fileType(fileMetadataExt(file)) == "ebook" {
			file["isSupplementary"] = stringValue(file["ino"]) != primaryIno
		}
	}
	filesJSON, _ := json.Marshal(files)
	encodedPrimary, _ := json.Marshal(primary)
	missing := 1
	for _, audio := range decodeArray(rawAudio) {
		if !truthy(audio["exclude"]) {
			missing = 0
			break
		}
	}
	if primary != nil {
		missing = 0
	}
	if _, err := h.db.ExecContext(r.Context(), `UPDATE books SET ebookFile=?,updatedAt=? WHERE id=?`, string(encodedPrimary), dbNow(), scope.mediaID); err != nil {
		writeError(w, http.StatusInternalServerError, "database write failed")
		return
	}
	if _, err := h.db.ExecContext(r.Context(), `UPDATE libraryItems SET libraryFiles=?,isMissing=?,updatedAt=? WHERE id=?`, string(filesJSON), missing, dbNow(), itemID); err != nil {
		writeError(w, http.StatusInternalServerError, "database write failed")
		return
	}
	if h.emit != nil {
		if item, err := h.ItemJSON(r.Context(), itemID, true); err == nil {
			h.emit("*", "item_updated", item)
		}
	}
	w.WriteHeader(http.StatusOK)
}

func truthy(value any) bool {
	b, _ := value.(bool)
	return b
}

func anyStrings(value any) []string {
	values, _ := value.([]string)
	return values
}

func placeholders(n int) string {
	if n <= 0 {
		return ""
	}
	return strings.TrimRight(strings.Repeat("?,", n), ",")
}

func floatValue(value any) float64 {
	switch v := value.(type) {
	case float64:
		return v
	case int:
		return float64(v)
	case int64:
		return float64(v)
	case json.Number:
		n, _ := v.Float64()
		return n
	default:
		return 0
	}
}
