// Package autochat polls the autochat process for its current state. History
// lives in the shared database and is read by the query package instead, so
// it stays available while autochat is down.
package autochat

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"os"
	"sync"
	"syscall"
	"time"
)

const (
	overviewInterval = 5 * time.Second
	activityInterval = 60 * time.Second
	requestTimeout   = 4 * time.Second
	responseLimit    = 8 << 20
	apiPrefix        = "/nyaturingtest/api"
)

type Client struct {
	baseURL string
	token   string
	http    *http.Client

	mu         sync.RWMutex
	reachable  bool
	lastOKMS   int64
	reason     string
	overview   json.RawMessage
	activity   json.RawMessage
	activityMS int64
}

func NewClient(baseURL, token string) *Client {
	return &Client{
		baseURL: baseURL,
		token:   token,
		http:    &http.Client{Timeout: requestTimeout},
		reason:  "正在连接",
	}
}

// Status is what the sidebar and the page poll; the JSON from autochat is
// passed through untouched, so this package does not know its shape. The last
// overview stays after a failure so the page can show it greyed out.
type Status struct {
	Reachable  bool            `json:"reachable"`
	LastOKMS   int64           `json:"last_ok_ms,omitempty"`
	Reason     string          `json:"reason,omitempty"`
	Overview   json.RawMessage `json:"overview"`
	ActivityMS int64           `json:"activity_ms,omitempty"`
}

func (c *Client) Status() Status {
	c.mu.RLock()
	defer c.mu.RUnlock()
	return Status{
		Reachable:  c.reachable,
		LastOKMS:   c.lastOKMS,
		Reason:     c.reason,
		Overview:   c.overview,
		ActivityMS: c.activityMS,
	}
}

// Activity returns the last successful activity snapshot, or null before the
// first one.
func (c *Client) Activity() json.RawMessage {
	c.mu.RLock()
	defer c.mu.RUnlock()
	return c.activity
}

func (c *Client) Start(ctx context.Context) {
	overviewTicker := time.NewTicker(overviewInterval)
	activityTicker := time.NewTicker(activityInterval)
	defer overviewTicker.Stop()
	defer activityTicker.Stop()

	c.pollOverview(ctx)
	c.pollActivity(ctx)
	for {
		select {
		case <-ctx.Done():
			return
		case <-overviewTicker.C:
			recovered := c.pollOverview(ctx)
			// Coming back from a restart should not leave yesterday's timeline
			// on screen for up to a minute.
			if recovered {
				c.pollActivity(ctx)
			}
		case <-activityTicker.C:
			c.pollActivity(ctx)
		}
	}
}

// pollOverview reports whether autochat just became reachable. Only those
// transitions are logged: autochat being down must not write a line per poll.
func (c *Client) pollOverview(ctx context.Context) bool {
	status, body, err := c.Forward(ctx, "/overview")
	if err == nil && status != http.StatusOK {
		err = errors.New(httpReason(status))
	}

	c.mu.Lock()
	defer c.mu.Unlock()
	if err != nil {
		if c.reachable {
			slog.Warn("autochat became unreachable", "reason", err.Error())
		}
		c.reachable = false
		c.reason = err.Error()
		return false
	}
	recovered := !c.reachable
	if recovered {
		slog.Info("autochat connected")
	}
	c.reachable = true
	c.lastOKMS = time.Now().UnixMilli()
	c.reason = ""
	c.overview = body
	return recovered
}

func (c *Client) pollActivity(ctx context.Context) {
	status, body, err := c.Forward(ctx, "/activity")
	if err != nil || status != http.StatusOK {
		return
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	c.activity = body
	c.activityMS = time.Now().UnixMilli()
}

// Forward sends one GET to autochat. The error text is shown on the page.
func (c *Client) Forward(
	ctx context.Context,
	path string,
) (int, []byte, error) {
	request, err := http.NewRequestWithContext(
		ctx,
		http.MethodGet,
		c.baseURL+apiPrefix+path,
		nil,
	)
	if err != nil {
		return 0, nil, fmt.Errorf("build autochat request: %w", err)
	}
	request.Header.Set("Authorization", "Bearer "+c.token)
	response, err := c.http.Do(request)
	if err != nil {
		return 0, nil, errors.New(networkReason(err))
	}
	defer response.Body.Close()
	body, err := io.ReadAll(io.LimitReader(response.Body, responseLimit))
	if err != nil {
		return 0, nil, errors.New(networkReason(err))
	}
	return response.StatusCode, body, nil
}

func networkReason(err error) string {
	switch {
	case errors.Is(err, syscall.ECONNREFUSED):
		return "连接被拒绝，进程没有运行"
	case errors.Is(err, context.DeadlineExceeded), errors.Is(err, os.ErrDeadlineExceeded):
		return "请求超时，进程可能卡住了"
	default:
		return err.Error()
	}
}

func httpReason(status int) string {
	switch status {
	case http.StatusUnauthorized:
		return "token 不对，检查两边的配置"
	case http.StatusNotFound:
		return "接口不存在，autochat 没有配置 console_token 或版本太旧"
	default:
		return fmt.Sprintf("autochat 返回 %d", status)
	}
}
