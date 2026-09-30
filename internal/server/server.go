package server

import (
	"bytes"
	"compress/gzip"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"mime"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"

	"hakubot-webconsole/internal/database"
	"hakubot-webconsole/internal/query"
	"hakubot-webconsole/internal/storage"
	"hakubot-webconsole/internal/sysmonitor"
	"hakubot-webconsole/internal/timefmt"
)

type Server struct {
	db           *sql.DB
	repository   *query.Repository
	storage      *storage.Manager
	collector    *sysmonitor.Collector
	publicOrigin string
	mux          *http.ServeMux
}

func New(
	db *sql.DB,
	repository *query.Repository,
	storageManager *storage.Manager,
	collector *sysmonitor.Collector,
	publicOrigin string,
	static http.Handler,
) http.Handler {
	server := &Server{
		db:           db,
		repository:   repository,
		storage:      storageManager,
		collector:    collector,
		publicOrigin: publicOrigin,
		mux:          http.NewServeMux(),
	}
	server.routes(static)
	return securityHeaders(server.mux)
}

func (s *Server) routes(static http.Handler) {
	s.mux.HandleFunc("GET /healthz", s.health)
	s.mux.HandleFunc("GET /readyz", s.ready)
	s.mux.HandleFunc("GET /api/events", s.listEvents)
	s.mux.HandleFunc("GET /api/events/{id}", s.getEvent)
	s.mux.HandleFunc(
		"GET /api/events/{id}/raw/{part}",
		s.downloadEventRaw,
	)
	s.mux.HandleFunc("GET /api/filters", s.filters)
	s.mux.HandleFunc("GET /api/stats", s.stats)
	s.mux.HandleFunc("GET /api/bot-status", s.botStatus)
	s.mux.HandleFunc("GET /api/diagnostics", s.listDiagnostics)
	s.mux.HandleFunc("GET /api/diagnostics/{id}", s.downloadDiagnostic)
	s.mux.HandleFunc("GET /api/storage", s.storageStats)
	s.mux.HandleFunc("GET /api/system/status", s.systemStatus)
	s.mux.HandleFunc("GET /api/system/history", s.systemHistory)
	s.mux.HandleFunc(
		"POST /api/storage/cleanup/preview",
		s.cleanupPreview,
	)
	s.mux.HandleFunc("POST /api/storage/cleanup", s.cleanup)
	s.mux.HandleFunc("POST /api/storage/reclaim", s.reclaim)
	s.mux.Handle("GET /", static)
}

func securityHeaders(next http.Handler) http.Handler {
	return http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		header := writer.Header()
		header.Set("Cache-Control", "no-store")
		header.Set("Pragma", "no-cache")
		header.Set("X-Content-Type-Options", "nosniff")
		header.Set("X-Frame-Options", "DENY")
		header.Set("Referrer-Policy", "no-referrer")
		header.Set(
			"Content-Security-Policy",
			"default-src 'self'; connect-src 'self'; "+
				"img-src 'self' data:; style-src 'self' 'unsafe-inline'; "+
				"script-src 'self' 'unsafe-inline'; "+
				"frame-ancestors 'none'; base-uri 'none'; form-action 'self'",
		)
		next.ServeHTTP(writer, request)
	})
}

func (s *Server) health(writer http.ResponseWriter, _ *http.Request) {
	writeJSON(writer, http.StatusOK, map[string]string{"status": "ok"})
}

func (s *Server) ready(writer http.ResponseWriter, request *http.Request) {
	if err := database.Ready(request.Context(), s.db); err != nil {
		writeError(writer, http.StatusServiceUnavailable, "not ready")
		return
	}
	writeJSON(writer, http.StatusOK, map[string]string{"status": "ready"})
}

func (s *Server) listEvents(
	writer http.ResponseWriter,
	request *http.Request,
) {
	filters, err := parseEventFilters(request)
	if err != nil {
		writeError(writer, http.StatusBadRequest, err.Error())
		return
	}
	page, err := s.repository.ListEvents(request.Context(), filters)
	if err != nil {
		s.internalError(writer, "list events", err)
		return
	}
	writeJSON(writer, http.StatusOK, page)
}

func (s *Server) getEvent(writer http.ResponseWriter, request *http.Request) {
	id, err := positiveID(request.PathValue("id"))
	if err != nil {
		writeError(writer, http.StatusBadRequest, "invalid event id")
		return
	}
	event, err := s.repository.GetEvent(request.Context(), id)
	if errors.Is(err, sql.ErrNoRows) {
		writeError(writer, http.StatusNotFound, "event not found")
		return
	}
	if err != nil {
		s.internalError(writer, "get event", err)
		return
	}
	writeJSON(writer, http.StatusOK, event)
}

