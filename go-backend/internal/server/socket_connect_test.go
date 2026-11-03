package server

import (
	"fmt"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"golang.org/x/net/websocket"

	"audiobookshelf-compat/internal/bootstrap"
	"audiobookshelf-compat/internal/config"
	"audiobookshelf-compat/internal/store"
)

// socketStage names where a connect+auth cycle stopped. stageOK means the
// server answered the auth event with init.
type socketStage string

const (
	stageOK      socketStage = "ok"
	stageOpen    socketStage = "no-open-packet"
	stageConnect socketStage = "no-40-ack"
	stageInit    socketStage = "no-init"
)

const socketCycleTimeout = 2 * time.Second

// runSocketCycle performs one Engine.IO websocket open, Socket.IO connect and
// auth, and reports the stage that failed, if any.
func runSocketCycle(wsURL, token string) (socketStage, error) {
	conn, err := websocket.Dial(wsURL, "", "http://127.0.0.1/")
	if err != nil {
		return stageOpen, err
	}
	defer conn.Close()
	_ = conn.SetDeadline(time.Now().Add(socketCycleTimeout))
	var msg string
	if err := websocket.Message.Receive(conn, &msg); err != nil || !strings.HasPrefix(msg, "0{") {
		return stageOpen, fmt.Errorf("open packet %q: %v", msg, err)
	}
	if err := websocket.Message.Send(conn, "40"); err != nil {
		return stageConnect, err
	}
	for !strings.HasPrefix(msg, "40{") {
		if err := websocket.Message.Receive(conn, &msg); err != nil {
			return stageConnect, fmt.Errorf("waiting for 40: %v", err)
		}
	}
	if err := websocket.Message.Send(conn, `42["auth","`+token+`"]`); err != nil {
		return stageInit, err
	}
	for !strings.HasPrefix(msg, `42["init"`) {
		if err := websocket.Message.Receive(conn, &msg); err != nil {
			return stageInit, fmt.Errorf("waiting for init: %v", err)
		}
	}
	return stageOK, nil
}

// socketTally counts cycles and failures per stage.
type socketTally struct {
	total    int
	failures map[socketStage]int
}

func newSocketTally() socketTally { return socketTally{failures: map[socketStage]int{}} }

func (t *socketTally) add(stage socketStage) {
	t.total++
	if stage != stageOK {
		t.failures[stage]++
	}
}

func (t *socketTally) merge(other socketTally) {
	t.total += other.total
	for stage, n := range other.failures {
		t.failures[stage] += n
	}
}

func (t socketTally) failed() int {
	n := 0
	for _, count := range t.failures {
		n += count
	}
	return n
}

func (t socketTally) String() string {
	return fmt.Sprintf("%d/%d failed (no-open-packet=%d no-40-ack=%d no-init=%d)", t.failed(), t.total, t.failures[stageOpen], t.failures[stageConnect], t.failures[stageInit])
}

// TestSocketConnectAuthLoop is the C25 regression: connect and auth must reach
// init on every cycle, on the legacy and the router-prefixed Engine.IO paths,
// sequentially and under concurrency.
func TestSocketConnectAuthLoop(t *testing.T) {
	cfg := config.Config{ConfigPath: filepath.Join(t.TempDir(), "config"), MetadataPath: filepath.Join(t.TempDir(), "metadata"), JWTSecret: "test-secret", RouterBasePath: "/audiobookshelf"}
	if err := bootstrap.Ensure(cfg); err != nil {
		t.Fatal(err)
	}
	db, err := store.OpenReadWrite(filepath.Join(cfg.ConfigPath, "absdatabase.sqlite"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	if err := insertTestRoot(db, cfg, "runtime-test", "password"); err != nil {
		t.Fatal(err)
	}
	rt, err := newRuntime(db.DB(), cfg, db, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer rt.Close()
	srv := httptest.NewServer(rt)
	defer srv.Close()
	token := makeToken(t, "test-secret", `{"userId":"root-test","username":"runtime-test","type":"access","exp":4102444800}`)
	wsHost := "ws" + strings.TrimPrefix(srv.URL, "http")

	for _, path := range []string{"/socket.io", "/audiobookshelf/socket.io"} {
		wsURL := wsHost + path + "/?EIO=4&transport=websocket"
		t.Run("sequential"+path, func(t *testing.T) {
			tally := newSocketTally()
			for i := 0; i < 200; i++ {
				stage, err := runSocketCycle(wsURL, token)
				if stage != stageOK {
					t.Logf("iteration %d stopped at %s: %v", i, stage, err)
				}
				tally.add(stage)
			}
			t.Logf("%s sequential: %s", path, tally)
			if tally.failed() > 0 {
				t.Fatalf("%s sequential: %s", path, tally)
			}
		})
		t.Run("concurrent"+path, func(t *testing.T) {
			var (
				mu    sync.Mutex
				total = newSocketTally()
				wg    sync.WaitGroup
			)
			for g := 0; g < 10; g++ {
				wg.Add(1)
				go func() {
					defer wg.Done()
					local := newSocketTally()
					for i := 0; i < 20; i++ {
						stage, err := runSocketCycle(wsURL, token)
						if stage != stageOK {
							t.Logf("concurrent stopped at %s: %v", stage, err)
						}
						local.add(stage)
					}
					mu.Lock()
					total.merge(local)
					mu.Unlock()
				}()
			}
			wg.Wait()
			t.Logf("%s concurrent: %s", path, total)
			if total.failed() > 0 {
				t.Fatalf("%s concurrent: %s", path, total)
			}
		})
	}
}
