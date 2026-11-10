package people

import (
	"audiobookshelf-compat/internal/access"
	"audiobookshelf-compat/internal/httputil"
	"audiobookshelf-compat/internal/ids"
	"audiobookshelf-compat/internal/jsonvalue"
	"audiobookshelf-compat/internal/logging"
	"audiobookshelf-compat/internal/mediatools"
	"audiobookshelf-compat/internal/store"
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"net/http"
	"net/url"
	"os"
	"path"
	"path/filepath"
	"strings"
	"syscall"
)

// PurgeAuthorImageCache exposes the author cache purge to the people domain.
func (h *Handler) PurgeAuthorImageCache(entityID string) { h.purgeImageCache(entityID) }

// SaveAuthorImage exposes the author image download to the people domain.
func (h *Handler) SaveAuthorImage(ctx context.Context, authorID, imageURL string) (string, string) {
	return h.saveAuthorImage(ctx, authorID, imageURL)
}

// purgeImageCache is CacheManager.purgeImageCache (CacheManager.js:97-126): it
// removes every cached image whose name starts with the entity id.
func (h *Handler) purgeImageCache(entityID string) {
	if entityID == "" {
		return
	}
	dir := h.imageCachePath()
	entries, err := os.ReadDir(dir)
	if err != nil {
		return
	}
	for _, entry := range entries {
		if strings.HasPrefix(entry.Name(), entityID) {
			logging.Debugf("[CacheManager] Going to purge %s", entry.Name())
			if err := os.Remove(filepath.Join(dir, entry.Name())); err != nil && !errors.Is(err, os.ErrNotExist) {
				logging.Errorf(`[CacheManager] Failed to remove cache "%s" %v`, filepath.Join(dir, entry.Name()), err)
			}
		}
	}
}

// imageCachePath mirrors CacheManager.ImageCachePath (CacheManager.js:29).
func (h *Handler) imageCachePath() string {
	return h.cfg.Dirs().ImageCache
}

// authorImagePath is the file AuthorFinder.saveAuthorImage writes
// (AuthorFinder.js:43-66): <metadata>/authors/<id>.png for a url ending in
// "png", otherwise .jpg.
func (h *Handler) authorImagePath(authorID, imageURL string) string {
	parts := strings.Split(strings.ToLower(imageURL), ".")
	ext := "jpg"
	if parts[len(parts)-1] == "png" {
		ext = "png"
	}
	return path.Join(filepath.ToSlash(h.cfg.Dirs().Authors), authorID+"."+ext)
}

// saveAuthorImage is AuthorFinder.saveAuthorImage: it downloads the image
// (fileUtils.downloadImageFile: image/* but not svg) and returns the written
// path, or the error message upstream would send.
func (h *Handler) saveAuthorImage(ctx context.Context, authorID, imageURL string) (string, string) {
	authorDir := h.cfg.Dirs().Authors
	if err := os.MkdirAll(authorDir, 0o755); err != nil {
		return "", err.Error()
	}
	outputPath := h.authorImagePath(authorID, imageURL)
	if err := httputil.DownloadImage(ctx, imageURL, filepath.FromSlash(outputPath)); err != nil {
		message := axiosMessage(err)
		if message == "" {
			message = "Unknown error"
		}
		logging.Errorf(`[AuthorFinder] Download image file failed for "%s" %s`, imageURL, message) // AuthorFinder.js:61
		return "", message
	}
	return outputPath, ""
}

// axiosMessage maps a Go transport error onto the message axios/Node report
// for the same failure (err.message in AuthorFinder.saveAuthorImage).
func axiosMessage(err error) string {
	var urlErr *url.Error
	if errors.As(err, &urlErr) {
		err = urlErr.Err
	}
	var netErr net.Error
	if errors.As(err, &netErr) && netErr.Timeout() {
		return "timeout of 30000ms exceeded"
	}
	var dnsErr *net.DNSError
	if errors.As(err, &dnsErr) {
		return "getaddrinfo ENOTFOUND " + dnsErr.Name
	}
	var opErr *net.OpError
	if errors.As(err, &opErr) && errors.Is(err, syscall.ECONNREFUSED) && opErr.Addr != nil {
		return "connect ECONNREFUSED " + opErr.Addr.String()
	}
	return err.Error()
}

