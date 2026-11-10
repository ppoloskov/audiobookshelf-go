package podcasts

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"time"

	"audiobookshelf-compat/internal/cronspec"
	"audiobookshelf-compat/internal/dbtime"
	"audiobookshelf-compat/internal/events"
	"audiobookshelf-compat/internal/httputil"
	"audiobookshelf-compat/internal/ids"
	"audiobookshelf-compat/internal/items"
	"audiobookshelf-compat/internal/logging"
	"audiobookshelf-compat/internal/serversettings"
	"audiobookshelf-compat/internal/store"
	"audiobookshelf-compat/internal/textclean"
	"audiobookshelf-compat/internal/textsort"
)

func (h *Handler) createPodcast(w http.ResponseWriter, r *http.Request) error {
	var body map[string]any
	if json.NewDecoder(io.LimitReader(r.Body, 4<<20)).Decode(&body) != nil {
		return httputil.Status(400, "Invalid request body")
	}
	item, status, message, err := h.createPodcastFromBody(r.Context(), body)
	if status == http.StatusInternalServerError && err != nil {
		return httputil.Internal(message, err)
	}
	if err != nil {
		return httputil.Status(status, message)
	}
	if item.Podcast.AutoDownloadEpisodes {
		h.addSchedule(item.ID)
	}
	return httputil.JSON(w, r, h.expanded(r.Context(), item))
}