func (s *Server) downloadEventRaw(
	writer http.ResponseWriter,
	request *http.Request,
) {
	id, err := positiveID(request.PathValue("id"))
	if err != nil {
		writeError(writer, http.StatusBadRequest, "invalid event id")
		return
	}
	part := request.PathValue("part")
	if part != "input" && part != "output" && part != "logs" {
		writeError(writer, http.StatusBadRequest, "invalid raw part")
		return
	}
	payload, err := s.repository.EventRaw(request.Context(), id, part)
	if errors.Is(err, sql.ErrNoRows) {
		writeError(writer, http.StatusNotFound, "raw event data not found")
		return
	}
	if err != nil {
		s.internalError(writer, "read event raw data", err)
		return
	}
	s.serveVerifiedPayload(writer, payload, true)
}

func (s *Server) downloadDiagnostic(
	writer http.ResponseWriter,
	request *http.Request,
) {
	id, err := positiveID(request.PathValue("id"))
	if err != nil {
		writeError(writer, http.StatusBadRequest, "invalid diagnostic id")
		return
	}
	payload, err := s.repository.DiagnosticRaw(request.Context(), id)
	if errors.Is(err, sql.ErrNoRows) {
		writeError(writer, http.StatusNotFound, "diagnostic not found")
		return
	}
	if err != nil {
		s.internalError(writer, "read diagnostic", err)
		return
	}
	s.serveVerifiedPayload(writer, payload, false)
}

// serveVerifiedPayload decompresses fully and checks the stored SHA-256
// before writing anything, so a corrupt payload never leaks partially.
func (s *Server) serveVerifiedPayload(
	writer http.ResponseWriter,
	payload query.RawPayload,
	attachment bool,
) {
	reader, err := gzip.NewReader(bytes.NewReader(payload.Compressed))
	if err != nil {
		s.internalError(writer, "open diagnostic payload", err)
		return
	}
	content, err := io.ReadAll(reader)
	if err != nil {
		s.internalError(writer, "read diagnostic payload", err)
		return
	}
	sum := sha256.Sum256(content)
	if hex.EncodeToString(sum[:]) != payload.SHA256 {
		s.internalError(
			writer,
			"verify diagnostic payload",
			errors.New("SHA-256 mismatch"),
		)
		return
	}

	disposition, extension := "inline", "log"
	if attachment {
		disposition, extension = "attachment", "json"
	}
	safeTime := strings.NewReplacer(" ", "_", ":", "-").
		Replace(payload.TimeDisplay)
	filename := fmt.Sprintf(
		"%s_%s_GMT+8.%s",
		payload.FilenameStem,
		safeTime,
		extension,
	)
	writer.Header().Set("Content-Type", payload.ContentType)
	writer.Header().Set(
		"Content-Disposition",
		fmt.Sprintf(`%s; filename="%s"`, disposition, filename),
	)
	writer.WriteHeader(http.StatusOK)
	_, _ = writer.Write(content)
}

func (s *Server) filters(writer http.ResponseWriter, request *http.Request) {
	filters, err := s.repository.Filters(request.Context())
	if err != nil {
		s.internalError(writer, "list filters", err)
		return
	}
	writeJSON(writer, http.StatusOK, filters)
}

func (s *Server) stats(writer http.ResponseWriter, request *http.Request) {
	filters, err := parseEventFilters(request)
	if err != nil {
		writeError(writer, http.StatusBadRequest, err.Error())
		return
	}
	stats, err := s.repository.Stats(request.Context(), filters)
	if err != nil {
		s.internalError(writer, "query stats", err)
		return
	}
	writeJSON(writer, http.StatusOK, stats)
}

func (s *Server) botStatus(
	writer http.ResponseWriter,
	request *http.Request,
) {
	bots, err := s.repository.BotStatus(request.Context(), time.Now())
	if err != nil {
		s.internalError(writer, "query bot status", err)
		return
	}
	online := len(bots) > 0
	for _, bot := range bots {
		if !bot.Online {
			online = false
			break
		}
	}
	writeJSON(
		writer,
		http.StatusOK,
		map[string]any{"online": online, "bots": bots},
	)
}

