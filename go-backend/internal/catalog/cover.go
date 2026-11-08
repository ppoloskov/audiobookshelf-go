// Cover routes mirroring upstream LibraryItemController.getCover/uploadCover
// and managers/CoverManager + managers/CacheManager (pinned v2.37.2). The
// derived cache files live in the configured metadata tree exactly like the
// source, and the resize is performed by the same external ffmpeg binary with
// the same arguments, so encode output stays byte-identical when the ffmpeg
// features used by both sides match.
package catalog

import (
	"audiobookshelf-compat/internal/access"
	"audiobookshelf-compat/internal/dbtime"
	"audiobookshelf-compat/internal/httputil"
	"audiobookshelf-compat/internal/libitem"
	"audiobookshelf-compat/internal/logging"
	"audiobookshelf-compat/internal/mediatools"
	"audiobookshelf-compat/internal/serversettings"
	"audiobookshelf-compat/internal/store"
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"io"
	"mime/multipart"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"strings"
)

// coverCachePath mirrors CacheManager.initializeCachePaths (CacheManager.js:26-30).
func (h *Handler) coverCachePath() string {
	return filepath.Join(h.cfg.MetadataPath, "cache", "covers")
}

// expressFileETag mirrors the file-derived weak ETag of express 'send':
// "<size hex>-<mtimeMs hex>" — observed as W/"78-1a1875cf4bd" for a 120-byte
// file in the recorded differential run.
func expressFileETag(size int64, modTime int64) string {
	return fmt.Sprintf("W/\"%x-%x\"", size, modTime)
}

// reqSupportsWebp mirrors server/utils/index.js:144-147, including the bare
// */* accept that undici/fetch-style clients send.
func reqSupportsWebp(accept string) bool {
	return accept == "*/*" || strings.Contains(accept, "image/webp")
}

func clampCoverDimension(raw string, max int) *int {
	parsed, err := strconv.Atoi(strings.TrimSpace(raw))
	if err != nil || parsed <= 0 {
		return nil
	}
	if parsed > max {
		parsed = max
	}
	return &parsed
}

// coverRaw mirrors the raw branch of LibraryItemController.getCover
// (LibraryItemController.js:858-884): sendFile semantics with the express
// send defaults — Cache-Control public,max-age=0, weak ETag, Last-Modified.
func (h *Handler) coverRaw(w http.ResponseWriter, r *http.Request, coverPath string) error {
	info, err := os.Stat(coverPath)
	if err != nil {
		return httputil.Status(http.StatusNotFound, "")
	}
	// node_modules/send/index.js:862: the Cache-Control default is only
	// applied when the handler has not already set one (the ts branch of
	// getCover sets 'private, max-age=86400').
	if w.Header().Get("Cache-Control") == "" {
		w.Header().Set("Cache-Control", "public, max-age=0")
	}
	if info.Mode().IsRegular() {
		w.Header().Set("ETag", expressFileETag(info.Size(), info.ModTime().UnixMilli()))
	}
	http.ServeFile(w, r, coverPath)
	return nil
}

