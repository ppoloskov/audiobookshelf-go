package settings

import (
	"encoding/json"
	"fmt"
	"slices"
	"strconv"
	"strings"
)

// Email is the email-settings document (EmailSettings.toJSON). Pass is stored
// in plain text, as upstream does.
type Email struct {
	ID                 string          `json:"id"`
	Host               *string         `json:"host"`
	Port               Port            `json:"port"`
	Secure             bool            `json:"secure"`
	RejectUnauthorized bool            `json:"rejectUnauthorized"`
	User               *string         `json:"user"`
	Pass               *string         `json:"pass"`
	TestAddress        *string         `json:"testAddress"`
	FromAddress        *string         `json:"fromAddress"`
	EreaderDevices     []EreaderDevice `json:"ereaderDevices"`
}

// EreaderDevice is one e-reader a user may send ebooks to. AvailabilityOption
// is adminOrUp, userOrUp, guestOrUp or specificUsers; empty means adminOrUp.
// Users (user ids) matter only for specificUsers.
type EreaderDevice struct {
	Name               string   `json:"name"`
	Email              string   `json:"email"`
	AvailabilityOption string   `json:"availabilityOption,omitzero"`
	Users              []string `json:"users,omitzero"`
}

// Port is an SMTP port. Reading a stored row accepts a number or a numeric
// string (older rows); anything else reads as 465, the default.
type Port int

// UnmarshalJSON implements json.Unmarshaler; null keeps the current value.
func (p *Port) UnmarshalJSON(data []byte) error {
	var value any // number, string or junk: the type is decided below
	if err := json.Unmarshal(data, &value); err != nil {
		return err
	}
	switch v := value.(type) {
	case nil:
	case float64:
		*p = Port(v)
	case string:
		n, err := strconv.Atoi(strings.TrimSpace(v))
		if err != nil {
			n = 465
		}
		*p = Port(n)
	default:
		*p = 465
	}
	return nil
}

// DefaultEmail is the document of a server that stored no email settings.
func DefaultEmail() Email {
	return Email{ID: EmailKey, Port: 465, Secure: true, RejectUnauthorized: true, EreaderDevices: []EreaderDevice{}}
}

func (e *Email) normalize() {
	e.ID = EmailKey
	if e.EreaderDevices == nil {
		e.EreaderDevices = []EreaderDevice{}
	}
}

// String redacts the SMTP password so %v and %+v cannot log it.
func (e Email) String() string {
	if e.Pass != nil {
		hidden := "redacted"
		e.Pass = &hidden
	}
	data, err := json.Marshal(e)
	if err != nil {
		return fmt.Sprintf("Email{Host:%v}", e.Host)
	}
	return string(data)
}

// AllowedFor reports whether a user of the given type ("root", "admin", "user",
// "guest") and id may send to the device (EmailSettings.checkUserCanAccessDevice).
func (d EreaderDevice) AllowedFor(userID, userType string) bool {
	admin := userType == "root" || userType == "admin"
	switch d.AvailabilityOption {
	case "", "adminOrUp":
		return admin
	case "userOrUp":
		return admin || userType == "user"
	case "guestOrUp":
		return true
	case "specificUsers":
		return slices.Contains(d.Users, userID)
	}
	return false
}

// DevicesFor is the devices (with a name and an address) the user may send to
// (EmailSettings.getEReaderDevices). It never returns nil.
func (e Email) DevicesFor(userID, userType string) []EreaderDevice {
	devices := []EreaderDevice{}
	for _, device := range e.EreaderDevices {
		if device.Name != "" && device.Email != "" && device.AllowedFor(userID, userType) {
			devices = append(devices, device)
		}
	}
	return devices
}
