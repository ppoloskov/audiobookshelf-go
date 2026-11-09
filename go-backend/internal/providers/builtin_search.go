package providers

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"time"

	"audiobookshelf-compat/internal/httputil"
	"audiobookshelf-compat/internal/logging"
	"audiobookshelf-compat/internal/textclean"
)

func (h *Handler) searchBooks(w http.ResponseWriter, r *http.Request) error {
	provider := queryValue(r, "provider", "google")
	title, author := r.URL.Query().Get("title"), r.URL.Query().Get("author")
	// The item match modal names the item it matches; TS (SearchController.fetchLibraryItem)
	// answers 404 for an item that does not exist, before any provider is asked.
	if id := r.URL.Query().Get("id"); id != "" {
		var found int
		if err := h.db.QueryRowContext(r.Context(), `SELECT COUNT(*) FROM libraryItems WHERE id = ?`, id).Scan(&found); err != nil {
			return httputil.JSONStatus(w, r, http.StatusInternalServerError, map[string]string{"error": "Internal server error"})
		}
		if found == 0 {
			return httputil.JSONStatus(w, r, http.StatusNotFound, map[string]string{"error": fmt.Sprintf(`library item "%s" not found`, id)})
		}
	}
	var results []bookMatch
	var err error
	if strings.HasPrefix(provider, "custom-") {
		results, err = h.searchCustom(r.Context(), provider, "book", title, author, r.URL.Query().Get("isbn"), 10*time.Second)
	} else if title != "" {
		results, err = h.searchBuiltinBooks(r.Context(), provider, title, author, r.URL.Query().Get("asin"), r.URL.Query().Get("region"))
	}
	if err != nil || results == nil {
		results = []bookMatch{}
	}
	for i := range results {
		results[i].sanitize()
	}
	return httputil.JSON(w, r, results)
}

func (h *Handler) searchBuiltinBooks(ctx context.Context, provider, title, author, asin, region string) ([]bookMatch, error) {
	switch provider {
	case "itunes":
		return h.searchITunesBooks(ctx, title)
	case "openlibrary":
		return h.searchOpenLibrary(ctx, title, author)
	case "audiobookcovers":
		return h.searchAudiobookCovers(ctx, title)
	case "fantlab":
		return h.searchFantLab(ctx, title, author)
	default:
		if !strings.HasPrefix(provider, "audible") {
			return h.searchGoogleBooks(ctx, title, author)
		}
		if region == "" && strings.Contains(provider, ".") {
			region = provider[strings.LastIndex(provider, ".")+1:]
		}
		return h.searchAudible(ctx, provider, title, author, asin, region)
	}
}

type googleItem struct {
	ID     string `json:"id"`
	Volume struct {
		Title         string   `json:"title"`
		Subtitle      string   `json:"subtitle"`
		Authors       []string `json:"authors"`
		Publisher     string   `json:"publisher"`
		PublishedDate string   `json:"publishedDate"`
		Description   string   `json:"description"`
		Identifiers   []struct {
			Type       string `json:"type"`
			Identifier string `json:"identifier"`
		} `json:"industryIdentifiers"`
		Categories []string          `json:"categories"`
		ImageLinks map[string]string `json:"imageLinks"`
	} `json:"volumeInfo"`
}

func (g googleItem) toMatch() bookMatch {
	v := g.Volume
	var isbn string
	for _, identifier := range v.Identifiers {
		if identifier.Type == "ISBN_13" {
			isbn = identifier.Identifier
			break
		}
		if isbn == "" && identifier.Type == "ISBN_10" {
			isbn = identifier.Identifier
		}
	}
	return bookMatch{ID: g.ID, matchCore: matchCore{Title: text(v.Title), Subtitle: text(v.Subtitle), Author: text(strings.Join(v.Authors, ", ")), Publisher: text(v.Publisher),
		PublishedYear: year{strings.Split(v.PublishedDate, "-")[0]}, Description: text(v.Description), Cover: text(largestImage(v.ImageLinks)), Genres: v.Categories, ISBN: text(isbn)}}
}

