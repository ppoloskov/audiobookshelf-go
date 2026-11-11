package podcast

import (
	"cmp"
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"

	"audiobookshelf-compat/internal/cronspec"
	"audiobookshelf-compat/internal/db"
	"audiobookshelf-compat/internal/events"
	"audiobookshelf-compat/internal/httpx"
	"audiobookshelf-compat/internal/ids"
	"audiobookshelf-compat/internal/item"
	"audiobookshelf-compat/internal/textclean"
	"audiobookshelf-compat/internal/textsort"
)

// createRequest is the body of POST /api/podcasts. Tags, autoDownloadEpisodes
// and autoDownloadSchedule are read at the top level, as before; the web
// client's media.autoDownloadEpisodes is ignored.
type createRequest struct {
	LibraryID            string     `json:"libraryId"`
	FolderID             string     `json:"folderId"`
	Path                 string     `json:"path"`
	Media                *mediaBody `json:"media"`
	Tags                 []string   `json:"tags"`
	AutoDownloadEpisodes bool       `json:"autoDownloadEpisodes"`
	AutoDownloadSchedule string     `json:"autoDownloadSchedule"`
}

type mediaBody struct {
	Metadata *podcastMetadata `json:"metadata"`
}

type podcastMetadata struct {
	Title          string   `json:"title"`
	Author         string   `json:"author"`
	Description    string   `json:"description"`
	ReleaseDate    string   `json:"releaseDate"`
	Genres         []string `json:"genres"`
	FeedURL        string   `json:"feedUrl"`
	ImageURL       string   `json:"imageUrl"`
	ItunesPageURL  string   `json:"itunesPageUrl"`
	ItunesID       idText   `json:"itunesId"`
	ItunesArtistID idText   `json:"itunesArtistId"`
	Language       string   `json:"language"`
	Explicit       bool     `json:"explicit"`
	Type           string   `json:"type"`
}

// idText is an iTunes id, which the web client sends as the number iTunes
// returned. Any other JSON type reads as no id.
type idText string

func (s *idText) UnmarshalJSON(data []byte) error {
	var v any
	if err := json.Unmarshal(data, &v); err != nil {
		return err
	}
	switch v := v.(type) {
	case string:
		*s = idText(v)
	case float64:
		*s = idText(strconv.FormatFloat(v, 'f', -1, 64))
	}
	return nil
}

var errPodcastExists = httpx.BadRequest("Podcast already exists")

func (h *Handler) createPodcast(w http.ResponseWriter, r *http.Request) error {
	req, err := httpx.DecodeLimit[createRequest](w, r, 4<<20)
	if err != nil {
		return err
	}
	if req.Media == nil || req.Media.Metadata == nil {
		return httpx.BadRequest(`Invalid request body. "media" and "media.metadata" are required`)
	}
	it, err := h.create(r.Context(), &req)
	if err != nil {
		return err
	}
	if it.Podcast.AutoDownloadEpisodes {
		h.Reschedule(r.Context(), it.ID)
	}
	return httpx.JSON(w, r, h.expanded(it))
}

