// Cover routes mirroring upstream LibraryItemController.getCover/uploadCover
// and managers/CoverManager + managers/CacheManager (pinned v2.37.2). The
// derived cache files live in the configured metadata tree exactly like the
// source, and the resize is performed by the same external ffmpeg binary with
// the same arguments, so encode output stays byte-identical when the ffmpeg
// features used by both sides match.
package catalog

import (
	"bytes"
	"context"
	"crypto/sha1"
	"database/sql"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"mime/multipart"
	"net"
	"net/http"
	"net/netip"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"audiobookshelf-compat/internal/store"
)

// sendTextStatus writes an Express-style body+status pair without the
// trailing newline http.Error would add (res.status(..).send('...')); express
// 'send' also attaches the weak ETag.
func sendTextStatus(w http.ResponseWriter, status int, message string) {
	_, _ = sendExpressText(w, status, message)
}

// coverCachePath mirrors CacheManager.initializeCachePaths (CacheManager.js:26-30).
func (h *Handler) coverCachePath() string {
	return filepath.Join(h.cfg.MetadataPath, "cache", "covers")
}

// expressETag computes the weak ETag of a buffered response body the way
// express 'send' does (server/etag.js): W/"<len hex>-<sha1 base64 minus padding>".
func expressETag(body []byte) string {
	sum := sha1.Sum(body)
	hash := strings.TrimRight(base64.StdEncoding.EncodeToString(sum[:]), "=")
	return fmt.Sprintf("W/\"%x-%s\"", len(body), hash)
}

// writeExpressJSON mirrors Express res.json for small buffered JSON replies.
func writeExpressJSON(w http.ResponseWriter, status int, value any) {
	var buffer bytes.Buffer
	encoder := json.NewEncoder(&buffer)
	encoder.SetEscapeHTML(false)
	if err := encoder.Encode(value); err != nil {
		writeError(w, http.StatusInternalServerError, "")
		return
	}
	body := bytes.TrimSuffix(buffer.Bytes(), []byte{'\n'})
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.Header().Set("Content-Length", strconv.Itoa(len(body)))
	w.Header().Set("ETag", expressETag(body))
	w.WriteHeader(status)
	_, _ = w.Write(body)
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
func (h *Handler) coverRaw(w http.ResponseWriter, r *http.Request, coverPath string) {
	info, err := os.Stat(coverPath)
	if err != nil {
		writeStatus(w, http.StatusNotFound)
		return
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
}

// handleCoverCache mirrors CacheManager.handleCoverCache (CacheManager.js:43-102):
// content-type from the selected format; cache hits stream chunked with no
// Content-Length; misses resize via ffmpeg scale=<w||-1>:<h||-1> into
// <MetadataPath>/cache/covers/<id>_<width>[x<height>].<format>; a bad format
// or dimension is a sendStatus(400), a missing cover sendStatus(404), and an
// encoder failure sendStatus(500) — including matching this environment where
// the shared ffmpeg build lacks libwebp.
func (h *Handler) handleCoverCache(w http.ResponseWriter, r *http.Request, itemID string) {
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
		writeStatus(w, http.StatusBadRequest)
		return
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
		streamCoverCache(w, r, cachePath)
		return
	}
	var coverPath sql.NullString
	err := h.db.QueryRowContext(r.Context(), `SELECT COALESCE(b.coverPath, p.coverPath) FROM libraryItems li LEFT JOIN books b ON b.id = li.mediaId AND li.mediaType = 'book' LEFT JOIN podcasts p ON p.id = li.mediaId AND li.mediaType = 'podcast' WHERE li.id = ?`, itemID).Scan(&coverPath)
	if err != nil || !coverPath.Valid || coverPath.String == "" {
		writeStatus(w, http.StatusNotFound)
		return
	}
	if _, statErr := os.Stat(coverPath.String); statErr != nil {
		writeStatus(w, http.StatusNotFound)
		return
	}
	if err := os.MkdirAll(h.coverCachePath(), 0o755); err != nil {
		writeStatus(w, http.StatusInternalServerError)
		return
	}
	// ffmpegHelpers.resizeImage (ffmpegHelpers.js:78-97): the width or height
	// that is absent stays -1 so ffmpeg preserves the aspect ratio, and the
	// output filename selects the muxer exactly as upstream does.
	args := []string{"-hide_banner", "-loglevel", "error", "-y", "-i", coverPath.String, "-vf", fmt.Sprintf("scale=%d:%d", width, height), cachePath}
	cmd := exec.CommandContext(r.Context(), "ffmpeg", args...)
	cmd.Stdout = nil
	cmd.Stderr = nil
	if err := cmd.Run(); err != nil {
		writeStatus(w, http.StatusInternalServerError)
		return
	}
	streamCoverCache(w, r, cachePath)
}

// streamCoverCache mirrors the cache-hit/done path: a bare chunked stream
// without Content-Length (node stream pipe semantics).
func streamCoverCache(w http.ResponseWriter, r *http.Request, cachePath string) {
	file, err := os.Open(cachePath)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "")
		return
	}
	defer file.Close()
	w.WriteHeader(http.StatusOK)
	if flusher, ok := w.(http.Flusher); ok {
		flusher.Flush()
	}
	if r.Method != http.MethodHead {
		_, _ = io.Copy(w, file)
	}
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

