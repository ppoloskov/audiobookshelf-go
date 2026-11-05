// Package httputil holds the response writers every feature package shares.
//
// The defaults reproduce Express: JSON from res.json and text from res.send /
// res.sendStatus carry a Content-Length, text bodies carry a weak ETag, and
// no body gets a trailing newline. Several handlers historically framed their
// responses slightly differently (no ETag, no Content-Length, a trailing
// newline from json.Encoder). Those differences are observable, so they stay
// reachable through explicit Options instead of being silently unified.
package httputil

import "net/http"

// Option adjusts how a response is framed.
type Option func(*options)

type options struct {
	noContentLength bool
	noETag          bool
	contentType     string
	streamed        bool
	request         *http.Request
}

func apply(opts []Option) options {
	var o options
	for _, opt := range opts {
		opt(&o)
	}
	return o
}

// WithoutContentLength omits the explicit Content-Length header.
func WithoutContentLength() Option { return func(o *options) { o.noContentLength = true } }

// WithoutETag omits the weak ETag header on text responses.
func WithoutETag() Option { return func(o *options) { o.noETag = true } }

// ContentType overrides the Content-Type of a text response.
func ContentType(contentType string) Option {
	return func(o *options) { o.contentType = contentType }
}

// Streamed writes JSON through json.NewEncoder directly onto the response:
// the body ends in a newline and no Content-Length is set explicitly.
func Streamed() Option { return func(o *options) { o.streamed = true } }

// ForRequest makes a HEAD request receive the headers of the GET response
// without a body.
func ForRequest(r *http.Request) Option { return func(o *options) { o.request = r } }

func (o options) head() bool { return o.request != nil && o.request.Method == http.MethodHead }
