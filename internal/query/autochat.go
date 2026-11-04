package query

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"time"
)

const (
	autochatLookPage    = 20
	autochatRecentError = 20
	autochatRuns        = 14
	hourMS              = int64(time.Hour / time.Millisecond)
)

// LLMHour is one hour of one endpoint; Index counts from the start of the 24
// hour window, so hours with no calls are simply absent.
type LLMHour struct {
	Index        int     `json:"index"`
	OK           int64   `json:"ok"`
	Failed       int64   `json:"failed"`
	AvgLatencyMS float64 `json:"avg_latency_ms"`
}

type LLMEndpoint struct {
	Endpoint string `json:"endpoint"`
	Model    string `json:"model"`
	// Totals for the last hour.
	RecentOK     int64     `json:"recent_ok"`
	RecentFailed int64     `json:"recent_failed"`
	RecentAvgMS  float64   `json:"recent_avg_ms"`
	Hours        []LLMHour `json:"hours"`
}

type LLMError struct {
	CreatedAtMS int64  `json:"created_at_ms"`
	Endpoint    string `json:"endpoint"`
	Model       string `json:"model"`
	SessionID   string `json:"session_id,omitempty"`
	ErrorType   string `json:"error_type,omitempty"`
	LatencyMS   int64  `json:"latency_ms"`
}

type AutochatHistory struct {
	FromMS       int64         `json:"from_ms"`
	Endpoints    []LLMEndpoint `json:"endpoints"`
	RecentErrors []LLMError    `json:"recent_errors"`
	Warnings24h  int64         `json:"warnings_24h"`
}

// AutochatHistory reads what autochat wrote, so it works while autochat is down.
func (r *Repository) AutochatHistory(
	ctx context.Context,
	now time.Time,
) (AutochatHistory, error) {
	nowMS := now.UnixMilli()
	// Whole hours, so a bar does not change meaning between refreshes.
	fromMS := (nowMS/hourMS - 23) * hourMS
	recentFromMS := nowMS - hourMS
	history := AutochatHistory{
		FromMS:       fromMS,
		Endpoints:    []LLMEndpoint{},
		RecentErrors: []LLMError{},
	}

	rows, err := r.db.QueryContext(ctx, `
		SELECT endpoint, (created_at_ms - ?) / ?, MAX(model),
		       SUM(ok), SUM(1 - ok), AVG(latency_ms),
		       COALESCE(SUM(CASE WHEN created_at_ms >= ? THEN ok END), 0),
		       COALESCE(SUM(CASE WHEN created_at_ms >= ? THEN 1 - ok END), 0),
		       COALESCE(SUM(CASE WHEN created_at_ms >= ? THEN latency_ms END), 0)
		FROM autochat_llm_calls
		WHERE created_at_ms >= ?
		GROUP BY endpoint, 2
		ORDER BY endpoint, 2`,
		fromMS, hourMS, recentFromMS, recentFromMS, recentFromMS, fromMS,
	)
	if err != nil {
		return AutochatHistory{}, fmt.Errorf("query llm hours: %w", err)
	}
	defer rows.Close()
	byEndpoint := map[string]int{}
	recentLatency := map[string]int64{}
	for rows.Next() {
		var endpoint, model string
		var hour LLMHour
		var recentOK, recentFailed, recentLatencySum int64
		if err := rows.Scan(
			&endpoint, &hour.Index, &model,
			&hour.OK, &hour.Failed, &hour.AvgLatencyMS,
			&recentOK, &recentFailed, &recentLatencySum,
		); err != nil {
			return AutochatHistory{}, fmt.Errorf("scan llm hour: %w", err)
		}
		index, seen := byEndpoint[endpoint]
		if !seen {
			index = len(history.Endpoints)
			byEndpoint[endpoint] = index
			history.Endpoints = append(
				history.Endpoints,
				LLMEndpoint{Endpoint: endpoint, Hours: []LLMHour{}},
			)
		}
		item := &history.Endpoints[index]
		// Rows are ordered by hour, so this ends on the latest model name.
		item.Model = model
		item.Hours = append(item.Hours, hour)
		item.RecentOK += recentOK
		item.RecentFailed += recentFailed
		recentLatency[endpoint] += recentLatencySum
	}
	if err := rows.Err(); err != nil {
		return AutochatHistory{}, fmt.Errorf("iterate llm hours: %w", err)
	}
	for index := range history.Endpoints {
		item := &history.Endpoints[index]
		if calls := item.RecentOK + item.RecentFailed; calls > 0 {
			item.RecentAvgMS = float64(recentLatency[item.Endpoint]) / float64(calls)
		}
	}

	errorRows, err := r.db.QueryContext(ctx, `
		SELECT created_at_ms, endpoint, model, COALESCE(session_id, ''),
		       COALESCE(error_type, ''), latency_ms
		FROM autochat_llm_calls
		WHERE ok = 0
		ORDER BY created_at_ms DESC, id DESC
		LIMIT ?`,
		autochatRecentError,
	)
	if err != nil {
		return AutochatHistory{}, fmt.Errorf("query llm errors: %w", err)
	}
	defer errorRows.Close()
	for errorRows.Next() {
		var item LLMError
		if err := errorRows.Scan(
			&item.CreatedAtMS, &item.Endpoint, &item.Model,
			&item.SessionID, &item.ErrorType, &item.LatencyMS,
		); err != nil {
			return AutochatHistory{}, fmt.Errorf("scan llm error: %w", err)
		}
		history.RecentErrors = append(history.RecentErrors, item)
	}
	if err := errorRows.Err(); err != nil {
		return AutochatHistory{}, fmt.Errorf("iterate llm errors: %w", err)
	}

	if err := r.db.QueryRowContext(ctx, `
		SELECT COUNT(*) FROM diagnostic_logs
		WHERE created_at_ms >= ?
		  AND (plugin_name = 'nyaturingtest' OR plugin_name LIKE 'autochat/%')`,
		nowMS-24*hourMS,
	).Scan(&history.Warnings24h); err != nil {
		return AutochatHistory{}, fmt.Errorf("count autochat warnings: %w", err)
	}
	return history, nil
}

