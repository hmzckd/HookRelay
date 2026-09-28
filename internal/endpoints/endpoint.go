package endpoints

import "time"

// DemoSecretRef binds each local receiver to its own signing key ID.
func DemoSecretRef(url string) (string, bool) {
	switch url {
	case "http://127.0.0.1:18080/hook":
		return "demo/a-v1", true
	case "http://127.0.0.1:18081/hook":
		return "demo/b-v1", true
	default:
		return "", false
	}
}

type Endpoint struct {
	ID        string    `json:"id"`
	Name      string    `json:"name"`
	URL       string    `json:"url"`
	KeyID     string    `json:"key_id"`
	Version   int       `json:"version"`
	Enabled   bool      `json:"enabled"`
	CreatedAt time.Time `json:"created_at"`
}

type Page struct {
	Items      []Endpoint `json:"items"`
	Limit      int        `json:"limit"`
	Offset     int        `json:"offset"`
	NextOffset *int       `json:"next_offset,omitempty"`
}

type Update struct {
	ExpectedVersion int     `json:"expected_version"`
	URL             *string `json:"url"`
	KeyID           *string `json:"key_id"`
	Enabled         *bool   `json:"enabled"`
}
