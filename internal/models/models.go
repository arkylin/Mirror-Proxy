package models

import "time"

type ProxyRequest struct {
	ID        string    `json:"id"`
	LinkID    string    `json:"link_id"`
	URL       string    `json:"url"`
	Method    string    `json:"method"`
	Status    int       `json:"status"`
	Bytes     int64     `json:"bytes"`
	Duration  int64     `json:"duration_ms"`
	ClientIP  string    `json:"client_ip"`
	CreatedAt time.Time `json:"created_at"`
}

type Stats struct {
	TotalRequests int64            `json:"total_requests"`
	TotalBytes    int64            `json:"total_bytes"`
	LinkStats     map[string]*LinkStat `json:"link_stats"`
}

type LinkStat struct {
	LinkID        string `json:"link_id"`
	RequestCount  int64  `json:"request_count"`
	BytesTransferred int64 `json:"bytes_transferred"`
	LastAccess    int64  `json:"last_access"`
}
