package server

import (
	"bytes"
	"compress/gzip"
	"crypto/sha256"
	"crypto/subtle"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"mime"
	"net/http"
	"strconv"
	"strings"
	"time"

	"hakubot-webconsole/internal/csrf"
	"hakubot-webconsole/internal/database"
	"hakubot-webconsole/internal/query"
	"hakubot-webconsole/internal/storage"
	"hakubot-webconsole/internal/timefmt"
)

type Server struct {
	db            *sql.DB
	repository    *query.Repository
	cursor        query.CursorCodec
	storage       *storage.Manager
	csrf          *csrf.Protector
	confirmations *csrf.ConfirmationStore
	stream        http.Handler
	static        http.Handler
	mux           *http.ServeMux
}

type Options struct {
	Storage       *storage.Manager
	CSRF          *csrf.Protector
	Confirmations *csrf.ConfirmationStore
	Stream        http.Handler
	Static        http.Handler
}

func New(
	db *sql.DB,
	repository *query.Repository,
	cursor query.CursorCodec,
	options Options,
) *Server {
	server := &Server{
		db:            db,
		repository:    repository,
		cursor:        cursor,
		storage:       options.Storage,
		csrf:          options.CSRF,
		confirmations: options.Confirmations,
		stream:        options.Stream,
		static:        options.Static,
		mux:           http.NewServeMux(),
	}
	server.routes()
	return server
}

func (s *Server) Handler() http.Handler {
	return securityHeaders(s.mux)
}