// handleCoverCache mirrors CacheManager.handleCoverCache (CacheManager.js:43-102):
// content-type from the selected format; cache hits stream chunked with no
// Content-Length; misses resize via ffmpeg scale=<w||-1>:<h||-1> into
// <MetadataPath>/cache/covers/<id>_<width>[x<height>].<format>; a bad format
// or dimension is a sendStatus(400), a missing cover sendStatus(404), and an
// encoder failure sendStatus(500) — including matching this environment where
// the shared ffmpeg build lacks libwebp.
func (h *Handler) handleCoverCache(w http.ResponseWriter, r *http.Request, itemID string) error {
	formatParam := r.URL.Query().Get("format")
	widthValue := clampCoverDimension(r.URL.Query().Get("width"), 4096)
	heightValue := clampCoverDimension(r.URL.Query().Get("height"), 4096)
	format := formatParam
	if format == "" {
		if reqSupportsWebp(r.Header.Get("Accept")) {
			format = "webp"
		} else {
			format = "jpeg"
		}
	}
	if format != "webp" && format != "jpeg" && format != "png" {
		return httputil.Status(http.StatusBadRequest, "")
	}
	width := 400
	if widthValue != nil {
		width = *widthValue
	}
	height := -1
	if heightValue != nil {
		height = *heightValue
	}
	w.Header().Set("Content-Type", "image/"+format)

	cacheName := fmt.Sprintf("%s_%d", itemID, width)
	if heightValue != nil {
		// CacheManager.js:53 — the height component appears only when a
		// height dimension was requested.
		cacheName += fmt.Sprintf("x%d", *heightValue)
	}
	cachePath := filepath.Join(h.coverCachePath(), cacheName+"."+format)
	if _, statErr := os.Stat(cachePath); statErr == nil {
		return streamCoverCache(w, r, cachePath)
	}
	var coverPath sql.NullString
	err := h.db.QueryRowContext(r.Context(), `SELECT COALESCE(b.coverPath, p.coverPath) FROM libraryItems li LEFT JOIN books b ON b.id = li.mediaId AND li.mediaType = 'book' LEFT JOIN podcasts p ON p.id = li.mediaId AND li.mediaType = 'podcast' WHERE li.id = ?`, itemID).Scan(&coverPath)
	if err != nil || !coverPath.Valid || coverPath.String == "" {
		return httputil.Status(http.StatusNotFound, "")
	}
	if _, statErr := os.Stat(coverPath.String); statErr != nil {
		return httputil.Status(http.StatusNotFound, "")
	}
	if err := os.MkdirAll(h.coverCachePath(), 0o755); err != nil {
		return &httputil.Error{Status: 500, Cause: err}
	}
	if err := resizeImage(r.Context(), coverPath.String, cachePath, width, height); err != nil {
		return &httputil.Error{Status: 500, Cause: err}
	}
	return streamCoverCache(w, r, cachePath)
}

// resizeImage is ffmpegHelpers.resizeImage (ffmpegHelpers.js:78-97): the width
// or height that is absent is -1 so ffmpeg preserves the aspect ratio, and the
// output filename selects the muxer exactly as upstream does.
func resizeImage(ctx context.Context, input, output string, width, height int) error {
	return mediatools.Run(ctx, "-y", "-i", input, "-vf", fmt.Sprintf("scale=%d:%d", width, height), output)
}

// streamCoverCache mirrors the cache-hit/done path: a bare chunked stream
// without Content-Length (node stream pipe semantics).
func streamCoverCache(w http.ResponseWriter, r *http.Request, cachePath string) error {
	file, err := os.Open(cachePath)
	if err != nil {
		return httputil.Internal("", err)
	}
	defer file.Close()
	w.WriteHeader(http.StatusOK)
	if flusher, ok := w.(http.Flusher); ok {
		flusher.Flush()
	}
	if r.Method != http.MethodHead {
		_, _ = io.Copy(w, file)
	}
	return nil
}

var supportedImageTypes = map[string]bool{"jpeg": true, "jpg": true, "png": true, "webp": true}

const supportedCoverTypes = "png,jpg,jpeg,webp"

func sniffCoverType(header []byte) string {
	switch {
	case len(header) >= 3 && header[0] == 0xff && header[1] == 0xd8 && header[2] == 0xff:
		return "jpg"
	case len(header) >= 8 && string(header[:8]) == "\x89PNG\r\n\x1a\n":
		return "png"
	case len(header) >= 3 && string(header[:3]) == "GIF":
		return "gif"
	case len(header) >= 12 && string(header[8:12]) == "WEBP":
		return "webp"
	case len(header) >= 4 && string(header[:4]) == "FLIF":
		return "flif"
	case len(header) >= 10 && ((string(header[:4]) == "II*\x00" || string(header[:4]) == "MM\x00*") && string(header[8:10]) == "CR"):
		return "cr2"
	case len(header) >= 4 && (string(header[:4]) == "II*\x00" || string(header[:4]) == "MM\x00*"):
		return "tif"
	case len(header) >= 2 && string(header[:2]) == "BM":
		return "bmp"
	case len(header) >= 3 && string(header[:3]) == "II\xbc":
		return "jxr"
	case len(header) >= 4 && string(header[:4]) == "8BPS":
		return "psd"
	case len(header) >= 4 && string(header[:4]) == "\x00\x00\x01\x00":
		return "ico"
	case len(header) >= 4 && string(header[:4]) == "\x00\x00\x02\x00":
		return "cur"
	case len(header) >= 4 && string(header[:4]) == "BPG\xfb":
		return "bpg"
	case len(header) >= 12 && string(header[4:8]) == "ftyp" && coverHeicBrand(string(header[8:12])):
		return "heic"
	case len(header) >= 12 && string(header[:12]) == "\xabKTX 11\xbb\r\n\x1a\n":
		return "ktx"
	case len(header) >= 6 && string(header[:6]) == "<?xml ":
		return "xml"
	default:
		return ""
	}
}