// authorUploadImage is AuthorController.uploadImage (AuthorController.js:267-304).
func (h *Handler) authorUploadImage(w http.ResponseWriter, r *http.Request) error {
	user := store.RequestUser(r)
	a, err := AuthorRequest(r.Context(), h.db, r, user)
	if err != nil {
		return err
	}
	if !access.For(r.Context(), user).Upload() {
		logging.Warnf(`User "%s" attempted to upload an image without permission`, user.Username)
		return httputil.Status(http.StatusForbidden, "")
	}
	ctx := r.Context()
	body := httputil.DecodeObjectFields(r)
	rawURL, present := body["url"]
	var imageURL string
	var isString bool
	if present {
		isString = json.Unmarshal(rawURL, &imageURL) == nil
	}
	// `!req.body.url`: absent, null, false, 0 and "" are all falsy.
	var generic any
	if present {
		_ = json.Unmarshal(rawURL, &generic)
	}
	if !present || !jsonvalue.Truthy(generic) {
		logging.Errorf("[AuthorController] Invalid request payload. 'url' not in request body")
		return httputil.Status(http.StatusBadRequest, "Invalid request payload. 'url' not in request body")
	}
	if !isString || (!strings.HasPrefix(imageURL, "http:") && !strings.HasPrefix(imageURL, "https:")) {
		shown := imageURL
		if !isString {
			shown = jsonvalue.Text(generic)
		}
		logging.Errorf(`[AuthorController] Invalid request payload. Invalid url "%s"`, shown)
		return httputil.Status(http.StatusBadRequest, fmt.Sprintf(`Invalid request payload. Invalid url "%s"`, shown))
	}
	logging.Debugf(`[AuthorController] Requesting download author image from url "%s"`, imageURL)
	imagePath, message := h.saveAuthorImage(ctx, a.ID, imageURL)
	if message != "" {
		return httputil.Status(http.StatusBadRequest, message)
	}
	if a.ImagePath.Valid && a.ImagePath.String != "" {
		h.purgeImageCache(a.ID)
	}
	update := &AuthorColumnUpdate{}
	update.Force("imagePath", &imagePath) // changed('imagePath', true): bump updatedAt
	if err := update.Save(ctx, h.db, a.ID); err != nil {
		return httputil.Internal("database write failed", err)
	}
	expanded, err := AuthorExpanded(ctx, h.db, a.ID)
	if err != nil {
		return httputil.Internal("database read failed", err)
	}
	h.emit.ToAll("author_updated", expanded)
	return httputil.JSON(w, r, AuthorImageResult{Author: expanded.AuthorView})
}

// authorDeleteImage is AuthorController.deleteImage (AuthorController.js:313-329).
func (h *Handler) authorDeleteImage(w http.ResponseWriter, r *http.Request) error {
	a, err := AuthorRequest(r.Context(), h.db, r, store.RequestUser(r))
	if err != nil {
		return err
	}
	ctx := r.Context()
	if !a.ImagePath.Valid || a.ImagePath.String == "" {
		logging.Errorf(`[AuthorController] Author "%s" has no imagePath set`, a.ImagePath.String)
		return httputil.Status(http.StatusBadRequest, "Author has no image path set")
	}
	logging.Infof(`[AuthorController] Removing image for author "%s" at "%s"`, a.Name.String, a.ImagePath.String)
	h.purgeImageCache(a.ID)
	// CoverManager.removeFile (CoverManager.js:34-44).
	if _, err := os.Stat(a.ImagePath.String); err != nil {
		logging.Warnf("[CoverManager] Attempting to remove file that does not exist %s", a.ImagePath.String)
	} else if err := os.Remove(a.ImagePath.String); err != nil {
		return httputil.Internal("internal error", err)
	}
	update := &AuthorColumnUpdate{}
	update.Change("imagePath", store.NullStringPtr(a.ImagePath), nil)
	if err := update.Save(ctx, h.db, a.ID); err != nil {
		return httputil.Internal("database write failed", err)
	}
	expanded, err := AuthorExpanded(ctx, h.db, a.ID)
	if err != nil {
		return httputil.Internal("database read failed", err)
	}
	h.emit.ToAll("author_updated", expanded)
	return httputil.JSON(w, r, AuthorImageResult{Author: expanded.AuthorView})
}

