package stream

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"net"
	"net/http"
	"sync"
	"time"

	"hakubot-webconsole/internal/csrf"
	"hakubot-webconsole/internal/query"
)

type Handler struct {
	db         *sql.DB
	repository *query.Repository
	limiter    *connectionLimiter
	interval   time.Duration
}

func NewHandler(db *sql.DB, repository *query.Repository) *Handler {
	return &Handler{
		db:         db,
		repository: repository,
		limiter:    newConnectionLimiter(3),
		interval:   time.Second,
	}
}

func (h *Handler) ServeHTTP(
	writer http.ResponseWriter,
	request *http.Request,
) {
	flusher, ok := writer.(http.Flusher)
	if !ok {
		http.Error(writer, "streaming unsupported", http.StatusInternalServerError)
		return
	}
	key := connectionKey(request)
	if !h.limiter.acquire(key) {
		http.Error(
			writer,
			"too many event streams",
			http.StatusTooManyRequests,
		)
		return
	}
	defer h.limiter.release(key)

	header := writer.Header()
	header.Set("Content-Type", "text/event-stream; charset=utf-8")
	header.Set("Cache-Control", "no-store")
	header.Set("Connection", "keep-alive")
	header.Set("X-Accel-Buffering", "no")
	writer.WriteHeader(http.StatusOK)
	_, _ = fmt.Fprint(writer, "event: ready\ndata: {}\n\n")
	flusher.Flush()

	last, err := h.snapshot(request.Context())
	if err != nil {
		return
	}
	poll := time.NewTicker(h.interval)
	defer poll.Stop()
	keepalive := time.NewTicker(15 * time.Second)
	defer keepalive.Stop()

	for {
		select {
		case <-request.Context().Done():
			return
		case <-keepalive.C:
			_, _ = fmt.Fprint(writer, ": keep-alive\n\n")
			flusher.Flush()
		case <-poll.C:
			current, err := h.snapshot(request.Context())
			if err != nil {
				continue
			}
			if current.eventID != last.eventID {
				if !writeEvent(writer, flusher, "events_changed", struct{}{}) {
					return
				}
			}
			if current.diagnosticID != last.diagnosticID {
				if !writeEvent(
					writer,
					flusher,
					"diagnostics_changed",
					struct{}{},
				) {
					return
				}
			}
			if current.botFingerprint != last.botFingerprint {
				if !writeEvent(
					writer,
					flusher,
					"bot_status",
					current.botPayload,
				) {
					return
				}
			}
			last = current
		}
	}
}

type snapshot struct {
	eventID        int64
	diagnosticID   int64
	botFingerprint string
	botPayload     any
}

func (h *Handler) snapshot(ctx context.Context) (snapshot, error) {
	var result snapshot
	if err := h.db.QueryRowContext(
		ctx,
		"SELECT COALESCE(MAX(id), 0) FROM response_events",
	).Scan(&result.eventID); err != nil {
		return snapshot{}, err
	}
	if err := h.db.QueryRowContext(
		ctx,
		"SELECT COALESCE(MAX(id), 0) FROM diagnostic_logs",
	).Scan(&result.diagnosticID); err != nil {
		return snapshot{}, err
	}
	bots, err := h.repository.BotStatus(ctx, time.Now())
	if err != nil {
		return snapshot{}, err
	}
	online := len(bots) > 0
	for _, bot := range bots {
		if !bot.Online {
			online = false
			break
		}
	}
	payload := map[string]any{"online": online, "bots": bots}
	encoded, err := json.Marshal(payload)
	if err != nil {
		return snapshot{}, err
	}
	result.botFingerprint = string(encoded)
	result.botPayload = payload
	return result, nil
}

func writeEvent(
	writer http.ResponseWriter,
	flusher http.Flusher,
	event string,
	value any,
) bool {
	encoded, err := json.Marshal(value)
	if err != nil {
		return true
	}
	if _, err := fmt.Fprintf(
		writer,
		"event: %s\ndata: %s\n\n",
		event,
		encoded,
	); err != nil {
		return false
	}
	flusher.Flush()
	return true
}

func connectionKey(request *http.Request) string {
	if user := csrf.AuthenticatedUser(request); user != "" {
		return "user:" + user
	}
	host, _, err := net.SplitHostPort(request.RemoteAddr)
	if err == nil {
		return "ip:" + host
	}
	return "ip:" + request.RemoteAddr
}

type connectionLimiter struct {
	mu      sync.Mutex
	active  map[string]int
	maximum int
}

func newConnectionLimiter(maximum int) *connectionLimiter {
	return &connectionLimiter{
		active:  make(map[string]int),
		maximum: maximum,
	}
}

func (l *connectionLimiter) acquire(key string) bool {
	l.mu.Lock()
	defer l.mu.Unlock()
	if l.active[key] >= l.maximum {
		return false
	}
	l.active[key]++
	return true
}

func (l *connectionLimiter) release(key string) {
	l.mu.Lock()
	defer l.mu.Unlock()
	l.active[key]--
	if l.active[key] <= 0 {
		delete(l.active, key)
	}
}
