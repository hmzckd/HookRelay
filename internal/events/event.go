package events

import (
	"encoding/json"
	"time"
)

type Input struct {
	Type        string          `json:"type"`
	Payload     json.RawMessage `json:"payload"`
	EndpointIDs []string        `json:"endpoint_ids"`
}

type Delivery struct {
	ID              string     `json:"id"`
	EventID         string     `json:"event_id,omitempty"`
	EndpointID      string     `json:"endpoint_id"`
	EndpointURL     string     `json:"endpoint_url"`
	EndpointVersion int        `json:"endpoint_version"`
	Status          string     `json:"status"`
	AttemptCount    int        `json:"attempt_count"`
	NextAttemptAt   *time.Time `json:"next_attempt_at,omitempty"`
	TerminalReason  string     `json:"terminal_reason,omitempty"`
	CreatedAt       time.Time  `json:"created_at"`
}

type Attempt struct {
	ID           string     `json:"id"`
	DeliveryID   string     `json:"delivery_id"`
	Status       string     `json:"status"`
	StartedAt    time.Time  `json:"started_at"`
	FinishedAt   *time.Time `json:"finished_at,omitempty"`
	HTTPStatus   *int       `json:"http_status,omitempty"`
	FailureClass string     `json:"failure_class,omitempty"`
}

type AttemptPage struct {
	Items      []Attempt `json:"items"`
	Limit      int       `json:"limit"`
	Offset     int       `json:"offset"`
	NextOffset *int      `json:"next_offset,omitempty"`
}

type Event struct {
	ID         string          `json:"id"`
	Type       string          `json:"type"`
	Payload    json.RawMessage `json:"payload"`
	CreatedAt  time.Time       `json:"created_at"`
	Deliveries []Delivery      `json:"deliveries"`
}
