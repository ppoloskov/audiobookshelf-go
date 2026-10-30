package providers

import (
	"context"
	"encoding/json"
	"fmt"
	"html"
	"io"
	"net/http"
	"net/url"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"time"

	"audiobookshelf-compat/internal/auth"
)

func (h *Handler) searchBooks(w http.ResponseWriter, r *http.Request) {
	provider := queryValue(r, "provider", "google")
	title, author := r.URL.Query().Get("title"), r.URL.Query().Get("author")
	var results []map[string]any
	var err error
	if strings.HasPrefix(provider, "custom-") {
		results, err = h.searchCustom(r.Context(), provider, "book", title, author, r.URL.Query().Get("isbn"), 10*time.Second)
	} else if title == "" {
		results = []map[string]any{}
	} else {
		results, err = h.searchBuiltinBooks(r.Context(), provider, title, author, r.URL.Query().Get("asin"), r.URL.Query().Get("region"))
	}
	if err != nil {
		results = []map[string]any{}
	}
	for _, book := range results {
		if description, ok := book["description"].(string); ok && description != "" {
			book["description"] = auth.SanitizeHTML(description)
			book["descriptionPlain"] = stripHTML(book["description"].(string))
		}
	}
	writeJSON(w, http.StatusOK, results)
}

func (h *Handler) searchBuiltinBooks(ctx context.Context, provider, title, author, asin, region string) ([]map[string]any, error) {
	switch provider {
	case "itunes":
		return h.searchITunesBooks(ctx, title)
	case "openlibrary":
		return h.searchOpenLibrary(ctx, title, author)
	case "audiobookcovers":
		return h.searchAudiobookCovers(ctx, title)
	case "fantlab":
		return h.searchFantLab(ctx, title, author)
	case "audible", "audible.ca", "audible.uk", "audible.au", "audible.fr", "audible.de", "audible.jp", "audible.it", "audible.in", "audible.es":
		if region == "" && strings.Contains(provider, ".") {
			region = provider[strings.LastIndex(provider, ".")+1:]
		}
		return h.searchAudible(ctx, provider, title, author, asin, region)
	default:
		return h.searchGoogleBooks(ctx, title, author)
	}
}

func (h *Handler) searchGoogleBooks(ctx context.Context, title, author string) ([]map[string]any, error) {
	query := "intitle:" + title
	if author != "" {
		query += " inauthor:" + author
	}
	endpoint := h.endpoint("google") + "?q=" + url.QueryEscape(query)
	var response struct {
		Items []struct {
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
		} `json:"items"`
	}
	if err := h.getJSON(ctx, endpoint, &response); err != nil {
		return []map[string]any{}, nil
	}
	out := make([]map[string]any, 0, len(response.Items))
	for _, item := range response.Items {
		v := item.Volume
		if v.Title == "" {
			continue
		}
		authorName := any(nil)
		if len(v.Authors) > 0 {
			authorName = strings.Join(v.Authors, ", ")
		}
		year := any(nil)
		if parts := strings.Split(v.PublishedDate, "-"); len(parts) > 0 && parts[0] != "" {
			year = parts[0]
		}
		var isbn any
		for _, identifier := range v.Identifiers {
			if identifier.Type == "ISBN_13" {
				isbn = identifier.Identifier
				break
			}
			if isbn == nil && identifier.Type == "ISBN_10" {
				isbn = identifier.Identifier
			}
		}
		cover := largestImage(v.ImageLinks)
		genres := any(nil)
		if len(v.Categories) > 0 {
			genres = v.Categories
		}
		out = append(out, map[string]any{"id": item.ID, "title": v.Title, "subtitle": nullString(v.Subtitle), "author": authorName, "publisher": nullString(v.Publisher), "publishedYear": year, "description": nullString(v.Description), "cover": cover, "genres": genres, "isbn": isbn})
	}
	return out, nil
}

