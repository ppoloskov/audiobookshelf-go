package server

import (
	"bufio"
	"fmt"
	"io"
	"net"
	"net/http"
	"path"
	"regexp"
	"slices"
	"strings"
	"time"

	"audiobookshelf-compat/internal/httpx"
	"audiobookshelf-compat/internal/logging"
	"audiobookshelf-compat/internal/settings"
)

type middleware func(http.Handler) http.Handler

// chain wraps h so that the first middleware is the outermost.
func chain(h http.Handler, middlewares ...middleware) http.Handler {
	for i := len(middlewares) - 1; i >= 0; i-- {
		h = middlewares[i](h)
	}
	return h
}

// recoverPanics logs a handler panic with its stack and, if nothing was
// written yet, answers 500. http.ErrAbortHandler passes through. It also
// writes the DEBUG access line (TRACE for the liveness probes).
func recoverPanics(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		rec := &recorder{ResponseWriter: w}
		begin := time.Now()
		defer func() { logAccess(r, rec, begin) }()
		defer func() {
			recovered := recover()
			if recovered == nil {
				return
			}
			if recovered == http.ErrAbortHandler {
				panic(recovered)
			}
			logging.RecoverRequest(r, recovered)
			if !rec.started {
				_ = httpx.Text(w, http.StatusInternalServerError, "Internal Server Error")
			}
		}()
		next.ServeHTTP(rec, r)
	})
}

func logAccess(r *http.Request, rec *recorder, begin time.Time) {
	level := logging.LevelDebug
	if isLivenessProbe(r.URL.Path) {
		level = logging.LevelTrace
	}
	if !rec.started || logging.Default().Level() > level {
		return
	}
	line := fmt.Sprintf("[HTTP] %s %s %d %dms", r.Method, r.URL.Path, max(rec.status, http.StatusOK), time.Since(begin).Milliseconds())
	if level == logging.LevelTrace {
		logging.Trace(line)
	} else {
		logging.Debug(line)
	}
}

// isLivenessProbe: probes fire every few seconds, with or without the base path.
func isLivenessProbe(urlPath string) bool {
	return !strings.HasPrefix(urlPath, "/api/") && (strings.HasSuffix(urlPath, "/healthcheck") || strings.HasSuffix(urlPath, "/ping"))
}

// recorder notes whether the response has started. It forwards Flush,
// ReadFrom (sendfile) and Hijack (WebSocket upgrades) and exposes Unwrap for
// http.ResponseController.
type recorder struct {
	http.ResponseWriter
	started bool
	status  int
}

func (g *recorder) WriteHeader(status int) {
	if !g.started {
		g.status = status
	}
	g.started = true
	g.ResponseWriter.WriteHeader(status)
}

func (g *recorder) Write(p []byte) (int, error) {
	g.started = true
	return g.ResponseWriter.Write(p)
}

func (g *recorder) ReadFrom(src io.Reader) (int64, error) {
	g.started = true
	if rf, ok := g.ResponseWriter.(io.ReaderFrom); ok {
		return rf.ReadFrom(src)
	}
	return io.Copy(struct{ io.Writer }{g.ResponseWriter}, src)
}

func (g *recorder) Flush() {
	g.started = true
	_ = http.NewResponseController(g.ResponseWriter).Flush() // an unflushable writer has nothing to flush
}

func (g *recorder) Hijack() (net.Conn, *bufio.ReadWriter, error) {
	g.started = true
	return http.NewResponseController(g.ResponseWriter).Hijack()
}

func (g *recorder) Unwrap() http.ResponseWriter { return g.ResponseWriter }

// securityHeaders sets Referrer-Policy and, unless the allowIframe setting is
// on, Content-Security-Policy: frame-ancestors (server/Server.js:260).
func securityHeaders(st *settings.Store) middleware {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			w.Header().Set("Referrer-Policy", "no-referrer")
			if !st.Server().AllowIframe {
				w.Header().Set("Content-Security-Policy", "frame-ancestors 'self'")
			}
			next.ServeHTTP(w, r)
		})
	}
}

// corsPath is the item cover and ebook route the mobile apps fetch
// cross-origin (server/Server.js:279). Upstream does not anchor it either.
var corsPath = regexp.MustCompile(`/api/items/[a-z0-9-]{36}/(?:ebook|cover)(?:/[0-9]+)?`)

// mobileOrigins are always allowed on a CORS path (server/Server.js:280).
var mobileOrigins = []string{"capacitor://localhost", "http://localhost"}

// corsAllowed ports the rule of server/Server.js:270-295. CORS applies to the
// cover/ebook paths, to every path when allowedOrigins is set, and to every
// path with allowAll. Within that, an origin is allowed when it is a mobile
// origin or in allowedOrigins, or when allowAll is set. Upstream also allows
// everything in development mode, and sends "undefined" for a missing Origin;
// here a request without an Origin is never a CORS request.
func corsAllowed(allowAll bool, allowedOrigins []string, urlPath, origin string) bool {
	if origin == "" || !(allowAll || len(allowedOrigins) > 0 || corsPath.MatchString(urlPath)) {
		return false
	}
	return allowAll || slices.Contains(mobileOrigins, origin) || slices.Contains(allowedOrigins, origin)
}

// corsPolicy answers allowed cross-origin requests with the CORS headers and
// an OPTIONS preflight with 200 "OK", as upstream does. Other requests pass
// untouched (upstream does not block them either).
func corsPolicy(allowAll bool, st *settings.Store) middleware {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			origin := r.Header.Get("Origin")
			if corsAllowed(allowAll, st.Server().AllowedOrigins, r.URL.Path, origin) {
				h := w.Header()
				h.Set("Access-Control-Allow-Origin", origin)
				h.Set("Access-Control-Allow-Methods", "GET, POST, PATCH, PUT, DELETE, OPTIONS")
				h.Set("Access-Control-Allow-Headers", "*")
				h.Set("Access-Control-Allow-Credentials", "true")
				if r.Method == http.MethodOptions {
					_ = httpx.Text(w, http.StatusOK, "OK")
					return
				}
			}
			next.ServeHTTP(w, r)
		})
	}
}

// basePath serves a request that arrives under the router base path with the
// prefix removed; requests without the prefix are served as they are, so both
// forms work. The bare base path redirects to its root.
func basePath(base string) middleware {
	base = strings.TrimRight(base, "/")
	return func(next http.Handler) http.Handler {
		if base == "" {
			return next
		}
		strip := http.StripPrefix(base, next)
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			switch {
			case r.URL.Path == base:
				http.Redirect(w, r, base+"/", http.StatusPermanentRedirect)
			case strings.HasPrefix(r.URL.Path, base+"/"):
				strip.ServeHTTP(w, r)
			default:
				next.ServeHTTP(w, r)
			}
		})
	}
}

// cleanPath routes "/x/" as "/x", as Express did, and answers 404 to any other
// path that is not in canonical form ("//", "..") instead of letting the mux
// redirect it. The root stays.
func cleanPath(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		p := r.URL.Path
		if len(p) > 1 {
			p = strings.TrimSuffix(p, "/")
		}
		if p != path.Clean(p) {
			_ = httpx.Status(w, http.StatusNotFound)
			return
		}
		if p != r.URL.Path {
			trimmed := *r
			u := *r.URL
			u.Path, u.RawPath = p, ""
			trimmed.URL = &u
			r = &trimmed
		}
		next.ServeHTTP(w, r)
	})
}
