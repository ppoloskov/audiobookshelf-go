package item

import (
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"mime/multipart"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"audiobookshelf-compat/internal/auth"
	"audiobookshelf-compat/internal/db"
	"audiobookshelf-compat/internal/httpx"
	"audiobookshelf-compat/internal/mediatools"
)

const (
	supportedCovers = "png,jpg,jpeg,webp"
	maxCoverUpload  = 64 << 20
	maxCoverSize    = 4096 // largest width or height of a resized cover
)

// sniffedImages maps http.DetectContentType results to file extensions.
var sniffedImages = map[string]string{
	"image/jpeg": "jpg", "image/png": "png", "image/webp": "webp",
	"image/gif": "gif", "image/bmp": "bmp", "image/x-icon": "ico",
}

// mediaTable is the table of the item's media, "" for another media type.
func (r itemRef) mediaTable() string {
	switch r.MediaType {
	case "book":
		return "books"
	case "podcast":
		return "podcasts"
	}
	return ""
}

// coverDir is the folder a new cover goes to: the item's own folder when the
// server stores covers with items and the item has one, else the metadata folder.
func (h *Handler) coverDir(ref itemRef, withItem bool) string {
	if withItem && !bool(ref.IsFile) && ref.Path != "" {
		return ref.Path
	}
	return filepath.Join(h.deps.ItemMetadataDir, ref.ID)
}

// cover is LibraryItemController.getCover: the original file with ?raw, else a
// resized copy from the cache. It is public, so ref.LibraryID is not checked
// against a user.
func (h *Handler) cover(w http.ResponseWriter, r *http.Request) error {
	ctx := r.Context()
	ref, err := h.loadRef(ctx, r.PathValue("id"))
	if err != nil {
		return err
	}
	if r.URL.Query().Get("ts") != "" { // set before any existence check, as upstream does
		w.Header().Set("Cache-Control", "private, max-age=86400")
	}
	path, err := h.coverPath(ctx, ref)
	if err != nil {
		return err
	}
	if path == "" {
		return httpx.NotFound()
	}
	if r.URL.Query().Get("raw") != "" {
		if !h.inLibrary(ctx, path, ref.LibraryID) && !(h.deps.ItemMetadataDir != "" && within(path, []string{h.deps.ItemMetadataDir})) {
			return httpx.NotFound()
		}
		if w.Header().Get("Cache-Control") == "" {
			w.Header().Set("Cache-Control", "public, max-age=0")
		}
		return httpx.ServeFile(w, r, path)
	}
	return h.resizedCover(w, r, ref.ID, path)
}

// coverPath is the stored cover of the item, "" when it has none.
func (h *Handler) coverPath(ctx context.Context, ref itemRef) (string, error) {
	table := ref.mediaTable()
	if table == "" {
		return "", nil
	}
	var path *string
	if err := h.db.Get(ctx, &path, `SELECT coverPath FROM `+table+` WHERE id = ?`, ref.MediaID); err != nil {
		return "", httpx.Internal("database read failed", err)
	}
	return db.Val(path), nil
}

// dimension is a requested width or height: 0 when absent or not positive,
// capped at maxCoverSize.
func dimension(raw string) int {
	n, err := strconv.Atoi(strings.TrimSpace(raw))
	if err != nil || n <= 0 {
		return 0
	}
	return min(n, maxCoverSize)
}