func coverHeicBrand(brand string) bool {
	switch brand {
	case "mif1", "msf1", "heic", "heix", "hevc", "hevx":
		return true
	default:
		return false
	}
}

func validateCoverImage(path string) (string, string) {
	file, err := os.Open(path)
	if err != nil {
		return "", "Invalid image"
	}
	defer file.Close()
	header := make([]byte, 12)
	n, _ := io.ReadFull(file, header)
	ext := sniffCoverType(header[:n])
	if ext == "" {
		return "", "Invalid image"
	}
	if !supportedImageTypes[ext] {
		return "", fmt.Sprintf("Invalid image type %s (Supported: %s)", ext, supportedCoverTypes)
	}
	return ext, ""
}

func downloadCoverFromURL(ctx context.Context, rawURL, tempPath string) (string, string) {
	if err := httputil.DownloadImage(ctx, rawURL, tempPath); err != nil {
		return "", "Failed to download image from url"
	}
	ext, message := validateCoverImage(tempPath)
	if message != "" {
		_ = os.Remove(tempPath)
		return "", message
	}
	return ext, ""
}

// uploadCover mirrors LibraryItemController.uploadCover (LibraryItemController.js:295-333)
// + CoverManager.uploadCover/downloadCoverFromUrlNew: upload permission gate,
// multipart or SSRF-filtered URL source, cover written to
// storeCoverWithItem?itemPath:metadata/items/<id>, older cover.* files removed,
// the item's cached covers purged, media.coverPath persisted, the library item
// updatedAt bumped, an item_updated event emitted after the commit, and the
// {success, cover} JSON answer carrying an express-style weak ETag.
func (h *Handler) uploadCover(w http.ResponseWriter, r *http.Request) error {
	user, itemID := store.RequestUser(r), r.PathValue("id")
	if !access.For(r.Context(), user).Upload() {
		return httputil.Status(http.StatusForbidden, "")
	}
	scope, err := h.itemFor(r.Context(), user, itemID, false)
	if err != nil {
		return err
	}
	var fileHeader *multipart.FileHeader
	var coverURL string
	if strings.HasPrefix(r.Header.Get("Content-Type"), "multipart/form-data") {
		if err := r.ParseMultipartForm(10 << 20); err == nil && r.MultipartForm != nil {
			if values := r.MultipartForm.Value["url"]; len(values) > 0 {
				coverURL = values[0]
			}
			if coverURL == "" {
				if files := r.MultipartForm.File["cover"]; len(files) > 0 {
					fileHeader = files[0]
				}
			}
		}
	} else {
		var body struct {
			URL string `json:"url"`
		}
		if json.NewDecoder(http.MaxBytesReader(w, r.Body, 10_000_000)).Decode(&body) == nil {
			coverURL = body.URL
		}
	}
	if fileHeader == nil && coverURL == "" {
		return httputil.Status(http.StatusBadRequest, "Invalid request no file or url")
	}
	var ext string
	if fileHeader != nil {
		ext = strings.TrimPrefix(strings.ToLower(filepath.Ext(fileHeader.Filename)), ".")
		if ext == "" || !supportedImageTypes[ext] {
			extension := filepath.Ext(strings.ToLower(fileHeader.Filename))
			return httputil.Status(http.StatusBadRequest, fmt.Sprintf("Invalid image type %s (Supported: %s)", extension, supportedCoverTypes))
		}
	}
	storeWithItem := serversettings.Load(r.Context(), h.db).Bool("storeCoverWithItem")
	coverDir := filepath.Join(h.cfg.MetadataPath, "items", itemID)
	if storeWithItem && !scope.isFile {
		coverDir = scope.path
	}
	if err := os.MkdirAll(coverDir, 0o755); err != nil {
		message := "Failed to move cover into destination"
		if coverURL != "" {
			message = "Failed to fetch image from url"
		}
		return httputil.Status(http.StatusBadRequest, message)
	}
	var coverFullPath string
	if coverURL != "" {
		tempPath := filepath.Join(coverDir, "cover")
		var message string
		ext, message = downloadCoverFromURL(r.Context(), coverURL, tempPath)
		if message != "" {
			return httputil.Status(http.StatusBadRequest, message)
		}
		coverFullPath = filepath.Join(coverDir, "cover."+ext)
		if err := os.Rename(tempPath, coverFullPath); err != nil {
			return httputil.Status(http.StatusBadRequest, "Failed to fetch image from url")
		}
	} else {
		coverFullPath = filepath.Join(coverDir, "cover."+ext)
		source, err := fileHeader.Open()
		if err != nil {
			return httputil.Status(http.StatusBadRequest, "Failed to move cover into destination")
		}
		target, err := os.Create(coverFullPath)
		if err != nil {
			source.Close()
			return httputil.Status(http.StatusBadRequest, "Failed to move cover into destination")
		}
		if _, err := io.Copy(target, source); err != nil {
			target.Close()
			source.Close()
			return httputil.Status(http.StatusBadRequest, "Failed to move cover into destination")
		}
		target.Close()
		source.Close()
	}
	removeOldCovers(coverDir, "."+ext)
	h.purgeCoverCache(itemID)
	table := "books"
	if scope.mediaType == "podcast" {
		table = "podcasts"
	}
	writeErr := func() error {
		tx, txErr := h.db.BeginTx(r.Context(), nil)
		if txErr != nil {
			return txErr
		}
		defer tx.Rollback()
		if _, txErr = tx.ExecContext(r.Context(), fmt.Sprintf(`UPDATE %s SET coverPath=? WHERE id=?`, table), coverFullPath, scope.mediaID); txErr != nil {
			return txErr
		}
		// req.libraryItem.changed('updatedAt', true); await save()
		if _, txErr = tx.ExecContext(r.Context(), `UPDATE libraryItems SET updatedAt=? WHERE id=?`, dbtime.Now(), itemID); txErr != nil {
			return txErr
		}
		return tx.Commit()
	}()
	if writeErr != nil {
		return httputil.Internal("database write failed", writeErr)
	}
	// SocketAuthority.libraryItemEmitter filters per client; the hub applies
	// the same libraryId filtering for "*" payloads.
	if err := h.itemUpdated(r.Context(), itemID); err != nil {
		return httputil.Internal("database read failed", err)
	}
	body := []byte(`{"success":true,"cover":"` + escapeJSONString(coverFullPath) + `"}`)
	w.Header().Set("Content-Length", strconv.Itoa(len(body)))
	w.Header().Set("ETag", httputil.ExpressETag(body))
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(http.StatusOK)
	if r.Method != http.MethodHead {
		_, _ = w.Write(body)
	}
	return nil
}