func (s *Server) listDiagnostics(
	writer http.ResponseWriter,
	request *http.Request,
) {
	values := request.URL.Query()
	filters := query.DiagnosticFilters{
		Level:  strings.TrimSpace(values.Get("level")),
		Plugin: strings.TrimSpace(values.Get("plugin")),
		Limit:  parseLimit(values.Get("limit")),
	}
	switch filters.Level {
	case "", "WARNING", "ERROR", "CRITICAL":
	default:
		writeError(
			writer,
			http.StatusBadRequest,
			"level must be WARNING, ERROR, or CRITICAL",
		)
		return
	}
	var err error
	filters.FromMS, filters.ToMS, err = parseTimeRange(
		values.Get("from"),
		values.Get("to"),
	)
	if err != nil {
		writeError(writer, http.StatusBadRequest, err.Error())
		return
	}
	filters.CursorMS, filters.CursorID, err = parseCursor(values.Get("cursor"))
	if err != nil {
		writeError(writer, http.StatusBadRequest, err.Error())
		return
	}
	page, err := s.repository.ListDiagnostics(request.Context(), filters)
	if err != nil {
		s.internalError(writer, "list diagnostics", err)
		return
	}
	writeJSON(writer, http.StatusOK, page)
}

func (s *Server) storageStats(
	writer http.ResponseWriter,
	request *http.Request,
) {
	stats, err := s.storage.Stats(request.Context())
	if err != nil {
		s.internalError(writer, "query storage usage", err)
		return
	}
	writeJSON(writer, http.StatusOK, stats)
}

type cutoffRequest struct {
	Cutoff string `json:"cutoff"`
}

func (s *Server) cleanupPreview(
	writer http.ResponseWriter,
	request *http.Request,
) {
	cutoff, ok := s.parseCutoffMutation(writer, request)
	if !ok {
		return
	}
	preview, err := s.storage.Preview(request.Context(), cutoff)
	if errors.Is(err, storage.ErrFutureCutoff) {
		writeError(writer, http.StatusBadRequest, err.Error())
		return
	}
	if err != nil {
		s.internalError(writer, "preview cleanup", err)
		return
	}
	writeJSON(writer, http.StatusOK, map[string]any{"preview": preview})
}

func (s *Server) cleanup(
	writer http.ResponseWriter,
	request *http.Request,
) {
	cutoff, ok := s.parseCutoffMutation(writer, request)
	if !ok {
		return
	}
	result, err := s.storage.Cleanup(request.Context(), cutoff)
	if errors.Is(err, storage.ErrBusy) {
		writeError(writer, http.StatusConflict, err.Error())
		return
	}
	if errors.Is(err, storage.ErrFutureCutoff) {
		writeError(writer, http.StatusBadRequest, err.Error())
		return
	}
	if err != nil {
		s.internalError(writer, "cleanup storage", err)
		return
	}
	slog.Info(
		"webconsole storage cleanup completed",
		"cutoff",
		result.CutoffDisplay,
		"responses_deleted",
		result.ResponsesDeleted,
		"diagnostics_deleted",
		result.DiagnosticsDeleted,
	)
	writeJSON(writer, http.StatusOK, result)
}

func (s *Server) reclaim(
	writer http.ResponseWriter,
	request *http.Request,
) {
	if !s.validateMutation(writer, request) {
		return
	}
	var body struct{}
	if err := decodeJSONBody(writer, request, &body); err != nil {
		writeError(writer, http.StatusBadRequest, err.Error())
		return
	}
	result, err := s.storage.Reclaim(request.Context())
	if errors.Is(err, storage.ErrBusy) {
		writeError(writer, http.StatusConflict, err.Error())
		return
	}
	if err != nil {
		s.internalError(writer, "reclaim storage", err)
		return
	}
	slog.Info("webconsole incremental storage reclaim completed")
	writeJSON(writer, http.StatusOK, result)
}

func (s *Server) parseCutoffMutation(
	writer http.ResponseWriter,
	request *http.Request,
) (time.Time, bool) {
	if !s.validateMutation(writer, request) {
		return time.Time{}, false
	}
	var body cutoffRequest
	if err := decodeJSONBody(writer, request, &body); err != nil {
		writeError(writer, http.StatusBadRequest, err.Error())
		return time.Time{}, false
	}
	cutoff, err := timefmt.Parse(body.Cutoff)
	if err != nil {
		writeError(writer, http.StatusBadRequest, "invalid GMT+8 cutoff")
		return time.Time{}, false
	}
	return cutoff, true
}

// validateMutation blocks cross-site requests: browsers attach Basic Auth
// automatically, but a cross-origin JSON POST carries a foreign Origin.
func (s *Server) validateMutation(
	writer http.ResponseWriter,
	request *http.Request,
) bool {
	mediaType, _, err := mime.ParseMediaType(
		request.Header.Get("Content-Type"),
	)
	if err != nil || mediaType != "application/json" {
		writeError(
			writer,
			http.StatusUnsupportedMediaType,
			"Content-Type must be application/json",
		)
		return false
	}
	origin, err := url.Parse(request.Header.Get("Origin"))
	if err != nil || origin.Scheme+"://"+origin.Host != s.publicOrigin {
		writeError(writer, http.StatusForbidden, "Origin is not allowed")
		return false
	}
	return true
}