func (h *Handler) searchGoogleBooks(ctx context.Context, title, author string) ([]bookMatch, error) {
	query := "intitle:" + title
	if author != "" {
		query += " inauthor:" + author
	}
	var response struct {
		Items []googleItem `json:"items"`
	}
	out := []bookMatch{}
	if h.getJSON(ctx, h.endpoint("google")+"?q="+url.QueryEscape(query), &response) != nil {
		return out, nil
	}
	for _, item := range response.Items {
		if item.Volume.Title != "" {
			out = append(out, item.toMatch())
		}
	}
	return out, nil
}

// itunesItem is one iTunes search result; artwork collects its artworkUrl<size> keys.
type itunesItem struct {
	CollectionID   any      `json:"collectionId"`
	ArtistID       any      `json:"artistId"`
	TrackCount     any      `json:"trackCount"`
	CollectionName string   `json:"collectionName"`
	ArtistName     string   `json:"artistName"`
	Description    string   `json:"description"`
	ReleaseDate    string   `json:"releaseDate"`
	PrimaryGenre   string   `json:"primaryGenreName"`
	Genres         []string `json:"genres"`
	FeedURL        string   `json:"feedUrl"`
	ViewURL        string   `json:"collectionViewUrl"`
	Explicitness   string   `json:"trackExplicitness"`
	artwork        []struct {
		size int
		url  string
	}
}

func (i *itunesItem) UnmarshalJSON(b []byte) error {
	type plain itunesItem
	_ = json.Unmarshal(b, (*plain)(i)) // a mistyped field stays zero
	var all map[string]any
	_ = json.Unmarshal(b, &all)
	for key, value := range all {
		if size, ok := strings.CutPrefix(key, "artworkUrl"); ok {
			if link, ok := value.(string); ok {
				n, _ := strconv.Atoi(size)
				i.artwork = append(i.artwork, struct {
					size int
					url  string
				}{n, link})
			}
		}
	}
	sort.Slice(i.artwork, func(a, b int) bool { return i.artwork[a].size < i.artwork[b].size })
	return nil
}

// cover is the 600px artwork, else the first larger one, else a smaller one resized to 600.
func (i itunesItem) cover() string {
	for _, c := range i.artwork {
		if c.size == 600 && c.url != "" {
			return c.url
		}
	}
	for _, c := range i.artwork {
		if c.size > 600 {
			return c.url
		}
	}
	for _, c := range i.artwork {
		if dims := fmt.Sprintf("%dx%d", c.size, c.size); strings.Contains(c.url, dims) {
			return strings.Replace(c.url, dims, "600x600", 1)
		}
	}
	if len(i.artwork) > 0 {
		return i.artwork[len(i.artwork)-1].url
	}
	return ""
}

func (i itunesItem) toMatch() bookMatch {
	m := bookMatch{ID: i.CollectionID, ArtistID: i.ArtistID, matchCore: matchCore{Title: text(i.CollectionName), Author: text(strings.ReplaceAll(i.ArtistName, " & ", ", ")),
		Description: text(i.Description), PublishedYear: year{strings.Split(i.ReleaseDate, "-")[0]}, Cover: text(i.cover())}}
	if i.PrimaryGenre != "" {
		m.Genres = list{i.PrimaryGenre}
	}
	return m
}

// podcastResult is iTunes.cleanPodcast (server/providers/iTunes.js:140-157).
type podcastResult struct {
	ID               any      `json:"id"`
	ArtistID         any      `json:"artistId"`
	Title            *string  `json:"title"`
	ArtistName       *string  `json:"artistName"`
	Description      string   `json:"description"`
	DescriptionPlain string   `json:"descriptionPlain"`
	ReleaseDate      *string  `json:"releaseDate"`
	Genres           []string `json:"genres"`
	Cover            *string  `json:"cover"`
	TrackCount       any      `json:"trackCount"`
	FeedURL          *string  `json:"feedUrl"`
	PageURL          *string  `json:"pageUrl"`
	Explicit         bool     `json:"explicit"`
}