func (h *Handler) searchITunesBooks(ctx context.Context, title string) ([]map[string]any, error) {
	if title == "" {
		return []map[string]any{}, nil
	}
	query := url.Values{"term": {title}, "entity": {"audiobook"}, "media": {"audiobook"}}
	endpoint := h.endpoint("itunes") + "?" + query.Encode()
	var response struct {
		Results []map[string]any `json:"results"`
	}
	if err := h.getJSON(ctx, endpoint, &response); err != nil {
		return []map[string]any{}, nil
	}
	out := make([]map[string]any, 0, len(response.Results))
	for _, item := range response.Results {
		author := strings.ReplaceAll(stringField(item, "artistName"), " & ", ", ")
		var year any
		if date := stringField(item, "releaseDate"); date != "" {
			year = strings.Split(date, "-")[0]
		}
		var genres any
		if genre := stringField(item, "primaryGenreName"); genre != "" {
			genres = []string{genre}
		}
		out = append(out, map[string]any{"id": item["collectionId"], "artistId": nullAny(item["artistId"]), "title": item["collectionName"], "author": author, "description": nullAny(item["description"]), "publishedYear": year, "genres": genres, "cover": itunesArtwork(item)})
	}
	return out, nil
}

func (h *Handler) searchOpenLibrary(ctx context.Context, title, author string) ([]map[string]any, error) {
	query := url.Values{"title": {title}}
	if author != "" {
		query.Set("author", author)
	}
	endpoint := strings.TrimRight(h.endpoint("openlibrary"), "/") + "/search.json?" + query.Encode()
	var response struct {
		Docs []struct {
			Title        string   `json:"title"`
			AuthorNames  []string `json:"author_name"`
			FirstYear    any      `json:"first_publish_year"`
			CoverEdition string   `json:"cover_edition_key"`
			Key          string   `json:"key"`
		} `json:"docs"`
	}
	if err := h.getJSON(ctx, endpoint, &response); err != nil {
		return []map[string]any{}, nil
	}
	out := make([]map[string]any, 0, len(response.Docs))
	for _, doc := range response.Docs {
		if doc.Key == "" {
			continue
		}
		var works struct {
			Covers           []int `json:"covers"`
			FirstPublishDate any   `json:"first_publish_date"`
			Description      any   `json:"description"`
		}
		worksURL := strings.TrimRight(h.endpoint("openlibrary"), "/") + "/" + strings.TrimPrefix(doc.Key, "/") + ".json"
		_ = h.getJSON(ctx, worksURL, &works)
		cover := any(nil)
		if doc.CoverEdition != "" {
			cover = "https://covers.openlibrary.org/b/OLID/" + doc.CoverEdition + "-L.jpg"
		}
		covers := []string{}
		for _, id := range works.Covers {
			if id > 0 {
				covers = append(covers, fmt.Sprintf("https://covers.openlibrary.org/b/id/%d-L.jpg", id))
			}
		}
		author := any(nil)
		if len(doc.AuthorNames) > 0 {
			author = strings.Join(doc.AuthorNames, ", ")
		}
		year := doc.FirstYear
		if year == nil {
			year = firstYear(works.FirstPublishDate)
		}
		description := works.Description
		if obj, ok := description.(map[string]any); ok {
			description = obj["value"]
		}
		out = append(out, map[string]any{"title": doc.Title, "author": author, "publishedYear": stringOrNull(year), "edition": nullString(doc.CoverEdition), "cover": cover, "id": pathTail(doc.Key), "key": doc.Key, "covers": covers, "first_publish_date": works.FirstPublishDate, "description": description})
	}
	return out, nil
}

func (h *Handler) searchAudiobookCovers(ctx context.Context, title string) ([]map[string]any, error) {
	if title == "" {
		return []map[string]any{}, nil
	}
	query := url.Values{"q": {title}}
	endpoint := h.endpoint("audiobookcovers")
	if !strings.Contains(endpoint, "?") {
		endpoint += "?"
	}
	endpoint += query.Encode()
	var response []struct {
		Versions struct {
			PNG struct {
				Original string `json:"original"`
			} `json:"png"`
		} `json:"versions"`
	}
	if err := h.getJSON(ctx, endpoint, &response); err != nil {
		return []map[string]any{}, nil
	}
	out := []map[string]any{}
	for _, item := range response {
		if item.Versions.PNG.Original != "" {
			out = append(out, map[string]any{"cover": item.Versions.PNG.Original})
		}
	}
	return out, nil
}

