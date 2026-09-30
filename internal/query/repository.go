package query

import (
	"context"
	"database/sql"
	"fmt"
	"sort"
	"strings"
	"time"

	"hakubot-webconsole/internal/timefmt"
)

type Repository struct {
	db *sql.DB
}

func NewRepository(db *sql.DB) *Repository {
	return &Repository{db: db}
}

// Filters arrive validated from the HTTP layer. CursorID == 0 means no cursor.
type EventFilters struct {
	Status   string
	FromMS   *int64
	ToMS     *int64
	GroupID  string
	Plugin   string
	CursorMS int64
	CursorID int64
	Limit    int
}

type EventSummary struct {
	ID              int64  `json:"id"`
	TimeDisplay     string `json:"time_display"`
	Status          string `json:"status"`
	PluginName      string `json:"plugin_name,omitempty"`
	ModuleName      string `json:"module_name,omitempty"`
	GroupID         string `json:"group_id,omitempty"`
	UserID          string `json:"user_id,omitempty"`
	RequestSummary  string `json:"request_summary"`
	ResponseSummary string `json:"response_summary"`
	DurationMS      *int64 `json:"duration_ms,omitempty"`
	MaxLogLevel     string `json:"max_log_level,omitempty"`
	startedAtMS     int64
}

type EventPage struct {
	Items      []EventSummary `json:"items"`
	NextCursor string         `json:"next_cursor,omitempty"`
}

func (r *Repository) ListEvents(
	ctx context.Context,
	filters EventFilters,
) (EventPage, error) {
	where, args := buildEventWhere(filters)
	query := `
		SELECT id, started_at_ms, status,
		       COALESCE(plugin_name, ''), COALESCE(module_name, ''),
		       COALESCE(group_id, ''), COALESCE(user_id, ''),
		       COALESCE(request_summary, ''), COALESCE(response_summary, ''),
		       duration_ms, COALESCE(max_log_level, '')
		FROM response_events` + where + `
		ORDER BY started_at_ms DESC, id DESC
		LIMIT ?`
	args = append(args, filters.Limit+1)
	rows, err := r.db.QueryContext(ctx, query, args...)
	if err != nil {
		return EventPage{}, fmt.Errorf("list events: %w", err)
	}
	defer rows.Close()

	items := make([]EventSummary, 0, filters.Limit)
	for rows.Next() {
		var item EventSummary
		var duration sql.NullInt64
		if err := rows.Scan(
			&item.ID,
			&item.startedAtMS,
			&item.Status,
			&item.PluginName,
			&item.ModuleName,
			&item.GroupID,
			&item.UserID,
			&item.RequestSummary,
			&item.ResponseSummary,
			&duration,
			&item.MaxLogLevel,
		); err != nil {
			return EventPage{}, fmt.Errorf("scan event: %w", err)
		}
		if duration.Valid {
			item.DurationMS = &duration.Int64
		}
		item.TimeDisplay = timefmt.FormatMilliseconds(item.startedAtMS)
		items = append(items, item)
	}
	if err := rows.Err(); err != nil {
		return EventPage{}, fmt.Errorf("iterate events: %w", err)
	}

	page := EventPage{Items: items}
	if len(items) > filters.Limit {
		last := items[filters.Limit-1]
		page.Items = items[:filters.Limit]
		page.NextCursor = EncodeCursor(last.startedAtMS, last.ID)
	}
	return page, nil
}

