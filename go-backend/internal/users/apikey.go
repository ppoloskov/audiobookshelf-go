package users

// KeyUser is the user an API key names as its owner or creator.
type KeyUser struct {
	ID       string `json:"id"`
	Username string `json:"username"`
	Type     string `json:"type"`
}

// APIKey is an apiKeys row as ApiKeyController serializes it (apiKey.toJSON()):
// dates are ISO-8601 strings with milliseconds and NULL is null.
type APIKey struct {
	ID              string   `json:"id"`
	Name            string   `json:"name"`
	Description     *string  `json:"description"`
	ExpiresAt       *string  `json:"expiresAt"`
	LastUsedAt      *string  `json:"lastUsedAt"`
	IsActive        bool     `json:"isActive"`
	Permissions     any      `json:"permissions"`
	UserID          *string  `json:"userId"`
	CreatedByUserID *string  `json:"createdByUserId"`
	CreatedAt       *string  `json:"createdAt"`
	UpdatedAt       *string  `json:"updatedAt"`
	User            *KeyUser `json:"user"`
	CreatedByUser   *KeyUser `json:"createdByUser"`
}

// NewAPIKey is a key to create. ExpiresIn is in seconds; 0 never expires.
type NewAPIKey struct {
	Name, UserID, CreatedBy string
	ExpiresIn               float64
	Active                  bool
}

// SessionInfo is a live refresh-token session as GET /api/me/sessions lists
// it. Current marks the session the caller's refresh token belongs to.
type SessionInfo struct {
	ID                   string
	IPAddress, UserAgent *string
	CreatedAt, UpdatedAt any // Unix milliseconds, or nil
	Current              bool
}