func (h *Handler) searchPodcasts(w http.ResponseWriter, r *http.Request) {
	term := r.URL.Query().Get("term")
	if _, exists := r.URL.Query()["term"]; !exists {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "Missing required query parameter: term"})
		return
	}
	if term == "" {
		writeJSON(w, http.StatusOK, nil)
		return
	}
	country := queryValue(r, "country", "us")
	query := url.Values{"term": {term}, "entity": {"podcast"}, "media": {"podcast"}, "country": {country}}
	endpoint := h.endpoint("itunes") + "?" + query.Encode()
	var response struct {
		Results []map[string]any `json:"results"`
	}
	if err := h.getJSON(r.Context(), endpoint, &response); err != nil {
		writeJSON(w, http.StatusOK, []any{})
		return
	}
	out := make([]map[string]any, 0, len(response.Results))
	for _, item := range response.Results {
		description := stringField(item, "description")
		out = append(out, map[string]any{"id": item["collectionId"], "artistId": nullAny(item["artistId"]), "title": item["collectionName"], "artistName": item["artistName"], "description": auth.SanitizeHTML(description), "descriptionPlain": stripHTML(description), "releaseDate": item["releaseDate"], "genres": valueArray(item["genres"]), "cover": itunesArtwork(item), "trackCount": item["trackCount"], "feedUrl": item["feedUrl"], "pageUrl": item["collectionViewUrl"], "explicit": stringField(item, "trackExplicitness") == "explicit"})
	}
	writeJSON(w, http.StatusOK, out)
}

func (h *Handler) searchCovers(w http.ResponseWriter, r *http.Request) {
	title := r.URL.Query().Get("title")
	if _, exists := r.URL.Query()["title"]; !exists {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "Missing required query parameter: title"})
		return
	}
	if title == "" {
		writeJSON(w, http.StatusOK, map[string]any{"results": nil})
		return
	}
	if r.URL.Query().Get("podcast") == "1" {
		query := url.Values{"term": {title}, "entity": {"podcast"}, "media": {"podcast"}}
		var response struct {
			Results []map[string]any `json:"results"`
		}
		covers := []string{}
		if h.getJSON(r.Context(), h.endpoint("itunes")+"?"+query.Encode(), &response) == nil {
			seen := map[string]bool{}
			for _, item := range response.Results {
				if cover, ok := itunesArtwork(item).(string); ok && cover != "" && !seen[cover] {
					seen[cover] = true
					covers = append(covers, cover)
				}
			}
		}
		writeJSON(w, http.StatusOK, map[string]any{"results": covers})
		return
	}
	provider := queryValue(r, "provider", "google")
	providers := []string{provider}
	if provider == "all" {
		providers = []string{"google", "itunes", "openlibrary", "fantlab", "audiobookcovers", "audible", "audible.ca", "audible.uk", "audible.au", "audible.fr", "audible.de", "audible.jp", "audible.it", "audible.in", "audible.es"}
	} else if provider == "best" {
		providers = []string{"google", "fantlab", "audible"}
	}
	seen := map[string]bool{}
	covers := []string{}
	for _, p := range providers {
		var results []map[string]any
		var err error
		if strings.HasPrefix(p, "custom-") {
			results, err = h.searchCustom(r.Context(), p, "book", title, r.URL.Query().Get("author"), "", 10*time.Second)
		} else {
			results, err = h.searchBuiltinBooks(r.Context(), p, title, r.URL.Query().Get("author"), "", "")
		}
		if err != nil {
			continue
		}
		for _, result := range results {
			if list, ok := result["covers"].([]string); ok {
				for _, cover := range list {
					if cover != "" && !seen[cover] {
						seen[cover] = true
						covers = append(covers, cover)
					}
				}
			}
			if cover, ok := result["cover"].(string); ok && cover != "" && !seen[cover] {
				seen[cover] = true
				covers = append(covers, cover)
			}
		}
	}
	writeJSON(w, http.StatusOK, map[string]any{"results": covers})
}