func (i itunesItem) toPodcast() podcastResult {
	genres := i.Genres
	if genres == nil {
		genres = []string{}
	}
	return podcastResult{ID: i.CollectionID, ArtistID: i.ArtistID, Title: nonEmpty(i.CollectionName), ArtistName: nonEmpty(i.ArtistName), Description: textclean.Sanitize(i.Description),
		DescriptionPlain: textclean.Strip(i.Description), ReleaseDate: nonEmpty(i.ReleaseDate), Genres: genres, Cover: nonEmpty(i.cover()), TrackCount: i.TrackCount,
		FeedURL: nonEmpty(i.FeedURL), PageURL: nonEmpty(i.ViewURL), Explicit: i.Explicitness == "explicit"}
}

func nonEmpty(s string) *string {
	if s == "" {
		return nil
	}
	return &s
}

func (h *Handler) searchITunesBooks(ctx context.Context, title string) ([]bookMatch, error) {
	out := []bookMatch{}
	if title == "" {
		return out, nil
	}
	query := url.Values{"term": {title}, "entity": {"audiobook"}, "media": {"audiobook"}}
	var response struct {
		Results []itunesItem `json:"results"`
	}
	if h.getJSON(ctx, h.endpoint("itunes")+"?"+query.Encode(), &response) != nil {
		return out, nil
	}
	for _, item := range response.Results {
		out = append(out, item.toMatch())
	}
	return out, nil
}

type olDoc struct {
	Title        string   `json:"title"`
	AuthorNames  []string `json:"author_name"`
	FirstYear    text     `json:"first_publish_year"`
	CoverEdition string   `json:"cover_edition_key"`
	Key          string   `json:"key"`
}

type olWork struct {
	Covers           []int `json:"covers"`
	FirstPublishDate text  `json:"first_publish_date"`
	Description      text  `json:"description"`
}

func (d olDoc) toMatch(w olWork) bookMatch {
	m := bookMatch{ID: pathTail(d.Key), Key: d.Key, Edition: d.CoverEdition, FirstPublishDate: string(w.FirstPublishDate), matchCore: matchCore{Title: text(d.Title),
		Author: text(strings.Join(d.AuthorNames, ", ")), PublishedYear: year{string(d.FirstYear)}, Description: w.Description}}
	if d.CoverEdition != "" {
		m.Cover = text("https://covers.openlibrary.org/b/OLID/" + d.CoverEdition + "-L.jpg")
	}
	for _, id := range w.Covers {
		if id > 0 {
			m.Covers = append(m.Covers, fmt.Sprintf("https://covers.openlibrary.org/b/id/%d-L.jpg", id))
		}
	}
	if m.PublishedYear.IsZero() {
		if y, _, _ := strings.Cut(string(w.FirstPublishDate), "-"); onlyDigits.MatchString(y) {
			m.PublishedYear = year{y}
		}
	}
	return m
}

func (h *Handler) searchOpenLibrary(ctx context.Context, title, author string) ([]bookMatch, error) {
	query := url.Values{"title": {title}}
	if author != "" {
		query.Set("author", author)
	}
	base := strings.TrimRight(h.endpoint("openlibrary"), "/")
	var response struct {
		Docs []olDoc `json:"docs"`
	}
	out := []bookMatch{}
	if h.getJSON(ctx, base+"/search.json?"+query.Encode(), &response) != nil {
		return out, nil
	}
	for _, doc := range response.Docs {
		if doc.Key == "" {
			continue
		}
		var works olWork
		_ = h.getJSON(ctx, base+"/"+strings.TrimPrefix(doc.Key, "/")+".json", &works)
		out = append(out, doc.toMatch(works))
	}
	return out, nil
}

