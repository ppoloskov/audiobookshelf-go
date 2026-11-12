package author

import (
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"time"

	"audiobookshelf-compat/internal/auth"
	"audiobookshelf-compat/internal/db"
	"audiobookshelf-compat/internal/httpx"
	"audiobookshelf-compat/internal/ids"
	"audiobookshelf-compat/internal/mediatools"
)

const (
	downloadTimeout = 30 * time.Second
	maxDimension    = 4096
)

// purgeImageCache removes every cached resize whose name starts with the
// entity id (CacheManager.purgeImageCache, js:97-126).
func (h *Handler) purgeImageCache(entityID string) {
	entries, err := os.ReadDir(h.deps.ImageCacheDir)
	if err != nil || entityID == "" {
		return // no cache directory yet: nothing to purge
	}
	for _, entry := range entries {
		if strings.HasPrefix(entry.Name(), entityID) {
			if err := os.Remove(filepath.Join(h.deps.ImageCacheDir, entry.Name())); err != nil && !errors.Is(err, os.ErrNotExist) {
				slog.Error("remove cached author image", "file", entry.Name(), "error", err)
			}
		}
	}
}

// saveImage is AuthorFinder.saveAuthorImage (js:43-66): it downloads the image
// to <image dir>/<id>.png for a url ending in "png", else .jpg, and returns the path.
func (h *Handler) saveImage(ctx context.Context, authorID, imageURL string) (string, error) {
	if err := os.MkdirAll(h.deps.ImageDir, 0o755); err != nil {
		return "", err
	}
	ext := "jpg"
	if strings.HasSuffix(strings.ToLower(imageURL), ".png") {
		ext = "png"
	}
	dest := filepath.Join(h.deps.ImageDir, authorID+"."+ext)
	if err := h.download(ctx, imageURL, dest); err != nil {
		return "", err
	}
	return dest, nil
}

// download fetches an http(s) image into dest: a 2xx image/* response that is
// not SVG (fileUtils.downloadImageFile). A failed download leaves no file.
func (h *Handler) download(ctx context.Context, rawURL, dest string) (err error) {
	ctx, cancel := context.WithTimeout(ctx, downloadTimeout)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, rawURL, nil)
	if err != nil {
		return errors.New("Invalid URL")
	}
	req.Header.Set("User-Agent", "audiobookshelf (+https://audiobookshelf.org)")
	resp, err := h.client.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return fmt.Errorf("Request failed with status code %d", resp.StatusCode)
	}
	if kind := resp.Header.Get("Content-Type"); !strings.HasPrefix(kind, "image/") || kind == "image/svg+xml" {
		return fmt.Errorf("Invalid content type %q", kind)
	}
	file, err := os.Create(dest)
	if err != nil {
		return err
	}
	defer func() {
		if closeErr := file.Close(); err == nil {
			err = closeErr
		}
		if err != nil {
			_ = os.Remove(dest) // a failed download leaves no file
		}
	}()
	_, err = io.Copy(file, resp.Body)
	return err
}

// message is the text axios reports for a failed download, which the web
// client shows (AuthorFinder.saveAuthorImage logs and returns err.message).
func message(err error) string {
	var urlErr *url.Error
	if errors.As(err, &urlErr) {
		err = urlErr.Err
	}
	var netErr net.Error
	var dnsErr *net.DNSError
	var opErr *net.OpError
	switch {
	case errors.As(err, &netErr) && netErr.Timeout():
		return "timeout of 30000ms exceeded"
	case errors.As(err, &dnsErr):
		return "getaddrinfo ENOTFOUND " + dnsErr.Name
	case errors.As(err, &opErr) && errors.Is(err, syscall.ECONNREFUSED) && opErr.Addr != nil:
		return "connect ECONNREFUSED " + opErr.Addr.String()
	case err.Error() == "":
		return "Unknown error"
	}
	return err.Error()
}