func (h *Handler) searchAuthor(w http.ResponseWriter, r *http.Request) {
	name := r.URL.Query().Get("q")
	if _, exists := r.URL.Query()["q"]; !exists {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "Internal server error"})
		return
	}
	if name == "" {
		writeJSON(w, http.StatusOK, nil)
		return
	}
	results, err := h.findAuthorByName(r.Context(), name, queryValue(r, "region", ""))
	if err != nil {
		writeJSON(w, http.StatusOK, nil)
		return
	}
	writeJSON(w, http.StatusOK, results)
}
func (h *Handler) searchChapters(w http.ResponseWriter, r *http.Request) {
	asin := strings.ToUpper(r.URL.Query().Get("asin"))
	if _, exists := r.URL.Query()["asin"]; !exists {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "Internal server error"})
		return
	}
	if !validASIN(asin) {
		writeJSON(w, http.StatusOK, map[string]string{"error": "Invalid ASIN", "stringKey": "MessageInvalidAsin"})
		return
	}
	query := url.Values{"region": {strings.ToLower(queryValue(r, "region", "us"))}}
	endpoint := strings.TrimRight(h.endpoint("audnexus"), "/") + "/books/" + url.PathEscape(asin) + "/chapters?" + query.Encode()
	var result any
	if err := h.getJSON(r.Context(), endpoint, &result); err != nil || result == nil {
		writeJSON(w, http.StatusOK, map[string]string{"error": "Chapters not found", "stringKey": "MessageChaptersNotFound"})
		return
	}
	writeJSON(w, http.StatusOK, result)
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
	return json.Unmarshal(body, target)
}

func (h *Handler) findAuthorByName(ctx context.Context, name, region string) (any, error) {
	query := url.Values{"name": {name}}
	if region != "" {
		query.Set("region", region)
	}
	endpoint := strings.TrimRight(h.endpoint("audnexus"), "/") + "/authors?" + query.Encode()
	var candidates []map[string]any
	if err := h.getJSON(ctx, endpoint, &candidates); err != nil {
		return nil, nil
	}
	best := -1
	bestDistance := 4
	for i, candidate := range candidates {
		candidateName := stringField(candidate, "name")
		distance := levenshtein(candidateName, name)
		if distance < bestDistance {
			best, bestDistance = i, distance
		}
	}
	if best < 0 {
		return nil, nil
	}
	asin := stringField(candidates[best], "asin")
	if asin == "" {
		return nil, nil
	}
	detail := strings.TrimRight(h.endpoint("audnexus"), "/") + "/authors/" + url.PathEscape(strings.ToUpper(asin))
	if region != "" {
		detail += "?" + url.Values{"region": {region}}.Encode()
	}
	var author map[string]any
	if err := h.getJSON(ctx, detail, &author); err != nil {
		return nil, nil
	}
	return map[string]any{"asin": author["asin"], "description": author["description"], "image": nullAny(author["image"]), "name": author["name"]}, nil
}

