package catalog

import (
	"audiobookshelf-compat/internal/access"
	"audiobookshelf-compat/internal/httputil"
	"audiobookshelf-compat/internal/jsonvalue"
	"audiobookshelf-compat/internal/logging"
	"audiobookshelf-compat/internal/store"
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"os"
	"path"
	"path/filepath"
	"strings"
	"syscall"
	"time"
)

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
	return filepath.Join(h.cfg.MetadataPath, "cache", "images")
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
	return path.Join(filepath.ToSlash(filepath.Join(h.cfg.MetadataPath, "authors")), authorID+"."+ext)
}

// saveAuthorImage is AuthorFinder.saveAuthorImage: it downloads the image
// (fileUtils.downloadImageFile: image/* but not svg) and returns the written
// path, or the error message upstream would send.
func (h *Handler) saveAuthorImage(ctx context.Context, authorID, imageURL string) (string, string) {
	authorDir := filepath.Join(h.cfg.MetadataPath, "authors")
	if err := os.MkdirAll(authorDir, 0o755); err != nil {
		return "", err.Error()
	}
	outputPath := h.authorImagePath(authorID, imageURL)
	if err := downloadImageFile(ctx, imageURL, filepath.FromSlash(outputPath)); err != nil {
		message := axiosMessage(err)
		if message == "" {
			message = "Unknown error"
		}
		logging.Errorf(`[AuthorFinder] Download image file failed for "%s" %s`, imageURL, message) // AuthorFinder.js:61
		return "", message
	}
	return outputPath, ""
}

// downloadImageFile is fileUtils.downloadFile with the image content-type
// filter (fileUtils.js:292-358): a GET with a 30 second timeout through the
// SSRF filter whose 2xx image/* (not svg) response body is written to filepath.
func downloadImageFile(ctx context.Context, rawURL, filepath string) error {
	u, err := url.Parse(rawURL)
	if err != nil || u.Hostname() == "" || (u.Scheme != "http" && u.Scheme != "https") {
		return errors.New("Invalid URL")
	}
	transport := coverURLTransport()
	if httputil.SSRFFilterDisabled(u.Hostname()) {
		transport.DialContext = (&net.Dialer{}).DialContext
	}
	client := &http.Client{Transport: transport, Timeout: 30 * time.Second}
	client.CheckRedirect = func(req *http.Request, via []*http.Request) error {
		if len(via) > 5 {
			return errors.New("Maximum number of redirects exceeded")
		}
		return nil
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, rawURL, nil)
	if err != nil {
		return errors.New("Invalid URL")
	}
	req.Header.Set("User-Agent", "audiobookshelf (+https://audiobookshelf.org)")
	response, err := client.Do(req)
	if err != nil {
		return err
	}
	defer response.Body.Close()
	if response.StatusCode < 200 || response.StatusCode >= 300 {
		return fmt.Errorf("Request failed with status code %d", response.StatusCode)
	}
	contentType := response.Header.Get("Content-Type")
	if !strings.HasPrefix(contentType, "image/") || contentType == "image/svg+xml" {
		return fmt.Errorf("Invalid content type %q", contentType)
	}
	file, err := os.Create(filepath)
	if err != nil {
		return err
	}
	_, copyErr := io.Copy(file, response.Body)
	closeErr := file.Close()
	if copyErr != nil {
		return copyErr
	}
	return closeErr
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
	user := requestUser(r)
	a, err := h.authorRequest(r, user)
	if err != nil {
		return err
	}
	if !access.For(r.Context(), user).Upload() {
		logging.Warnf(`User "%s" attempted to upload an image without permission`, user.Username)
		return httputil.Status(http.StatusForbidden, "")
	}
	ctx := r.Context()
	body := jsonBodyObject(r)
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
	imagePath, message := h.saveAuthorImage(ctx, a.id, imageURL)
	if message != "" {
		return httputil.Status(http.StatusBadRequest, message)
	}
	if a.imagePath.Valid && a.imagePath.String != "" {
		h.purgeImageCache(a.id)
	}
	update := &authorColumnUpdate{}
	update.force("imagePath", &imagePath) // changed('imagePath', true): bump updatedAt
	if err := update.save(ctx, h.db, a.id); err != nil {
		return httputil.Internal("database write failed", err)
	}
	expanded, err := h.authorExpanded(ctx, a.id)
	if err != nil {
		return httputil.Internal("database read failed", err)
	}
	if h.emit != nil {
		h.emit("*", "author_updated", expanded)
	}
	return httputil.JSON(w, r, authorImageResult{Author: expanded.authorView})
}

