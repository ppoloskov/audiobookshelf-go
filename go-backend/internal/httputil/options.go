// Package httputil holds the response writers every feature package shares.
//
// JSON and text responses carry a Content-Length; text bodies carry a weak
// ETag. Framing details beyond that are not contract (ADR-002).
package httputil

import "net/http"

type Option func(*options)

type options struct {
	request *http.Request
}

func apply(opts []Option) options {
	var o options
	for _, opt := range opts {
		opt(&o)
	}
	return o
}

// ForRequest makes a HEAD request receive the headers of the GET response
// without a body.
func ForRequest(r *http.Request) Option { return func(o *options) { o.request = r } }

func (o options) head() bool { return o.request != nil && o.request.Method == http.MethodHead }