// create adds the podcast and its library item for req.Media.Metadata, makes
// the folder when missing and announces item_added to the admins.
func (h *Handler) create(ctx context.Context, req *createRequest) (*item.Item, error) {
	meta := req.Media.Metadata
	if strings.TrimSpace(req.Path) == "" {
		return nil, httpx.BadRequest(`Invalid request body. "path" must be a non-empty string`)
	}
	var folder struct {
		Path      *string `db:"path"`
		LibraryID *string `db:"libraryId"`
		MediaType *string `db:"mediaType"`
	}
	switch err := h.db.Get(ctx, &folder, `SELECT f.path AS path, f.libraryId AS libraryId, l.mediaType AS mediaType FROM libraryFolders f JOIN libraries l ON l.id=f.libraryId WHERE f.id=?`, req.FolderID); {
	case errors.Is(err, sql.ErrNoRows):
		return nil, httpx.Error(http.StatusNotFound, "Folder not found")
	case err != nil:
		return nil, httpx.Internal("database read failed", err)
	}
	if db.Val(folder.LibraryID) != req.LibraryID || db.Val(folder.MediaType) != "podcast" {
		return nil, httpx.Error(http.StatusNotFound, "Folder not found")
	}
	root, rootErr := filepath.Abs(db.Val(folder.Path))
	path, pathErr := filepath.Abs(req.Path)
	rel, relErr := filepath.Rel(root, path)
	if errors.Join(rootErr, pathErr, relErr) != nil || rel == ".." || strings.HasPrefix(rel, ".."+string(os.PathSeparator)) {
		return nil, httpx.BadRequest("Podcast path must be inside the selected library folder")
	}
	var existing int
	if err := h.db.Get(ctx, &existing, `SELECT COUNT(*) FROM libraryItems WHERE path=?`, filepath.ToSlash(path)); err != nil {
		return nil, httpx.Internal("database read failed", err)
	}
	if existing > 0 {
		return nil, errPodcastExists
	}
	if req.AutoDownloadSchedule != "" && !cronspec.Valid(req.AutoDownloadSchedule) {
		return nil, httpx.BadRequest("Invalid auto download schedule cron expression")
	}
	server := h.settings.Server()
	schedule := cmp.Or(req.AutoDownloadSchedule, server.PodcastEpisodeSchedule, "0 * * * *")

	madeDir := false
	if _, err := os.Stat(path); os.IsNotExist(err) {
		if err := os.MkdirAll(path, 0o755); err != nil {
			return nil, httpx.BadRequest("Invalid podcast path")
		}
		madeDir = true
	}
	defer func() {
		if madeDir {
			_ = os.Remove(path) // the item was not created; best effort to undo the folder
		}
	}()
	st, err := os.Stat(path)
	if err != nil || !st.IsDir() {
		return nil, httpx.BadRequest("Invalid podcast path")
	}

	now := db.Now()
	itemID, mediaID := ids.Must(), ids.Must()
	title := ptrIfSet(meta.Title)
	titleSort := ptrIfSet(textsort.TitleIgnorePrefix(meta.Title, server.SortingPrefixes))
	var description *string
	if meta.Description != "" {
		description = db.Ptr(textclean.Sanitize(meta.Description))
	}
	podcast := db.Podcast{ID: mediaID, Title: title, TitleIgnorePrefix: titleSort, Author: ptrIfSet(meta.Author), ReleaseDate: ptrIfSet(meta.ReleaseDate),
		FeedURL: ptrIfSet(meta.FeedURL), ImageURL: ptrIfSet(meta.ImageURL), Description: description, ItunesPageURL: ptrIfSet(meta.ItunesPageURL),
		ItunesID: ptrIfSet(string(meta.ItunesID)), ItunesArtistID: ptrIfSet(string(meta.ItunesArtistID)), Language: ptrIfSet(meta.Language),
		PodcastType: ptrIfSet(meta.Type), Explicit: db.Bool(meta.Explicit), AutoDownloadEpisodes: db.Bool(req.AutoDownloadEpisodes),
		AutoDownloadSchedule: &schedule, LastEpisodeCheck: now, MaxEpisodesToKeep: db.Ptr[int64](0), MaxNewEpisodesToDownload: db.Ptr[int64](3),
		Tags: db.JSON[[]string]{V: nonNil(req.Tags)}, Genres: db.JSON[[]string]{V: nonNil(meta.Genres)}, NumEpisodes: db.Ptr[int64](0), CreatedAt: now, UpdatedAt: now}
	relPath := filepath.ToSlash(rel)
	if relPath == "." {
		relPath = ""
	}
	libraryItem := db.LibraryItem{ID: itemID, Ino: db.Ptr(inode(st)), Path: db.Ptr(filepath.ToSlash(path)), RelPath: &relPath, MediaID: &mediaID, MediaType: db.Ptr("podcast"),
		Mtime: now, Ctime: now, Birthtime: now, Size: db.Ptr[int64](0), LibraryFiles: db.JSON[[]db.LibraryFile]{V: []db.LibraryFile{}},
		ExtraData: db.JSON[*db.LibraryItemExtra]{V: &db.LibraryItemExtra{}}, Title: title, TitleIgnorePrefix: titleSort,
		CreatedAt: now, UpdatedAt: now, LibraryID: &req.LibraryID, LibraryFolderID: &req.FolderID}
	err = h.db.Tx(ctx, func(ctx context.Context) error {
		if err := h.db.Insert(ctx, "podcasts", &podcast); err != nil {
			return err
		}
		return h.db.Insert(ctx, "libraryItems", &libraryItem)
	})
	if err != nil {
		return nil, httpx.Internal("Failed to create podcast", err)
	}
	madeDir = false // a committed item keeps its folder
	it, err := h.load(ctx, itemID)
	if err != nil {
		return nil, httpx.Internal("database read failed", err)
	}
	h.emit.ToAdmins("item_added", h.expanded(it))
	return it, nil
}

// ptrIfSet is a pointer to s, nil (SQL NULL) when s is empty.
func ptrIfSet(s string) *string {
	if s == "" {
		return nil
	}
	return &s
}

// nonNil makes a nil list an empty one, so a JSON column writes [] not NULL.
func nonNil(list []string) []string {
	if list == nil {
		return []string{}
	}
	return list
}

// inode is the file's inode number as text; without one, its mtime in nanoseconds.
func inode(st os.FileInfo) string {
	if sys, ok := st.Sys().(*syscall.Stat_t); ok {
		return strconv.FormatUint(uint64(sys.Ino), 10)
	}
	return strconv.FormatInt(st.ModTime().UnixNano(), 10)
}