func (h *Handler) searchFantLab(ctx context.Context, title, author string) ([]map[string]any, error) {
	search := title
	if author != "" {
		search += " " + author
	}
	query := url.Values{"q": {search}, "page": {"1"}, "onlymatches": {"1"}}
	var matches []map[string]any
	if err := h.getJSON(ctx, h.endpoint("fantlab")+"/search-works?"+query.Encode(), &matches); err != nil {
		return []map[string]any{}, nil
	}
	filtered := map[int]bool{7: true, 11: true, 12: true, 22: true, 23: true, 24: true, 25: true, 26: true, 46: true, 47: true, 49: true, 51: true, 52: true, 55: true, 56: true, 57: true}
	out := []map[string]any{}
	for _, match := range matches {
		workID := fmt.Sprint(match["work_id"])
		typ := intValue(match["work_type_id"])
		if workID == "<nil>" || filtered[typ] {
			continue
		}
		var b map[string]any
		if h.getJSON(ctx, h.endpoint("fantlab")+"/work/"+url.PathEscape(workID)+"/extended", &b) != nil || len(b) == 0 {
			continue
		}
		authors := []string{}
		if list, ok := b["authors"].([]any); ok {
			for _, v := range list {
				if authorObj, ok := v.(map[string]any); ok {
					if name := strings.TrimSpace(stringField(authorObj, "name")); name != "" {
						authors = append(authors, name)
					}
				}
			}
		}
		var subtitle any
		if alts, ok := b["work_name_alts"].([]any); ok && len(alts) > 0 {
			subtitle = alts[0]
		}
		var cover any
		if image := stringField(b, "image"); image != "" {
			cover = "https://fantlab.ru" + image
		}
		var genres []string
		if cls, ok := b["classificatory"].(map[string]any); ok {
			if groups, ok := cls["genre_group"].([]any); ok {
				for _, group := range groups {
					g, ok := group.(map[string]any)
					if !ok || intValue(g["genre_group_id"]) != 1 {
						continue
					}
					if roots, ok := g["genre"].([]any); ok && len(roots) > 0 {
						root, _ := roots[0].(map[string]any)
						if label := stringField(root, "label"); label != "" {
							genres = append(genres, label)
						}
						if subs, ok := root["genre"].([]any); ok {
							for _, sub := range subs {
								if child, ok := sub.(map[string]any); ok && stringField(child, "label") != "" {
									genres = append(genres, stringField(child, "label"))
								}
							}
						}
					}
					break
				}
			}
		}
		var isbn any
		if editions, ok := b["editions_blocks"].(map[string]any); ok {
			block := editions["30"]
			if block == nil {
				block = editions["10"]
			}
			if blockMap, ok := block.(map[string]any); ok {
				if list, ok := blockMap["list"].([]any); ok && len(list) > 0 {
					last, _ := list[len(list)-1].(map[string]any)
					isbn = nullAny(last["isbn"])
					editionID := fmt.Sprint(last["edition_id"])
					if editionID != "<nil>" && editionID != "" {
						var edition map[string]any
						if h.getJSON(ctx, h.endpoint("fantlab")+"/edition/"+url.PathEscape(editionID), &edition) == nil && stringField(edition, "image") != "" {
							cover = "https://fantlab.ru" + stringField(edition, "image")
						}
					}
				}
			}
		}
		out = append(out, map[string]any{"id": b["work_id"], "title": b["work_name"], "subtitle": nullAny(subtitle), "author": joinOrNil(authors), "publisher": nil, "publishedYear": nullAny(b["work_year"]), "description": nullAny(b["work_description"]), "cover": cover, "genres": genres, "isbn": isbn})
	}
	return out, nil
}

