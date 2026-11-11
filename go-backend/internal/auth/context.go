package auth

import (
	"context"
	"net/http"

	"audiobookshelf-compat/internal/httpx"
)

type principalKey struct{}

// WithUser returns a context carrying the authenticated principal.
func WithUser(ctx context.Context, p *Principal) context.Context {
	return context.WithValue(ctx, principalKey{}, p)
}

// User returns the principal of the request, or nil when it is
// unauthenticated (public routes such as covers).
func User(ctx context.Context) *Principal {
	p, _ := ctx.Value(principalKey{}).(*Principal)
	return p
}

// Admin runs next for root and admin users and answers 403 to anyone else,
// including an unauthenticated or inactive principal. The /api/emails routes
// answer 404 upstream (EmailController.js adminMiddleware); they must wrap
// their own check.
func Admin(next http.Handler) http.Handler {
	return httpx.Handler(func(w http.ResponseWriter, r *http.Request) error {
		if p := User(r.Context()); !p.IsAdmin() || !p.active() {
			return httpx.Forbidden()
		}
		next.ServeHTTP(w, r)
		return nil
	})
}