type AutochatLook struct {
	ID          int64    `json:"id"`
	CreatedAtMS int64    `json:"created_at_ms"`
	Trigger     string   `json:"trigger"`
	Unread      int64    `json:"unread"`
	Willing     *float64 `json:"willing"`
	Threshold   float64  `json:"threshold"`
	SelfShare   float64  `json:"self_share"`
	Muted       bool     `json:"muted"`
	Speak       bool     `json:"speak"`
	Focus       string   `json:"focus,omitempty"`
	React       string   `json:"react,omitempty"`
	Poke        string   `json:"poke,omitempty"`
}

type AutochatLookPage struct {
	Items      []AutochatLook `json:"items"`
	NextBefore int64          `json:"next_before,omitempty"`
}

// AutochatLooks returns a group's latest decisions; before pages by row id,
// which grows with time.
func (r *Repository) AutochatLooks(
	ctx context.Context,
	groupID string,
	before int64,
) (AutochatLookPage, error) {
	where := "WHERE group_id = ?"
	args := []any{groupID}
	if before > 0 {
		where += " AND id < ?"
		args = append(args, before)
	}
	rows, err := r.db.QueryContext(ctx, `
		SELECT id, created_at_ms, trigger, unread, willing, threshold, self_share,
		       muted, speak, COALESCE(focus, ''), COALESCE(react, ''), COALESCE(poke, '')
		FROM autochat_looks `+where+`
		ORDER BY id DESC
		LIMIT ?`,
		append(args, autochatLookPage+1)...,
	)
	if err != nil {
		return AutochatLookPage{}, fmt.Errorf("query looks: %w", err)
	}
	defer rows.Close()
	page := AutochatLookPage{Items: []AutochatLook{}}
	for rows.Next() {
		var item AutochatLook
		var willing sql.NullFloat64
		if err := rows.Scan(
			&item.ID, &item.CreatedAtMS, &item.Trigger, &item.Unread, &willing,
			&item.Threshold, &item.SelfShare, &item.Muted, &item.Speak,
			&item.Focus, &item.React, &item.Poke,
		); err != nil {
			return AutochatLookPage{}, fmt.Errorf("scan look: %w", err)
		}
		if willing.Valid {
			item.Willing = &willing.Float64
		}
		page.Items = append(page.Items, item)
	}
	if err := rows.Err(); err != nil {
		return AutochatLookPage{}, fmt.Errorf("iterate looks: %w", err)
	}
	if len(page.Items) > autochatLookPage {
		page.Items = page.Items[:autochatLookPage]
		page.NextBefore = page.Items[autochatLookPage-1].ID
	}
	return page, nil
}

type AutochatRun struct {
	ID           int64           `json:"id"`
	StartedAtMS  int64           `json:"started_at_ms"`
	FinishedAtMS int64           `json:"finished_at_ms"`
	Status       string          `json:"status"`
	Detail       json.RawMessage `json:"detail"`
}

func (r *Repository) AutochatMaintenance(
	ctx context.Context,
) ([]AutochatRun, error) {
	rows, err := r.db.QueryContext(ctx, `
		SELECT id, started_at_ms, finished_at_ms, status, detail_json
		FROM autochat_maintenance_runs
		ORDER BY started_at_ms DESC, id DESC
		LIMIT ?`,
		autochatRuns,
	)
	if err != nil {
		return nil, fmt.Errorf("query maintenance runs: %w", err)
	}
	defer rows.Close()
	runs := []AutochatRun{}
	for rows.Next() {
		var run AutochatRun
		var detail string
		if err := rows.Scan(
			&run.ID, &run.StartedAtMS, &run.FinishedAtMS, &run.Status, &detail,
		); err != nil {
			return nil, fmt.Errorf("scan maintenance run: %w", err)
		}
		run.Detail = json.RawMessage(detail)
		runs = append(runs, run)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate maintenance runs: %w", err)
	}
	return runs, nil
}
