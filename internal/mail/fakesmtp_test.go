package mail

import (
	"bufio"
	"encoding/base64"
	"fmt"
	"net"
	"strings"
	"sync"
	"testing"
)

// fakeSMTP is a minimal SMTP server on a local listener. It accepts any number
// of sessions, records each delivered message and can require AUTH PLAIN.
type fakeSMTP struct {
	listener net.Listener
	user     string // when set, AUTH PLAIN with user/pass is required
	pass     string
	rejectTo bool // answer RCPT TO with 550

	mu       sync.Mutex
	messages []delivered
}

type delivered struct{ From, To, Data string }

func newFakeSMTP(t *testing.T) *fakeSMTP {
	t.Helper()
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	f := &fakeSMTP{listener: listener}
	t.Cleanup(func() { _ = listener.Close() })
	go func() {
		for {
			conn, err := listener.Accept()
			if err != nil {
				return
			}
			go f.serve(conn)
		}
	}()
	return f
}

func (f *fakeSMTP) addr() (host string, port int) {
	tcp := f.listener.Addr().(*net.TCPAddr)
	return tcp.IP.String(), tcp.Port
}

func (f *fakeSMTP) received() []delivered {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]delivered(nil), f.messages...)
}

func (f *fakeSMTP) serve(conn net.Conn) {
	defer conn.Close()
	reply := func(format string, args ...any) { _, _ = fmt.Fprintf(conn, format+"\r\n", args...) }
	reader := bufio.NewReader(conn)
	reply("220 localhost ESMTP")
	var current delivered
	for {
		line, err := reader.ReadString('\n')
		if err != nil {
			return
		}
		command := strings.TrimSpace(line)
		switch {
		case strings.HasPrefix(command, "EHLO"):
			if f.user != "" {
				reply("250-localhost")
				reply("250 AUTH PLAIN")
			} else {
				reply("250 localhost")
			}
		case strings.HasPrefix(command, "AUTH PLAIN "):
			raw, _ := base64.StdEncoding.DecodeString(strings.TrimPrefix(command, "AUTH PLAIN "))
			if string(raw) == "\x00"+f.user+"\x00"+f.pass {
				reply("235 ok")
			} else {
				reply("535 bad credentials")
			}
		case strings.HasPrefix(command, "MAIL FROM:"):
			current = delivered{From: strings.Trim(strings.TrimPrefix(command, "MAIL FROM:"), "<>")}
			reply("250 ok")
		case strings.HasPrefix(command, "RCPT TO:"):
			if f.rejectTo {
				reply("550 no such user")
				continue
			}
			current.To = strings.Trim(strings.TrimPrefix(command, "RCPT TO:"), "<>")
			reply("250 ok")
		case command == "DATA":
			reply("354 go ahead")
			var data strings.Builder
			for {
				part, err := reader.ReadString('\n')
				if err != nil {
					return
				}
				if part == ".\r\n" {
					break
				}
				data.WriteString(strings.TrimPrefix(part, ".")) // undo dot-stuffing
			}
			current.Data = data.String()
			f.mu.Lock()
			f.messages = append(f.messages, current)
			f.mu.Unlock()
			reply("250 queued")
		case command == "QUIT":
			reply("221 bye")
			return
		default:
			reply("250 ok")
		}
	}
}