func buildEventWhere(filters EventFilters) (string, []any) {
	conditions := make([]string, 0, 6)
	args := make([]any, 0, 8)
	if filters.Status != "" {
		conditions = append(conditions, "status = ?")
		args = append(args, filters.Status)
	}
	if filters.FromMS != nil {
		conditions = append(conditions, "started_at_ms >= ?")
		args = append(args, *filters.FromMS)
	}
	if filters.ToMS != nil {
		conditions = append(conditions, "started_at_ms <= ?")
		args = append(args, *filters.ToMS)
	}
	if filters.GroupID != "" {
		conditions = append(conditions, "group_id = ?")
		args = append(args, filters.GroupID)
	}
	if filters.Plugin != "" {
		conditions = append(conditions, "plugin_name = ?")
		args = append(args, filters.Plugin)
	}
	if filters.CursorID != 0 {
		conditions = append(
			conditions,
			"(started_at_ms < ? OR (started_at_ms = ? AND id < ?))",
		)
		args = append(
			args,
			filters.CursorMS,
			filters.CursorMS,
			filters.CursorID,
		)
	}
	if len(conditions) == 0 {
		return "", args
	}
	return " WHERE " + strings.Join(conditions, " AND "), args
}

type EventDetail struct {
	TimeDisplay        string `json:"time_display"`
	FinishedDisplay    string `json:"finished_display,omitempty"`
	Status             string `json:"status"`
	PluginName         string `json:"plugin_name,omitempty"`
	ModuleName         string `json:"module_name,omitempty"`
	MatcherType        string `json:"matcher_type,omitempty"`
	MatcherLine        *int64 `json:"matcher_lineno,omitempty"`
	EventName          string `json:"event_name,omitempty"`
	GroupID            string `json:"group_id,omitempty"`
	UserID             string `json:"user_id,omitempty"`
	SourceMessageID    string `json:"source_message_id,omitempty"`
	RunID              string `json:"run_id"`
	DurationMS         *int64 `json:"duration_ms,omitempty"`
	SendCount          int64  `json:"send_count"`
	SendSuccessCount   int64  `json:"send_success_count"`
	SendFailureCount   int64  `json:"send_failure_count"`
	RequestSummary     string `json:"request_summary"`
	ResponseSummary    string `json:"response_summary"`
	ErrorType          string `json:"error_type,omitempty"`
	ErrorMessage       string `json:"error_message,omitempty"`
	HasFullDiagnostics bool   `json:"has_full_diagnostics"`
}

func (r *Repository) GetEvent(
	ctx context.Context,
	id int64,
) (EventDetail, error) {
	var detail EventDetail
	var startedAtMS int64
	var duration, matcherLine, finished sql.NullInt64
	err := r.db.QueryRowContext(
		ctx,
		`SELECT started_at_ms, finished_at_ms, status,
		        COALESCE(plugin_name, ''), COALESCE(module_name, ''),
		        COALESCE(matcher_type, ''), matcher_lineno,
		        COALESCE(event_name, ''), COALESCE(group_id, ''),
		        COALESCE(user_id, ''), COALESCE(source_message_id, ''),
		        run_id, duration_ms,
		        send_count, send_success_count, send_failure_count,
		        COALESCE(request_summary, ''),
		        COALESCE(response_summary, ''),
		        COALESCE(error_type, ''), COALESCE(error_message, ''),
		        has_full_diagnostics = 1
		   FROM response_events WHERE id = ?`,
		id,
	).Scan(
		&startedAtMS,
		&finished,
		&detail.Status,
		&detail.PluginName,
		&detail.ModuleName,
		&detail.MatcherType,
		&matcherLine,
		&detail.EventName,
		&detail.GroupID,
		&detail.UserID,
		&detail.SourceMessageID,
		&detail.RunID,
		&duration,
		&detail.SendCount,
		&detail.SendSuccessCount,
		&detail.SendFailureCount,
		&detail.RequestSummary,
		&detail.ResponseSummary,
		&detail.ErrorType,
		&detail.ErrorMessage,
		&detail.HasFullDiagnostics,
	)
	if err != nil {
		return EventDetail{}, fmt.Errorf("get event: %w", err)
	}
	detail.TimeDisplay = timefmt.FormatMilliseconds(startedAtMS)
	if finished.Valid {
		detail.FinishedDisplay = timefmt.FormatMilliseconds(finished.Int64)
	}
	if duration.Valid {
		detail.DurationMS = &duration.Int64
	}
	if matcherLine.Valid {
		detail.MatcherLine = &matcherLine.Int64
	}
	return detail, nil
}