func (s *Server) routes() {
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
	s.mux.HandleFunc("GET /api/csrf", s.issueCSRF)
	s.mux.HandleFunc("GET /api/storage", s.storageStats)
	s.mux.HandleFunc(
		"POST /api/storage/cleanup/preview",
		s.cleanupPreview,
	)
	s.mux.HandleFunc("POST /api/storage/cleanup", s.cleanup)
	s.mux.HandleFunc("POST /api/storage/reclaim", s.reclaim)
	if s.stream != nil {
		s.mux.Handle("GET /api/stream", s.stream)
	}
	if s.static != nil {
		s.mux.Handle("GET /", s.static)
	}
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
				"img-src 'self' data:; style-src 'self'; script-src 'self'; "+
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
	filters, err := s.parseEventFilters(request)
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
	payload, err := s.repository.EventRaw(request.Context(), id, part)
	if errors.Is(err, sql.ErrNoRows) {
		writeError(writer, http.StatusNotFound, "raw event data not found")
		return
	}
	if err != nil {
		if strings.Contains(err.Error(), "invalid raw part") {
			writeError(writer, http.StatusBadRequest, "invalid raw part")
			return
		}
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

func (s *Server) serveVerifiedPayload(
	writer http.ResponseWriter,
	payload query.RawPayload,
	attachment bool,
) {
	if err := verifyCompressedPayload(payload); err != nil {
		s.internalError(writer, "verify diagnostic payload", err)
		return
	}
	reader, err := gzip.NewReader(bytes.NewReader(payload.Compressed))
	if err != nil {
		s.internalError(writer, "open diagnostic payload", err)
		return
	}
	defer reader.Close()

	disposition := "inline"
	if attachment {
		disposition = "attachment"
	}
	safeTime := strings.NewReplacer(
		" ", "_",
		":", "-",
	).Replace(payload.TimeDisplay)
	filename := fmt.Sprintf(
		"%s_%s_GMT+8.%s",
		payload.FilenameStem,
		safeTime,
		map[bool]string{true: "json", false: "log"}[attachment],
	)
	writer.Header().Set("Content-Type", payload.ContentType)
	writer.Header().Set(
		"Content-Disposition",
		fmt.Sprintf(`%s; filename="%s"`, disposition, filename),
	)
	writer.WriteHeader(http.StatusOK)
	if _, err := io.Copy(writer, reader); err != nil {
		slog.Error("stream diagnostic payload", "error", err)
	}
}

func verifyCompressedPayload(payload query.RawPayload) error {
	expected, err := hex.DecodeString(payload.SHA256)
	if err != nil || len(expected) != sha256.Size {
		return errors.New("invalid stored SHA-256")
	}
	reader, err := gzip.NewReader(bytes.NewReader(payload.Compressed))
	if err != nil {
		return fmt.Errorf("open gzip: %w", err)
	}
	hash := sha256.New()
	_, copyErr := io.Copy(hash, reader)
	closeErr := reader.Close()
	if copyErr != nil {
		return fmt.Errorf("read gzip: %w", copyErr)
	}
	if closeErr != nil {
		return fmt.Errorf("close gzip: %w", closeErr)
	}
	if subtle.ConstantTimeCompare(hash.Sum(nil), expected) != 1 {
		return errors.New("diagnostic SHA-256 mismatch")
	}
	return nil
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
	filters, err := s.parseEventFilters(request)
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
	var err error
	filters.FromMS, filters.ToMS, err = parseTimeRange(
		values.Get("from"),
		values.Get("to"),
	)
	if err != nil {
		writeError(writer, http.StatusBadRequest, err.Error())
		return
	}
	if value := strings.TrimSpace(values.Get("cursor")); value != "" {
		cursorMS, cursorID, decodeErr := s.cursor.Decode(value)
		if decodeErr != nil {
			writeError(writer, http.StatusBadRequest, "invalid cursor")
			return
		}
		filters.CursorMS = &cursorMS
		filters.CursorID = &cursorID
	}
	page, err := s.repository.ListDiagnostics(request.Context(), filters)
	if err != nil {
		if isValidationError(err) {
			writeError(writer, http.StatusBadRequest, err.Error())
			return
		}
		s.internalError(writer, "list diagnostics", err)
		return
	}
	writeJSON(writer, http.StatusOK, page)
}

func (s *Server) issueCSRF(
	writer http.ResponseWriter,
	_ *http.Request,
) {
	if s.csrf == nil {
		writeError(writer, http.StatusServiceUnavailable, "CSRF unavailable")
		return
	}
	token, err := s.csrf.Issue(writer)
	if err != nil {
		s.internalError(writer, "issue CSRF token", err)
		return
	}
	writeJSON(writer, http.StatusOK, map[string]string{"csrf_token": token})
}

func (s *Server) storageStats(
	writer http.ResponseWriter,
	request *http.Request,
) {
	if s.storage == nil {
		writeError(writer, http.StatusServiceUnavailable, "storage unavailable")
		return
	}
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

type cleanupRequest struct {
	Cutoff            string `json:"cutoff"`
	ConfirmationToken string `json:"confirmation_token"`
}

func (s *Server) cleanupPreview(
	writer http.ResponseWriter,
	request *http.Request,
) {
	if !s.validateMutation(writer, request) {
		return
	}
	var body cutoffRequest
	if err := decodeJSONBody(writer, request, &body); err != nil {
		writeError(writer, http.StatusBadRequest, err.Error())
		return
	}
	cutoff, err := timefmt.Parse(body.Cutoff)
	if err != nil {
		writeError(writer, http.StatusBadRequest, "invalid GMT+8 cutoff")
		return
	}
	preview, err := s.storage.Preview(request.Context(), cutoff)
	if err != nil {
		writeError(writer, http.StatusBadRequest, err.Error())
		return
	}
	token, err := s.confirmations.Issue(csrf.ConfirmationClaims{
		User:            csrf.AuthenticatedUser(request),
		CutoffMS:        cutoff.UnixMilli(),
		ResponseCount:   preview.ResponseCount,
		DiagnosticCount: preview.DiagnosticCount,
	})
	if err != nil {
		s.internalError(writer, "issue cleanup confirmation", err)
		return
	}
	writeJSON(
		writer,
		http.StatusOK,
		map[string]any{
			"preview":            preview,
			"confirmation_token": token,
			"expires_in_seconds": 300,
		},
	)
}

func (s *Server) cleanup(
	writer http.ResponseWriter,
	request *http.Request,
) {
	if !s.validateMutation(writer, request) {
		return
	}
	var body cleanupRequest
	if err := decodeJSONBody(writer, request, &body); err != nil {
		writeError(writer, http.StatusBadRequest, err.Error())
		return
	}
	cutoff, err := timefmt.Parse(body.Cutoff)
	if err != nil {
		writeError(writer, http.StatusBadRequest, "invalid GMT+8 cutoff")
		return
	}
	_, err = s.confirmations.Consume(
		body.ConfirmationToken,
		csrf.AuthenticatedUser(request),
		cutoff.UnixMilli(),
	)
	if err != nil {
		writeError(writer, http.StatusForbidden, err.Error())
		return
	}
	result, err := s.storage.Cleanup(request.Context(), cutoff)
	if errors.Is(err, storage.ErrBusy) {
		writeError(writer, http.StatusConflict, err.Error())
		return
	}
	if err != nil {
		s.internalError(writer, "cleanup storage", err)
		return
	}
	slog.Info(
		"webconsole storage cleanup completed",
		"user",
		csrf.AuthenticatedUser(request),
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
	slog.Info(
		"webconsole incremental storage reclaim completed",
		"user",
		csrf.AuthenticatedUser(request),
	)
	writeJSON(writer, http.StatusOK, result)
}

func (s *Server) validateMutation(
	writer http.ResponseWriter,
	request *http.Request,
) bool {
	if s.storage == nil || s.csrf == nil || s.confirmations == nil {
		writeError(writer, http.StatusServiceUnavailable, "mutation unavailable")
		return false
	}
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
	if err := s.csrf.Validate(request); err != nil {
		writeError(writer, http.StatusForbidden, err.Error())
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

func (s *Server) parseEventFilters(
	request *http.Request,
) (query.EventFilters, error) {
	values := request.URL.Query()
	filters := query.EventFilters{
		Status:  strings.TrimSpace(values.Get("status")),
		GroupID: strings.TrimSpace(values.Get("group_id")),
		Plugin:  strings.TrimSpace(values.Get("plugin")),
		Limit:   parseLimit(values.Get("limit")),
	}
	var err error
	filters.FromMS, filters.ToMS, err = parseTimeRange(
		values.Get("from"),
		values.Get("to"),
	)
	if err != nil {
		return query.EventFilters{}, err
	}
	if len(filters.GroupID) > 32 || len(filters.Plugin) > 256 {
		return query.EventFilters{}, errors.New("filter value is too long")
	}
	if value := strings.TrimSpace(values.Get("cursor")); value != "" {
		cursorMS, cursorID, decodeErr := s.cursor.Decode(value)
		if decodeErr != nil {
			return query.EventFilters{}, errors.New("invalid cursor")
		}
		filters.CursorMS = &cursorMS
		filters.CursorID = &cursorID
	}
	return filters, nil
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
	if parsed > 200 {
		return 200
	}
	return parsed
}

func positiveID(value string) (int64, error) {
	id, err := strconv.ParseInt(value, 10, 64)
	if err != nil || id <= 0 {
		return 0, errors.New("id must be positive")
	}
	return id, nil
}

func isValidationError(err error) bool {
	message := err.Error()
	return strings.Contains(message, "must") ||
		strings.Contains(message, "cursor")
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
