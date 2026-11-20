package settings

// Notifications is the notification-settings document
// (NotificationSettings.toJSON).
type Notifications struct {
	ID                   string         `json:"id"`
	AppriseType          string         `json:"appriseType"`
	AppriseAPIURL        *string        `json:"appriseApiUrl"`
	Notifications        []Notification `json:"notifications"`
	MaxFailedAttempts    int            `json:"maxFailedAttempts"`
	MaxNotificationQueue int            `json:"maxNotificationQueue"`
	NotificationDelay    int            `json:"notificationDelay"` // milliseconds between deliveries
}

// Notification is one notification rule (Notification.toJSON). Times are Unix
// milliseconds; LastFiredAt is nil until it fires once.
type Notification struct {
	ID                           string   `json:"id"`
	LibraryID                    *string  `json:"libraryId"`
	EventName                    string   `json:"eventName"`
	URLs                         []string `json:"urls"`
	TitleTemplate                string   `json:"titleTemplate"`
	BodyTemplate                 string   `json:"bodyTemplate"`
	Enabled                      bool     `json:"enabled"`
	Type                         *string  `json:"type"`
	LastFiredAt                  *int64   `json:"lastFiredAt"`
	LastAttemptFailed            bool     `json:"lastAttemptFailed"`
	NumConsecutiveFailedAttempts int      `json:"numConsecutiveFailedAttempts"`
	NumTimesFired                int      `json:"numTimesFired"`
	CreatedAt                    int64    `json:"createdAt"`
}

// DefaultNotifications is the document of a server that stored none.
func DefaultNotifications() Notifications {
	return Notifications{ID: NotificationsKey, AppriseType: "api", Notifications: []Notification{},
		MaxFailedAttempts: 5, MaxNotificationQueue: 20, NotificationDelay: 1000}
}

func (n *Notifications) normalize() {
	n.ID = NotificationsKey
	if n.Notifications == nil {
		n.Notifications = []Notification{}
	}
	for i := range n.Notifications {
		if n.Notifications[i].URLs == nil {
			n.Notifications[i].URLs = []string{}
		}
	}
}

// IsUseable reports whether an Apprise API URL is configured.
func (n Notifications) IsUseable() bool { return n.AppriseAPIURL != nil && *n.AppriseAPIURL != "" }

// Find returns the notification with the id, or nil. The pointer aims into
// n.Notifications.
func (n *Notifications) Find(id string) *Notification {
	for i := range n.Notifications {
		if n.Notifications[i].ID == id {
			return &n.Notifications[i]
		}
	}
	return nil
}

// Active is the enabled notifications for an event name.
func (n Notifications) Active(eventName string) []Notification {
	var active []Notification
	for _, notification := range n.Notifications {
		if notification.Enabled && notification.EventName == eventName {
			active = append(active, notification)
		}
	}
	return active
}