// resizedCover is CacheManager.handleCoverCache: <cache>/<id>_<width>[x<height>].<format>,
// made with ffmpeg on first use. A bad format is a 400, an encoder failure a 500.
func (h *Handler) resizedCover(w http.ResponseWriter, r *http.Request, itemID, source string) error {
	query := r.URL.Query()
	format := query.Get("format")
	if format == "" {
		format = "jpeg"
		// A bare */* is what fetch-style clients send (server/utils/index.js:144-147).
		if accept := r.Header.Get("Accept"); accept == "*/*" || strings.Contains(accept, "image/webp") {
			format = "webp"
		}
	}
	if format != "webp" && format != "jpeg" && format != "png" {
		return httpx.BadRequest("")
	}
	width, height := dimension(query.Get("width")), dimension(query.Get("height"))
	if width == 0 {
		width = 400
	}
	name := fmt.Sprintf("%s_%d", itemID, width)
	if height > 0 {
		name += fmt.Sprintf("x%d", height)
	}
	cache := filepath.Join(h.deps.CoverCacheDir, name+"."+format)
	w.Header().Set("Content-Type", "image/"+format)
	if _, err := os.Stat(cache); err == nil {
		return httpx.ServeFile(w, r, cache)
	}
	if _, err := os.Stat(source); err != nil {
		return httpx.NotFound()
	}
	if err := os.MkdirAll(h.deps.CoverCacheDir, 0o755); err != nil {
		return httpx.Internal("", err)
	}
	if height == 0 {
		height = -1 // ffmpeg keeps the aspect ratio
	}
	if err := mediatools.ResizeImage(r.Context(), source, cache, width, height); err != nil {
		return httpx.Internal("", err)
	}
	return httpx.ServeFile(w, r, cache)
}

// coverResult is the body of the cover upload and choose routes.
type coverResult struct {
	Success bool   `json:"success"`
	Cover   string `json:"cover"`
}

// imageExt is the extension of the image at path, judged by its content. The
// message is the client-facing refusal, "" when it is a supported image.
func imageExt(path string) (ext, message string) {
	file, err := os.Open(path)
	if err != nil {
		return "", "Invalid image"
	}
	defer file.Close()
	head := make([]byte, 512)
	n, _ := io.ReadFull(file, head) // a short file is sniffed as far as it goes
	ext = sniffedImages[http.DetectContentType(head[:n])]
	switch ext {
	case "":
		return "", "Invalid image"
	case "jpg", "png", "webp":
		return ext, ""
	}
	return "", fmt.Sprintf("Invalid image type %s (Supported: %s)", ext, supportedCovers)
}

// uploadCover is LibraryItemController.uploadCover (js:295-333): the image is
// the "cover" file of a multipart form or the "url" of a form or JSON body.
// Order: upload permission, 404, library access, then the body.
func (h *Handler) uploadCover(w http.ResponseWriter, r *http.Request, user *auth.Principal) error {
	ctx := r.Context()
	if !user.CanUpload() {
		return httpx.Forbidden()
	}
	ref, err := h.readable(ctx, user, r.PathValue("id"))
	if err != nil {
		return err
	}
	r.Body = http.MaxBytesReader(w, r.Body, maxCoverUpload)
	var file *multipart.FileHeader
	var imageURL string
	if strings.HasPrefix(r.Header.Get("Content-Type"), "multipart/form-data") {
		if r.ParseMultipartForm(10<<20) == nil {
			imageURL = r.PostFormValue("url")
			if files := r.MultipartForm.File["cover"]; imageURL == "" && len(files) > 0 {
				file = files[0]
			}
		}
	} else if body, err := httpx.DecodeLimit[struct {
		URL string `json:"url"`
	}](w, r, maxCoverUpload); err == nil { // a malformed body is "no file or url"
		imageURL = body.URL
	}
	if file == nil && imageURL == "" {
		return httpx.BadRequest("Invalid request no file or url")
	}
	dir := h.coverDir(ref, h.settings.Server().StoreCoverWithItem)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return httpx.BadRequest("Failed to move cover into destination")
	}
	var cover string
	if imageURL != "" {
		cover, err = h.saveCoverFromURL(ctx, dir, ref.ID, imageURL)
	} else {
		cover, err = h.saveCoverUpload(dir, ref.ID, file)
	}
	if err != nil {
		return httpx.BadRequest(err.Error())
	}
	if err := h.db.Tx(ctx, func(ctx context.Context) error {
		if _, err := h.db.Exec(ctx, `UPDATE `+ref.mediaTable()+` SET coverPath = ? WHERE id = ?`, cover, ref.MediaID); err != nil {
			return err
		}
		return h.db.Update(ctx, "libraryItems", &db.LibraryItem{ID: ref.ID, UpdatedAt: db.Now()}, "updatedAt")
	}); err != nil {
		return httpx.Internal("database write failed", err)
	}
	if err := h.announce(ctx, ref.ID); err != nil {
		return httpx.Internal("database read failed", err)
	}
	return httpx.JSON(w, r, coverResult{true, cover})
}