// authorDeleteImage is AuthorController.deleteImage (AuthorController.js:313-329).
func (h *Handler) authorDeleteImage(w http.ResponseWriter, r *http.Request) error {
	a, err := h.authorRequest(r, requestUser(r))
	if err != nil {
		return err
	}
	ctx := r.Context()
	if !a.imagePath.Valid || a.imagePath.String == "" {
		logging.Errorf(`[AuthorController] Author "%s" has no imagePath set`, a.imagePath.String)
		return httputil.Status(http.StatusBadRequest, "Author has no image path set")
	}
	logging.Infof(`[AuthorController] Removing image for author "%s" at "%s"`, a.name.String, a.imagePath.String)
	h.purgeImageCache(a.id)
	// CoverManager.removeFile (CoverManager.js:34-44).
	if _, err := os.Stat(a.imagePath.String); err != nil {
		logging.Warnf("[CoverManager] Attempting to remove file that does not exist %s", a.imagePath.String)
	} else if err := os.Remove(a.imagePath.String); err != nil {
		return httputil.Internal("internal error", err)
	}
	update := &authorColumnUpdate{}
	update.set("imagePath", store.NullStringPtr(a.imagePath), nil)
	if err := update.save(ctx, h.db, a.id); err != nil {
		return httputil.Internal("database write failed", err)
	}
	expanded, err := h.authorExpanded(ctx, a.id)
	if err != nil {
		return httputil.Internal("database read failed", err)
	}
	if h.emit != nil {
		h.emit("*", "author_updated", expanded)
	}
	return httputil.JSON(w, r, authorImageResult{Author: expanded.authorView})
}

// authorImage is AuthorController.getImage (AuthorController.js:391-419) over
// CacheManager.handleAuthorCache (CacheManager.js:155-190). The route is
// public (Auth.js:21).
func (h *Handler) authorImage(w http.ResponseWriter, r *http.Request) error {
	authorID := r.PathValue("id")
	query := r.URL.Query()
	if query.Get("raw") != "" {
		a, err := h.loadAuthor(r.Context(), authorID)
		if errors.Is(err, sql.ErrNoRows) {
			logging.Warnf(`[AuthorController] Author "%s" not found`, authorID)
			return httputil.Status(http.StatusNotFound, "")
		}
		if err != nil {
			return httputil.Internal("database read failed", err)
		}
		if !a.imagePath.Valid || a.imagePath.String == "" {
			logging.Warnf(`[AuthorController] Author "%s" has invalid imagePath: %s`, a.name.String, a.imagePath.String)
			return httputil.Status(http.StatusNotFound, "")
		}
		if _, err := os.Stat(a.imagePath.String); err != nil {
			logging.Warnf(`[AuthorController] Author "%s" has invalid imagePath: %s`, a.name.String, a.imagePath.String)
			return httputil.Status(http.StatusNotFound, "")
		}
		h.coverRaw(w, r, a.imagePath.String)
		return nil
	}
	format := query.Get("format")
	if format == "" {
		if reqSupportsWebp(r.Header.Get("Accept")) {
			format = "webp"
		} else {
			format = "jpeg"
		}
	}
	widthValue := clampCoverDimension(query.Get("width"), 4096)
	heightValue := clampCoverDimension(query.Get("height"), 4096)
	if format != "webp" && format != "jpeg" && format != "png" || !isUUID(authorID) {
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
		streamCoverCache(w, r, cachePath)
		return nil
	}
	a, err := h.loadAuthor(r.Context(), authorID)
	if err != nil && !errors.Is(err, sql.ErrNoRows) {
		return httputil.Internal("database read failed", err)
	}
	if err != nil || !a.imagePath.Valid || a.imagePath.String == "" {
		return httputil.Status(http.StatusNotFound, "")
	}
	if _, err := os.Stat(a.imagePath.String); err != nil {
		return httputil.Status(http.StatusNotFound, "")
	}
	if err := os.MkdirAll(h.imageCachePath(), 0o755); err != nil {
		return &httputil.Error{Status: 500, Cause: err}
	}
	if err := resizeImage(r.Context(), a.imagePath.String, cachePath, width, height); err != nil {
		return &httputil.Error{Status: 500, Cause: err}
	}
	streamCoverCache(w, r, cachePath)
	return nil
}