func (h *Handler) searchAudiobookCovers(ctx context.Context, title string) ([]bookMatch, error) {
	out := []bookMatch{}
	if title == "" {
		return out, nil
	}
	endpoint := h.endpoint("audiobookcovers")
	if !strings.Contains(endpoint, "?") {
		endpoint += "?"
	}
	var response []struct {
		Versions struct {
			PNG struct {
				Original string `json:"original"`
			} `json:"png"`
		} `json:"versions"`
	}
	if h.getJSON(ctx, endpoint+url.Values{"q": {title}}.Encode(), &response) != nil {
		return out, nil
	}
	for _, item := range response {
		if item.Versions.PNG.Original != "" {
			out = append(out, bookMatch{matchCore: matchCore{Cover: text(item.Versions.PNG.Original)}})
		}
	}
	return out, nil
}

// queryParamAsString is getQueryParamAsString (server/utils/index.js:326-343):
// an absent parameter is the default (or an error when required), an array or
// a value over 1000 characters is a ValidationError.
func queryParamAsString(r *http.Request, name, fallback string, required bool) (string, error) {
	values, present := r.URL.Query()[name]
	if !present {
		if required {
			return "", fmt.Errorf(`Query parameter "%s" is required`, name)
		}
		return fallback, nil
	}
	if len(values) > 1 {
		return "", fmt.Errorf(`Query parameter "%s" is an array`, name)
	}
	if len([]rune(values[0])) > 1000 {
		return "", fmt.Errorf(`Query parameter "%s" is too long`, name)
	}
	return values[0], nil
}

// searchPodcasts is GET /api/search/podcast, SearchController.findPodcasts
// (SearchController.js:141-156) over PodcastFinder and iTunes.searchPodcasts.
func (h *Handler) searchPodcasts(w http.ResponseWriter, r *http.Request) error {
	term, err := queryParamAsString(r, "term", "", true)
	if err != nil {
		logging.Errorf("[SearchController] findPodcasts: %s", err.Error())
		return httputil.JSONStatus(w, r, http.StatusBadRequest, map[string]string{"error": err.Error()})
	}
	country, err := queryParamAsString(r, "country", "us", false)
	if err != nil {
		logging.Errorf("[SearchController] findPodcasts: %s", err.Error())
		return httputil.JSONStatus(w, r, http.StatusBadRequest, map[string]string{"error": err.Error()})
	}
	if term == "" {
		return httputil.JSON(w, r, nil)
	}
	results, ok := h.itunesPodcasts(r.Context(), term, country)
	if !ok {
		return httputil.JSON(w, r, []any{})
	}
	return httputil.JSON(w, r, results)
}

// itunesPodcasts is iTunes.searchPodcasts (server/providers/iTunes.js:164-168):
// the cleaned podcast results; country is left out of the query when empty.
func (h *Handler) itunesPodcasts(ctx context.Context, term, country string) ([]podcastResult, bool) {
	query := url.Values{"term": {term}, "entity": {"podcast"}, "media": {"podcast"}}
	if country != "" {
		query.Set("country", country)
	}
	var response struct {
		Results []itunesItem `json:"results"`
	}
	if h.getJSON(ctx, h.endpoint("itunes")+"?"+query.Encode(), &response) != nil {
		return nil, false
	}
	out := make([]podcastResult, len(response.Results))
	for i, item := range response.Results {
		out[i] = item.toPodcast()
	}
	return out, true
}

func (h *Handler) searchCovers(w http.ResponseWriter, r *http.Request) error {
	title := r.URL.Query().Get("title")
	if _, exists := r.URL.Query()["title"]; !exists {
		return httputil.JSONStatus(w, r, http.StatusBadRequest, map[string]string{"error": "Missing required query parameter: title"})
	}
	if title == "" {
		return httputil.JSON(w, r, map[string]any{"results": nil})
	}
	req := CoverRequest{Title: title, Author: r.URL.Query().Get("author"), Provider: queryValue(r, "provider", "google"), Podcast: r.URL.Query().Get("podcast") == "1"}
	seen := map[string]bool{}
	covers := []string{}
	for _, provider := range coverProviders(req) {
		list, err := h.providerCovers(r.Context(), provider, req)
		if err != nil {
			continue
		}
		for _, cover := range list {
			if !seen[cover] {
				seen[cover] = true
				covers = append(covers, cover)
			}
		}
	}
	return httputil.JSON(w, r, map[string]any{"results": covers})
}