// saveCoverUpload stores an uploaded image as <dir>/cover.<ext>. The error text
// is sent to the client.
func (h *Handler) saveCoverUpload(dir, itemID string, header *multipart.FileHeader) (string, error) {
	ext := strings.TrimPrefix(strings.ToLower(filepath.Ext(header.Filename)), ".")
	if !strings.Contains(","+supportedCovers+",", ","+ext+",") || ext == "" {
		return "", fmt.Errorf("Invalid image type %s (Supported: %s)", filepath.Ext(strings.ToLower(header.Filename)), supportedCovers)
	}
	failed := errors.New("Failed to move cover into destination")
	source, err := header.Open()
	if err != nil {
		return "", failed
	}
	defer source.Close()
	path := filepath.Join(dir, "cover."+ext)
	if err := writeFile(path, source); err != nil {
		return "", failed
	}
	h.coverStored(dir, itemID, ext)
	return path, nil
}

// writeFile creates path with the content of source.
func writeFile(path string, source io.Reader) error {
	target, err := os.Create(path)
	if err != nil {
		return err
	}
	_, err = io.Copy(target, source)
	return errors.Join(err, target.Close())
}

// saveCoverFromURL is CoverManager.downloadCoverFromUrlNew (CoverManager.js:
// 300-345): the image is fetched into <dir>/cover.<ext>. The error text is sent
// to the client by the upload route and only logged by quick match.
func (h *Handler) saveCoverFromURL(ctx context.Context, dir, itemID, imageURL string) (string, error) {
	fetchFailed := errors.New("Failed to fetch image from url")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return "", fetchFailed
	}
	temp := filepath.Join(dir, "cover")
	if err := h.download(ctx, imageURL, temp); err != nil {
		slog.Warn("download cover", "url", imageURL, "error", err)
		return "", errors.New("Failed to download image from url")
	}
	ext, message := imageExt(temp)
	if message != "" {
		_ = os.Remove(temp) // the download is unusable
		return "", errors.New(message)
	}
	path := filepath.Join(dir, "cover."+ext)
	if err := os.Rename(temp, path); err != nil {
		return "", fetchFailed
	}
	h.coverStored(dir, itemID, ext)
	slog.Info("downloaded cover", "path", path, "url", imageURL)
	return path, nil
}

// download fetches an http(s) image into dest (30 second budget); a 2xx
// response of type image/ other than SVG is accepted. A failure leaves no file.
func (h *Handler) download(ctx context.Context, rawURL, dest string) error {
	if u, err := url.Parse(rawURL); err != nil || u.Hostname() == "" || (u.Scheme != "http" && u.Scheme != "https") {
		return errors.New("invalid URL")
	}
	ctx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, rawURL, nil)
	if err != nil {
		return err
	}
	req.Header.Set("User-Agent", "audiobookshelf (+https://audiobookshelf.org)")
	resp, err := h.client.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return fmt.Errorf("status %d", resp.StatusCode)
	}
	if kind := resp.Header.Get("Content-Type"); !strings.HasPrefix(kind, "image/") || kind == "image/svg+xml" {
		return fmt.Errorf("content type %q", kind)
	}
	if err := writeFile(dest, resp.Body); err != nil {
		_ = os.Remove(dest) // do not keep a partial image
		return err
	}
	return nil
}

// coverStored follows a new cover file in dir: the item's older cover images
// and its cached resizes go (CoverManager.removeOldCovers, which removes only
// this list of extensions).
func (h *Handler) coverStored(dir, itemID, kept string) {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return
	}
	for _, entry := range entries {
		ext := filepath.Ext(entry.Name())
		switch strings.ToLower(ext) {
		case ".jpeg", ".jpg", ".png", ".webp", ".jiff":
		default:
			continue
		}
		if entry.IsDir() || !strings.EqualFold(strings.TrimSuffix(entry.Name(), ext), "cover") || strings.EqualFold(ext, "."+kept) {
			continue
		}
		_ = os.Remove(filepath.Join(dir, entry.Name())) // a leftover old cover is harmless
	}
	purgeCoverCache(h.deps.CoverCacheDir, itemID)
}