func ssrfFilterDisabled(host string) bool {
	if os.Getenv("DISABLE_SSRF_REQUEST_FILTER") == "1" || os.Getenv("EXP_PROXY_SUPPORT") == "1" {
		return true
	}
	for _, candidate := range strings.Split(os.Getenv("SSRF_REQUEST_FILTER_WHITELIST"), ",") {
		if strings.TrimSpace(candidate) == host && host != "" {
			return true
		}
	}
	return false
}

func publicUnicastAddress(ip netip.Addr) bool {
	if !ip.IsValid() || ip.Zone() != "" || ip.Is4In6() || !ip.IsGlobalUnicast() || ip.IsPrivate() || ip.IsLoopback() || ip.IsLinkLocalUnicast() {
		return false
	}
	prefixes := []string{
		"0.0.0.0/8", "100.64.0.0/10", "192.0.0.0/24", "192.0.2.0/24", "192.88.99.0/24",
		"198.18.0.0/15", "198.51.100.0/24", "203.0.113.0/24", "240.0.0.0/4",
		"2001:db8::/32", "2001:2::/48", "2001:3::/32", "2001:4:112::/48", "2001:10::/28",
		"2001:20::/28", "64:ff9b::/96", "::ffff:0:0/96", "2002::/16", "2001::/32",
	}
	for _, raw := range prefixes {
		prefix, _ := netip.ParsePrefix(raw)
		if prefix.Contains(ip) {
			return false
		}
	}
	return true
}

func coverURLTransport() *http.Transport {
	return &http.Transport{DialContext: func(ctx context.Context, network, address string) (net.Conn, error) {
		host, port, err := net.SplitHostPort(address)
		if err != nil {
			return nil, err
		}
		ip, parseErr := netip.ParseAddr(host)
		if parseErr != nil {
			ips, lookupErr := net.DefaultResolver.LookupNetIP(ctx, "ip", host)
			if lookupErr != nil {
				return nil, lookupErr
			}
			if len(ips) == 0 {
				return nil, errors.New("host resolved to no addresses")
			}
			ip = ips[0]
		}
		if !publicUnicastAddress(ip) {
			return nil, fmt.Errorf("call to %s is blocked", ip)
		}
		return (&net.Dialer{}).DialContext(ctx, network, net.JoinHostPort(ip.String(), port))
	}}
}