func (h *Handler) searchAuthor(w http.ResponseWriter, r *http.Request) error {
	name := r.URL.Query().Get("q")
	if _, exists := r.URL.Query()["q"]; !exists {
		// TS SearchController.findAuthor: a missing required parameter is a ValidationError, 400.
		return httputil.JSONStatus(w, r, http.StatusBadRequest, map[string]string{"error": `Query parameter "q" is required`})
	}
	if name == "" {
		return httputil.JSON(w, r, nil)
	}
	return httputil.JSON(w, r, h.FindAuthorByName(r.Context(), name, queryValue(r, "region", "")))
}

func (h *Handler) searchChapters(w http.ResponseWriter, r *http.Request) error {
	// A missing asin is the same answer as an invalid one: TS findChapters turns the
	// required-parameter ValidationError into the Invalid ASIN body with status 200.
	asin := strings.ToUpper(r.URL.Query().Get("asin"))
	if !validASIN(asin) {
		return httputil.JSON(w, r, map[string]string{"error": "Invalid ASIN", "stringKey": "MessageInvalidAsin"})
	}
	query := url.Values{"region": {strings.ToLower(queryValue(r, "region", "us"))}}
	endpoint := strings.TrimRight(h.endpoint("audnexus"), "/") + "/books/" + url.PathEscape(asin) + "/chapters?" + query.Encode()
	var result any
	if err := h.getJSON(r.Context(), endpoint, &result); err != nil || result == nil {
		return httputil.JSON(w, r, map[string]string{"error": "Chapters not found", "stringKey": "MessageChaptersNotFound"})
	}
	return httputil.JSON(w, r, result)
}

func (h *Handler) getJSON(ctx context.Context, endpoint string, target any) error {
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, endpoint, nil)
	if err != nil {
		return err
	}
	request.Header.Set("Accept", "application/json")
	client := h.client
	if client == nil {
		client = http.DefaultClient
	}
	copy := *client
	if copy.Timeout == 0 || copy.Timeout > 10*time.Second {
		copy.Timeout = 10 * time.Second
	}
	response, err := copy.Do(request)
	if err != nil {
		return err
	}
	defer response.Body.Close()
	if response.StatusCode < 200 || response.StatusCode >= 300 {
		return fmt.Errorf("provider returned HTTP %d", response.StatusCode)
	}
	body, err := io.ReadAll(io.LimitReader(response.Body, 8<<20))
	if err != nil {
		return err
	}
	// A field of another type than the struct expects stays zero, as an absent one does.
	var mistyped *json.UnmarshalTypeError
	if err := json.Unmarshal(body, target); !errors.As(err, &mistyped) {
		return err
	}
	return nil
}

type fantWork struct {
	WorkID         any    `json:"work_id"`
	Name           text   `json:"work_name"`
	Alts           []text `json:"work_name_alts"`
	Authors        []named
	Year           any  `json:"work_year"`
	Description    text `json:"work_description"`
	Image          string
	Classificatory struct {
		GenreGroup []struct {
			ID    int `json:"genre_group_id"`
			Genre []struct {
				Label string
				Genre []struct{ Label string }
			}
		} `json:"genre_group"`
	}
	Editions map[string]struct {
		List []struct {
			ISBN      text
			EditionID any `json:"edition_id"`
		}
	} `json:"editions_blocks"`
}

type named struct {
	Name string `json:"name"`
}

