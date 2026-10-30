// Package playback implements the pinned server's playback session, direct
// media, byte-range, and HLS routes. It reads and writes only the existing
// Audiobookshelf tables; it never creates or migrates schema.
package playback

import (
	"context"
	"crypto/rand"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math"
	"net"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"time"

	"audiobookshelf-compat/internal/config"
	"audiobookshelf-compat/internal/dbtime"
	"audiobookshelf-compat/internal/store"
)

type EmitFunc func(userID, event string, payload any)

type Handler struct {
	db       *sql.DB
	cfg      config.Config
	emit     EmitFunc
	mu       sync.RWMutex
	sessions map[string]*session
	localIDs map[string]string
}

type session struct {
	ID, UserID, LibraryID, LibraryItemID, BookID, EpisodeID string
	MediaType, DisplayTitle, DisplayAuthor                  string
	CoverPath                                               any
	DeviceID                                                string
	PlayMethod                                              int
	MediaPlayer                                             string
	Duration, StartTime, CurrentTime                        float64
	TimeListening                                           float64
	StartedAt, UpdatedAt                                    int64
	Date, DayOfWeek                                         string
	MediaMetadata                                           map[string]any
	Chapters                                                []any
	AudioTracks                                             []map[string]any
	Item                                                    map[string]any
	Device                                                  map[string]any
	Files                                                   []audioFile
	stream                                                  *transcode
}

type itemData struct {
	ID, LibraryID, MediaID, MediaType string
	Title, Author                     string
	CoverPath                         any
	Duration                          float64
	Metadata                          map[string]any
	Chapters                          []any
	Tags                              []string
	Files                             []audioFile
	Episodes                          []episodeData
	ItemJSON                          map[string]any
	Explicit                          bool
}

type audioFile struct {
	Index    any            `json:"index"`
	Ino      string         `json:"ino"`
	Metadata map[string]any `json:"metadata"`
	Duration float64        `json:"duration"`
	BitRate  any            `json:"bitRate"`
	Channels any            `json:"channels"`
	Codec    any            `json:"codec"`
	Format   any            `json:"format"`
	MimeType string         `json:"mimeType"`
	Exclude  bool           `json:"exclude"`
	Fields   map[string]any `json:"-"`
}

type episodeData struct {
	ID, Title string
	Audio     audioFile
	Duration  float64
	Chapters  []any
	JSON      map[string]any
}

type transcode struct {
	mu            sync.Mutex
	root          string
	audioCodec    string
	cmd           *exec.Cmd
	ctx           context.Context
	cancel        context.CancelFunc
	startTime     float64
	segmentStart  int
	segmentLength int
	complete      bool
	resetting     bool
	done          chan struct{}
}

// New uses the existing database and configuration. It doesn't initialize a
// playbackSessions table or any other schema.
func New(db *sql.DB, cfg config.Config, emit func(userID, event string, payload any)) *Handler {
	if emit == nil {
		emit = func(string, string, any) {}
	}
	return &Handler{db: db, cfg: cfg, emit: emit, sessions: map[string]*session{}, localIDs: map[string]string{}}
}

// Close stops every in-memory session and waits briefly for FFmpeg children.
func (h *Handler) Close() {
	h.mu.Lock()
	sessions := h.sessions
	h.sessions = map[string]*session{}
	h.mu.Unlock()
	for _, s := range sessions {
		if s.stream != nil {
			_ = s.stream.close()
		}
	}
}

// Handle reports whether this package owns the request path. Public audio and
// HLS resources are bearer-free upstream capability URLs; API routes require
// the authenticated user supplied by the server dispatcher.
func (h *Handler) Handle(w http.ResponseWriter, r *http.Request, user store.User) bool {
	parts := splitPath(r.URL.Path)
	if len(parts) == 0 {
		return false
	}
	switch {
	case parts[0] == "public" && len(parts) == 5 && parts[1] == "session" && parts[3] == "track":
		h.handleTrack(w, r, parts[2], parts[4])
		return true
	case parts[0] == "hls" && len(parts) == 3:
		h.handleHLS(w, r, parts[1], parts[2])
		return true
	case parts[0] == "api" && len(parts) >= 3 && parts[1] == "items":
		if len(parts) == 4 && parts[3] == "play" {
			h.handleStart(w, r, user, parts[2], "")
			return true
		}
		if len(parts) == 5 && parts[3] == "play" {
			h.handleStart(w, r, user, parts[2], parts[4])
			return true
		}
		if len(parts) == 5 && parts[3] == "file" {
			h.handleItemFile(w, r, user, parts[2], parts[4])
			return true
		}
	case parts[0] == "api" && len(parts) >= 2 && parts[1] == "sessions":
		if len(parts) == 2 && r.Method == http.MethodGet {
			h.handleHistory(w, r, user)
			return true
		}
		if len(parts) == 3 && parts[2] == "open" && r.Method == http.MethodGet {
			h.handleOpenSessions(w, r, user)
			return true
		}
		if len(parts) == 4 && parts[2] == "batch" && parts[3] == "delete" && r.Method == http.MethodPost {
			h.handleBatchDelete(w, r, user)
			return true
		}
		if len(parts) == 3 && r.Method == http.MethodDelete {
			h.handleDeleteHistory(w, r, user, parts[2])
			return true
		}
	case parts[0] == "api" && len(parts) >= 3 && parts[1] == "session":
		if len(parts) == 3 && parts[2] == "local" && r.Method == http.MethodPost {
			h.handleLocalSync(w, r, user, false)
			return true
		}
		if len(parts) == 3 && parts[2] == "local-all" && r.Method == http.MethodPost {
			h.handleLocalSync(w, r, user, true)
			return true
		}
		if len(parts) == 3 && r.Method == http.MethodGet {
			h.handleGetSession(w, r, user, parts[2])
			return true
		}
		if len(parts) == 4 && parts[3] == "sync" && r.Method == http.MethodPost {
			h.handleSync(w, r, user, parts[2], false)
			return true
		}
		if len(parts) == 4 && parts[3] == "close" && r.Method == http.MethodPost {
			h.handleSync(w, r, user, parts[2], true)
			return true
		}
	}
	return false
}

func splitPath(path string) []string {
	trimmed := strings.Trim(path, "/")
	if trimmed == "" {
		return nil
	}
	parts := strings.Split(trimmed, "/")
	for _, p := range parts {
		if p == "" || p == "." || p == ".." {
			return nil
		}
	}
	return parts
}

func (h *Handler) handleStart(w http.ResponseWriter, r *http.Request, user store.User, itemID, episodeID string) {
	if r.Method != http.MethodPost {
		methodNotAllowed(w)
		return
	}
	if user.ID == "" {
		http.Error(w, "Unauthorized", http.StatusUnauthorized)
		return
	}
	item, err := h.loadItem(r.Context(), itemID, episodeID)
	if errors.Is(err, sql.ErrNoRows) {
		http.NotFound(w, r)
		return
	}
	if err != nil {
		h.serverError(w, err)
		return
	}
	if !canAccess(user, item) {
		http.Error(w, "Forbidden", http.StatusForbidden)
		return
	}
	if len(item.Files) == 0 {
		http.NotFound(w, r)
		return
	}
	var body map[string]any
	if r.Body != nil {
		data, readErr := io.ReadAll(io.LimitReader(r.Body, 1<<20+1))
		if readErr != nil {
			http.Error(w, "Invalid request body", 400)
			return
		}
		if len(data) > 1<<20 {
			http.Error(w, "Request body too large", http.StatusRequestEntityTooLarge)
			return
		}
		if len(strings.TrimSpace(string(data))) > 0 && json.Unmarshal(data, &body) != nil {
			http.Error(w, "Invalid request body", 400)
			return
		}
	}
	if body == nil {
		body = map[string]any{}
	}
	device, _ := body["deviceInfo"].(map[string]any)
	if device == nil {
		device = map[string]any{}
	}
	deviceID, device, err := h.upsertDevice(r.Context(), user, r, cleanDevice(device))
	if err != nil {
		h.serverError(w, err)
		return
	}
	mediaPlayer, _ := body["mediaPlayer"].(string)
	if mediaPlayer == "" {
		mediaPlayer = "unknown"
	}
	supported := stringSlice(body["supportedMimeTypes"])
	forceDirect, _ := body["forceDirectPlay"].(bool)
	forceTranscode, _ := body["forceTranscode"].(bool)
	progress, err := h.readProgress(r.Context(), user.ID, item, episodeID)
	if err != nil {
		h.serverError(w, err)
		return
	}
	start := 0.0
	if progress != nil && !progress.finished {
		start = progress.currentTime
	}
	start = math.Max(0, start)
	playMethod := 0
	if forceDirect || (!forceTranscode && len(supported) > 0 && allSupported(item.Files, supported)) {
		playMethod = 0
	} else {
		playMethod = 2
	}
	s := &session{ID: newUUID(), UserID: user.ID, LibraryID: item.LibraryID, LibraryItemID: item.ID, MediaType: item.MediaType, DisplayTitle: item.Title, DisplayAuthor: item.Author, CoverPath: item.CoverPath, DeviceID: deviceID, PlayMethod: playMethod, MediaPlayer: mediaPlayer, Duration: item.Duration, StartTime: start, CurrentTime: start, TimeListening: 0, StartedAt: nowMS(), UpdatedAt: nowMS(), Date: time.Now().Format("2006-01-02"), DayOfWeek: time.Now().Weekday().String(), MediaMetadata: item.Metadata, Chapters: item.Chapters, Item: item.ItemJSON, Device: device, Files: item.Files}
	if episodeID != "" {
		s.EpisodeID = episodeID
		for _, ep := range item.Episodes {
			if ep.ID == episodeID {
				s.Duration = ep.Duration
				s.Chapters = ep.Chapters
				break
			}
		}
	}
	if item.MediaType == "book" {
		s.BookID = item.MediaID
	}
	if item.MediaType == "podcast" && s.EpisodeID != "" {
		s.BookID = ""
	}
	if playMethod == 0 {
		s.AudioTracks = makeDirectTracks(s, item)
	} else {
		if _, err := exec.LookPath(ffmpegExecutable()); err != nil {
			writeJSON(w, http.StatusNotImplemented, map[string]string{"error": "FFmpeg is required for transcoded playback"})
			return
		}
		t, err := h.startTranscode(s, item, start)
		if err != nil {
			h.serverError(w, err)
			return
		}
		s.stream = t
		s.AudioTracks = []map[string]any{{"index": 1, "startOffset": 0, "duration": s.Duration, "title": s.DisplayTitle, "contentUrl": "/hls/" + s.ID + "/output.m3u8", "mimeType": "application/vnd.apple.mpegurl", "codec": nil, "metadata": nil}}
	}
	h.mu.Lock()
	h.sessions[s.ID] = s
	h.mu.Unlock()
	h.emit("@admins", "user_stream_update", h.publicUser(user, s))
	writeJSON(w, http.StatusOK, s.clientJSON())
}

