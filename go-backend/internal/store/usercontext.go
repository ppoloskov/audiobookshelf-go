package store

import "context"

type userContextKey struct{}

// ContextWithUser returns a context carrying the authenticated user. The
// server's routing layer authenticates a request once and hands handlers
// registered on an http.ServeMux the user through the request context.
func ContextWithUser(ctx context.Context, user User) context.Context {
	return context.WithValue(ctx, userContextKey{}, user)
}

// UserFromContext returns the user stored by ContextWithUser. Public routes
// (cover and author image reads) are dispatched without one; the zero User is
// returned for them.
func UserFromContext(ctx context.Context) (User, bool) {
	user, ok := ctx.Value(userContextKey{}).(User)
	return user, ok
}