func (h *Handler) searchFantLab(ctx context.Context, title, author string) ([]bookMatch, error) {
	search := title
	if author != "" {
		search += " " + author
	}
	query := url.Values{"q": {search}, "page": {"1"}, "onlymatches": {"1"}}
	var matches []struct {
		WorkID     text `json:"work_id"`
		WorkTypeID text `json:"work_type_id"`
	}
	out := []bookMatch{}
	if h.getJSON(ctx, h.endpoint("fantlab")+"/search-works?"+query.Encode(), &matches) != nil {
		return out, nil
	}
	skipped := map[int]bool{7: true, 11: true, 12: true, 22: true, 23: true, 24: true, 25: true, 26: true, 46: true, 47: true, 49: true, 51: true, 52: true, 55: true, 56: true, 57: true}
	for _, match := range matches {
		if typ, _ := strconv.Atoi(string(match.WorkTypeID)); match.WorkID == "" || skipped[typ] {
			continue
		}
		var w fantWork
		if h.getJSON(ctx, h.endpoint("fantlab")+"/work/"+url.PathEscape(string(match.WorkID))+"/extended", &w) != nil || w.WorkID == nil && w.Name == "" {
			continue
		}
		m := bookMatch{ID: w.WorkID, matchCore: matchCore{Title: w.Name, PublishedYear: year{w.Year}, Description: w.Description}} // the year as FantLab sends it, a number
		var authors []string
		for _, a := range w.Authors {
			if name := strings.TrimSpace(a.Name); name != "" {
				authors = append(authors, name)
			}
		}
		m.Author = text(strings.Join(authors, ", "))
		if len(w.Alts) > 0 {
			m.Subtitle = w.Alts[0]
		}
		if w.Image != "" {
			m.Cover = text("https://fantlab.ru" + w.Image)
		}
		for _, group := range w.Classificatory.GenreGroup {
			if group.ID != 1 {
				continue
			}
			if len(group.Genre) > 0 {
				root := group.Genre[0]
				if root.Label != "" {
					m.Genres = append(m.Genres, root.Label)
				}
				for _, sub := range root.Genre {
					if sub.Label != "" {
						m.Genres = append(m.Genres, sub.Label)
					}
				}
			}
			break
		}
		block, ok := w.Editions["30"]
		if !ok {
			block = w.Editions["10"]
		}
		if len(block.List) > 0 {
			last := block.List[len(block.List)-1]
			m.ISBN = last.ISBN
			if id, _ := stringValue(last.EditionID); id != "" {
				var edition struct{ Image string }
				if h.getJSON(ctx, h.endpoint("fantlab")+"/edition/"+url.PathEscape(id), &edition) == nil && edition.Image != "" {
					m.Cover = text("https://fantlab.ru" + edition.Image)
				}
			}
		}
		out = append(out, m)
	}
	return out, nil
}

type audnexusBook struct {
	ASIN          text `json:"asin"`
	Title         text
	Subtitle      text
	Authors       []named
	Narrators     []named
	PublisherName text
	ReleaseDate   string
	RuntimeMin    num `json:"runtimeLengthMin"`
	Summary       text
	Image         text
	ISBN          text
	FormatType    string
	IsAdult       bool
	Language      string
	Region        text
	Rating        text
	Genres        []struct{ Name, Type string }
	SeriesPrimary *struct{ Name, Position string }
	SeriesSecond  *struct{ Name, Position string } `json:"seriesSecondary"`
}

var sequenceNumber = regexp.MustCompile(`\.\d+|\d+(?:\.\d+)?`)