func (h *Handler) searchAudible(ctx context.Context, provider, title, author, asin, region string) ([]map[string]any, error) {
	regions := map[string]string{"us": ".com", "ca": ".ca", "uk": ".co.uk", "au": ".com.au", "fr": ".fr", "de": ".de", "jp": ".co.jp", "it": ".it", "in": ".in", "es": ".es"}
	if _, ok := regions[region]; !ok {
		region = ""
	}
	item, lookup := "", ""
	if validASIN(strings.ToUpper(asin)) {
		item = strings.ToUpper(asin)
	} else if validASIN(strings.ToUpper(title)) {
		item = strings.ToUpper(title)
	}
	if item != "" {
		lookup = strings.TrimRight(h.endpoint("audnexus"), "/") + "/books/" + url.PathEscape(item)
		if region != "" {
			lookup += "?" + url.Values{"region": {region}}.Encode()
		}
	}
	var products []map[string]any
	if item == "" {
		params := url.Values{"num_results": {"10"}, "products_sort_by": {"Relevance"}, "title": {title}}
		if author != "" {
			params.Set("author", author)
		}
		base := h.endpoint("audible")
		if region != "" {
			if strings.Contains(base, "api.audible.com") {
				base = strings.Replace(base, "api.audible.com", "api.audible"+regions[region], 1)
			}
		}
		var result struct {
			Products []map[string]any `json:"products"`
		}
		if h.getJSON(ctx, base+"?"+params.Encode(), &result) != nil {
			return []map[string]any{}, nil
		}
		for _, product := range result.Products {
			a := strings.ToUpper(stringField(product, "asin"))
			if !validASIN(a) {
				continue
			}
			endpoint := strings.TrimRight(h.endpoint("audnexus"), "/") + "/books/" + url.PathEscape(a)
			if region != "" {
				endpoint += "?" + url.Values{"region": {region}}.Encode()
			}
			var book map[string]any
			if h.getJSON(ctx, endpoint, &book) == nil && stringField(book, "asin") != "" {
				products = append(products, book)
			}
		}
	} else {
		var book map[string]any
		if h.getJSON(ctx, lookup, &book) == nil && stringField(book, "asin") != "" {
			products = append(products, book)
		}
	}
	out := make([]map[string]any, 0, len(products))
	for _, book := range products {
		out = append(out, cleanAudibleBook(book))
	}
	return out, nil
}

func cleanAudibleBook(item map[string]any) map[string]any {
	joinNames := func(raw any) any {
		list, ok := raw.([]any)
		if !ok {
			return nil
		}
		names := []string{}
		for _, v := range list {
			if obj, ok := v.(map[string]any); ok && stringField(obj, "name") != "" {
				names = append(names, stringField(obj, "name"))
			}
		}
		return joinOrNil(names)
	}
	var genres, tags []string
	if list, ok := item["genres"].([]any); ok {
		seenG, seenT := map[string]bool{}, map[string]bool{}
		for _, v := range list {
			obj, ok := v.(map[string]any)
			if !ok {
				continue
			}
			name, typ := stringField(obj, "name"), stringField(obj, "type")
			if name == "" {
				continue
			}
			if typ == "genre" && !seenG[name] {
				genres = append(genres, name)
				seenG[name] = true
			}
			if typ == "tag" && !seenT[name] {
				tags = append(tags, name)
				seenT[name] = true
			}
		}
	}
	series := []map[string]any{}
	for _, key := range []string{"seriesPrimary", "seriesSecondary"} {
		if obj, ok := item[key].(map[string]any); ok {
			series = append(series, map[string]any{"series": stringField(obj, "name"), "sequence": cleanSequence(stringField(obj, "name"), stringField(obj, "position"))})
		}
	}
	var year any
	if date := stringField(item, "releaseDate"); date != "" {
		year = strings.Split(date, "-")[0]
	}
	var duration float64
	switch v := item["runtimeLengthMin"].(type) {
	case float64:
		duration = v
	case string:
		duration, _ = strconv.ParseFloat(v, 64)
	}
	language := stringField(item, "language")
	if language != "" {
		language = strings.ToUpper(language[:1]) + language[1:]
	}
	rating := item["rating"]
	if rating == nil {
		rating = item["region"]
	}
	return map[string]any{"title": item["title"], "subtitle": nullAny(item["subtitle"]), "author": joinNames(item["authors"]), "narrator": joinNames(item["narrators"]), "publisher": item["publisherName"], "publishedYear": year, "description": nullAny(item["summary"]), "cover": item["image"], "asin": item["asin"], "isbn": item["isbn"], "genres": nilIfEmpty(genres), "tags": nilIfEmpty(tags), "series": nilIfEmpty(series), "language": nullString(language), "duration": duration, "region": nullAny(item["region"]), "rating": rating, "abridged": stringField(item, "formatType") == "abridged", "explicit": item["isAdult"] == true}
}