// uploadImage is AuthorController.uploadImage (js:267-304): the image comes
// from the url in the body.
func (h *Handler) uploadImage(w http.ResponseWriter, r *http.Request, user *auth.Principal) error {
	ctx := r.Context()
	a, err := h.load(r, user)
	if err != nil {
		return err
	}
	if !user.CanUpload() {
		slog.Warn("user attempted to upload an author image without permission", "user", db.Val(user.Username))
		return httpx.Forbidden()
	}
	body, err := httpx.Decode[struct{ URL any }](w, r)
	if err != nil {
		return err
	}
	imageURL, isText := body.URL.(string)
	if body.URL == nil || body.URL == false || body.URL == float64(0) || imageURL == "" && isText { // JS falsy
		return httpx.BadRequest("Invalid request payload. 'url' not in request body")
	}
	if !strings.HasPrefix(imageURL, "http:") && !strings.HasPrefix(imageURL, "https:") {
		return httpx.BadRequest(fmt.Sprintf(`Invalid request payload. Invalid url "%v"`, body.URL))
	}
	path, err := h.saveImage(ctx, a.ID, imageURL)
	if err != nil {
		return httpx.BadRequest(message(err))
	}
	if db.Val(a.ImagePath) != "" {
		h.purgeImageCache(a.ID)
	}
	a.ImagePath = &path
	return h.imageSaved(w, r, a)
}

// deleteImage is AuthorController.deleteImage (js:313-329).
func (h *Handler) deleteImage(w http.ResponseWriter, r *http.Request, user *auth.Principal) error {
	a, err := h.load(r, user)
	if err != nil {
		return err
	}
	if db.Val(a.ImagePath) == "" {
		return httpx.BadRequest("Author has no image path set")
	}
	h.purgeImageCache(a.ID)
	if err := os.Remove(*a.ImagePath); err != nil && !errors.Is(err, os.ErrNotExist) { // CoverManager.removeFile
		return httpx.Internal("", err)
	}
	a.ImagePath = nil
	return h.imageSaved(w, r, a)
}

// imageSaved stores the author's new image path and answers {author}.
func (h *Handler) imageSaved(w http.ResponseWriter, r *http.Request, a db.Author) error {
	if err := h.save(r.Context(), &a, "imagePath"); err != nil {
		return httpx.Internal("database write failed", err)
	}
	if err := h.announce(r.Context(), a); err != nil {
		return httpx.Internal("database read failed", err)
	}
	return httpx.JSON(w, r, struct {
		Author View `json:"author"`
	}{viewOf(a, nil)})
}

// image is AuthorController.getImage (js:391-419) over CacheManager.
// handleAuthorCache: the file itself with ?raw, else a resized copy cached by
// size. The route is public, like covers.
func (h *Handler) image(w http.ResponseWriter, r *http.Request) error {
	id, query := r.PathValue("id"), r.URL.Query()
	raw := query.Get("raw") != ""
	format := query.Get("format")
	if format == "" {
		format = "jpeg"
		if accept := r.Header.Get("Accept"); accept == "*/*" || strings.Contains(accept, "image/webp") {
			format = "webp"
		}
	}
	if !raw && (format != "webp" && format != "jpeg" && format != "png" || !ids.Valid(id)) {
		return httpx.BadRequest("")
	}
	width, height := dimension(query.Get("width")), dimension(query.Get("height"))
	if width == 0 {
		width = 400
	}
	cachePath := ""
	if !raw {
		name := fmt.Sprintf("%s_%d", id, width)
		if height > 0 {
			name += fmt.Sprintf("x%d", height)
		}
		cachePath = filepath.Join(h.deps.ImageCacheDir, name+"."+format)
		w.Header().Set("Content-Type", "image/"+format)
		if _, err := os.Stat(cachePath); err == nil {
			return httpx.ServeFile(w, r, cachePath)
		}
	}
	var a db.Author
	if err := h.db.Get(r.Context(), &a, `SELECT * FROM authors WHERE id = ?`, id); err != nil && !isNoRows(err) {
		return httpx.Internal("database read failed", err)
	} else if err != nil || db.Val(a.ImagePath) == "" {
		return httpx.NotFound()
	}
	if raw {
		return httpx.ServeFile(w, r, *a.ImagePath)
	}
	if _, err := os.Stat(*a.ImagePath); err != nil {
		return httpx.NotFound()
	}
	if err := os.MkdirAll(h.deps.ImageCacheDir, 0o755); err != nil {
		return httpx.Internal("", err)
	}
	if height == 0 {
		height = -1 // keep the aspect ratio
	}
	if err := mediatools.ResizeImage(r.Context(), *a.ImagePath, cachePath, width, height); err != nil {
		return httpx.Internal("", err)
	}
	return httpx.ServeFile(w, r, cachePath)
}

// dimension is a positive size capped at maxDimension, 0 for anything else.
func dimension(raw string) int {
	n, err := strconv.Atoi(strings.TrimSpace(raw))
	if err != nil || n <= 0 {
		return 0
	}
	return min(n, maxDimension)
}
