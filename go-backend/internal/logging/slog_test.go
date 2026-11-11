package logging

import (
	"context"
	"errors"
	"log/slog"
	"regexp"
	"testing"
)

func TestSlogHandlerForwardsToLogger(t *testing.T) {
	l, _, errBuf, _ := newTestLogger(t)
	log := slog.New(&slogHandler{logger: l})
	log.Error("request failed", "path", "/api/x", "error", errors.New("boom"), "n", 3)

	got := l.Recent()
	if len(got) != 1 {
		t.Fatalf("entries = %+v", got)
	}
	entry := got[0]
	if entry.LevelName != "ERROR" || entry.Level != int(LevelError) || entry.Message != "request failed path=/api/x error=boom n=3" {
		t.Fatalf("entry = %+v", entry)
	}
	if !regexp.MustCompile(`^slog_test\.go:\d+$`).MatchString(entry.Source) {
		t.Fatalf("source = %q", entry.Source)
	}
	if errBuf.Len() == 0 {
		t.Fatal("ERROR did not reach the console")
	}
}

func TestSlogHandlerLevels(t *testing.T) {
	l, _, _, _ := newTestLogger(t)
	l.ChangeLevel(LevelDebug)
	log := slog.New(&slogHandler{logger: l})
	log.Debug("d")
	log.Info("i")
	log.Warn("w")
	log.Error("e")
	log.Log(context.Background(), slog.LevelDebug-4, "below debug")
	var names []string
	for _, entry := range l.Recent() {
		if entry.Message != "Set Log Level to DEBUG" {
			names = append(names, entry.LevelName+":"+entry.Message)
		}
	}
	want := []string{"DEBUG:d", "INFO:i", "WARN:w", "ERROR:e", "DEBUG:below debug"}
	if len(names) != len(want) {
		t.Fatalf("got %v", names)
	}
	for i := range want {
		if names[i] != want[i] {
			t.Fatalf("got %v, want %v", names, want)
		}
	}
}

func TestSlogHandlerEnabledFollowsLevel(t *testing.T) {
	l, _, _, _ := newTestLogger(t) // INFO
	h := &slogHandler{logger: l}
	ctx := context.Background()
	for level, want := range map[slog.Level]bool{slog.LevelDebug: false, slog.LevelInfo: true, slog.LevelWarn: true, slog.LevelError: true} {
		if got := h.Enabled(ctx, level); got != want {
			t.Errorf("Enabled(%v) = %v, want %v", level, got, want)
		}
	}
	slog.New(h).Debug("hidden")
	if len(l.Recent()) != 0 {
		t.Fatalf("debug record was logged at INFO: %+v", l.Recent())
	}
	l.ChangeLevel(LevelWarn)
	if h.Enabled(ctx, slog.LevelInfo) {
		t.Fatal("INFO enabled at WARN")
	}
}

func TestSlogHandlerAttrsAndGroups(t *testing.T) {
	l, _, _, _ := newTestLogger(t)
	log := slog.New(&slogHandler{logger: l}).With("a", 1).WithGroup("g").With("b", "x")
	log.Info("m", "c", true, slog.Group("h", "d", 2.5), "err", error(nil))
	got := l.Recent()
	if len(got) != 1 || got[0].Message != "m a=1 g.b=x g.c=true g.h.d=2.5 g.err=<nil>" {
		t.Fatalf("entries = %+v", got)
	}
}

func TestSlogHandlerDefaultUsesDefaultLogger(t *testing.T) {
	h, ok := SlogHandler().(*slogHandler)
	if !ok || h.logger != Default() {
		t.Fatalf("SlogHandler() = %#v", SlogHandler())
	}
}