func (b audnexusBook) toMatch() bookMatch {
	joinNames := func(list []named) text {
		var names []string
		for _, n := range list {
			if n.Name != "" {
				names = append(names, n.Name)
			}
		}
		return text(strings.Join(names, ", "))
	}
	m := bookMatch{Region: string(b.Region), Rating: string(b.Rating), Abridged: new(b.FormatType == "abridged"), Explicit: new(b.IsAdult),
		matchCore: matchCore{Title: b.Title, Subtitle: b.Subtitle, Author: joinNames(b.Authors), Narrator: joinNames(b.Narrators), Publisher: b.PublisherName,
			PublishedYear: year{strings.Split(b.ReleaseDate, "-")[0]}, Description: b.Summary, Cover: b.Image, ASIN: b.ASIN, ISBN: b.ISBN, Duration: b.RuntimeMin}}
	if b.Language != "" {
		m.Language = text(strings.ToUpper(b.Language[:1]) + b.Language[1:])
	}
	seenGenre, seenTag := map[string]bool{}, map[string]bool{}
	for _, g := range b.Genres {
		switch {
		case g.Name == "":
		case g.Type == "genre" && !seenGenre[g.Name]:
			seenGenre[g.Name] = true
			m.Genres = append(m.Genres, g.Name)
		case g.Type == "tag" && !seenTag[g.Name]:
			seenTag[g.Name] = true
			m.Tags = append(m.Tags, g.Name)
		}
	}
	for _, s := range []*struct{ Name, Position string }{b.SeriesPrimary, b.SeriesSecond} {
		if s != nil {
			sequence := s.Position
			if found := sequenceNumber.FindString(sequence); found != "" {
				sequence = found
			}
			m.Series = append(m.Series, seriesEntry{s.Name, text(sequence)})
		}
	}
	return m
}

func (h *Handler) searchAudible(ctx context.Context, provider, title, author, asin, region string) ([]bookMatch, error) {
	regions := map[string]string{"us": ".com", "ca": ".ca", "uk": ".co.uk", "au": ".com.au", "fr": ".fr", "de": ".de", "jp": ".co.jp", "it": ".it", "in": ".in", "es": ".es"}
	if _, ok := regions[region]; !ok {
		region = ""
	}
	bookURL := func(asin string) string {
		endpoint := strings.TrimRight(h.endpoint("audnexus"), "/") + "/books/" + url.PathEscape(asin)
		if region != "" {
			endpoint += "?" + url.Values{"region": {region}}.Encode()
		}
		return endpoint
	}
	fetch := func(asin string) (audnexusBook, bool) {
		var book audnexusBook
		return book, h.getJSON(ctx, bookURL(asin), &book) == nil && book.ASIN != ""
	}
	out := []bookMatch{}
	var found []audnexusBook
	if item := firstASIN(asin, title); item != "" {
		if book, ok := fetch(item); ok {
			found = append(found, book)
		}
	} else {
		params := url.Values{"num_results": {"10"}, "products_sort_by": {"Relevance"}, "title": {title}}
		if author != "" {
			params.Set("author", author)
		}
		base := h.endpoint("audible")
		if region != "" && strings.Contains(base, "api.audible.com") {
			base = strings.Replace(base, "api.audible.com", "api.audible"+regions[region], 1)
		}
		var result struct {
			Products []struct{ ASIN string }
		}
		if h.getJSON(ctx, base+"?"+params.Encode(), &result) != nil {
			return out, nil
		}
		for _, product := range result.Products {
			if a := strings.ToUpper(product.ASIN); validASIN(a) {
				if book, ok := fetch(a); ok {
					found = append(found, book)
				}
			}
		}
	}
	for _, book := range found {
		out = append(out, book.toMatch())
	}
	return out, nil
}

// firstASIN is the first of the candidates that is an ASIN, upper-cased.
func firstASIN(candidates ...string) string {
	for _, c := range candidates {
		if c = strings.ToUpper(c); validASIN(c) {
			return c
		}
	}
	return ""
}

func largestImage(images map[string]string) string {
	if len(images) == 0 {
		return ""
	}
	for _, key := range []string{"extraLarge", "large", "medium", "small", "thumbnail", "smallThumbnail"} {
		if value := images[key]; value != "" {
			return strings.Replace(value, "http:", "https:", 1)
		}
	}
	keys := sortedKeys(images)
	return strings.Replace(images[keys[len(keys)-1]], "http:", "https:", 1)
}

func pathTail(value string) string {
	parts := strings.Split(strings.TrimRight(value, "/"), "/")
	return parts[len(parts)-1]
}
func validASIN(value string) bool {
	if len(value) != 10 {
		return false
	}
	for _, r := range value {
		if !(r >= 'A' && r <= 'Z' || r >= '0' && r <= '9') {
			return false
		}
	}
	return true
}
