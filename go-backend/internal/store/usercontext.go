package store

import (
	"context"
	"net/http"
	"strings"
)

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

// RequestUser is the user the routing layer authenticated for r (the zero User
// on public routes).
func RequestUser(r *http.Request) User {
	user, _ := UserFromContext(r.Context())
	return user
}

// Placeholders is the "?,?,?" list of n SQL parameters ("" when n <= 0).
func Placeholders(n int) string {
	if n <= 0 {
		return ""
	}
	return strings.TrimSuffix(strings.Repeat("?,", n), ",")
}

// StringArgs returns values as variadic SQL arguments.
func StringArgs(values []string) []any {
	args := make([]any, len(values))
	for i, value := range values {
		args[i] = value
	}
	return args
}