// authorImage is AuthorController.getImage (AuthorController.js:391-419) over
// CacheManager.handleAuthorCache (CacheManager.js:155-190). The route is
// public (Auth.js:21).
func (h *Handler) authorImage(w http.ResponseWriter, r *http.Request) error {
	authorID := r.PathValue("id")
	query := r.URL.Query()
	if query.Get("raw") != "" {
		a, err := LoadAuthor(r.Context(), h.db, authorID)
		if errors.Is(err, sql.ErrNoRows) {
			logging.Warnf(`[AuthorController] Author "%s" not found`, authorID)
			return httputil.Status(http.StatusNotFound, "")
		}
		if err != nil {
			return httputil.Internal("database read failed", err)
		}
		if !a.ImagePath.Valid || a.ImagePath.String == "" {
			logging.Warnf(`[AuthorController] Author "%s" has invalid imagePath: %s`, a.Name.String, a.ImagePath.String)
			return httputil.Status(http.StatusNotFound, "")
		}
		if _, err := os.Stat(a.ImagePath.String); err != nil {
			logging.Warnf(`[AuthorController] Author "%s" has invalid imagePath: %s`, a.Name.String, a.ImagePath.String)
			return httputil.Status(http.StatusNotFound, "")
		}
		httputil.RawFile(w, r, a.ImagePath.String)
		return nil
	}
	format := query.Get("format")
	if format == "" {
		if httputil.SupportsWebP(r.Header.Get("Accept")) {
			format = "webp"
		} else {
			format = "jpeg"
		}
	}
	widthValue := httputil.ClampDimension(query.Get("width"), 4096)
	heightValue := httputil.ClampDimension(query.Get("height"), 4096)
	if format != "webp" && format != "jpeg" && format != "png" || !ids.Valid(authorID) {
		return httputil.Status(http.StatusBadRequest, "")
	}
	width, height := 400, -1
	if widthValue != nil {
		width = *widthValue
	}
	if heightValue != nil {
		height = *heightValue
	}
	w.Header().Set("Content-Type", "image/"+format)
	cacheName := fmt.Sprintf("%s_%d", authorID, width)
	if heightValue != nil {
		cacheName += fmt.Sprintf("x%d", *heightValue)
	}
	cachePath := filepath.Join(h.imageCachePath(), cacheName+"."+format)
	if _, err := os.Stat(cachePath); err == nil {
		httputil.StreamCache(w, r, cachePath)
		return nil
	}
	a, err := LoadAuthor(r.Context(), h.db, authorID)
	if err != nil && !errors.Is(err, sql.ErrNoRows) {
		return httputil.Internal("database read failed", err)
	}
	if err != nil || !a.ImagePath.Valid || a.ImagePath.String == "" {
		return httputil.Status(http.StatusNotFound, "")
	}
	if _, err := os.Stat(a.ImagePath.String); err != nil {
		return httputil.Status(http.StatusNotFound, "")
	}
	if err := os.MkdirAll(h.imageCachePath(), 0o755); err != nil {
		return &httputil.Error{Status: 500, Cause: err}
	}
	if err := mediatools.ResizeImage(r.Context(), a.ImagePath.String, cachePath, width, height); err != nil {
		return &httputil.Error{Status: 500, Cause: err}
	}
	httputil.StreamCache(w, r, cachePath)
	return nil
}
