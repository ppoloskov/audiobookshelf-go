// Package media runs ffprobe and ffmpeg, and knows which file extensions are
// audio, image, ebook or other kinds (see kinds.go). It is the only place that resolves the tool paths (FFPROBE_PATH,
// FFMPEG_PATH) and the only reader of their output.
package media

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"strconv"
	"strings"
)

func binary(env, name string) string {
	if p := strings.TrimSpace(os.Getenv(env)); p != "" {
		return p
	}
	return name
}

type Info struct {
	Format   Format    `json:"format"`
	Streams  []Stream  `json:"streams"`
	Chapters []Chapter `json:"chapters"`
}

type Format struct {
	Name     string            `json:"format_name"`
	LongName string            `json:"format_long_name"`
	Duration string            `json:"duration"`
	BitRate  string            `json:"bit_rate"`
	Tags     map[string]string `json:"tags"`
}

type Stream struct {
	Codec         string            `json:"codec_name"`
	CodecLong     string            `json:"codec_long_name"`
	Type          string            `json:"codec_type"`
	Index         int               `json:"index"`
	Channels      int               `json:"channels"`
	ChannelLayout string            `json:"channel_layout"`
	TimeBase      string            `json:"time_base"`
	BitRate       string            `json:"bit_rate"`
	Tags          map[string]string `json:"tags"`
	Disposition   struct {
		Default int `json:"default"`
	} `json:"disposition"`
}

type Chapter struct {
	StartTime string            `json:"start_time"`
	EndTime   string            `json:"end_time"`
	Start     json.Number       `json:"start"`
	End       json.Number       `json:"end"`
	TimeBase  string            `json:"time_base"`
	Tags      map[string]string `json:"tags"`
}

// Seconds are the chapter's start and end; ticks in the chapter's time base
// stand in for a missing start_time or end_time.
func (c Chapter) Seconds() (start, end float64) {
	start, _ = strconv.ParseFloat(c.StartTime, 64)
	end, _ = strconv.ParseFloat(c.EndTime, 64)
	if c.StartTime == "" {
		start = c.seconds(c.Start)
	}
	if c.EndTime == "" {
		end = c.seconds(c.End)
	}
	return start, end
}

func (c Chapter) seconds(ticks json.Number) float64 {
	n, _ := ticks.Int64()
	den := 1.0
	if parts := strings.Split(c.TimeBase, "/"); len(parts) == 2 {
		if parsed, err := strconv.ParseFloat(parts[1], 64); err == nil && parsed != 0 {
			den = parsed
		}
	}
	return float64(n) / den
}

func Probe(ctx context.Context, path string) (Info, error) {
	var info Info
	raw, err := run(ctx, binary("FFPROBE_PATH", "ffprobe"), "-v", "error", "-show_format", "-show_streams", "-show_chapters", "-of", "json", path)
	if err != nil {
		return info, err
	}
	return info, json.Unmarshal(raw, &info)
}

// ProbeRaw is ffprobe's full JSON report of a file (programs and private data
// included), for the route that hands it to the client unchanged.
func ProbeRaw(ctx context.Context, path string) (any, error) {
	raw, err := run(ctx, binary("FFPROBE_PATH", "ffprobe"), "-hide_banner", "-loglevel", "fatal", "-show_error", "-show_format", "-show_streams", "-show_programs", "-show_chapters", "-show_private_data", "-print_format", "json", path)
	if err != nil {
		return nil, err
	}
	var out any
	return out, json.Unmarshal(raw, &out)
}

// Run runs ffmpeg with the arguments. On failure the error carries what
// ffmpeg wrote to stderr.
func Run(ctx context.Context, args ...string) error {
	_, err := run(ctx, binary("FFMPEG_PATH", "ffmpeg"), append([]string{"-hide_banner", "-loglevel", "error"}, args...)...)
	return err
}

func run(ctx context.Context, bin string, args ...string) ([]byte, error) {
	var stdout, stderr bytes.Buffer
	cmd := exec.CommandContext(ctx, bin, args...)
	cmd.Stdout, cmd.Stderr = &stdout, &stderr
	if err := cmd.Run(); err != nil {
		if msg := strings.TrimSpace(stderr.String()); msg != "" {
			return nil, fmt.Errorf("%w: %s", err, msg[max(0, len(msg)-1024):])
		}
		return nil, err
	}
	return stdout.Bytes(), nil
}

func FFmpegAvailable() bool {
	_, err := exec.LookPath(binary("FFMPEG_PATH", "ffmpeg"))
	return err == nil
}

// ResizeImage is ffmpegHelpers.resizeImage: scale to width/height, with -1
// preserving the aspect ratio; the output extension selects the muxer.
func ResizeImage(ctx context.Context, input, output string, width, height int) error {
	return Run(ctx, "-y", "-i", input, "-vf", fmt.Sprintf("scale=%d:%d", width, height), output)
}