type Stats struct {
	Total       int64   `json:"total"`
	Success     int64   `json:"success"`
	Failure     int64   `json:"failure"`
	SuccessRate float64 `json:"success_rate"`
}

func (r *Repository) Stats(
	ctx context.Context,
	filters EventFilters,
) (Stats, error) {
	filters.CursorID = 0
	where, args := buildEventWhere(filters)
	var stats Stats
	err := r.db.QueryRowContext(
		ctx,
		`SELECT COUNT(*),
		        COALESCE(SUM(status = 'success'), 0),
		        COALESCE(SUM(status = 'failure'), 0)
		   FROM response_events`+where,
		args...,
	).Scan(&stats.Total, &stats.Success, &stats.Failure)
	if err != nil {
		return Stats{}, fmt.Errorf("query stats: %w", err)
	}
	if stats.Total > 0 {
		stats.SuccessRate = float64(stats.Success) / float64(stats.Total)
	}
	return stats, nil
}

type Filters struct {
	Groups  []string `json:"groups"`
	Plugins []string `json:"plugins"`
}

func (r *Repository) Filters(ctx context.Context) (Filters, error) {
	groups, err := distinctStrings(
		ctx,
		r.db,
		`SELECT DISTINCT group_id FROM response_events
		  WHERE group_id IS NOT NULL AND group_id != ''
		  ORDER BY group_id`,
	)
	if err != nil {
		return Filters{}, fmt.Errorf("list groups: %w", err)
	}
	plugins, err := distinctStrings(
		ctx,
		r.db,
		`SELECT plugin_name FROM response_events
		  WHERE plugin_name IS NOT NULL AND plugin_name != ''
		 UNION
		 SELECT plugin_name FROM diagnostic_logs
		  WHERE plugin_name IS NOT NULL AND plugin_name != ''
		  ORDER BY plugin_name`,
	)
	if err != nil {
		return Filters{}, fmt.Errorf("list plugins: %w", err)
	}
	loggerNames, err := distinctStrings(
		ctx,
		r.db,
		`SELECT DISTINCT logger_name FROM diagnostic_logs
		  WHERE (plugin_name IS NULL OR plugin_name = '')
		    AND logger_name IS NOT NULL AND logger_name != ''
		  ORDER BY logger_name`,
	)
	if err != nil {
		return Filters{}, fmt.Errorf("list diagnostic origins: %w", err)
	}
	pluginSet := make(map[string]struct{}, len(plugins)+len(loggerNames))
	for _, plugin := range plugins {
		pluginSet[plugin] = struct{}{}
	}
	for _, loggerName := range loggerNames {
		if plugin := diagnosticPluginFromLogger(loggerName); plugin != "" {
			pluginSet[plugin] = struct{}{}
		}
	}
	plugins = plugins[:0]
	for plugin := range pluginSet {
		plugins = append(plugins, plugin)
	}
	sort.Strings(plugins)
	return Filters{Groups: groups, Plugins: plugins}, nil
}

func distinctStrings(
	ctx context.Context,
	db *sql.DB,
	query string,
) ([]string, error) {
	rows, err := db.QueryContext(ctx, query)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	values := make([]string, 0)
	for rows.Next() {
		var value string
		if err := rows.Scan(&value); err != nil {
			return nil, err
		}
		values = append(values, value)
	}
	return values, rows.Err()
}

type BotStatus struct {
	BotID  string `json:"bot_id"`
	Online bool   `json:"online"`
}

