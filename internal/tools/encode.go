package tools

import (
	"cmp"
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"regexp"
	"strings"

	"monstrobook/internal/auth"
	"monstrobook/internal/db"
	"monstrobook/internal/httpx"
	"monstrobook/internal/media"
)

type encodeParams struct {
	codec    string
	channels string
	bitrate  string
}

var (
	codecRe   = regexp.MustCompile(`^[a-zA-Z0-9_]{1,32}$`)
	bitrateRe = regexp.MustCompile(`^[0-9]+[kKmM]?$`)
)

func parseEncodeParams(query url.Values) (encodeParams, error) {
	params := encodeParams{
		codec:    cmp.Or(query.Get("codec"), "aac"),
		channels: cmp.Or(query.Get("channels"), "2"),
		bitrate:  cmp.Or(query.Get("bitrate"), "128k"),
	}
	if !codecRe.MatchString(params.codec) {
		return encodeParams{}, httpx.BadRequest("Invalid codec")
	}
	if len(params.channels) != 1 || params.channels[0] < '1' || params.channels[0] > '8' {
		return encodeParams{}, httpx.BadRequest("Invalid channels")
	}
	if len(params.bitrate) > 16 || !bitrateRe.MatchString(params.bitrate) || strings.Trim(params.bitrate, "0kKmM") == "" {
		return encodeParams{}, httpx.BadRequest("Invalid bitrate")
	}
	return params, nil
}

// encode starts the merge of the audiobook into one m4b file
// (AbMergeManager.startAudiobookMerge).
func (h *Handler) encode(w http.ResponseWriter, r *http.Request, b book) error {
	switch {
	case !b.isBook():
		return httpx.BadRequest("Invalid library item: not a book")
	case len(b.files) == 0:
		return httpx.BadRequest("Invalid audiobook: no audio tracks")
	case h.has(b.item.ID, "encode-m4b"):
		return httpx.BadRequest("Audiobook is already processing")
	}
	params, err := parseEncodeParams(r.URL.Query())
	if err != nil {
		return err
	}
	title := db.Val(b.media.Title)
	j := h.newJob("encode-m4b", auth.User(r.Context()).ID, b, "Encoding M4b", "MessageTaskEncodingM4b",
		fmt.Sprintf("Encoding audiobook %q into a single m4b file.", title), "MessageTaskEncodingM4bDescription")
	h.start(j, func() error { return h.merge(j, b, params) })
	return httpx.Status(w, http.StatusOK)
}

// merge encodes the tracks into <item>.m4b: beside a single-file item, inside a
// folder item. The sources are copied to the item cache first.
func (h *Handler) merge(j *job, b book, params encodeParams) error {
	if err := os.MkdirAll(j.cache, 0o755); err != nil {
		return err
	}
	itemPath := db.Val(b.item.Path)
	name, dir := filepath.Base(itemPath), itemPath
	if b.item.IsFile {
		name, dir = strings.TrimSuffix(name, filepath.Ext(name)), filepath.Dir(itemPath)
	}
	target, temp := filepath.Join(dir, name+".m4b"), filepath.Join(j.cache, name+".tmp.m4b")
	_ = os.Remove(temp) // a leftover of an earlier attempt
	args := []string{"-y"}
	inputs := filepath.Join(j.cache, "inputs.txt")
	if len(b.files) > 1 {
		var list strings.Builder
		for _, f := range b.files {
			abs, err := filepath.Abs(f.path)
			if err != nil {
				return err
			}
			fmt.Fprintf(&list, "file '%s'\n", strings.ReplaceAll(abs, "'", `'\''`))
		}
		if err := os.WriteFile(inputs, []byte(list.String()), 0o600); err != nil {
			return err
		}
		defer os.Remove(inputs)
		args = append(args, "-safe", "0", "-f", "concat", "-i", inputs)
	} else {
		args = append(args, "-i", b.files[0].path)
	}
	metadata := filepath.Join(j.cache, "metadata.txt")
	if err := os.WriteFile(metadata, []byte(ffmetadata(b, true)), 0o600); err != nil {
		return err
	}
	defer os.Remove(metadata)
	// Every input comes before the output options: an option that follows
	// "-i cover" and precedes it is read as the cover input's option. The cover
	// is a still image, so it is encoded as mjpeg (the mp4 muxer takes it).
	args = append(args, "-i", metadata)
	var cover []string
	if path := b.cover(); path != "" {
		args = append(args, "-i", path)
		cover = []string{"-map", "2:v?", "-c:v", "mjpeg", "-disposition:v", "attached_pic"}
	}
	args = append(args, "-map", "0:a", "-map_metadata", "1", "-map_chapters", "1")
	args = append(args, cover...)
	args = append(args, "-acodec", params.codec, "-ac", params.channels, "-b:a", params.bitrate, "-f", "mp4", temp)
	if err := media.Run(j.ctx, args...); err != nil {
		_ = os.Remove(temp)
		if errors.Is(j.ctx.Err(), context.Canceled) {
			return errCanceled
		}
		return fmt.Errorf("ffmpeg m4b encode: %w", err)
	}
	for _, f := range b.files {
		if j.ctx.Err() != nil {
			_ = os.Remove(temp)
			return errCanceled
		}
		backup := filepath.Join(j.cache, filepath.Base(f.path))
		if _, err := os.Stat(backup); err == nil {
			continue
		}
		if err := copyFile(f.path, backup); err != nil {
			_ = os.Remove(temp)
			return err
		}
	}
	if err := os.Rename(temp, target); err != nil {
		_ = os.Remove(temp)
		return err
	}
	return nil
}

func copyFile(src, dst string) error {
	in, err := os.Open(src)
	if err != nil {
		return err
	}
	defer in.Close()
	out, err := os.OpenFile(dst, os.O_CREATE|os.O_TRUNC|os.O_WRONLY, 0o600)
	if err != nil {
		return err
	}
	_, err = io.Copy(out, in)
	return errors.Join(err, out.Close())
}