func cleanSequence(_ string, sequence string) string {
	if sequence == "" {
		return ""
	}
	re := regexp.MustCompile(`\.\d+|\d+(?:\.\d+)?`)
	if found := re.FindString(sequence); found != "" {
		return found
	}
	return sequence
}
func intValue(value any) int {
	switch v := value.(type) {
	case float64:
		return int(v)
	case int:
		return v
	case string:
		n, _ := strconv.Atoi(v)
		return n
	}
	return 0
}
func joinOrNil(values []string) any {
	if len(values) == 0 {
		return nil
	}
	return strings.Join(values, ", ")
}
func nilIfEmpty[T any](values []T) any {
	if len(values) == 0 {
		return nil
	}
	return values
}

func largestImage(images map[string]string) any {
	if len(images) == 0 {
		return nil
	}
	for _, key := range []string{"extraLarge", "large", "medium", "small", "thumbnail", "smallThumbnail"} {
		if value := images[key]; value != "" {
			return strings.Replace(value, "http:", "https:", 1)
		}
	}
	keys := sortedKeys(images)
	return strings.Replace(images[keys[len(keys)-1]], "http:", "https:", 1)
}
func itunesArtwork(item map[string]any) any {
	if value := stringField(item, "artworkUrl600"); value != "" {
		return value
	}
	sizes := []struct {
		size int
		url  string
	}{}
	for key, value := range item {
		if strings.HasPrefix(key, "artworkUrl") {
			size, _ := strconv.Atoi(strings.TrimPrefix(key, "artworkUrl"))
			if url, ok := value.(string); ok {
				sizes = append(sizes, struct {
					size int
					url  string
				}{size, url})
			}
		}
	}
	sort.Slice(sizes, func(i, j int) bool { return sizes[i].size < sizes[j].size })
	for _, candidate := range sizes {
		if candidate.size > 600 {
			return candidate.url
		}
	}
	for _, candidate := range sizes {
		if strings.Contains(candidate.url, fmt.Sprintf("%dx%d", candidate.size, candidate.size)) {
			return strings.Replace(candidate.url, fmt.Sprintf("%dx%d", candidate.size, candidate.size), "600x600", 1)
		}
	}
	if len(sizes) > 0 {
		return sizes[len(sizes)-1].url
	}
	return nil
}
func stringField(values map[string]any, key string) string { v, _ := values[key].(string); return v }
func nullString(value string) any {
	if value == "" {
		return nil
	}
	return value
}
func nullAny(value any) any {
	if value == nil || value == "" {
		return nil
	}
	return value
}
func valueArray(value any) any {
	if value == nil {
		return []any{}
	}
	return value
}
func firstYear(value any) any {
	str, ok := value.(string)
	if !ok || str == "" {
		return nil
	}
	year := strings.Split(str, "-")[0]
	if _, err := strconv.Atoi(year); err != nil {
		return nil
	}
	return year
}
func stringOrNull(value any) any {
	switch v := value.(type) {
	case string:
		return nullString(v)
	case float64:
		return strconv.FormatFloat(v, 'f', -1, 64)
	case json.Number:
		return v.String()
	default:
		return nil
	}
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
func stripHTML(value string) string {
	var out strings.Builder
	inside := false
	for _, r := range value {
		switch r {
		case '<':
			inside = true
			continue
		case '>':
			inside = false
			continue
		}
		if !inside {
			out.WriteRune(r)
		}
	}
	return strings.TrimSpace(html.UnescapeString(out.String()))
}
func levenshtein(a, b string) int {
	ar, br := []rune(strings.ToLower(a)), []rune(strings.ToLower(b))
	previous := make([]int, len(br)+1)
	for j := range previous {
		previous[j] = j
	}
	for i, ra := range ar {
		current := make([]int, len(br)+1)
		current[0] = i + 1
		for j, rb := range br {
			cost := 0
			if ra != rb {
				cost = 1
			}
			current[j+1] = min(current[j]+1, previous[j+1]+1, previous[j]+cost)
		}
		previous = current
	}
	return previous[len(br)]
}