func downloadCoverFromURL(ctx context.Context, rawURL, tempPath string) (string, string) {
	u, err := url.Parse(rawURL)
	if err != nil || u.Hostname() == "" || (u.Scheme != "http" && u.Scheme != "https") {
		return "", "Failed to download image from url"
	}
	transport := coverURLTransport()
	client := &http.Client{Transport: transport, Timeout: 30 * time.Second}
	experimentalProxySupport := os.Getenv("EXP_PROXY_SUPPORT") == "1"
	if ssrfFilterDisabled(u.Hostname()) {
		transport.DialContext = (&net.Dialer{}).DialContext
	}
	client.CheckRedirect = func(req *http.Request, via []*http.Request) error {
		if experimentalProxySupport && (req.Response == nil || (req.Response.StatusCode != http.StatusMovedPermanently && req.Response.StatusCode != http.StatusFound)) {
			return http.ErrUseLastResponse
		}
		if len(via) >= 5 {
			return errors.New("stopped after 5 redirects")
		}
		return nil
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, rawURL, nil)
	if err != nil {
		return "", "Failed to download image from url"
	}
	req.Header.Set("User-Agent", "audiobookshelf (+https://audiobookshelf.org)")
	response, err := client.Do(req)
	if err != nil {
		return "", "Failed to download image from url"
	}
	defer response.Body.Close()
	contentType := response.Header.Get("Content-Type")
	if response.StatusCode < 200 || response.StatusCode >= 300 || !strings.HasPrefix(contentType, "image/") || contentType == "image/svg+xml" {
		return "", "Failed to download image from url"
	}
	temp, err := os.OpenFile(tempPath, os.O_CREATE|os.O_TRUNC|os.O_WRONLY, 0o666)
	if err != nil {
		return "", "Failed to download image from url"
	}
	_, copyErr := io.Copy(temp, response.Body)
	closeErr := temp.Close()
	if copyErr != nil || closeErr != nil {
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
func (h *Handler) uploadCover(w http.ResponseWriter, r *http.Request, user store.User, itemID string) {
	var permissions map[string]any
	_ = json.Unmarshal(user.Permissions, &permissions)
	if !user.IsActive || permissions["upload"] != true {
		writeError(w, http.StatusForbidden, "")
		return
	}
	var libraryID, itemPath, mediaType, mediaID string
	var isFile int
	if err := h.db.QueryRowContext(r.Context(), `SELECT libraryId, path, mediaType, mediaId, COALESCE(isFile,0) FROM libraryItems WHERE id=?`, itemID).Scan(&libraryID, &itemPath, &mediaType, &mediaID, &isFile); err != nil {
		if err == sql.ErrNoRows {
			writeError(w, http.StatusNotFound, "")
			return
		}
		writeError(w, http.StatusInternalServerError, "database read failed")
		return
	}
	if !userCanAccessLibrary(user, libraryID) {
		writeError(w, http.StatusForbidden, "")
		return
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
		sendTextStatus(w, http.StatusBadRequest, "Invalid request no file or url")
		return
	}
	var ext string
	if fileHeader != nil {
		ext = strings.TrimPrefix(strings.ToLower(filepath.Ext(fileHeader.Filename)), ".")
		if ext == "" || !supportedImageTypes[ext] {
			extension := filepath.Ext(strings.ToLower(fileHeader.Filename))
			sendTextStatus(w, http.StatusBadRequest, fmt.Sprintf("Invalid image type %s (Supported: %s)", extension, supportedCoverTypes))
			return
		}
	}
	var storeWithItem bool
	var settingsRaw []byte
	if err := h.db.QueryRowContext(r.Context(), `SELECT value FROM settings WHERE key='server-settings'`).Scan(&settingsRaw); err == nil {
		var settings map[string]any
		_ = json.Unmarshal(settingsRaw, &settings)
		storeWithItem, _ = settings["storeCoverWithItem"].(bool)
	}
	coverDir := filepath.Join(h.cfg.MetadataPath, "items", itemID)
	if storeWithItem && isFile == 0 {
		coverDir = itemPath
	}
	if err := os.MkdirAll(coverDir, 0o755); err != nil {
		message := "Failed to move cover into destination"
		if coverURL != "" {
			message = "Failed to fetch image from url"
		}
		sendTextStatus(w, http.StatusBadRequest, message)
		return
	}
	var coverFullPath string
	if coverURL != "" {
		tempPath := filepath.Join(coverDir, "cover")
		var message string
		ext, message = downloadCoverFromURL(r.Context(), coverURL, tempPath)
		if message != "" {
			sendTextStatus(w, http.StatusBadRequest, message)
			return
		}
		coverFullPath = filepath.Join(coverDir, "cover."+ext)
		if err := os.Rename(tempPath, coverFullPath); err != nil {
			sendTextStatus(w, http.StatusBadRequest, "Failed to fetch image from url")
			return
		}
	} else {
		coverFullPath = filepath.Join(coverDir, "cover."+ext)
		source, err := fileHeader.Open()
		if err != nil {
			sendTextStatus(w, http.StatusBadRequest, "Failed to move cover into destination")
			return
		}
		target, err := os.Create(coverFullPath)
		if err != nil {
			source.Close()
			sendTextStatus(w, http.StatusBadRequest, "Failed to move cover into destination")
			return
		}
		if _, err := io.Copy(target, source); err != nil {
			target.Close()
			source.Close()
			sendTextStatus(w, http.StatusBadRequest, "Failed to move cover into destination")
			return
		}
		target.Close()
		source.Close()
	}
	removeOldCovers(coverDir, "."+ext)
	h.purgeCoverCache(itemID)
	table := "books"
	if mediaType == "podcast" {
		table = "podcasts"
	}
	writeErr := func() error {
		tx, txErr := h.db.BeginTx(r.Context(), nil)
		if txErr != nil {
			return txErr
		}
		defer tx.Rollback()
		if _, txErr = tx.ExecContext(r.Context(), fmt.Sprintf(`UPDATE %s SET coverPath=? WHERE id=?`, table), coverFullPath, mediaID); txErr != nil {
			return txErr
		}
		// req.libraryItem.changed('updatedAt', true); await save()
		if _, txErr = tx.ExecContext(r.Context(), `UPDATE libraryItems SET updatedAt=? WHERE id=?`, dbNow(), itemID); txErr != nil {
			return txErr
		}
		return tx.Commit()
	}()
	if writeErr != nil {
		writeError(w, http.StatusInternalServerError, "database write failed")
		return
	}
	if item, itemErr := h.ItemJSON(r.Context(), itemID, true); itemErr == nil && h.emit != nil {
		// SocketAuthority.libraryItemEmitter filters per client; the hub applies
		// the same libraryId filtering for "*" payloads.
		h.emit("*", "item_updated", item)
	}
	body := []byte(`{"success":true,"cover":"` + escapeJSONString(coverFullPath) + `"}`)
	w.Header().Set("Content-Length", strconv.Itoa(len(body)))
	w.Header().Set("ETag", expressETag(body))
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(http.StatusOK)
	if r.Method != http.MethodHead {
		_, _ = w.Write(body)
	}
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