// removeOldCovers mirrors CoverManager.removeOldCovers, which removes only
// the image extensions in its explicit allowlist.
func removeOldCovers(coverDir, keptExt string) {
	imageExtensions := map[string]bool{".jpeg": true, ".jpg": true, ".png": true, ".webp": true, ".jiff": true}
	entries, err := os.ReadDir(coverDir)
	if err != nil {
		return
	}
	for _, entry := range entries {
		name := entry.Name()
		if len(name) <= len("cover.") || !strings.EqualFold(name[:len("cover.")], "cover.") || entry.IsDir() {
			continue
		}
		if !strings.EqualFold(strings.TrimSuffix(name, filepath.Ext(name)), "cover") {
			continue
		}
		ext := strings.ToLower(filepath.Ext(name))
		if !imageExtensions[ext] || ext == strings.ToLower(keptExt) {
			continue
		}
		_ = os.Remove(filepath.Join(coverDir, name))
	}
}

// purgeCoverCache mirrors CacheManager.purgeCoverCache (CacheManager.js:93-98).
func (h *Handler) purgeCoverCache(itemID string) {
	entries, err := os.ReadDir(h.coverCachePath())
	if err != nil {
		return
	}
	for _, entry := range entries {
		if strings.HasPrefix(entry.Name(), itemID+"_") {
			_ = os.Remove(filepath.Join(h.coverCachePath(), entry.Name()))
		}
	}
}

// escapeJSONString keeps the cover path a valid JSON string; the source path
// comes from trusted server configuration joined with the item UUID.
func escapeJSONString(value string) string {
	encoded, _ := json.Marshal(value)
	return strings.TrimSuffix(strings.TrimPrefix(string(encoded), "\""), "\"")
}

