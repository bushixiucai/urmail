package billing

import "time"

// CalendarSpec binds an established calendar or a first-payment estimate to a quote.
// Provisional dates become authoritative only when paid service is fulfilled.
type CalendarSpec struct {
	Wall        *CalendarAnchor `json:"wall,omitempty"`
	Timezone    string          `json:"timezone"`
	AnchorAt    time.Time       `json:"anchor_at"`
	Version     int64           `json:"version"`
	Reanchor    bool            `json:"reanchor"`
	Provisional bool            `json:"provisional"`
}

// Anchor returns the persisted wall clock, or validates a provisional estimate.
func (s CalendarSpec) Anchor() (CalendarAnchor, error) {
	if s.Version < 0 {
		return CalendarAnchor{}, ErrInvalidCalendarAnchor
	}
	if s.Wall != nil {
		if s.Wall.Timezone != s.Timezone || s.AnchorAt.IsZero() {
			return CalendarAnchor{}, ErrInvalidCalendarAnchor
		}
		if _, err := s.Wall.location(); err != nil {
			return CalendarAnchor{}, err
		}
		return *s.Wall, nil
	}
	return NewCalendarAnchor(s.AnchorAt, s.Timezone)
}