func (r *Repository) BotStatus(
	ctx context.Context,
	now time.Time,
) ([]BotStatus, error) {
	rows, err := r.db.QueryContext(
		ctx,
		`SELECT bot_id, connected, connection_started_at_ms,
		        last_heartbeat_at_ms, heartbeat_online, heartbeat_good,
		        collector_heartbeat_at_ms
		   FROM bot_status ORDER BY bot_id`,
	)
	if err != nil {
		return nil, fmt.Errorf("query bot status: %w", err)
	}
	defer rows.Close()
	nowMS := now.UnixMilli()
	statuses := make([]BotStatus, 0)
	for rows.Next() {
		var status BotStatus
		var connected int
		var connectionStarted, heartbeatAt sql.NullInt64
		var heartbeatOnline, heartbeatGood sql.NullInt64
		var collectorHeartbeat int64
		if err := rows.Scan(
			&status.BotID,
			&connected,
			&connectionStarted,
			&heartbeatAt,
			&heartbeatOnline,
			&heartbeatGood,
			&collectorHeartbeat,
		); err != nil {
			return nil, fmt.Errorf("scan bot status: %w", err)
		}
		collectorFresh := nowMS-collectorHeartbeat <= 45_000
		heartbeatFresh := heartbeatAt.Valid &&
			nowMS-heartbeatAt.Int64 <= 90_000
		heartbeatGrace := !heartbeatAt.Valid &&
			connectionStarted.Valid &&
			nowMS-connectionStarted.Int64 <= 90_000
		heartbeatHealthy := heartbeatGrace ||
			(heartbeatFresh &&
				heartbeatOnline.Valid && heartbeatOnline.Int64 == 1 &&
				heartbeatGood.Valid && heartbeatGood.Int64 == 1)
		status.Online = connected == 1 &&
			collectorFresh &&
			heartbeatHealthy
		statuses = append(statuses, status)
	}
	return statuses, rows.Err()
}

type DiagnosticFilters struct {
	Level    string
	Plugin   string
	FromMS   *int64
	ToMS     *int64
	CursorMS int64
	CursorID int64
	Limit    int
}

type DiagnosticSummary struct {
	ID             int64  `json:"id"`
	TimeDisplay    string `json:"time_display"`
	Level          string `json:"level"`
	LoggerName     string `json:"logger_name,omitempty"`
	ModuleName     string `json:"module_name,omitempty"`
	PluginName     string `json:"plugin_name,omitempty"`
	MessageSummary string `json:"message_summary"`
	createdAtMS    int64
}

type DiagnosticPage struct {
	Items      []DiagnosticSummary `json:"items"`
	NextCursor string              `json:"next_cursor,omitempty"`
}

func diagnosticPluginFromLogger(loggerName string) string {
	parts := strings.Split(strings.TrimSpace(loggerName), ".")
	if len(parts) == 0 {
		return ""
	}
	if parts[0] == "plugins" && len(parts) > 1 {
		return parts[1]
	}
	return parts[0]
}