func (h *Handler) removeCover(w http.ResponseWriter, r *http.Request) error {
	scope, err := h.itemFor(r.Context(), store.RequestUser(r), r.PathValue("id"), true)
	if err != nil {
		return err
	}
	table, err := scope.mediaTable()
	if err != nil {
		return err
	}
	res, err := h.db.ExecContext(r.Context(), `UPDATE `+table+` SET coverPath=NULL,updatedAt=? WHERE id=? AND coverPath IS NOT NULL`, dbtime.Now(), scope.mediaID)
	if err != nil {
		return httputil.Internal("database write failed", err)
	}
	if err := h.coverChanged(r.Context(), res, scope.id); err != nil {
		return err
	}
	return httputil.OK(w, r)
}

func (h *Handler) updateCover(w http.ResponseWriter, r *http.Request) error {
	itemID := r.PathValue("id")
	scope, err := h.itemFor(r.Context(), store.RequestUser(r), itemID, true)
	if err != nil {
		return err
	}
	var body struct {
		Cover string `json:"cover"`
	}
	if json.NewDecoder(http.MaxBytesReader(w, r.Body, 10_000_000)).Decode(&body) != nil || body.Cover == "" {
		return httputil.Status(400, "Invalid request no cover path")
	}
	if strings.HasPrefix(body.Cover, "http:") || strings.HasPrefix(body.Cover, "https:") {
		logging.RequestDebug(r, "responding 500: cover path is a URL, a local file is required")
		return httputil.Status(500, "Invalid cover path")
	}
	raw, err := h.libraryFiles(r.Context(), itemID)
	if err != nil {
		return err
	}
	valid := false
	for _, file := range store.DecodeJSONSlice[libitem.File](raw) {
		if file.Path() == body.Cover {
			valid = true
			break
		}
	}
	if !valid {
		logging.RequestDebug(r, "responding 500: cover path is not one of the library item files")
		return httputil.Status(500, "Invalid cover path")
	}
	table, err := scope.mediaTable()
	if err != nil {
		return err
	}
	var currentCover sql.NullString
	if err := h.db.QueryRowContext(r.Context(), `SELECT coverPath FROM `+table+` WHERE id=?`, scope.mediaID).Scan(&currentCover); err != nil {
		return httputil.Internal("database read failed", err)
	}
	if currentCover.Valid && currentCover.String == body.Cover {
		return httputil.JSON(w, r, struct {
			Success bool   `json:"success"`
			Cover   string `json:"cover"`
		}{true, body.Cover})
	}
	info, err := os.Stat(body.Cover)
	if err != nil {
		return httputil.Internal("Cover path does not exist", err)
	}
	if !info.Mode().IsRegular() {
		logging.RequestDebug(r, "responding 500: cover path is not a regular file")
		return httputil.Status(500, "Cover path is not a file")
	}
	ext, validationError := validateCoverImage(body.Cover)
	if validationError != "" {
		logging.RequestDebug(r, "responding 500: "+validationError)
		return httputil.Status(500, validationError)
	}
	coverDir := filepath.Join(h.cfg.MetadataPath, "items", itemID)
	if serversettings.Load(r.Context(), h.db).Bool("storeCoverWithItem") && !scope.isFile {
		coverDir = scope.path
	}
	coverPath := body.Cover
	if !strings.HasPrefix(coverPath, coverDir) {
		if err := os.MkdirAll(coverDir, 0o755); err != nil {
			return httputil.Internal("Failed to copy cover to dir", err)
		}
		coverPath = filepath.Join(coverDir, "cover."+ext)
		source, err := os.Open(body.Cover)
		if err != nil {
			return httputil.Internal("Failed to copy cover to dir", err)
		}
		target, err := os.Create(coverPath)
		if err != nil {
			_ = source.Close()
			return httputil.Internal("Failed to copy cover to dir", err)
		}
		_, copyErr := io.Copy(target, source)
		targetErr := target.Close()
		sourceErr := source.Close()
		if copyErr != nil || targetErr != nil || sourceErr != nil {
			return httputil.Internal("Failed to copy cover to dir", sourceErr)
		}
		removeOldCovers(coverDir, "."+ext)
	}
	res, err := h.db.ExecContext(r.Context(), `UPDATE `+table+` SET coverPath=?,updatedAt=? WHERE id=? AND (coverPath IS NULL OR coverPath!=?)`, coverPath, dbtime.Now(), scope.mediaID, coverPath)
	if err != nil {
		return httputil.Internal("database write failed", err)
	}
	if err := h.coverChanged(r.Context(), res, itemID); err != nil {
		return err
	}
	return httputil.JSON(w, r, struct {
		Success bool   `json:"success"`
		Cover   string `json:"cover"`
	}{true, coverPath})
}