func decodeJSONBody(
	writer http.ResponseWriter,
	request *http.Request,
	target any,
) error {
	request.Body = http.MaxBytesReader(writer, request.Body, 8<<10)
	decoder := json.NewDecoder(request.Body)
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(target); err != nil {
		return errors.New("invalid JSON request body")
	}
	var trailing any
	if err := decoder.Decode(&trailing); !errors.Is(err, io.EOF) {
		return errors.New("request body must contain one JSON object")
	}
	return nil
}

func parseEventFilters(request *http.Request) (query.EventFilters, error) {
	values := request.URL.Query()
	filters := query.EventFilters{
		Status:  strings.TrimSpace(values.Get("status")),
		GroupID: strings.TrimSpace(values.Get("group_id")),
		Plugin:  strings.TrimSpace(values.Get("plugin")),
		Limit:   parseLimit(values.Get("limit")),
	}
	if filters.Status != "" &&
		filters.Status != "success" && filters.Status != "failure" {
		return query.EventFilters{}, errors.New(
			"status must be success or failure",
		)
	}
	if len(filters.GroupID) > 32 || len(filters.Plugin) > 256 {
		return query.EventFilters{}, errors.New("filter value is too long")
	}
	var err error
	filters.FromMS, filters.ToMS, err = parseTimeRange(
		values.Get("from"),
		values.Get("to"),
	)
	if err != nil {
		return query.EventFilters{}, err
	}
	filters.CursorMS, filters.CursorID, err = parseCursor(values.Get("cursor"))
	if err != nil {
		return query.EventFilters{}, err
	}
	return filters, nil
}

func parseCursor(value string) (int64, int64, error) {
	value = strings.TrimSpace(value)
	if value == "" {
		return 0, 0, nil
	}
	return query.DecodeCursor(value)
}

func parseTimeRange(from, to string) (*int64, *int64, error) {
	var fromMS, toMS *int64
	if strings.TrimSpace(from) != "" {
		value, err := timefmt.Parse(from)
		if err != nil {
			return nil, nil, fmt.Errorf("invalid from time: %w", err)
		}
		milliseconds := value.UnixMilli()
		fromMS = &milliseconds
	}
	if strings.TrimSpace(to) != "" {
		value, err := timefmt.Parse(to)
		if err != nil {
			return nil, nil, fmt.Errorf("invalid to time: %w", err)
		}
		milliseconds := value.UnixMilli()
		toMS = &milliseconds
	}
	if fromMS != nil && toMS != nil {
		if *fromMS > *toMS {
			return nil, nil, errors.New("from must not be later than to")
		}
		const maximumRange = int64(366 * 24 * time.Hour / time.Millisecond)
		if *toMS-*fromMS > maximumRange {
			return nil, nil, errors.New("time range cannot exceed 366 days")
		}
	}
	return fromMS, toMS, nil
}

func parseLimit(value string) int {
	parsed, err := strconv.Atoi(strings.TrimSpace(value))
	if err != nil || parsed <= 0 {
		return 50
	}
	return min(parsed, 200)
}

func positiveID(value string) (int64, error) {
	id, err := strconv.ParseInt(value, 10, 64)
	if err != nil || id <= 0 {
		return 0, errors.New("id must be positive")
	}
	return id, nil
}

func (s *Server) systemStatus(writer http.ResponseWriter, _ *http.Request) {
	writeJSON(writer, http.StatusOK, s.collector.Status())
}

func (s *Server) systemHistory(
	writer http.ResponseWriter,
	request *http.Request,
) {
	history, err := s.collector.History(
		request.Context(),
		request.URL.Query().Get("window"),
	)
	if err != nil {
		s.internalError(writer, "get system history", err)
		return
	}
	writeJSON(writer, http.StatusOK, history)
}

func (s *Server) internalError(
	writer http.ResponseWriter,
	operation string,
	err error,
) {
	slog.Error(operation, "error", err)
	writeError(writer, http.StatusInternalServerError, "internal server error")
}

func writeJSON(writer http.ResponseWriter, status int, value any) {
	writer.Header().Set("Content-Type", "application/json; charset=utf-8")
	writer.WriteHeader(status)
	if err := json.NewEncoder(writer).Encode(value); err != nil {
		slog.Error("encode JSON response", "error", err)
	}
}

func writeError(writer http.ResponseWriter, status int, message string) {
	writeJSON(writer, status, map[string]string{"error": message})
}