func (h *Handler) loadItem(ctx context.Context, id, episodeID string) (*itemData, error) {
	var li struct {
		id, libraryID, mediaID, mediaType string
		title, author, path, relPath, ino sql.NullString
		isFile, isMissing, isInvalid      int
		mtime, ctime, birthtime, lastScan any
		scanVersion                       sql.NullString
		libraryFiles, extraData           []byte
		size                              any
		created, updated                  any
		folderID                          sql.NullString
	}
	err := h.db.QueryRowContext(ctx, `SELECT id,libraryId,mediaId,mediaType,title,authorNamesFirstLast,path,relPath,ino,isFile,isMissing,isInvalid,mtime,ctime,birthtime,lastScan,lastScanVersion,libraryFiles,extraData,size,createdAt,updatedAt,libraryFolderId FROM libraryItems WHERE id = ?`, id).Scan(&li.id, &li.libraryID, &li.mediaID, &li.mediaType, &li.title, &li.author, &li.path, &li.relPath, &li.ino, &li.isFile, &li.isMissing, &li.isInvalid, &li.mtime, &li.ctime, &li.birthtime, &li.lastScan, &li.scanVersion, &li.libraryFiles, &li.extraData, &li.size, &li.created, &li.updated, &li.folderID)
	if err != nil {
		return nil, err
	}
	if li.isMissing != 0 || li.isInvalid != 0 {
		return nil, sql.ErrNoRows
	}
	out := &itemData{ID: li.id, LibraryID: li.libraryID, MediaID: li.mediaID, MediaType: li.mediaType, Title: li.title.String, Author: li.author.String, Tags: []string{}, Files: []audioFile{}}
	var media map[string]any
	if li.mediaType == "book" {
		media, err = h.loadBook(ctx, li.mediaID, li.id, &out.Files, &out.Metadata, &out.Chapters, &out.Tags, &out.Title, &out.Author, &out.CoverPath, &out.Duration)
	} else if li.mediaType == "podcast" {
		media, err = h.loadPodcast(ctx, li.mediaID, li.id, episodeID, &out.Episodes, &out.Files, &out.Metadata, &out.Chapters, &out.Tags, &out.Title, &out.Author, &out.CoverPath, &out.Duration)
	} else {
		return nil, fmt.Errorf("unsupported media type %q", li.mediaType)
	}
	if err != nil {
		return nil, err
	}
	if episodeID != "" {
		found := false
		for _, ep := range out.Episodes {
			if ep.ID == episodeID {
				found = true
				out.Files = []audioFile{ep.Audio}
				out.Duration = ep.Duration
				out.Chapters = ep.Chapters
				out.Title = ep.Title
				break
			}
		}
		if !found {
			return nil, sql.ErrNoRows
		}
	}
	out.Explicit, _ = media["explicit"].(bool)
	itemMedia := map[string]any{"id": li.mediaID, "libraryItemId": li.id, "metadata": media["metadata"], "coverPath": out.CoverPath, "tags": out.Tags}
	if li.mediaType == "book" {
		filesRaw := make([]map[string]any, 0, len(out.Files))
		tracks := make([]map[string]any, 0, len(out.Files))
		offset := 0.0
		for _, f := range out.Files {
			raw := audioJSON(f)
			filesRaw = append(filesRaw, raw)
			t := map[string]any{}
			for k, v := range raw {
				t[k] = v
			}
			t["title"] = f.Metadata["filename"]
			t["startOffset"] = offset
			t["contentUrl"] = "/api/items/" + li.id + "/file/" + f.Ino
			tracks = append(tracks, t)
			offset += f.Duration
		}
		itemMedia["audioFiles"] = filesRaw
		itemMedia["chapters"] = out.Chapters
		itemMedia["ebookFile"] = nil
		itemMedia["tracks"] = tracks
		itemMedia["numTracks"] = len(out.Files)
		itemMedia["numAudioFiles"] = len(out.Files)
		itemMedia["numChapters"] = len(out.Chapters)
		itemMedia["duration"] = out.Duration
		itemMedia["size"] = li.size
	} else {
		podcastEpisodes := make([]map[string]any, 0, len(out.Episodes))
		for _, ep := range out.Episodes {
			podcastEpisodes = append(podcastEpisodes, ep.JSON)
		}
		itemMedia["episodes"] = podcastEpisodes
		itemMedia["numEpisodes"] = len(out.Episodes)
	}
	var extra map[string]any
	_ = json.Unmarshal(li.extraData, &extra)
	if extra == nil {
		extra = map[string]any{}
	}
	var libraryFiles []any
	_ = json.Unmarshal(li.libraryFiles, &libraryFiles)
	if libraryFiles == nil {
		libraryFiles = []any{}
	}
	out.ItemJSON = map[string]any{"id": li.id, "ino": nullable(li.ino), "oldLibraryItemId": extra["oldLibraryItemId"], "libraryId": li.libraryID, "folderId": nullable(li.folderID), "path": li.path.String, "relPath": nullable(li.relPath), "isFile": li.isFile != 0, "mtimeMs": millis(li.mtime), "ctimeMs": millis(li.ctime), "birthtimeMs": millis(li.birthtime), "addedAt": millis(li.created), "updatedAt": millis(li.updated), "lastScan": millis(li.lastScan), "scanVersion": nullable(li.scanVersion), "isMissing": false, "isInvalid": false, "mediaType": li.mediaType, "media": itemMedia, "libraryFiles": libraryFiles, "numFiles": len(libraryFiles), "size": li.size}
	return out, nil
}

func (h *Handler) loadBook(ctx context.Context, id, itemID string, files *[]audioFile, metadata *map[string]any, chapters *[]any, tags *[]string, title, author *string, cover *any, duration *float64) (map[string]any, error) {
	var t, subtitle, year, published, publisher, description, isbn, asin, language, coverPath sql.NullString
	var explicit, abridged int
	var narrators, audio, chapterRaw, tagsRaw, genres []byte
	var dur sql.NullFloat64
	err := h.db.QueryRowContext(ctx, `SELECT title,subtitle,publishedYear,publishedDate,publisher,description,isbn,asin,language,explicit,abridged,narrators,coverPath,duration,audioFiles,chapters,tags,genres FROM books WHERE id = ?`, id).Scan(&t, &subtitle, &year, &published, &publisher, &description, &isbn, &asin, &language, &explicit, &abridged, &narrators, &coverPath, &dur, &audio, &chapterRaw, &tagsRaw, &genres)
	if err != nil {
		return nil, err
	}
	*title = t.String
	*cover = nullable(coverPath)
	if dur.Valid {
		*duration = dur.Float64
	}
	var au, ge []string
	_ = json.Unmarshal(narrators, &au)
	_ = json.Unmarshal(genres, &ge)
	_ = json.Unmarshal(tagsRaw, tags)
	if au == nil {
		au = []string{}
	}
	if ge == nil {
		ge = []string{}
	}
	if *tags == nil {
		*tags = []string{}
	}
	authors := []map[string]any{}
	rows, e := h.db.QueryContext(ctx, `SELECT a.id,a.name FROM authors a JOIN bookAuthors ba ON ba.authorId=a.id WHERE ba.bookId=? ORDER BY ba.createdAt ASC`, id)
	if e == nil {
		for rows.Next() {
			var x, y string
			if rows.Scan(&x, &y) == nil {
				authors = append(authors, map[string]any{"id": x, "name": y})
			}
		}
		rows.Close()
	}
	series := []map[string]any{}
	rows, e = h.db.QueryContext(ctx, `SELECT s.id,s.name,bs.sequence FROM series s JOIN bookSeries bs ON bs.seriesId=s.id WHERE bs.bookId=? ORDER BY bs.createdAt ASC`, id)
	if e == nil {
		for rows.Next() {
			var x, y string
			var seq sql.NullString
			if rows.Scan(&x, &y, &seq) == nil {
				series = append(series, map[string]any{"id": x, "name": y, "sequence": seq.String})
			}
		}
		rows.Close()
	}
	var rawFiles []map[string]any
	if err = json.Unmarshal(audio, &rawFiles); err != nil {
		return nil, fmt.Errorf("decode book audio files: %w", err)
	}
	for _, rf := range rawFiles {
		f, e := parseAudioFile(rf)
		if e != nil {
			return nil, e
		}
		if !f.Exclude {
			f.Index = numberOr(f.Index, len(*files))
			*files = append(*files, f)
		}
	}
	var ch []any
	_ = json.Unmarshal(chapterRaw, &ch)
	if ch == nil {
		ch = []any{}
	}
	*chapters = ch
	authorName, authorLF := joinAuthors(authors, false), joinAuthors(authors, true)
	seriesName := joinSeries(series)
	oldMeta := map[string]any{"title": t.String, "subtitle": nullable(subtitle), "authors": authors, "narrators": au, "series": series, "genres": ge, "publishedYear": nullable(year), "publishedDate": nullable(published), "publisher": nullable(publisher), "description": nullable(description), "isbn": nullable(isbn), "asin": nullable(asin), "language": nullable(language), "explicit": explicit != 0, "abridged": abridged != 0}
	*metadata = oldMeta
	*author = authorName
	expanded := map[string]any{}
	for k, v := range oldMeta {
		expanded[k] = v
	}
	descriptionPlain := any(nil)
	if description.Valid && description.String != "" {
		descriptionPlain = stripTags(description.String)
	}
	expanded["titleIgnorePrefix"] = h.titleIgnorePrefix(ctx, t.String)
	expanded["authorName"] = authorName
	expanded["authorNameLF"] = authorLF
	expanded["narratorName"] = strings.Join(au, ", ")
	expanded["seriesName"] = seriesName
	expanded["descriptionPlain"] = descriptionPlain
	return map[string]any{"explicit": explicit != 0, "metadata": expanded}, nil
}

