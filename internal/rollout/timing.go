package rollout

import "time"

// LoadTiming contains no paths, chat identities or content. Durations are
// measured with the monotonic clock; stage times describe this load only.
type LoadTiming struct {
	Started       time.Time `json:"started_at"`
	Mode          string    `json:"mode,omitempty"`
	LocateMS      int64     `json:"locate_ms"`
	RestoreMS     int64     `json:"restore_ms"`
	ReadMS        int64     `json:"read_ms"`
	VerifyMS      int64     `json:"verify_ms"`
	SaveMS        int64     `json:"save_ms"`
	FirstCountsMS int64     `json:"first_counts_ms"`
	ReadyMS       int64     `json:"ready_ms"`
	DeliveryMS    int64     `json:"delivery_ms"`
	ResumedBytes  int64     `json:"resumed_bytes"`
	ReadBytes     int64     `json:"read_bytes"`
	HistoryBytes  int64     `json:"history_bytes"`
	Segments      int       `json:"segments"`
	Archives      int       `json:"archives"`
}
