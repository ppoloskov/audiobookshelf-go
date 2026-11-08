package scanner

import (
	"context"
	"testing"
	"time"
)

// A watcher batch waits for the running scan of its library, and Cancel stops
// the batch once it holds the run.
func TestBeginWaitQueuesBehindRun(t *testing.T) {
	s := &Scanner{runs: map[string]*run{}}
	_, end, err := s.begin(context.Background(), "lib")
	if err != nil {
		t.Fatal(err)
	}
	got := make(chan context.Context, 1)
	go func() {
		ctx, endBatch, err := s.beginWait(context.Background(), "lib")
		if err != nil {
			t.Error(err)
			return
		}
		defer endBatch()
		got <- ctx
		<-ctx.Done()
	}()
	select {
	case <-got:
		t.Fatal("batch started while a scan was running")
	case <-time.After(50 * time.Millisecond):
	}
	end()
	var ctx context.Context
	select {
	case ctx = <-got:
	case <-time.After(time.Second):
		t.Fatal("batch did not start after the scan ended")
	}
	s.Cancel("lib")
	select {
	case <-ctx.Done():
	case <-time.After(time.Second):
		t.Fatal("Cancel did not stop the batch")
	}
}