func (h *Handler) loadPodcast(ctx context.Context, id, itemID, episodeID string, episodes *[]episodeData, files *[]audioFile, metadata *map[string]any, chapters *[]any, tags *[]string, title, author *string, cover *any, duration *float64) (map[string]any, error) {
	var t, a, rd, feed, image, desc, page, itunes, artist, lang, ptype, coverPath sql.NullString
	var explicit, auto int
	var genres, tagsRaw []byte
	var schedule sql.NullString
	var last any
	var maxKeep, maxNew int
	err := h.db.QueryRowContext(ctx, `SELECT title,author,releaseDate,feedURL,imageURL,description,itunesPageURL,itunesId,itunesArtistId,language,podcastType,explicit,autoDownloadEpisodes,autoDownloadSchedule,lastEpisodeCheck,maxEpisodesToKeep,maxNewEpisodesToDownload,coverPath,tags,genres FROM podcasts WHERE id=?`, id).Scan(&t, &a, &rd, &feed, &image, &desc, &page, &itunes, &artist, &lang, &ptype, &explicit, &auto, &schedule, &last, &maxKeep, &maxNew, &coverPath, &tagsRaw, &genres)
	if err != nil {
		return nil, err
	}
	*title = t.String
	*author = a.String
	*cover = nullable(coverPath)
	_ = json.Unmarshal(tagsRaw, tags)
	var ge []string
	_ = json.Unmarshal(genres, &ge)
	rows, err := h.db.QueryContext(ctx, `SELECT id,"index",season,episode,episodeType,title,subtitle,description,enclosureURL,enclosureSize,enclosureType,audioFile,chapters,extraData,publishedAt,createdAt,updatedAt FROM podcastEpisodes WHERE podcastId=? ORDER BY createdAt ASC`, id)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	for rows.Next() {
		var eid, et string
		var idx, season, episodeNo, episodeType, subtitle, description, enclosureURL, enclosureType sql.NullString
		var enclosureSize, published, created, updated any
		var raw, chraw, extraRaw []byte
		if err := rows.Scan(&eid, &idx, &season, &episodeNo, &episodeType, &et, &subtitle, &description, &enclosureURL, &enclosureSize, &enclosureType, &raw, &chraw, &extraRaw, &published, &created, &updated); err != nil {
			return nil, err
		}
		var af map[string]any
		if json.Unmarshal(raw, &af) != nil {
			continue
		}
		f, e := parseAudioFile(af)
		if e != nil {
			continue
		}
		var ch []any
		_ = json.Unmarshal(chraw, &ch)
		if ch == nil {
			ch = []any{}
		}
		var extra map[string]any
		_ = json.Unmarshal(extraRaw, &extra)
		enclosure := any(nil)
		if enclosureURL.Valid {
			length := any(nil)
			if enclosureSize != nil {
				length = fmt.Sprint(enclosureSize)
			}
			enclosure = map[string]any{"url": enclosureURL.String, "type": nullable(enclosureType), "length": length}
		}
		f.Index = 1
		track := map[string]any{}
		for k, v := range audioJSON(f) {
			track[k] = v
		}
		track["index"] = 1
		track["startOffset"] = 0
		track["title"] = f.Metadata["filename"]
		track["contentUrl"] = "/api/items/" + itemID + "/file/" + f.Ino
		oldID := any(nil)
		if extra != nil {
			oldID = extra["oldEpisodeId"]
		}
		jsonEpisode := map[string]any{"libraryItemId": itemID, "podcastId": id, "id": eid, "oldEpisodeId": oldID, "index": nullable(idx), "season": nullable(season), "episode": nullable(episodeNo), "episodeType": nullable(episodeType), "title": et, "subtitle": nullable(subtitle), "description": nullable(description), "enclosure": enclosure, "guid": nil, "pubDate": nil, "chapters": ch, "audioFile": audioJSON(f), "publishedAt": millis(published), "addedAt": millis(created), "updatedAt": millis(updated), "audioTrack": track, "size": f.Metadata["size"], "duration": f.Duration}
		if extra != nil {
			if extra["guid"] != nil {
				jsonEpisode["guid"] = extra["guid"]
			}
		}
		if pub, ok := published.(string); ok {
			jsonEpisode["pubDate"] = pub
		}
		ep := episodeData{ID: eid, Title: et, Audio: f, Duration: f.Duration, Chapters: ch, JSON: jsonEpisode}
		*episodes = append(*episodes, ep)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	oldMeta := map[string]any{"title": t.String, "author": a.String, "description": nullable(desc), "releaseDate": nullable(rd), "genres": ge, "feedUrl": nullable(feed), "imageUrl": nullable(image), "itunesPageUrl": nullable(page), "itunesId": nullable(itunes), "itunesArtistId": nullable(artist), "explicit": explicit != 0, "language": nullable(lang), "type": nullable(ptype)}
	*metadata = oldMeta
	*chapters = []any{}
	expanded := map[string]any{}
	for k, v := range oldMeta {
		expanded[k] = v
	}
	expanded["titleIgnorePrefix"] = h.titleIgnorePrefix(ctx, t.String)
	return map[string]any{"explicit": explicit != 0, "metadata": expanded}, nil
}

type progress struct {
	currentTime float64
	finished    bool
}

func (h *Handler) readProgress(ctx context.Context, userID string, item *itemData, episodeID string) (*progress, error) {
	mediaID, kind := item.MediaID, "book"
	if episodeID != "" {
		mediaID, kind = episodeID, "podcastEpisode"
	}
	var current sql.NullFloat64
	var finished int
	err := h.db.QueryRowContext(ctx, `SELECT currentTime,isFinished FROM mediaProgresses WHERE userId=? AND mediaItemId=? AND mediaItemType=? LIMIT 1`, userID, mediaID, kind).Scan(&current, &finished)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	return &progress{currentTime: current.Float64, finished: finished != 0}, nil
}

func (h *Handler) handleTrack(w http.ResponseWriter, r *http.Request, id, indexText string) {
	if r.Method != http.MethodGet && r.Method != http.MethodHead {
		methodNotAllowed(w)
		return
	}
	index, err := strconv.Atoi(indexText)
	if err != nil {
		http.Error(w, "", http.StatusBadRequest)
		return
	}
	h.mu.RLock()
	s := h.sessions[id]
	h.mu.RUnlock()
	if s == nil {
		http.NotFound(w, r)
		return
	}
	var chosen map[string]any
	for _, track := range s.AudioTracks {
		idx := int(number(track["index"], 1))
		if idx == index {
			chosen = track
			break
		}
	}
	if chosen == nil && s.MediaType == "podcast" && index == 0 && len(s.AudioTracks) > 0 {
		chosen = s.AudioTracks[0]
	}
	if chosen == nil {
		http.NotFound(w, r)
		return
	}
	if s.PlayMethod == 2 {
		if content, ok := chosen["contentUrl"].(string); ok && content != "" {
			http.Redirect(w, r, content, http.StatusFound)
			return
		}
	}
	ino, _ := chosen["ino"].(string)
	if ino == "" {
		if meta, ok := chosen["metadata"].(map[string]any); ok {
			_ = meta
		}
	}
	var f *audioFile
	for i := range s.Files {
		if s.Files[i].Ino == ino {
			f = &s.Files[i]
			break
		}
	}
	if f == nil {
		http.Error(w, "", http.StatusInternalServerError)
		return
	}
	path, _ := f.Metadata["path"].(string)
	if path == "" {
		http.Error(w, "", http.StatusInternalServerError)
		return
	}
	serveMedia(w, r, path, f.MimeType)
}

func (h *Handler) handleItemFile(w http.ResponseWriter, r *http.Request, user store.User, itemID, ino string) {
	if r.Method != http.MethodGet && r.Method != http.MethodHead {
		methodNotAllowed(w)
		return
	}
	item, err := h.loadItem(r.Context(), itemID, "")
	if errors.Is(err, sql.ErrNoRows) {
		http.NotFound(w, r)
		return
	}
	if err != nil {
		h.serverError(w, err)
		return
	}
	if !canAccess(user, item) {
		http.Error(w, "Forbidden", 403)
		return
	}
	for _, f := range item.Files {
		if f.Ino == ino {
			path, _ := f.Metadata["path"].(string)
			if path == "" {
				http.Error(w, "", 500)
				return
			}
			serveMedia(w, r, path, f.MimeType)
			return
		}
	}
	for _, ep := range item.Episodes {
		if ep.Audio.Ino == ino {
			path, _ := ep.Audio.Metadata["path"].(string)
			if path != "" {
				serveMedia(w, r, path, ep.Audio.MimeType)
				return
			}
		}
	}
	http.NotFound(w, r)
}

func (h *Handler) handleGetSession(w http.ResponseWriter, r *http.Request, user store.User, id string) {
	if user.ID == "" {
		http.Error(w, "Unauthorized", 401)
		return
	}
	s := h.openSession(w, r, user, id)
	if s == nil {
		return
	}
	writeJSON(w, 200, s.clientJSON())
}

func (h *Handler) openSession(w http.ResponseWriter, r *http.Request, user store.User, id string) *session {
	h.mu.RLock()
	s := h.sessions[id]
	h.mu.RUnlock()
	if s == nil {
		http.NotFound(w, r)
		return nil
	}
	if s.UserID != user.ID && !isAdmin(user) {
		http.Error(w, "Forbidden", 403)
		return nil
	}
	return s
}

func (h *Handler) handleSync(w http.ResponseWriter, r *http.Request, user store.User, id string, closeSession bool) {
	if user.ID == "" {
		http.Error(w, "Unauthorized", 401)
		return
	}
	s := h.openSession(w, r, user, id)
	if s == nil {
		return
	}
	var body map[string]any
	dec := json.NewDecoder(io.LimitReader(r.Body, 1<<20))
	if err := dec.Decode(&body); err != nil && err != io.EOF {
		http.Error(w, "Invalid request body", 400)
		return
	}
	if len(body) > 0 {
		ct, ok := asFloat(body["currentTime"])
		if !ok || ct < 0 {
			http.Error(w, "Invalid currentTime", 400)
			return
		}
		listened, ok := asFloat(body["timeListened"])
		if !ok || listened < 0 {
			http.Error(w, "Invalid timeListened", 400)
			return
		}
		previousCurrentTime := s.CurrentTime
		s.CurrentTime = ct
		s.TimeListening += listened
		s.UpdatedAt = nowMS()
		s.Date = time.UnixMilli(s.UpdatedAt).Format("2006-01-02")
		s.DayOfWeek = time.UnixMilli(s.UpdatedAt).Weekday().String()
		if err := h.saveSession(r.Context(), s); err != nil {
			h.serverError(w, err)
			return
		}
		if _, err := h.syncProgress(r.Context(), user.ID, s, body, previousCurrentTime); err != nil {
			h.serverError(w, err)
			return
		}
	}
	if closeSession {
		if len(body) == 0 {
			if err := h.saveSession(r.Context(), s); err != nil {
				h.serverError(w, err)
				return
			}
		}
		h.mu.Lock()
		delete(h.sessions, id)
		h.mu.Unlock()
		if s.stream != nil {
			_ = s.stream.close()
		}
		h.emit(user.ID, "user_session_closed", s.ID)
	}
	sendOK(w)
}

func (h *Handler) saveSession(ctx context.Context, s *session) error {
	if s.TimeListening <= 0 {
		return nil
	}
	return h.saveSessionRecord(ctx, s)
}

func (h *Handler) saveSessionRecord(ctx context.Context, s *session) error {
	mediaID, kind := s.BookID, "book"
	if s.EpisodeID != "" {
		mediaID, kind = s.EpisodeID, "podcastEpisode"
	}
	metadata, err := persistedMetadataJSON(s.MediaMetadata)
	if err != nil {
		return err
	}
	extra, _ := json.Marshal(map[string]any{"libraryItemId": s.LibraryItemID})
	now := dbtime.Format(time.UnixMilli(s.UpdatedAt))
	created := dbtime.Format(time.UnixMilli(s.StartedAt))
	_, err = h.db.ExecContext(ctx, `INSERT INTO playbackSessions (id,mediaItemId,mediaItemType,displayTitle,displayAuthor,duration,playMethod,mediaPlayer,startTime,currentTime,serverVersion,coverPath,timeListening,mediaMetadata,date,dayOfWeek,extraData,userId,deviceId,libraryId,createdAt,updatedAt) VALUES (?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?) ON CONFLICT(id) DO UPDATE SET currentTime=excluded.currentTime,timeListening=excluded.timeListening,updatedAt=excluded.updatedAt,date=excluded.date,dayOfWeek=excluded.dayOfWeek,mediaMetadata=excluded.mediaMetadata`, s.ID, mediaID, kind, s.DisplayTitle, s.DisplayAuthor, s.Duration, s.PlayMethod, s.MediaPlayer, s.StartTime, s.CurrentTime, "2.37.2", s.CoverPath, int64(s.TimeListening), string(metadata), s.Date, s.DayOfWeek, string(extra), s.UserID, nilIfEmpty(s.DeviceID), s.LibraryID, created, now)
	return err
}

// persistedMediaMetadata preserves the legacy Book.oldMetadataToJSON shape in
// playbackSessions without changing the expanded metadata returned by the API.
// Key ORDER matters: the persisted bytes feed rollback compatibility, and
// Node's JSON.stringify keeps insertion order, so this is marshaled through an
// ordered struct (Book.js:550-569). Missing keys marshal as explicit nulls.
type persistedMediaMetadata struct {
	Title         json.RawMessage `json:"title"`
	Subtitle      json.RawMessage `json:"subtitle"`
	Authors       json.RawMessage `json:"authors"`
	Narrators     json.RawMessage `json:"narrators"`
	Series        json.RawMessage `json:"series"`
	Genres        json.RawMessage `json:"genres"`
	PublishedYear json.RawMessage `json:"publishedYear"`
	PublishedDate json.RawMessage `json:"publishedDate"`
	Publisher     json.RawMessage `json:"publisher"`
	Description   json.RawMessage `json:"description"`
	ISBN          json.RawMessage `json:"isbn"`
	ASIN          json.RawMessage `json:"asin"`
	Language      json.RawMessage `json:"language"`
	Explicit      json.RawMessage `json:"explicit"`
	Abridged      json.RawMessage `json:"abridged"`
}

func persistedMetadataJSON(metadata map[string]any) ([]byte, error) {
	raw, err := json.Marshal(metadata)
	if err != nil {
		return nil, err
	}
	norm := map[string]json.RawMessage{}
	if err := json.Unmarshal(raw, &norm); err != nil {
		return nil, err
	}
	return json.Marshal(persistedMediaMetadata{
		Title: norm["title"], Subtitle: norm["subtitle"], Authors: norm["authors"], Narrators: norm["narrators"],
		Series: norm["series"], Genres: norm["genres"], PublishedYear: norm["publishedYear"], PublishedDate: norm["publishedDate"],
		Publisher: norm["publisher"], Description: norm["description"], ISBN: norm["isbn"], ASIN: norm["asin"],
		Language: norm["language"], Explicit: norm["explicit"], Abridged: norm["abridged"],
	})
}

func (h *Handler) syncProgress(ctx context.Context, userID string, s *session, body map[string]any, previousCurrentTime float64) (bool, error) {
	item, err := h.loadItem(ctx, s.LibraryItemID, s.EpisodeID)
	if err != nil {
		return false, err
	}
	mediaID, kind, podcastID := s.BookID, "book", any(nil)
	if s.EpisodeID != "" {
		mediaID, kind = s.EpisodeID, "podcastEpisode"
		podcastID = item.MediaID
	}
	duration := s.Duration
	if d, ok := asFloat(body["duration"]); ok && d > 0 {
		duration = d
	}
	current := s.CurrentTime
	progress := 0.0
	if duration > 0 {
		progress = math.Max(0, math.Min(current/duration, 1))
	}
	markFinished := false
	var settingsRaw []byte
	_ = h.db.QueryRowContext(ctx, `SELECT settings FROM libraries WHERE id=?`, s.LibraryID).Scan(&settingsRaw)
	var settings struct {
		MarkAsFinishedPercentComplete float64 `json:"markAsFinishedPercentComplete"`
		MarkAsFinishedTimeRemaining   float64 `json:"markAsFinishedTimeRemaining"`
	}
	_ = json.Unmarshal(settingsRaw, &settings)
	if duration > 0 {
		if settings.MarkAsFinishedPercentComplete > 0 {
			markFinished = progress > settings.MarkAsFinishedPercentComplete/100
		} else {
			remaining := settings.MarkAsFinishedTimeRemaining
			if remaining == 0 {
				remaining = 10
			}
			markFinished = duration-current < remaining
		}
	}
	var existingID string
	var oldFinished int
	var oldExtra []byte
	var oldFinishedAt sql.NullString
	err = h.db.QueryRowContext(ctx, `SELECT id,isFinished,extraData,finishedAt FROM mediaProgresses WHERE userId=? AND mediaItemId=? AND mediaItemType=? LIMIT 1`, userID, mediaID, kind).Scan(&existingID, &oldFinished, &oldExtra, &oldFinishedAt)
	if err != nil && !errors.Is(err, sql.ErrNoRows) {
		return false, err
	}
	if !errors.Is(err, sql.ErrNoRows) {
		var updated any
		if e := h.db.QueryRowContext(ctx, `SELECT updatedAt FROM mediaProgresses WHERE id=?`, existingID).Scan(&updated); e != nil {
			return false, e
		}
		if incoming, ok := body["lastUpdate"]; ok {
			if ms, valid := asFloat(incoming); valid && millis(updated) != nil && number(millis(updated), 0) > ms {
				return false, nil
			}
		}
	}
	if oldFinished != 0 && current != previousCurrentTime && !markFinished {
		markFinished = false
	}
	finishedAt := any(nil)
	if markFinished {
		finishedAt = oldFinishedAt.String
		if !oldFinishedAt.Valid {
			finishedAt = dbtime.Now()
		}
	} else if oldFinished != 0 && current != previousCurrentTime {
		finishedAt = nil
	}
	extra := map[string]any{"libraryItemId": s.LibraryItemID, "progress": progress}
	if len(oldExtra) > 0 {
		var old map[string]any
		_ = json.Unmarshal(oldExtra, &old)
		for k, v := range old {
			if k != "progress" && k != "libraryItemId" {
				extra[k] = v
			}
		}
	}
	extraRaw, _ := json.Marshal(extra)
	now := dbtime.Now()
	updatedAt := any(now)
	if raw, exists := body["lastUpdate"]; exists {
		if ms, ok := asFloat(raw); ok && ms > 0 {
			updatedAt = dbtime.Format(time.UnixMilli(int64(ms)))
		}
	}
	hide := false
	progressID := existingID
	if errors.Is(err, sql.ErrNoRows) {
		progressID = newUUID()
		_, err = h.db.ExecContext(ctx, `INSERT INTO mediaProgresses (id,mediaItemId,mediaItemType,duration,currentTime,isFinished,hideFromContinueListening,finishedAt,extraData,podcastId,userId,createdAt,updatedAt) VALUES (?,?,?,?,?,?,?,?,?,?,?,?,?)`, progressID, mediaID, kind, duration, current, markFinished, hide, finishedAt, string(extraRaw), podcastID, userID, now, now)
	} else {
		_, err = h.db.ExecContext(ctx, `UPDATE mediaProgresses SET duration=?,currentTime=?,isFinished=?,hideFromContinueListening=?,finishedAt=?,extraData=?,updatedAt=? WHERE id=?`, duration, current, markFinished, hide, finishedAt, string(extraRaw), updatedAt, existingID)
	}
	if err == nil {
		lastUpdate := nowMS()
		if millis(updatedAt) != nil {
			lastUpdate = int64(number(millis(updatedAt), float64(lastUpdate)))
		}
		h.emit(userID, "user_item_progress_updated", map[string]any{"id": progressID, "sessionId": s.ID, "deviceDescription": deviceDescription(s.Device), "data": map[string]any{"id": progressID, "libraryItemId": s.LibraryItemID, "mediaItemId": mediaID, "mediaItemType": kind, "duration": duration, "currentTime": current, "progress": progress, "isFinished": markFinished, "hideFromContinueListening": false, "lastUpdate": lastUpdate}})
	}
	return err == nil, err
}

func (h *Handler) handleHistory(w http.ResponseWriter, r *http.Request, user store.User) {
	if user.ID == "" {
		http.Error(w, "Unauthorized", 401)
		return
	}
	if !isAdmin(user) {
		http.Error(w, "", http.StatusNotFound)
		return
	}
	validSort := map[string]string{"displayTitle": "displayTitle", "duration": "duration", "playMethod": "playMethod", "startTime": "startTime", "currentTime": "currentTime", "timeListening": "timeListening", "updatedAt": "updatedAt", "createdAt": "createdAt"}
	sortKey := r.URL.Query().Get("sort")
	if _, ok := validSort[sortKey]; !ok {
		sortKey = "updatedAt"
	}
	direction := "ASC"
	if r.URL.Query().Get("desc") == "1" {
		direction = "DESC"
	}
	perPage := 10
	if n, e := strconv.Atoi(r.URL.Query().Get("itemsPerPage")); e == nil && n != 0 {
		perPage = n
	}
	if perPage < 1 {
		perPage = 10
	}
	page := 0
	if n, e := strconv.Atoi(r.URL.Query().Get("page")); e == nil {
		page = n
	}
	if page < 0 {
		page = 0
	}
	userFilter := r.URL.Query().Get("user")
	if !validUUID(userFilter) {
		userFilter = ""
	}
	where := ""
	args := []any{}
	if userFilter != "" {
		where = ` WHERE userId=?`
		args = append(args, userFilter)
	}
	var total int
	if err := h.db.QueryRowContext(r.Context(), `SELECT COUNT(*) FROM playbackSessions`+where, args...).Scan(&total); err != nil {
		h.serverError(w, err)
		return
	}
	query := `SELECT id,userId,libraryId,mediaItemId,mediaItemType,displayTitle,displayAuthor,duration,playMethod,mediaPlayer,startTime,currentTime,serverVersion,coverPath,timeListening,mediaMetadata,date,dayOfWeek,extraData,deviceId,createdAt,updatedAt FROM playbackSessions` + where + ` ORDER BY ` + validSort[sortKey] + ` ` + direction + ` LIMIT ? OFFSET ?`
	args = append(args, perPage, perPage*page)
	rows, err := h.db.QueryContext(r.Context(), query, args...)
	if err != nil {
		h.serverError(w, err)
		return
	}
	sessions := []historySession{}
	for rows.Next() {
		s, err := scanHistoryRow(rows)
		if err != nil {
			_ = rows.Close()
			h.serverError(w, err)
			return
		}
		sessions = append(sessions, s)
	}
	if err := rows.Err(); err != nil {
		_ = rows.Close()
		h.serverError(w, err)
		return
	}
	if err := rows.Close(); err != nil {
		h.serverError(w, err)
		return
	}
	result := []map[string]any{}
	for _, s := range sessions {
		device, err := h.oldDevice(r.Context(), s.deviceID.String)
		if err != nil {
			h.serverError(w, err)
			return
		}
		obj := s.oldJSON(device)
		var username sql.NullString
		_ = h.db.QueryRowContext(r.Context(), `SELECT username FROM users WHERE id=?`, s.userID).Scan(&username)
		if username.Valid {
			obj["user"] = map[string]any{"id": s.userID, "username": username.String}
		}
		result = append(result, obj)
	}
	payload := map[string]any{"total": total, "numPages": int(math.Ceil(float64(total) / float64(perPage))), "page": page, "itemsPerPage": perPage, "sessions": result}
	if userFilter != "" {
		payload["userId"] = userFilter
	}
	writeJSON(w, 200, payload)
}

func (h *Handler) handleOpenSessions(w http.ResponseWriter, r *http.Request, user store.User) {
	if user.ID == "" {
		http.Error(w, "Unauthorized", 401)
		return
	}
	if !isAdmin(user) {
		http.Error(w, "", http.StatusNotFound)
		return
	}
	writeJSON(w, 200, map[string]any{"sessions": h.OpenSessions(), "shareSessions": []any{}})
}

// OpenSessions returns the current in-memory playback sessions in the same
// shape used by GET /api/sessions/open. Runtime APIs use it to include active
// sessions in their online-users response.
func (h *Handler) OpenSessions() []map[string]any {
	h.mu.RLock()
	sessions := make([]*session, 0, len(h.sessions))
	for _, s := range h.sessions {
		sessions = append(sessions, s)
	}
	h.mu.RUnlock()
	out := make([]map[string]any, 0, len(sessions))
	for _, s := range sessions {
		entry := s.publicJSON()
		var username string
		_ = h.db.QueryRow(`SELECT username FROM users WHERE id=?`, s.UserID).Scan(&username)
		entry["user"] = map[string]any{"id": s.UserID, "username": username}
		out = append(out, entry)
	}
	return out
}

func (h *Handler) handleDeleteHistory(w http.ResponseWriter, r *http.Request, user store.User, id string) {
	if user.ID == "" {
		http.Error(w, "Unauthorized", 401)
		return
	}
	if !canDelete(user) {
		http.Error(w, "", 403)
		return
	}
	var exists int
	if err := h.db.QueryRowContext(r.Context(), `SELECT COUNT(*) FROM playbackSessions WHERE id=?`, id).Scan(&exists); err != nil {
		h.serverError(w, err)
		return
	}
	if exists == 0 {
		http.NotFound(w, r)
		return
	}
	h.removeOpenSession(id)
	if _, err := h.db.ExecContext(r.Context(), `DELETE FROM playbackSessions WHERE id=?`, id); err != nil {
		h.serverError(w, err)
		return
	}
	sendOK(w)
}

func (h *Handler) handleBatchDelete(w http.ResponseWriter, r *http.Request, user store.User) {
	if user.ID == "" {
		http.Error(w, "Unauthorized", 401)
		return
	}
	if !isAdmin(user) {
		http.Error(w, "", 403)
		return
	}
	var body struct {
		Sessions []string `json:"sessions"`
	}
	if err := json.NewDecoder(io.LimitReader(r.Body, 1<<20)).Decode(&body); err != nil || len(body.Sessions) == 0 {
		http.Error(w, `Invalid request body. "sessions" array of session id strings is required.`, 400)
		return
	}
	for _, id := range body.Sessions {
		if !validUUID(id) {
			http.Error(w, `Invalid request body. "sessions" array of session id strings is required.`, 400)
			return
		}
	}
	for _, id := range body.Sessions {
		h.removeOpenSession(id)
	}
	placeholders := strings.TrimRight(strings.Repeat("?,", len(body.Sessions)), ",")
	args := make([]any, len(body.Sessions))
	for i, id := range body.Sessions {
		args[i] = id
	}
	if _, err := h.db.ExecContext(r.Context(), `DELETE FROM playbackSessions WHERE id IN (`+placeholders+`)`, args...); err != nil {
		http.Error(w, "Failed to remove sessions", 500)
		return
	}
	sendOK(w)
}

func (h *Handler) removeOpenSession(id string) {
	h.mu.Lock()
	s := h.sessions[id]
	delete(h.sessions, id)
	h.mu.Unlock()
	if s != nil && s.stream != nil {
		_ = s.stream.close()
	}
}

func (h *Handler) handleLocalSync(w http.ResponseWriter, r *http.Request, user store.User, all bool) {
	if user.ID == "" {
		http.Error(w, "Unauthorized", 401)
		return
	}
	data, err := io.ReadAll(io.LimitReader(r.Body, 1<<20+1))
	if err != nil || len(data) > 1<<20 {
		http.Error(w, "Invalid request body", 400)
		return
	}
	var body map[string]any
	if json.Unmarshal(data, &body) != nil {
		http.Error(w, "Invalid request body", 400)
		return
	}
	device, _ := body["deviceInfo"].(map[string]any)
	if device == nil {
		device = map[string]any{}
	}
	_, device, err = h.upsertDevice(r.Context(), user, r, cleanDevice(device))
	if err != nil {
		h.serverError(w, err)
		return
	}
	if !all {
		result, errMsg := h.syncOneLocal(r.Context(), user, body, device)
		if errMsg != "" {
			http.Error(w, errMsg, 500)
			return
		}
		if result == nil {
			sendOK(w)
			return
		}
		sendOK(w)
		return
	}
	sessions, _ := body["sessions"].([]any)
	results := make([]map[string]any, 0, len(sessions))
	for _, raw := range sessions {
		obj, ok := raw.(map[string]any)
		if !ok {
			results = append(results, map[string]any{"id": nil, "success": false, "error": "Media item not found"})
			continue
		}
		result, msg := h.syncOneLocal(r.Context(), user, obj, device)
		if msg != "" {
			results = append(results, map[string]any{"id": obj["id"], "success": false, "error": msg})
		} else {
			results = append(results, result)
		}
	}
	writeJSON(w, 200, map[string]any{"results": results})
}

func (h *Handler) syncOneLocal(ctx context.Context, user store.User, obj, device map[string]any) (map[string]any, string) {
	itemID, _ := obj["libraryItemId"].(string)
	episodeID, _ := obj["episodeId"].(string)
	item, err := h.loadItem(ctx, itemID, episodeID)
	if err != nil {
		return nil, "Media item not found"
	}
	var libraryExists int
	if err = h.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM libraries WHERE id=?`, item.LibraryID).Scan(&libraryExists); err != nil || libraryExists == 0 {
		return nil, "Library not found"
	}
	oldID, _ := obj["id"].(string)
	id := oldID
	if strings.HasPrefix(id, "play_local_") {
		h.mu.Lock()
		if h.localIDs[id] == "" {
			h.localIDs[id] = newUUID()
		}
		id = h.localIDs[oldID]
		h.mu.Unlock()
	}
	if id == "" {
		id = newUUID()
	}
	updated := timeFromPayload(obj["updatedAt"])
	started := timeFromPayload(obj["startedAt"])
	if updated == 0 {
		updated = nowMS()
	}
	if started == 0 {
		started = updated
	}
	current := number(obj["currentTime"], 0)
	listened := number(obj["timeListening"], 0)
	duration := number(obj["duration"], item.Duration)
	if duration <= 0 {
		duration = item.Duration
	}
	mediaMeta, _ := obj["mediaMetadata"].(map[string]any)
	if mediaMeta == nil {
		mediaMeta = map[string]any{}
	}
	for k, v := range item.Metadata {
		if _, ok := mediaMeta[k]; !ok {
			mediaMeta[k] = v
		}
	}
	title, _ := obj["displayTitle"].(string)
	if title == "" {
		title = item.Title
	}
	author, _ := obj["displayAuthor"].(string)
	if author == "" {
		author = item.Author
	}
	playMethod := int(number(obj["playMethod"], 3))
	mediaType := item.MediaType
	bookID := item.MediaID
	if episodeID != "" {
		bookID = ""
	}
	s := &session{ID: id, UserID: user.ID, LibraryID: item.LibraryID, LibraryItemID: item.ID, BookID: bookID, EpisodeID: episodeID, MediaType: mediaType, DisplayTitle: title, DisplayAuthor: author, CoverPath: item.CoverPath, PlayMethod: playMethod, MediaPlayer: stringOr(obj["mediaPlayer"], "unknown"), Duration: duration, StartTime: number(obj["startTime"], 0), CurrentTime: current, TimeListening: listened, StartedAt: started, UpdatedAt: updated, Date: time.UnixMilli(updated).Format("2006-01-02"), DayOfWeek: time.UnixMilli(updated).Weekday().String(), MediaMetadata: mediaMeta, Chapters: item.Chapters, Device: device, Files: item.Files}
	if err = h.saveSessionRecord(ctx, s); err != nil {
		return nil, "Failed to save session"
	}
	payload := map[string]any{"currentTime": current, "timeListened": 0, "duration": duration, "lastUpdate": updated}
	synced, err := h.syncProgress(ctx, user.ID, s, payload, current)
	if err != nil {
		return nil, "Failed to sync progress"
	}
	return map[string]any{"id": id, "success": true, "progressSynced": synced}, ""
}

type historySession struct {
	id, userID, libraryID, mediaItemID, mediaItemType, displayTitle, displayAuthor string
	duration, playMethod, startTime, currentTime                                   float64
	mediaPlayer, serverVersion, coverPath, date, dayOfWeek                         string
	timeListening                                                                  int64
	mediaMetadata, extraData                                                       []byte
	deviceID                                                                       sql.NullString
	createdAt, updatedAt                                                           any
}

func scanHistoryRow(rows *sql.Rows) (historySession, error) {
	var s historySession
	var mp, md, ed sql.NullString
	var cp, sv, dt, dow sql.NullString
	err := rows.Scan(&s.id, &s.userID, &s.libraryID, &s.mediaItemID, &s.mediaItemType, &s.displayTitle, &s.displayAuthor, &s.duration, &s.playMethod, &mp, &s.startTime, &s.currentTime, &sv, &cp, &s.timeListening, &md, &dt, &dow, &ed, &s.deviceID, &s.createdAt, &s.updatedAt)
	s.mediaPlayer = mp.String
	s.serverVersion = sv.String
	s.coverPath = cp.String
	s.date = dt.String
	s.dayOfWeek = dow.String
	s.mediaMetadata = []byte(md.String)
	s.extraData = []byte(ed.String)
	return s, err
}
func (s historySession) oldJSON(device map[string]any) map[string]any {
	var extra, metadata map[string]any
	_ = json.Unmarshal(s.extraData, &extra)
	_ = json.Unmarshal(s.mediaMetadata, &metadata)
	if metadata == nil {
		metadata = map[string]any{}
	}
	podcast := s.mediaItemType == "podcastEpisode"
	bookID, episodeID := any(s.mediaItemID), any(nil)
	if podcast {
		bookID = nil
		episodeID = s.mediaItemID
	}
	return map[string]any{"id": s.id, "userId": s.userID, "libraryId": s.libraryID, "libraryItemId": extra["libraryItemId"], "bookId": bookID, "episodeId": episodeID, "mediaType": map[bool]string{true: "podcast", false: "book"}[podcast], "mediaMetadata": metadata, "chapters": []any{}, "displayTitle": s.displayTitle, "displayAuthor": s.displayAuthor, "coverPath": nullableString(s.coverPath), "duration": s.duration, "playMethod": s.playMethod, "mediaPlayer": nullableString(s.mediaPlayer), "deviceInfo": device, "serverVersion": nullableString(s.serverVersion), "date": nullableString(s.date), "dayOfWeek": nullableString(s.dayOfWeek), "timeListening": s.timeListening, "startTime": s.startTime, "currentTime": s.currentTime, "startedAt": millis(s.createdAt), "updatedAt": millis(s.updatedAt)}
}
func (h *Handler) oldDevice(ctx context.Context, id string) (map[string]any, error) {
	if id == "" {
		return nil, nil
	}
	var deviceID, userID, clientName, clientVersion, ip, deviceName, deviceVersion string
	var extraRaw []byte
	err := h.db.QueryRowContext(ctx, `SELECT deviceId,userId,clientName,clientVersion,ipAddress,deviceName,deviceVersion,extraData FROM devices WHERE id=?`, id).Scan(&deviceID, &userID, &clientName, &clientVersion, &ip, &deviceName, &deviceVersion, &extraRaw)
	if err != nil {
		return nil, err
	}
	var e map[string]any
	_ = json.Unmarshal(extraRaw, &e)
	browserVersion, sdkVersion := any(nil), any(nil)
	if clientName == "Abs Android" {
		sdkVersion = nullableString(deviceVersion)
	} else {
		browserVersion = nullableString(deviceVersion)
	}
	return map[string]any{"id": id, "deviceId": deviceID, "userId": userID, "ipAddress": ip, "browserName": e["browserName"], "browserVersion": browserVersion, "osName": e["osName"], "osVersion": e["osVersion"], "clientVersion": clientVersion, "manufacturer": e["manufacturer"], "model": e["model"], "sdkVersion": sdkVersion, "deviceName": deviceName, "clientName": clientName}, nil
}

func (h *Handler) titleIgnorePrefix(ctx context.Context, title string) string {
	var raw []byte
	if err := h.db.QueryRowContext(ctx, `SELECT value FROM settings WHERE key='server-settings'`).Scan(&raw); err != nil {
		return title
	}
	var settings struct {
		SortingPrefixes []string `json:"sortingPrefixes"`
	}
	if json.Unmarshal(raw, &settings) != nil {
		return title
	}
	for _, prefix := range settings.SortingPrefixes {
		if strings.HasPrefix(strings.ToLower(title), strings.ToLower(prefix)+" ") {
			rest := title[len(prefix)+1:]
			if prefix != "" {
				prefix = strings.ToUpper(prefix[:1]) + prefix[1:]
			}
			return rest + ", " + prefix
		}
	}
	return title
}

func (h *Handler) publicUser(user store.User, s *session) map[string]any {
	client := s.clientJSON()
	client["libraryItem"] = nil
	return map[string]any{"id": user.ID, "username": user.Username, "type": user.Type, "session": client, "lastSeen": user.LastSeen, "createdAt": user.CreatedAt}
}
func validUUID(v string) bool {
	if len(v) != 36 {
		return false
	}
	for i, c := range v {
		if i == 8 || i == 13 || i == 18 || i == 23 {
			if c != '-' {
				return false
			}
			continue
		}
		if !((c >= '0' && c <= '9') || (c >= 'a' && c <= 'f') || (c >= 'A' && c <= 'F')) {
			return false
		}
	}
	return true
}
func canDelete(u store.User) bool {
	if !u.IsActive {
		return false
	}
	var p map[string]any
	if json.Unmarshal(u.Permissions, &p) != nil {
		return false
	}
	allow, _ := p["delete"].(bool)
	return allow
}
func timeFromPayload(v any) int64 {
	if ms, ok := asFloat(v); ok {
		return int64(ms)
	}
	if s, ok := v.(string); ok {
		if ms, err := strconv.ParseInt(s, 10, 64); err == nil {
			return ms
		}
		if d, err := time.Parse(time.RFC3339Nano, s); err == nil {
			return d.UnixMilli()
		}
	}
	return 0
}
func stringOr(v any, fallback string) string {
	if s, ok := v.(string); ok && s != "" {
		return s
	}
	return fallback
}
func nullableString(v string) any {
	if v == "" {
		return nil
	}
	return v
}
func deviceDescription(device map[string]any) string {
	model := stringOr(device["model"], "")
	if model != "" {
		sdk := stringOr(device["sdkVersion"], "")
		version := stringOr(device["clientVersion"], "")
		if sdk != "" {
			return model + " SDK " + sdk + " / v" + version
		}
		return model + " / v" + version
	}
	return stringOr(device["osName"], "") + " " + stringOr(device["osVersion"], "") + " / " + stringOr(device["browserName"], "")
}
func (h *Handler) handleHLS(w http.ResponseWriter, r *http.Request, id, name string) {
	if r.Method != http.MethodGet && r.Method != http.MethodHead {
		methodNotAllowed(w)
		return
	}
	h.mu.RLock()
	s := h.sessions[id]
	h.mu.RUnlock()
	if s == nil || s.stream == nil {
		http.NotFound(w, r)
		return
	}
	t := s.stream
	if filepath.Base(name) != name || name == "." || name == ".." {
		http.Error(w, "", 400)
		return
	}
	ext := filepath.Ext(name)
	if ext != ".ts" && ext != ".m3u8" {
		http.Error(w, "", 400)
		return
	}
	path := filepath.Join(t.root, name)
	rel, err := filepath.Rel(t.root, path)
	if err != nil || strings.HasPrefix(rel, "..") || filepath.IsAbs(rel) {
		http.Error(w, "", 400)
		return
	}
	if _, err := os.Stat(path); err != nil {
		if ext == ".ts" {
			if seg, ok := segmentNumber(name); ok {
				if at := t.checkSegment(seg); at >= 0 {
					h.emit("*", "stream_reset", map[string]any{"startTime": at, "streamId": id})
				}
			}
		}
		http.NotFound(w, r)
		return
	}
	contentType := "video/mp2t"
	if ext == ".m3u8" {
		contentType = "application/vnd.apple.mpegurl"
	}
	f, err := os.Open(path)
	if err != nil {
		http.NotFound(w, r)
		return
	}
	defer f.Close()
	st, err := f.Stat()
	if err != nil {
		http.NotFound(w, r)
		return
	}
	w.Header().Set("Content-Type", contentType)
	w.Header().Set("Content-Length", strconv.FormatInt(st.Size(), 10))
	w.Header().Set("Cache-Control", "no-cache")
	http.ServeContent(w, r, name, st.ModTime(), f)
}

func (h *Handler) startTranscode(s *session, item *itemData, start float64) (*transcode, error) {
	root := filepath.Join(h.cfg.MetadataPath, "streams", s.ID)
	if err := os.MkdirAll(root, 0700); err != nil {
		return nil, fmt.Errorf("create HLS stream directory: %w", err)
	}
	concat := filepath.Join(root, "files.txt")
	var b strings.Builder
	for _, f := range item.Files {
		p, _ := f.Metadata["path"].(string)
		if p == "" {
			return nil, errors.New("audio track path is empty")
		}
		if _, err := os.Stat(p); err != nil {
			return nil, fmt.Errorf("audio track is unavailable: %w", err)
		}
		b.WriteString("file '")
		b.WriteString(strings.ReplaceAll(p, "'", "'\\''"))
		b.WriteString("'\n")
	}
	if err := os.WriteFile(concat, []byte(b.String()), 0600); err != nil {
		return nil, fmt.Errorf("write FFmpeg concat file: %w", err)
	}
	t := &transcode{root: root, audioCodec: transcodeAudioCodec(item.Files), startTime: start, segmentLength: 6, segmentStart: int(math.Floor(math.Max(start-30, 0) / 6))}
	if err := t.generatePlaylist(s.Duration); err != nil {
		return nil, err
	}
	if err := t.start(concat, s.Duration); err != nil {
		return nil, err
	}
	return t, nil
}

// transcodeAudioCodec mirrors Stream.js: AAC is copied by default and only
// the source's explicit MIME/codec set is re-encoded as AAC.
func transcodeAudioCodec(files []audioFile) string {
	if len(files) == 0 {
		return "copy"
	}
	file := files[0]
	codec := strings.ToLower(fmt.Sprint(file.Codec))
	switch file.MimeType {
	case "audio/flac", "audio/ogg", "audio/x-ms-wma", "audio/x-aiff", "audio/webm", "audio/amr-wb", "audio/x-caf":
		return "aac"
	}
	switch codec {
	case "alac", "ac3", "eac3", "opus":
		return "aac"
	default:
		return "copy"
	}
}

func (t *transcode) generatePlaylist(duration float64) error {
	num := int(math.Floor(duration / 6))
	last := duration - float64(num*6)
	var b strings.Builder
	b.WriteString("#EXTM3U\n#EXT-X-VERSION:3\n#EXT-X-ALLOW-CACHE:NO\n#EXT-X-TARGETDURATION:6\n#EXT-X-MEDIA-SEQUENCE:0\n#EXT-X-PLAYLIST-TYPE:VOD\n")
	for i := 0; i < num; i++ {
		fmt.Fprintf(&b, "#EXTINF:6,\noutput-%d.ts\n", i)
	}
	if last > 0 {
		fmt.Fprintf(&b, "#EXTINF:%v,\noutput-%d.ts\n", last, num)
	}
	b.WriteString("#EXT-X-ENDLIST")
	return os.WriteFile(filepath.Join(t.root, "output.m3u8"), []byte(b.String()), 0600)
}

func (t *transcode) start(concat string, duration float64) error {
	ctx, cancel := context.WithCancel(context.Background())
	args := []string{"-hide_banner", "-loglevel", "error"}
	adjusted := math.Max(t.startTime-30, 0)
	if adjusted > 0 {
		args = append(args, "-ss", strconv.FormatFloat(math.Round(adjusted*10)/10, 'f', 1, 64)+"s")
	}
	args = append(args, "-f", "concat", "-safe", "0", "-i", concat, "-map", "0:a", "-c:a", t.audioCodec, "-f", "hls", "-copyts", "-avoid_negative_ts", "make_non_negative", "-max_delay", "5000000", "-max_muxing_queue_size", "2048", "-hls_time", "6", "-hls_segment_type", "mpegts", "-start_number", strconv.Itoa(t.segmentStart), "-hls_playlist_type", "vod", "-hls_list_size", "0", "-hls_allow_cache", "0", "-hls_segment_filename", filepath.Join(t.root, "output-%d.ts"), filepath.Join(t.root, "final-output.m3u8"))
	cmd := exec.CommandContext(ctx, ffmpegExecutable(), args...)
	cmd.Stdout = io.Discard
	cmd.Stderr = io.Discard
	done := make(chan struct{})
	t.mu.Lock()
	t.ctx = ctx
	t.cancel = cancel
	t.cmd = cmd
	t.done = done
	t.complete = false
	t.resetting = false
	t.mu.Unlock()
	if err := cmd.Start(); err != nil {
		cancel()
		return fmt.Errorf("start FFmpeg: %w", err)
	}
	go func() {
		defer close(done)
		err := cmd.Wait()
		t.mu.Lock()
		t.complete = true
		t.cmd = nil
		t.mu.Unlock()
		if err != nil && !errors.Is(err, context.Canceled) {
			return
		}
		if err == nil {
			_ = t.promoteFinalPlaylist()
		}
	}()
	_ = duration
	return nil
}

func (t *transcode) promoteFinalPlaylist() error {
	src := filepath.Join(t.root, "final-output.m3u8")
	dst := filepath.Join(t.root, "output.m3u8")
	b, err := os.ReadFile(src)
	if err != nil {
		return err
	}
	// Keep the upstream public playlist URI stable while letting FFmpeg finalize
	// segment ordering and durations.
	return os.WriteFile(dst, b, 0600)
}

// checkSegment returns a nonnegative reset seek time or -1 when no reset is due.
func (t *transcode) checkSegment(seg int) float64 {
	t.mu.Lock()
	defer t.mu.Unlock()
	if t.resetting || t.complete {
		return -1
	}
	start := seg * 6
	reset := t.segmentStart > seg
	if !reset {
		return -1
	}
	seek := math.Max(float64(start-30), 0)
	t.resetting = true
	oldCancel := t.cancel
	root := t.root
	concat := filepath.Join(root, "files.txt")
	if oldCancel != nil {
		oldCancel()
	}
	go func() {
		time.Sleep(100 * time.Millisecond)
		entries, _ := os.ReadDir(root)
		for _, e := range entries {
			if e.Name() != "files.txt" {
				_ = os.Remove(filepath.Join(root, e.Name()))
			}
		}
		t.startTime = seek
		t.segmentStart = int(math.Floor(seek / 6))
		_ = t.generatePlaylist(float64(math.Max(float64(seg*6+6), 6)))
		_ = t.start(concat, 0)
	}()
	return float64(start)
}

func (t *transcode) close() error {
	t.mu.Lock()
	cancel := t.cancel
	done := t.done
	t.mu.Unlock()
	if cancel != nil {
		cancel()
	}
	if done != nil {
		select {
		case <-done:
		case <-time.After(3 * time.Second):
		}
	}
	return os.RemoveAll(t.root)
}

func makeDirectTracks(s *session, item *itemData) []map[string]any {
	out := make([]map[string]any, 0, len(item.Files))
	offset := 0.0
	for _, f := range item.Files {
		track := audioJSON(f)
		track["startOffset"] = offset
		track["title"] = f.Metadata["filename"]
		track["contentUrl"] = "/api/items/" + item.ID + "/file/" + f.Ino
		out = append(out, track)
		offset += f.Duration
	}
	if s.EpisodeID != "" {
		for _, ep := range item.Episodes {
			if ep.ID == s.EpisodeID {
				track := audioJSON(ep.Audio)
				track["index"] = 1
				track["startOffset"] = 0
				track["title"] = ep.Audio.Metadata["filename"]
				track["contentUrl"] = "/api/items/" + item.ID + "/file/" + ep.Audio.Ino
				return []map[string]any{track}
			}
		}
	}
	return out
}

func (s *session) publicJSON() map[string]any {
	return map[string]any{"id": s.ID, "userId": s.UserID, "libraryId": s.LibraryID, "libraryItemId": s.LibraryItemID, "bookId": nilIfEmpty(s.BookID), "episodeId": nilIfEmpty(s.EpisodeID), "mediaType": s.MediaType, "displayTitle": s.DisplayTitle, "displayAuthor": s.DisplayAuthor, "playMethod": s.PlayMethod, "mediaPlayer": s.MediaPlayer, "currentTime": s.CurrentTime, "startedAt": s.StartedAt, "updatedAt": s.UpdatedAt}
}
func (s *session) clientJSON() map[string]any {
	return map[string]any{"id": s.ID, "userId": s.UserID, "libraryId": s.LibraryID, "libraryItemId": s.LibraryItemID, "bookId": nilIfEmpty(s.BookID), "episodeId": nilIfEmpty(s.EpisodeID), "mediaType": s.MediaType, "mediaMetadata": s.MediaMetadata, "chapters": s.Chapters, "displayTitle": s.DisplayTitle, "displayAuthor": s.DisplayAuthor, "coverPath": s.CoverPath, "duration": s.Duration, "playMethod": s.PlayMethod, "mediaPlayer": s.MediaPlayer, "deviceInfo": s.Device, "serverVersion": "2.37.2", "date": s.Date, "dayOfWeek": s.DayOfWeek, "timeListening": s.TimeListening, "startTime": s.StartTime, "currentTime": s.CurrentTime, "startedAt": s.StartedAt, "updatedAt": s.UpdatedAt, "audioTracks": s.AudioTracks, "libraryItem": s.Item}
}

func parseAudioFile(raw map[string]any) (audioFile, error) {
	b, _ := json.Marshal(raw)
	var f audioFile
	if err := json.Unmarshal(b, &f); err != nil {
		return f, err
	}
	f.Fields = raw
	if f.Metadata == nil {
		return f, errors.New("audio file has no metadata")
	}
	ext, _ := f.Metadata["ext"].(string)
	format := strings.TrimPrefix(strings.ToLower(ext), ".")
	if format == "" {
		format, _ = f.Metadata["format"].(string)
	}
	f.MimeType = mimeForFormat(format)
	if f.Duration < 0 {
		f.Duration = 0
	}
	return f, nil
}

func canAccess(user store.User, item *itemData) bool {
	var p map[string]any
	if json.Unmarshal(user.Permissions, &p) != nil {
		return false
	}
	all, _ := p["accessAllLibraries"].(bool)
	if !all && !contains(stringSlice(p["librariesAccessible"]), item.LibraryID) {
		return false
	}
	if item.Explicit {
		allowed, _ := p["accessExplicitContent"].(bool)
		if !allowed {
			return false
		}
	}
	allTags, _ := p["accessAllTags"].(bool)
	if allTags {
		return true
	}
	selected := stringSlice(p["itemTagsSelected"])
	selectedNot, _ := p["selectedTagsNotAccessible"].(bool)
	if len(item.Tags) == 0 {
		return selectedNot
	}
	if selectedNot {
		for _, t := range item.Tags {
			if contains(selected, t) {
				return false
			}
		}
		return true
	}
	for _, t := range item.Tags {
		if contains(selected, t) {
			return true
		}
	}
	return false
}
func isAdmin(u store.User) bool { return u.Type == "root" || u.Type == "admin" }
func cleanDevice(in map[string]any) map[string]any {
	out := map[string]any{}
	allowed := []string{"id", "userId", "deviceId", "ipAddress", "browserName", "browserVersion", "osName", "osVersion", "deviceType", "clientVersion", "manufacturer", "model", "sdkVersion", "clientName", "deviceName"}
	for _, k := range allowed {
		if v, ok := in[k]; ok && v != nil {
			out[k] = v
		}
	}
	return out
}

func (h *Handler) upsertDevice(ctx context.Context, user store.User, r *http.Request, client map[string]any) (string, map[string]any, error) {
	get := func(key string) string { v, _ := client[key].(string); return v }
	ua := r.UserAgent()
	browser, browserVersion, osName, osVersion := parseUA(ua)
	clientVersion := get("clientVersion")
	if clientVersion == "" {
		clientVersion = "2.37.2"
	}
	clientName := get("clientName")
	manufacturer, model, sdk := get("manufacturer"), get("model"), get("sdkVersion")
	if sdk != "" {
		if clientName == "" {
			clientName = "Abs Android"
		}
	} else if model != "" {
		if clientName == "" {
			clientName = "Abs iOS"
		}
	} else if browser != "" && osName != "" {
		if clientName == "" {
			clientName = "Abs Web"
		}
	} else if clientName == "" {
		clientName = "Unknown"
	}
	deviceName := ""
	switch {
	case sdk != "":
		deviceName = strings.TrimSpace(manufacturer + " " + model)
	case model != "":
		deviceName = strings.TrimSpace(manufacturer + " " + model)
	case browser != "":
		deviceName = osName + " " + or(osVersion, "N/A") + " " + browser
	}
	ip := r.RemoteAddr
	if host, _, err := net.SplitHostPort(ip); err == nil {
		ip = host
	}
	dbID := newUUID()
	deviceKey := get("deviceId")
	if deviceKey == "" {
		deviceKey = dbID
	}
	var existing string
	err := h.db.QueryRowContext(ctx, `SELECT id FROM devices WHERE deviceId=? AND userId=? LIMIT 1`, deviceKey, user.ID).Scan(&existing)
	if err == nil {
		dbID = existing
	} else if !errors.Is(err, sql.ErrNoRows) {
		return "", nil, err
	}
	extra := map[string]any{}
	for key, value := range map[string]string{"manufacturer": manufacturer, "model": model, "osName": osName, "osVersion": osVersion, "browserName": browser} {
		if value != "" {
			extra[key] = value
		}
	}
	extraRaw, _ := json.Marshal(extra)
	version := browserVersion
	if sdk != "" {
		version = sdk
	}
	now := dbtime.Now()
	if existing == "" {
		_, err = h.db.ExecContext(ctx, `INSERT INTO devices (id,deviceId,clientName,clientVersion,ipAddress,deviceName,deviceVersion,extraData,userId,createdAt,updatedAt) VALUES (?,?,?,?,?,?,?,?,?,?,?)`, dbID, deviceKey, clientName, clientVersion, ip, nilIfEmpty(deviceName), nilIfEmpty(version), string(extraRaw), user.ID, now, now)
	} else {
		_, err = h.db.ExecContext(ctx, `UPDATE devices SET clientName=?,clientVersion=?,ipAddress=?,deviceName=?,deviceVersion=?,extraData=?,updatedAt=? WHERE id=?`, clientName, clientVersion, ip, nilIfEmpty(deviceName), nilIfEmpty(version), string(extraRaw), now, dbID)
	}
	if err != nil {
		return "", nil, err
	}
	info := map[string]any{"id": dbID, "userId": user.ID, "deviceId": deviceKey, "ipAddress": ip, "clientVersion": clientVersion, "clientName": clientName, "deviceName": nilIfEmpty(deviceName), "browserName": nilIfEmpty(browser), "browserVersion": nilIfEmpty(browserVersion), "osName": nilIfEmpty(osName), "osVersion": nilIfEmpty(osVersion), "deviceType": nil, "manufacturer": nilIfEmpty(manufacturer), "model": nilIfEmpty(model), "sdkVersion": nilIfEmpty(sdk)}
	for k, v := range info {
		if v == nil {
			delete(info, k)
		}
	}
	// The observed v2.37.2 playback-session device DTO omits deviceName,
	// manufacturer, model and sdkVersion entirely when no value exists
	// (PlaybackSession.js:86-104 / DeviceInfo.js:43-67 fixture behavior).
	return dbID, info, nil
}

func parseUA(ua string) (browser, browserVersion, osName, osVersion string) {
	if strings.Contains(ua, "Firefox/") {
		browser = "Firefox"
		browserVersion = after(ua, "Firefox/")
	} else if strings.Contains(ua, "Edg/") {
		browser = "Edge"
		browserVersion = after(ua, "Edg/")
	} else if strings.Contains(ua, "Chrome/") {
		browser = "Chrome"
		browserVersion = after(ua, "Chrome/")
	} else if strings.Contains(ua, "Safari/") {
		browser = "Safari"
		browserVersion = after(ua, "Version/")
	}
	if strings.Contains(ua, "Windows NT 10") {
		osName = "Windows"
		osVersion = "10"
	} else if strings.Contains(ua, "Mac OS X") {
		osName = "Mac OS"
		osVersion = after(ua, "Mac OS X ")
		osVersion = strings.ReplaceAll(osVersion, "_", ".")
	} else if strings.Contains(ua, "Android") {
		osName = "Android"
		osVersion = after(ua, "Android ")
	} else if strings.Contains(ua, "iPhone OS ") {
		osName = "iOS"
		osVersion = after(ua, "iPhone OS ")
		osVersion = strings.ReplaceAll(osVersion, "_", ".")
	} else if strings.Contains(ua, "Linux") {
		osName = "Linux"
	}
	return
}
func after(value, marker string) string {
	if i := strings.Index(value, marker); i >= 0 {
		v := value[i+len(marker):]
		if j := strings.IndexAny(v, " ;)"); j >= 0 {
			v = v[:j]
		}
		return v
	}
	return ""
}
func or(value, fallback string) string {
	if value != "" {
		return value
	}
	return fallback
}

func serveMedia(w http.ResponseWriter, r *http.Request, path, mime string) {
	if mime == "" {
		mime = mimeForFormat(strings.TrimPrefix(strings.ToLower(filepath.Ext(path)), "."))
	}
	f, err := os.Open(path)
	if err != nil {
		http.NotFound(w, r)
		return
	}
	defer f.Close()
	st, err := f.Stat()
	if err != nil || !st.Mode().IsRegular() {
		http.NotFound(w, r)
		return
	}
	w.Header().Set("Accept-Ranges", "bytes")
	if mime != "" {
		w.Header().Set("Content-Type", mime)
	}
	http.ServeContent(w, r, filepath.Base(path), st.ModTime(), f)
}

func mimeForFormat(ext string) string {
	switch strings.ToLower(ext) {
	case "mp3", "mpeg", "mpg":
		return "audio/mpeg"
	case "m4b", "m4a", "mp4":
		return "audio/mp4"
	case "ogg", "oga", "opus":
		return "audio/ogg"
	case "aac":
		return "audio/aac"
	case "flac":
		return "audio/flac"
	case "wma":
		return "audio/x-ms-wma"
	case "aiff", "aif":
		return "audio/x-aiff"
	case "webm", "webma":
		return "audio/webm"
	case "mka":
		return "audio/x-matroska"
	case "awb":
		return "audio/amr-wb"
	case "caf":
		return "audio/x-caf"
	}
	return "audio/mpeg"
}

func audioJSON(f audioFile) map[string]any {
	out := map[string]any{}
	for k, v := range f.Fields {
		out[k] = v
	}
	out["index"] = f.Index
	out["ino"] = f.Ino
	out["metadata"] = f.Metadata
	out["duration"] = f.Duration
	out["mimeType"] = f.MimeType
	if f.Codec != nil {
		out["codec"] = f.Codec
	}
	return out
}

func joinAuthors(authors []map[string]any, lastFirst bool) string {
	parts := make([]string, 0, len(authors))
	for _, a := range authors {
		name, _ := a["name"].(string)
		if lastFirst {
			name = toLastFirst(name)
		}
		parts = append(parts, name)
	}
	return strings.Join(parts, ", ")
}
func toLastFirst(name string) string {
	if strings.Contains(name, ",") {
		return name
	}
	fields := strings.Fields(name)
	if len(fields) < 2 {
		return name
	}
	return strings.Join(fields[1:], " ") + ", " + fields[0]
}
func joinSeries(series []map[string]any) string {
	parts := make([]string, 0, len(series))
	for _, s := range series {
		name, _ := s["name"].(string)
		seq, _ := s["sequence"].(string)
		if seq != "" {
			name += " #" + seq
		}
		parts = append(parts, name)
	}
	return strings.Join(parts, ", ")
}
func stripTags(s string) string {
	var b strings.Builder
	in := false
	for _, r := range s {
		if r == '<' {
			in = true
			continue
		}
		if r == '>' {
			in = false
			continue
		}
		if !in {
			b.WriteRune(r)
		}
	}
	return b.String()
}
func nullable(v sql.NullString) any {
	if v.Valid {
		return v.String
	}
	return nil
}
func millis(v any) any {
	if v == nil {
		return nil
	}
	switch x := v.(type) {
	case int64:
		return x
	case float64:
		return int64(x)
	case []byte:
		return millis(string(x))
	case string:
		if n, e := strconv.ParseInt(x, 10, 64); e == nil {
			return n
		}
		for _, layout := range []string{time.RFC3339Nano, "2006-01-02 15:04:05.999999999 -07:00", "2006-01-02 15:04:05.999999999 -0700 MST", "2006-01-02 15:04:05.999999999-07:00"} {
			if t, e := time.Parse(layout, x); e == nil {
				return t.UnixMilli()
			}
		}
	}
	return nil
}
func number(v any, fallback float64) float64 {
	if n, ok := asFloat(v); ok {
		return n
	}
	return fallback
}
func asFloat(v any) (float64, bool) {
	switch n := v.(type) {
	case float64:
		return n, true
	case int:
		return float64(n), true
	case int64:
		return float64(n), true
	case json.Number:
		f, e := n.Float64()
		return f, e == nil
	}
	return 0, false
}
func numberOr(v any, fallback int) any {
	if v == nil {
		return fallback
	}
	return v
}
func stringSlice(v any) []string {
	out := []string{}
	switch x := v.(type) {
	case []string:
		return x
	case []any:
		for _, a := range x {
			if s, ok := a.(string); ok {
				out = append(out, s)
			}
		}
	}
	return out
}
func allSupported(files []audioFile, supported []string) bool {
	if len(files) == 0 {
		return false
	}
	for _, f := range files {
		if !contains(supported, f.MimeType) {
			return false
		}
	}
	return true
}
func contains(values []string, want string) bool {
	for _, v := range values {
		if v == want {
			return true
		}
	}
	return false
}
func newUUID() string {
	var b [16]byte
	if _, err := rand.Read(b[:]); err != nil {
		now := time.Now().UnixNano()
		return fmt.Sprintf("%032x", now)
	}
	b[6] = (b[6] & 0x0f) | 0x40
	b[8] = (b[8] & 0x3f) | 0x80
	hexed := hex.EncodeToString(b[:])
	return hexed[:8] + "-" + hexed[8:12] + "-" + hexed[12:16] + "-" + hexed[16:20] + "-" + hexed[20:]
}
func nowMS() int64 { return time.Now().UnixMilli() }
func nilIfEmpty(v string) any {
	if v == "" {
		return nil
	}
	return v
}
func segmentNumber(name string) (int, bool) {
	base := strings.TrimSuffix(name, filepath.Ext(name))
	parts := strings.Split(base, "-")
	if len(parts) != 2 {
		return 0, false
	}
	n, e := strconv.Atoi(parts[1])
	return n, e == nil && n >= 0
}
func methodNotAllowed(w http.ResponseWriter) {
	http.Error(w, "Method Not Allowed", http.StatusMethodNotAllowed)
}
func writeJSON(w http.ResponseWriter, status int, value any) {
	b, err := json.Marshal(value)
	if err != nil {
		http.Error(w, "Internal Server Error", 500)
		return
	}
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.Header().Set("Content-Length", strconv.Itoa(len(b)))
	w.WriteHeader(status)
	_, _ = w.Write(b)
}
func sendOK(w http.ResponseWriter) {
	w.Header().Set("Content-Type", "text/plain; charset=utf-8")
	w.Header().Set("Content-Length", "2")
	w.WriteHeader(http.StatusOK)
	_, _ = w.Write([]byte("OK"))
}
func ffmpegExecutable() string {
	if p := strings.TrimSpace(os.Getenv("FFMPEG_PATH")); p != "" {
		return p
	}
	return "ffmpeg"
}
func (h *Handler) serverError(w http.ResponseWriter, err error) {
	_ = err
	http.Error(w, "Internal Server Error", http.StatusInternalServerError)
}
