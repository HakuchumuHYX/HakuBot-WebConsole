package query

import (
	"context"
	"database/sql"
	"fmt"
	"hash/fnv"
	"regexp"
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
	Total   int64 `json:"total"`
	Success int64 `json:"success"`
	Failure int64 `json:"failure"`
	// Successful responses that still logged a WARNING.
	Warning int64 `json:"warning"`
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
		        COALESCE(SUM(status = 'failure'), 0),
		        COALESCE(SUM(status = 'success' AND max_log_level = 'WARNING'), 0)
		   FROM response_events`+where,
		args...,
	).Scan(&stats.Total, &stats.Success, &stats.Failure, &stats.Warning)
	if err != nil {
		return Stats{}, fmt.Errorf("query stats: %w", err)
	}
	return stats, nil
}

type ActivityHour struct {
	StartMS int64 `json:"start_ms"`
	// Success excludes the responses counted in Warning.
	Success int64 `json:"success"`
	Warning int64 `json:"warning"`
	Failure int64 `json:"failure"`
}

type Overview struct {
	Stats       Stats          `json:"stats"`
	Hours       []ActivityHour `json:"hours"`
	Diagnostics int64          `json:"diagnostics"`
}

// Overview counts the sliding last 24 hours exactly, while the 24 hourly
// buckets align to whole hours, so the oldest bucket can reach a little
// further back than the totals.
func (r *Repository) Overview(
	ctx context.Context,
	now time.Time,
) (Overview, error) {
	const hourMS = int64(time.Hour / time.Millisecond)
	fromMS := now.Add(-24 * time.Hour).UnixMilli()
	stats, err := r.Stats(ctx, EventFilters{FromMS: &fromMS})
	if err != nil {
		return Overview{}, err
	}

	firstHour := now.UnixMilli()/hourMS - 23
	hours := make([]ActivityHour, 24)
	for i := range hours {
		hours[i].StartMS = (firstHour + int64(i)) * hourMS
	}
	rows, err := r.db.QueryContext(
		ctx,
		`SELECT started_at_ms / ?,
		        SUM(status = 'success' AND COALESCE(max_log_level, '') != 'WARNING'),
		        SUM(status = 'success' AND COALESCE(max_log_level, '') = 'WARNING'),
		        SUM(status = 'failure')
		   FROM response_events
		  WHERE started_at_ms >= ?
		  GROUP BY 1`,
		hourMS,
		firstHour*hourMS,
	)
	if err != nil {
		return Overview{}, fmt.Errorf("query activity: %w", err)
	}
	defer rows.Close()
	for rows.Next() {
		var hour int64
		var bucket ActivityHour
		if err := rows.Scan(
			&hour,
			&bucket.Success,
			&bucket.Warning,
			&bucket.Failure,
		); err != nil {
			return Overview{}, fmt.Errorf("scan activity: %w", err)
		}
		// Events stamped slightly in the future would fall past the last bucket.
		index := hour - firstHour
		if index < 0 || index >= int64(len(hours)) {
			continue
		}
		bucket.StartMS = hours[index].StartMS
		hours[index] = bucket
	}
	if err := rows.Err(); err != nil {
		return Overview{}, fmt.Errorf("iterate activity: %w", err)
	}

	var diagnostics int64
	if err := r.db.QueryRowContext(
		ctx,
		`SELECT COUNT(*) FROM diagnostic_logs WHERE created_at_ms >= ?`,
		fromMS,
	).Scan(&diagnostics); err != nil {
		return Overview{}, fmt.Errorf("count diagnostics: %w", err)
	}
	return Overview{Stats: stats, Hours: hours, Diagnostics: diagnostics}, nil
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
	Level  string
	Plugin string
	FromMS *int64
	ToMS   *int64
}

type DiagnosticOccurrence struct {
	ID          int64  `json:"id"`
	TimeDisplay string `json:"time_display"`
}

type DiagnosticGroup struct {
	Key          string                 `json:"key"`
	Level        string                 `json:"level"`
	PluginName   string                 `json:"plugin_name,omitempty"`
	LoggerName   string                 `json:"logger_name,omitempty"`
	Message      string                 `json:"message"`
	Count        int                    `json:"count"`
	LastMS       int64                  `json:"last_ms"`
	LastDisplay  string                 `json:"last_display"`
	FirstDisplay string                 `json:"first_display"`
	Occurrences  []DiagnosticOccurrence `json:"occurrences"`
}

type DiagnosticGroups struct {
	Total     int               `json:"total"`
	Groups    []DiagnosticGroup `json:"groups"`
	Truncated bool              `json:"truncated"`
}

// The page refreshes every 3 seconds, so the payload stays bounded: at most
// this many groups, latest occurrences per group, and runes per message.
const (
	maxDiagnosticGroups      = 100
	maxDiagnosticOccurrences = 20
	maxDiagnosticMessage     = 300
)

// Messages differing only in numbers (event ids, attempts, UIDs, URLs) are
// the same problem; collapsing digit runs is enough to group them.
var digitRun = regexp.MustCompile(`\d+`)

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

func (r *Repository) DiagnosticGroups(
	ctx context.Context,
	filters DiagnosticFilters,
) (DiagnosticGroups, error) {
	conditions := make([]string, 0, 4)
	args := make([]any, 0, 10)
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
	where := ""
	if len(conditions) > 0 {
		where = " WHERE " + strings.Join(conditions, " AND ")
	}
	rows, err := r.db.QueryContext(
		ctx,
		`SELECT id, created_at_ms, level,
		        COALESCE(logger_name, ''), COALESCE(plugin_name, ''),
		        message_summary
		   FROM diagnostic_logs`+where+`
		  ORDER BY created_at_ms DESC, id DESC`,
		args...,
	)
	if err != nil {
		return DiagnosticGroups{}, fmt.Errorf("list diagnostics: %w", err)
	}
	defer rows.Close()

	// Rows arrive newest first, so groups come out ordered by last occurrence
	// and each group's first row supplies its sample message.
	result := DiagnosticGroups{Groups: make([]DiagnosticGroup, 0)}
	index := make(map[string]int)
	for rows.Next() {
		var id, createdAtMS int64
		var level, loggerName, pluginName, message string
		if err := rows.Scan(
			&id,
			&createdAtMS,
			&level,
			&loggerName,
			&pluginName,
			&message,
		); err != nil {
			return DiagnosticGroups{}, fmt.Errorf("scan diagnostic: %w", err)
		}
		if pluginName == "" {
			pluginName = diagnosticPluginFromLogger(loggerName)
		}
		result.Total++
		display := timefmt.FormatMilliseconds(createdAtMS)
		key := level + "\x00" + pluginName + "\x00" +
			digitRun.ReplaceAllString(message, "#")
		position, found := index[key]
		if !found {
			if len(result.Groups) == maxDiagnosticGroups {
				result.Truncated = true
				continue
			}
			hash := fnv.New64a()
			hash.Write([]byte(key))
			if runes := []rune(message); len(runes) > maxDiagnosticMessage {
				message = string(runes[:maxDiagnosticMessage]) + "…"
			}
			position = len(result.Groups)
			index[key] = position
			result.Groups = append(result.Groups, DiagnosticGroup{
				Key:         fmt.Sprintf("%016x", hash.Sum64()),
				Level:       level,
				PluginName:  pluginName,
				LoggerName:  loggerName,
				Message:     message,
				LastMS:      createdAtMS,
				LastDisplay: display,
			})
		}
		group := &result.Groups[position]
		group.Count++
		group.FirstDisplay = display
		if len(group.Occurrences) < maxDiagnosticOccurrences {
			group.Occurrences = append(
				group.Occurrences,
				DiagnosticOccurrence{ID: id, TimeDisplay: display},
			)
		}
	}
	if err := rows.Err(); err != nil {
		return DiagnosticGroups{}, fmt.Errorf("iterate diagnostics: %w", err)
	}
	return result, nil
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