// chooseCover is LibraryItemController.updateCover (js:335-370): it makes one
// of the item's own image files its cover. Order: 404, 403, then the body.
func (h *Handler) chooseCover(w http.ResponseWriter, r *http.Request, user *auth.Principal) error {
	ctx := r.Context()
	ref, err := h.writable(ctx, user, r.PathValue("id"))
	if err != nil {
		return err
	}
	body, err := httpx.DecodeLimit[struct {
		Cover string `json:"cover"`
	}](w, r, 1<<20)
	if err != nil || body.Cover == "" {
		return httpx.BadRequest("Invalid request no cover path")
	}
	files, err := h.libraryFiles(ctx, ref.ID)
	if err != nil {
		return err
	}
	if !hasFile(files, body.Cover) {
		return httpx.Error(http.StatusInternalServerError, "Invalid cover path")
	}
	current, err := h.coverPath(ctx, ref)
	if err != nil {
		return err
	}
	if current == body.Cover {
		return httpx.JSON(w, r, coverResult{true, body.Cover})
	}
	if info, err := os.Stat(body.Cover); err != nil {
		return httpx.Internal("Cover path does not exist", err)
	} else if !info.Mode().IsRegular() {
		return httpx.Error(http.StatusInternalServerError, "Cover path is not a file")
	}
	ext, message := imageExt(body.Cover)
	if message != "" {
		return httpx.Error(http.StatusInternalServerError, message)
	}
	dir := h.coverDir(ref, h.settings.Server().StoreCoverWithItem)
	cover := body.Cover
	if !strings.HasPrefix(cover, dir) { // copy it into the folder covers live in
		failed := func(err error) error { return httpx.Internal("Failed to copy cover to dir", err) }
		source, err := os.Open(body.Cover)
		if err != nil {
			return failed(err)
		}
		defer source.Close()
		if err := os.MkdirAll(dir, 0o755); err != nil {
			return failed(err)
		}
		cover = filepath.Join(dir, "cover."+ext)
		if err := writeFile(cover, source); err != nil {
			return failed(err)
		}
		h.coverStored(dir, ref.ID, ext)
	}
	if err := h.changeCover(ctx, ref, &cover); err != nil {
		return err
	}
	return httpx.JSON(w, r, coverResult{true, cover})
}

// hasFile reports whether one of files lies at path.
func hasFile(files []db.LibraryFile, path string) bool {
	for _, f := range files {
		if f.Metadata.Path == path {
			return true
		}
	}
	return false
}

// removeCover is LibraryItemController.deleteCover: the file stays, the item
// forgets it. Order: 404, 403, 400 for a media type without a table.
func (h *Handler) removeCover(w http.ResponseWriter, r *http.Request, user *auth.Principal) error {
	ref, err := h.writable(r.Context(), user, r.PathValue("id"))
	if err != nil {
		return err
	}
	if ref.mediaTable() == "" {
		return httpx.BadRequest("")
	}
	if err := h.changeCover(r.Context(), ref, nil); err != nil {
		return err
	}
	return httpx.Status(w, http.StatusOK)
}

// changeCover sets the item's cover (nil clears it). When that changed the row,
// the item is stamped, its cached resizes dropped and item_updated sent.
func (h *Handler) changeCover(ctx context.Context, ref itemRef, cover *string) error {
	var changed int64
	err := h.db.Tx(ctx, func(ctx context.Context) error {
		now := db.Now()
		res, err := h.db.Exec(ctx, `UPDATE `+ref.mediaTable()+` SET coverPath = ?, updatedAt = ? WHERE id = ? AND COALESCE(coverPath, '') != COALESCE(?, '')`, cover, now, ref.MediaID, cover)
		if err != nil {
			return err
		}
		if changed, err = res.RowsAffected(); err != nil || changed == 0 {
			return err
		}
		return h.db.Update(ctx, "libraryItems", &db.LibraryItem{ID: ref.ID, UpdatedAt: now}, "updatedAt")
	})
	if err != nil {
		return httpx.Internal("database write failed", err)
	}
	if changed == 0 {
		return nil
	}
	purgeCoverCache(h.deps.CoverCacheDir, ref.ID)
	if err := h.announce(ctx, ref.ID); err != nil {
		return httpx.Internal("database read failed", err)
	}
	return nil
}