type opmlCreateRequest struct {
	Feeds                []string `json:"feeds"`
	LibraryID            string   `json:"libraryId"`
	FolderID             string   `json:"folderId"`
	AutoDownloadEpisodes bool     `json:"autoDownloadEpisodes"`
}

// createFromOPML starts the import in the background and answers at once; the
// outcome is reported through task events.
func (h *Handler) createFromOPML(w http.ResponseWriter, r *http.Request) error {
	req, err := httpx.DecodeLimit[opmlCreateRequest](w, r, 8<<20)
	if err != nil {
		return err
	}
	feedsError := httpx.BadRequest(`Invalid request body. "feeds" must be an array of RSS feed URLs`)
	if len(req.Feeds) == 0 {
		return feedsError
	}
	for _, link := range req.Feeds {
		if httpURL(link) == "" {
			return feedsError
		}
	}
	if req.LibraryID == "" || req.FolderID == "" {
		return httpx.BadRequest(`Invalid request body. "libraryId" and "folderId" are required`)
	}
	var folder db.LibraryFolder
	switch err := h.db.Get(r.Context(), &folder, `SELECT * FROM libraryFolders WHERE id=?`, req.FolderID); {
	case errors.Is(err, sql.ErrNoRows):
		return httpx.Error(http.StatusNotFound, "Folder not found")
	case err != nil:
		return httpx.Internal("database read failed", err)
	}
	if db.Val(folder.LibraryID) != req.LibraryID {
		return httpx.Error(http.StatusNotFound, "Folder not found")
	}
	task := events.NewTask(ids.Must(), "opml-import", struct{}{}).
		Titled("OPML import", "MessageTaskOpmlImport", nil).
		Described(fmt.Sprintf("Creating podcasts from %d RSS feeds", len(req.Feeds)), "MessageTaskOpmlImportDescription", len(req.Feeds))
	task.ShowSuccess = true
	h.emit.TaskStarted(task)
	h.spawn(func() { h.importOPML(req, db.Val(folder.Path), task) })
	return httpx.Status(w, http.StatusOK)
}

// importOPML creates one podcast per feed and reports each failure as its own
// finished task (PodcastManager.js:543-678).
func (h *Handler) importOPML(req opmlCreateRequest, folderPath string, task events.Task) {
	added := 0
	for _, link := range req.Feeds {
		parsed, err := h.fetchFeed(h.ctx, link)
		if err != nil || parsed.Metadata.Title == "" {
			h.opmlFailure(link, `Importing RSS feed "`+link+`"`, "MessageTaskOpmlImportFeedDescription", "Failed to get podcast feed", "MessageTaskOpmlImportFeedFailed")
			continue
		}
		m := parsed.Metadata
		create := createRequest{LibraryID: req.LibraryID, FolderID: req.FolderID, Path: filepath.ToSlash(filepath.Join(folderPath, podcastDirName(m.Title))),
			AutoDownloadEpisodes: req.AutoDownloadEpisodes,
			Media:                &mediaBody{Metadata: &podcastMetadata{Title: m.Title, Author: m.Author, Description: m.Description, Genres: m.Categories, FeedURL: m.FeedURL, ImageURL: m.Image}}}
		it, err := h.create(h.ctx, &create)
		if err == nil {
			added++
			if it.Podcast.AutoDownloadEpisodes {
				h.Reschedule(h.ctx, it.ID)
			}
			continue
		}
		text, key := "Failed to create podcast library item", "MessageTaskOpmlImportFeedPodcastFailed"
		if errors.Is(err, errPodcastExists) {
			text, key = "Podcast already exists at path", "MessageTaskOpmlImportFeedPodcastExists"
		}
		h.opmlFailure(m.Title, `Creating podcast "`+m.Title+`"`, "MessageTaskOpmlImportFeedPodcastDescription", text, key)
	}
	h.emit.TaskFinished(task.Described(fmt.Sprintf("Added %d podcasts", added), "MessageTaskOpmlImportFinished", added).Finished())
}

func (h *Handler) opmlFailure(subject, description, descriptionKey, errText, errKey string) {
	slog.Warn("podcast: OPML import", "subject", subject, "error", errText)
	h.emit.TaskStarted(events.NewTask(ids.Must(), "opml-import-feed", struct{}{}).
		Titled("OPML import feed", "MessageTaskOpmlImportFeed", nil).
		Described(description, descriptionKey, subject).
		Failed(errText, errKey).Finished())
}

// podcastDirName is the folder name of a podcast title, as the web client's
// sanitizeFilename makes it.
func podcastDirName(title string) string {
	name := strings.NewReplacer("/", " ", "\\", " ", ":", " - ", "?", "", "*", "", "|", "", "<", "", ">", "", `"`, "").Replace(strings.TrimSpace(title))
	name = strings.Join(strings.Fields(name), " ")
	if name == "" || name == "." || name == ".." {
		return "Podcast"
	}
	return name
}
