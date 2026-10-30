package podcasts

import (
	"context"
	"testing"

	"github.com/robfig/cron/v3"
)

func TestCloseStopsSchedulerAndCancelsCurrentDownloadIdempotently(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	scheduler := cron.New()
	entryID, err := scheduler.AddFunc("@every 1h", func() {})
	if err != nil {
		t.Fatal(err)
	}
	scheduler.Start()

	h := &Handler{
		scheduler: scheduler,
		current:   &downloadTask{ctx: ctx, cancel: cancel},
		queue:     []*downloadTask{{id: "queued"}},
	}

	h.Close()
	h.Close()

	select {
	case <-ctx.Done():
	default:
		t.Fatal("Close left the active download context running")
	}
	if scheduler.Entry(entryID).ID != entryID {
		t.Fatal("scheduler entry was unexpectedly removed")
	}
	if len(h.queue) != 0 {
		t.Fatalf("Close left queued downloads: %d", len(h.queue))
	}
}
