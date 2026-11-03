package server

import (
	"io"
	"net/http"
	"strconv"

	"audiobookshelf-compat/internal/logging"
)

// guardPanics runs next and turns a handler panic into what Express does for
// an exception thrown in a route: the stack is logged at ERROR and the client
// gets finalhandler's production page, `500` with `<pre>Internal Server
// Error</pre>` (express/finalhandler, NODE_ENV=production). If the handler had
// already started the response, only the log line is written, as Express can
// no longer change the status either.
func guardPanics(w http.ResponseWriter, r *http.Request, next http.HandlerFunc) {
	guard := &panicGuard{ResponseWriter: w}
	defer func() {
		recovered := recover()
		if recovered == nil {
			return
		}
		if recovered == http.ErrAbortHandler {
			panic(recovered) // net/http's own abort convention stays intact
		}
		logging.RecoverRequest(r, recovered)
		if guard.started {
			return
		}
		body := "<!DOCTYPE html>\n<html lang=\"en\">\n<head>\n<meta charset=\"utf-8\">\n<title>Error</title>\n</head>\n<body>\n<pre>Internal Server Error</pre>\n</body>\n</html>\n"
		h := w.Header()
		for _, key := range []string{"Content-Encoding", "Content-Range", "ETag", "Last-Modified", "Cache-Control", "Accept-Ranges"} {
			h.Del(key)
		}
		h.Set("Content-Type", "text/html; charset=utf-8")
		h.Set("Content-Length", strconv.Itoa(len(body)))
		h.Set("Content-Security-Policy", "default-src 'none'")
		h.Set("X-Content-Type-Options", "nosniff")
		w.WriteHeader(http.StatusInternalServerError)
		if r.Method != http.MethodHead {
			_, _ = io.WriteString(w, body)
		}
	}()
	next(guard, r)
}

// panicGuard records whether the response has started. It forwards Flush and
// ReadFrom so streaming and sendfile keep working, and Unwrap for
// http.ResponseController.
type panicGuard struct {
	http.ResponseWriter
	started bool
}

func (g *panicGuard) WriteHeader(status int) {
	g.started = true
	g.ResponseWriter.WriteHeader(status)
}

func (g *panicGuard) Write(p []byte) (int, error) {
	g.started = true
	return g.ResponseWriter.Write(p)
}

func (g *panicGuard) ReadFrom(src io.Reader) (int64, error) {
	g.started = true
	if rf, ok := g.ResponseWriter.(io.ReaderFrom); ok {
		return rf.ReadFrom(src)
	}
	return io.Copy(struct{ io.Writer }{g.ResponseWriter}, src)
}

func (g *panicGuard) Flush() {
	g.started = true
	if f, ok := g.ResponseWriter.(http.Flusher); ok {
		f.Flush()
	}
}

func (g *panicGuard) Unwrap() http.ResponseWriter { return g.ResponseWriter }
