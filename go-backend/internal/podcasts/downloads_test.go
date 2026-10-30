package podcasts

import (
	"errors"
	"net/http"
	"sync"
	"testing"
	"time"

	"audiobookshelf-compat/internal/config"
)

type gatedRoundTripper struct {
	entered chan struct{}
	release chan struct{}
}

func (rt *gatedRoundTripper) RoundTrip(*http.Request) (*http.Response, error) {
	close(rt.entered)
	<-rt.release
	return nil, errors.New("fixture transport failure")
}

func TestDownloadCompletionStatusPollingIsSynchronized(t *testing.T) {
	h := New(nil, config.Config{}, nil)
	h.allowPrivateNetwork = true
	rt := &gatedRoundTripper{entered: make(chan struct{}), release: make(chan struct{})}
	h.client = &http.Client{Transport: rt}
	d := &downloadTask{
		id: "download", item: podcastItem{ID: "item", Path: t.TempDir()},
		episode: map[string]any{"title": "Episode"}, url: "http://fixture.test/episode.mp3",
		createdAt: 1,
	}
	h.queueMu.Lock()
	h.current = d
	h.queue = append(h.queue, d)
	h.queueMu.Unlock()

	finished := make(chan struct{})
	go func() {
		h.runDownload(d)
		close(finished)
	}()
	select {
	case <-rt.entered:
	case <-time.After(5 * time.Second):
		t.Fatal("download did not reach controlled transport")
	}

	// Poll while work is blocked, then keep polling across the completion write.
	var pollers sync.WaitGroup
	stop := make(chan struct{})
	for i := 0; i < 4; i++ {
		pollers.Add(1)
		go func() {
			defer pollers.Done()
			for {
				select {
				case <-stop:
					return
				default:
					h.queueMu.Lock()
					_ = d.clientJSON()
					h.queueMu.Unlock()
				}
			}
		}()
	}
	close(rt.release)
	select {
	case <-finished:
	case <-time.After(5 * time.Second):
		t.Fatal("download did not finish")
	}
	close(stop)
	pollers.Wait()

	h.queueMu.Lock()
	status := d.clientJSON()
	h.queueMu.Unlock()
	if status["failed"] != true || status["isFinished"] != true {
		t.Fatalf("failed download status = %#v", status)
	}
	if status["finishedAt"] == nil {
		t.Fatalf("download lifecycle timestamps missing: %#v", status)
	}
}