// createPodcastFromBody performs podcast creation without coupling the operation
// to an HTTP request or response. The returned status and message preserve the
// create endpoint's existing validation and failure distinctions.
func (h *Handler) createPodcastFromBody(ctx context.Context, body map[string]any) (items.Item, int, string, error) {
	media, _ := body["media"].(map[string]any)
	meta, _ := media["metadata"].(map[string]any)
	if meta == nil {
		return items.Item{}, 400, `Invalid request body. "media" and "media.metadata" are required`, errors.New("missing podcast metadata")
	}
	libID, _ := body["libraryId"].(string)
	folderID, _ := body["folderId"].(string)
	rawPath, _ := body["path"].(string)
	if strings.TrimSpace(rawPath) == "" {
		return items.Item{}, 400, `Invalid request body. "path" must be a non-empty string`, errors.New("empty podcast path")
	}
	var folderPath, folderLibrary string
	var libraryType string
	err := h.db.QueryRowContext(ctx, `SELECT f.path,f.libraryId,l.mediaType FROM libraryFolders f JOIN libraries l ON l.id=f.libraryId WHERE f.id=?`, folderID).Scan(&folderPath, &folderLibrary, &libraryType)
	if errors.Is(err, sql.ErrNoRows) {
		return items.Item{}, 404, "Folder not found", err
	}
	if err != nil {
		return items.Item{}, 500, "database read failed", err
	}
	if folderLibrary != libID || libraryType != "podcast" {
		return items.Item{}, 404, "Folder not found", sql.ErrNoRows
	}
	root, err := filepath.Abs(folderPath)
	if err != nil {
		return items.Item{}, 400, "Invalid podcast path", err
	}
	path, err := filepath.Abs(rawPath)
	if err != nil {
		return items.Item{}, 400, "Invalid podcast path", err
	}
	rel, err := filepath.Rel(root, path)
	if err != nil || rel == ".." || strings.HasPrefix(rel, ".."+string(os.PathSeparator)) {
		return items.Item{}, 400, "Podcast path must be inside the selected library folder", errors.New("podcast path outside library folder")
	}
	var existing int
	if err = h.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM libraryItems WHERE path=?`, filepath.ToSlash(path)).Scan(&existing); err != nil {
		return items.Item{}, 500, "database read failed", err
	}
	if existing > 0 {
		return items.Item{}, 400, "Podcast already exists", errPodcastExists
	}
	schedule, _ := body["autoDownloadSchedule"].(string)
	if schedule != "" && !cronspec.Valid(schedule) {
		return items.Item{}, 400, "Invalid auto download schedule cron expression", errors.New("invalid auto download schedule")
	}
	settings := serversettings.Load(ctx, h.db)
	if schedule == "" {
		schedule = settings.String("podcastEpisodeSchedule")
		if schedule == "" {
			schedule = "0 * * * *"
		}
	}
	createdPath := false
	if _, e := os.Stat(path); os.IsNotExist(e) {
		if e = os.MkdirAll(path, 0755); e != nil {
			return items.Item{}, 400, "Invalid podcast path", e
		}
		createdPath = true
	} else if e != nil {
		return items.Item{}, 400, "Invalid podcast path", e
	}
	defer func() {
		if createdPath {
			_ = os.Remove(path)
		}
	}()
	st, err := os.Stat(path)
	if err != nil || !st.IsDir() {
		return items.Item{}, 400, "Invalid podcast path", errors.New("podcast path is not a directory")
	}
	now := time.Now().UTC()
	timestamp := dbtime.Format(now)
	id, err := ids.New()
	var mediaID string
	if err == nil {
		mediaID, err = ids.New()
	}
	if err != nil {
		return items.Item{}, 500, "Failed to create podcast", err
	}
	ino := strconv.FormatUint(uint64(st.ModTime().UnixNano()), 10)
	if raw := st.Sys(); raw != nil {
		if info, ok := raw.(*syscall.Stat_t); ok {
			ino = strconv.FormatUint(info.Ino, 10)
		}
	}
	title := anyString(meta["title"])
	sortTitle := textsort.TitleIgnorePrefix(title, settings.SortingPrefixes())
	description := metadataValue(meta, "description")
	if desc, ok := description.(string); ok {
		description = textclean.Sanitize(desc)
	}
	genres := stringArray(meta["genres"])
	tags := stringArray(body["tags"])
	genreJSON, _ := json.Marshal(genres)
	tagJSON, _ := json.Marshal(tags)
	explicit := meta["explicit"] == true
	auto := body["autoDownloadEpisodes"] == true
	tx, err := h.db.BeginTx(ctx, nil)
	if err != nil {
		return items.Item{}, 500, "Failed to create podcast", err
	}
	defer tx.Rollback()
	_, err = tx.ExecContext(ctx, `INSERT INTO podcasts (id,title,titleIgnorePrefix,author,releaseDate,feedURL,imageURL,description,itunesPageURL,itunesId,itunesArtistId,language,podcastType,explicit,autoDownloadEpisodes,autoDownloadSchedule,lastEpisodeCheck,maxEpisodesToKeep,maxNewEpisodesToDownload,coverPath,tags,genres,numEpisodes,createdAt,updatedAt) VALUES (?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?, ?,?)`, mediaID, store.NullIfEmpty(title), store.NullIfEmpty(sortTitle), metadataValue(meta, "author"), metadataValue(meta, "releaseDate"), metadataValue(meta, "feedUrl"), metadataValue(meta, "imageUrl"), description, metadataValue(meta, "itunesPageUrl"), metadataValue(meta, "itunesId"), metadataValue(meta, "itunesArtistId"), metadataValue(meta, "language"), metadataValue(meta, "type"), explicit, auto, schedule, timestamp, 0, 3, nil, string(tagJSON), string(genreJSON), 0, timestamp, timestamp)
	if err != nil {
		return items.Item{}, 500, "Failed to create podcast", err
	}
	libRel := filepath.ToSlash(rel)
	if libRel == "." {
		libRel = ""
	}
	_, err = tx.ExecContext(ctx, `INSERT INTO libraryItems (id,ino,path,relPath,mediaId,mediaType,isFile,isMissing,isInvalid,mtime,ctime,birthtime,size,libraryFiles,extraData,title,titleIgnorePrefix,libraryId,libraryFolderId,createdAt,updatedAt) VALUES (?,?,?,?,?,'podcast',0,0,0,?,?,?,0,'[]','{}',?,?,?,?,?,?)`, id, ino, filepath.ToSlash(path), libRel, mediaID, timestamp, timestamp, timestamp, store.NullIfEmpty(title), store.NullIfEmpty(sortTitle), libID, folderID, timestamp, timestamp)
	if err != nil {
		return items.Item{}, 500, "Failed to create podcast", err
	}
	if err = tx.Commit(); err != nil {
		return items.Item{}, 500, "Failed to create podcast", err
	}
	createdPath = false // committed items keep their directory even if loading fails
	item, err := h.loadItem(ctx, id)
	if err != nil {
		return items.Item{}, 500, "database read failed", err
	}
	h.emit.ToAdmins("item_added", h.expanded(ctx, item))
	return item, 200, "", nil
}

// metadataValue retains creation's string and numeric coercion and NULL defaults.
func metadataValue(meta map[string]any, key string) any {
	if value, ok := stringLike(meta[key]); ok && value != "" {
		return value
	}
	return nil
}

func (h *Handler) createFromOPML(w http.ResponseWriter, r *http.Request) error {
	var body struct {
		Feeds                []string `json:"feeds"`
		LibraryID            string   `json:"libraryId"`
		FolderID             string   `json:"folderId"`
		AutoDownloadEpisodes bool     `json:"autoDownloadEpisodes"`
	}
	if json.NewDecoder(io.LimitReader(r.Body, 8<<20)).Decode(&body) != nil || len(body.Feeds) == 0 {
		return httputil.Status(400, `Invalid request body. "feeds" must be an array of RSS feed URLs`)
	}
	for _, raw := range body.Feeds {
		if validFeedURL(raw) == "" {
			return httputil.Status(400, `Invalid request body. "feeds" must be an array of RSS feed URLs`)
		}
	}
	if body.LibraryID == "" || body.FolderID == "" {
		return httputil.Status(400, `Invalid request body. "libraryId" and "folderId" are required`)
	}
	var folderPath, libraryID string
	if err := h.db.QueryRowContext(r.Context(), `SELECT path,libraryId FROM libraryFolders WHERE id=?`, body.FolderID).Scan(&folderPath, &libraryID); errors.Is(err, sql.ErrNoRows) {
		return httputil.Status(404, "Folder not found")
	} else if err != nil {
		return httputil.Internal("database read failed", err)
	}
	if libraryID != body.LibraryID {
		return httputil.Status(404, "Folder not found")
	}
	id, err := ids.New()
	if err != nil {
		return &httputil.Error{Status: 500, Cause: err}
	}
	task := events.NewTask(id, "opml-import", struct{}{}).
		Titled("OPML import", "MessageTaskOpmlImport", nil).
		Described(fmt.Sprintf("Creating podcasts from %d RSS feeds", len(body.Feeds)), "MessageTaskOpmlImportDescription", len(body.Feeds))
	task.ShowSuccess = true
	h.emit.TaskStarted(task)
	go h.runOPMLImport(body.Feeds, body.LibraryID, body.FolderID, folderPath, body.AutoDownloadEpisodes, task)
	return httputil.OK(w, r)
}

func (h *Handler) runOPMLImport(feeds []string, libraryID, folderID, folderPath string, auto bool, task events.Task) {
	added := 0
	for _, feedURL := range feeds {
		feed, err := h.getFeed(context.Background(), feedURL)
		if err != nil || feed.Metadata.Title == "" {
			// PodcastManager.js:543-557: feed-fetch failures describe the feed URL.
			h.emitFailedOPMLFeed(feedURL, `Importing RSS feed "`+feedURL+`"`, "MessageTaskOpmlImportFeedDescription", "Failed to get podcast feed", "MessageTaskOpmlImportFeedFailed")
			continue
		}
		metadata := feed.Metadata
		title := metadata.Title
		path := filepath.Join(folderPath, sanitizePodcastFilename(title))
		payload := map[string]any{"libraryId": libraryID, "folderId": folderID, "path": filepath.ToSlash(path), "autoDownloadEpisodes": auto, "media": map[string]any{"autoDownloadEpisodes": auto, "metadata": map[string]any{"title": metadata.Title, "author": metadata.Author, "description": metadata.Description, "releaseDate": "", "genres": metadata.Categories, "feedUrl": metadata.FeedURL, "imageUrl": metadata.Image, "itunesPageUrl": "", "itunesId": "", "itunesArtistId": "", "language": ""}}}
		item, _, _, createErr := h.createPodcastFromBody(context.Background(), payload)
		if createErr == nil {
			added++
			if item.Podcast.AutoDownloadEpisodes {
				h.addSchedule(item.ID)
			}
			continue
		}
		errorText := "Failed to create podcast library item"
		errorKey := "MessageTaskOpmlImportFeedPodcastFailed"
		if errors.Is(createErr, errPodcastExists) {
			errorText = "Podcast already exists at path"
			errorKey = "MessageTaskOpmlImportFeedPodcastExists"
		}
		// PodcastManager.js:565-584/605-622/662-678: creation failures describe
		// the podcast title, not the feed URL.
		h.emitFailedOPMLFeed(title, `Creating podcast "`+title+`"`, "MessageTaskOpmlImportFeedPodcastDescription", errorText, errorKey)
	}
	h.emit.TaskFinished(task.Described(fmt.Sprintf("Added %d podcasts", added), "MessageTaskOpmlImportFinished", added).Finished())
}

func (h *Handler) emitFailedOPMLFeed(title, descriptionText, descriptionKey, errorText, errorKey string) {
	taskID, err := ids.New()
	if err != nil {
		logging.Errorf("[PodcastManager] could not create an id for the OPML feed failure task: %v", err)
		return
	}
	task := events.NewTask(taskID, "opml-import-feed", struct{}{}).
		Titled("OPML import feed", "MessageTaskOpmlImportFeed", nil).
		Described(descriptionText, descriptionKey, title).
		Failed(errorText, errorKey).Finished()
	h.emit.TaskStarted(task)
}

func sanitizePodcastFilename(title string) string {
	title = strings.TrimSpace(title)
	title = strings.NewReplacer("/", " ", "\\", " ", ":", " - ", "?", "", "*", "", "|", "", "<", "", ">", "", `"`, "").Replace(title)
	title = strings.Join(strings.Fields(title), " ")
	if title == "" || title == "." || title == ".." {
		return "Podcast"
	}
	return title
}