func (r *Repository) ListDiagnostics(
	ctx context.Context,
	filters DiagnosticFilters,
) (DiagnosticPage, error) {
	conditions := make([]string, 0, 5)
	args := make([]any, 0, 7)
	if filters.Level != "" {
		conditions = append(conditions, "level = ?")
		args = append(args, filters.Level)
	}
	if filters.Plugin != "" {
		conditions = append(
			conditions,
			`(plugin_name = ? OR (
				(plugin_name IS NULL OR plugin_name = '') AND (
					logger_name = ?
					OR substr(logger_name, 1, length(?) + 1) = ? || '.'
					OR logger_name = 'plugins.' || ?
					OR substr(logger_name, 1, length(?) + 9) =
						'plugins.' || ? || '.'
				)
			))`,
		)
		args = append(
			args,
			filters.Plugin,
			filters.Plugin,
			filters.Plugin,
			filters.Plugin,
			filters.Plugin,
			filters.Plugin,
			filters.Plugin,
		)
	}
	if filters.FromMS != nil {
		conditions = append(conditions, "created_at_ms >= ?")
		args = append(args, *filters.FromMS)
	}
	if filters.ToMS != nil {
		conditions = append(conditions, "created_at_ms <= ?")
		args = append(args, *filters.ToMS)
	}
	if filters.CursorID != 0 {
		conditions = append(
			conditions,
			"(created_at_ms < ? OR (created_at_ms = ? AND id < ?))",
		)
		args = append(
			args,
			filters.CursorMS,
			filters.CursorMS,
			filters.CursorID,
		)
	}
	where := ""
	if len(conditions) > 0 {
		where = " WHERE " + strings.Join(conditions, " AND ")
	}
	args = append(args, filters.Limit+1)
	rows, err := r.db.QueryContext(
		ctx,
		`SELECT id, created_at_ms, level,
		        COALESCE(logger_name, ''), COALESCE(module_name, ''),
		        COALESCE(plugin_name, ''), message_summary
		   FROM diagnostic_logs`+where+`
		  ORDER BY created_at_ms DESC, id DESC LIMIT ?`,
		args...,
	)
	if err != nil {
		return DiagnosticPage{}, fmt.Errorf("list diagnostics: %w", err)
	}
	defer rows.Close()

	items := make([]DiagnosticSummary, 0, filters.Limit)
	for rows.Next() {
		var item DiagnosticSummary
		if err := rows.Scan(
			&item.ID,
			&item.createdAtMS,
			&item.Level,
			&item.LoggerName,
			&item.ModuleName,
			&item.PluginName,
			&item.MessageSummary,
		); err != nil {
			return DiagnosticPage{}, fmt.Errorf("scan diagnostic: %w", err)
		}
		if item.PluginName == "" {
			item.PluginName = diagnosticPluginFromLogger(item.LoggerName)
		}
		item.TimeDisplay = timefmt.FormatMilliseconds(item.createdAtMS)
		items = append(items, item)
	}
	if err := rows.Err(); err != nil {
		return DiagnosticPage{}, fmt.Errorf("iterate diagnostics: %w", err)
	}
	page := DiagnosticPage{Items: items}
	if len(items) > filters.Limit {
		last := items[filters.Limit-1]
		page.Items = items[:filters.Limit]
		page.NextCursor = EncodeCursor(last.createdAtMS, last.ID)
	}
	return page, nil
}

type RawPayload struct {
	Compressed   []byte
	SHA256       string
	TimeDisplay  string
	ContentType  string
	FilenameStem string
}

// EventRawColumns maps each downloadable part of an event to its blob and
// hash columns; the HTTP layer registers one route per key.
var EventRawColumns = map[string][2]string{
	"input":  {"request_raw_gzip", "request_raw_sha256"},
	"output": {"response_raw_gzip", "response_raw_sha256"},
	"logs":   {"logs_raw_gzip", "logs_raw_sha256"},
}

func (r *Repository) EventRaw(
	ctx context.Context,
	id int64,
	part string,
) (RawPayload, error) {
	blobColumn, hashColumn := EventRawColumns[part][0], EventRawColumns[part][1]
	var payload RawPayload
	var startedAtMS int64
	err := r.db.QueryRowContext(
		ctx,
		`SELECT `+blobColumn+`, `+hashColumn+`, started_at_ms
		   FROM response_events
		  WHERE id = ? AND `+blobColumn+` IS NOT NULL
		    AND `+hashColumn+` IS NOT NULL`,
		id,
	).Scan(&payload.Compressed, &payload.SHA256, &startedAtMS)
	if err != nil {
		return RawPayload{}, err
	}
	payload.TimeDisplay = timefmt.FormatMilliseconds(startedAtMS)
	payload.ContentType = "application/json; charset=utf-8"
	payload.FilenameStem = fmt.Sprintf("event-%d-%s", id, part)
	return payload, nil
}

func (r *Repository) DiagnosticRaw(
	ctx context.Context,
	id int64,
) (RawPayload, error) {
	var payload RawPayload
	var createdAtMS int64
	err := r.db.QueryRowContext(
		ctx,
		`SELECT full_log_gzip, raw_sha256, created_at_ms
		   FROM diagnostic_logs WHERE id = ?`,
		id,
	).Scan(&payload.Compressed, &payload.SHA256, &createdAtMS)
	if err != nil {
		return RawPayload{}, err
	}
	payload.TimeDisplay = timefmt.FormatMilliseconds(createdAtMS)
	payload.ContentType = "text/plain; charset=utf-8"
	payload.FilenameStem = fmt.Sprintf("diagnostic-%d", id)
	return payload, nil
}
