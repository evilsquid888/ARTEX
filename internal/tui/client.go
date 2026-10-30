// Package tui implements ARTEX's terminal client without coupling to server state.
package tui

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"
)

type Client struct {
	Base, Token string
	HTTP        *http.Client
}
type Task struct {
	ID, Name, Description, Goal, Status string
	InFlight                            int `json:"in_flight"`
	GoalsMet                            int `json:"goals_met"`
	GoalsTotal                          int `json:"goals_total"`
}
type Activity struct {
	Seq                                     int64  `json:"seq"`
	IntentID                                string `json:"intent_id"`
	Worker, TS, Kind, Tool, Summary, Detail string
	IsError                                 bool `json:"is_error"`
}
type Worker struct {
	ID, State, Payload string
	Inherited          bool
}

func NewClient(base, token string) (*Client, error) {
	u, err := url.Parse(base)
	if err != nil || u.Host == "" || (u.Scheme != "http" && u.Scheme != "https") || u.User != nil || u.RawQuery != "" || u.Fragment != "" {
		return nil, fmt.Errorf("server must be an http(s) URL without credentials, query, or fragment")
	}
	return &Client{strings.TrimRight(base, "/"), token, &http.Client{Transport: &http.Transport{Proxy: http.ProxyFromEnvironment, ResponseHeaderTimeout: 30 * time.Second, TLSHandshakeTimeout: 10 * time.Second}, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}}, nil
}
func (c *Client) request(ctx context.Context, method, path string, body any) (*http.Response, error) {
	var b io.Reader
	if body != nil {
		raw, err := json.Marshal(body)
		if err != nil {
			return nil, err
		}
		b = bytes.NewReader(raw)
	}
	req, err := http.NewRequestWithContext(ctx, method, c.Base+path, b)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Content-Type", "application/json")
	if c.Token != "" {
		req.Header.Set("Authorization", "Bearer "+c.Token)
	}
	res, err := c.HTTP.Do(req)
	if err != nil {
		return nil, err
	}
	if res.StatusCode < 200 || res.StatusCode >= 300 {
		defer res.Body.Close()
		raw, _ := io.ReadAll(io.LimitReader(res.Body, 4096))
		return nil, fmt.Errorf("HTTP %d: %s", res.StatusCode, strings.TrimSpace(string(raw)))
	}
	return res, nil
}
func (c *Client) JSON(ctx context.Context, method, path string, body, out any) error {
	ctx, cancel := context.WithTimeout(ctx, 15*time.Second)
	defer cancel()
	res, err := c.request(ctx, method, path, body)
	if err != nil {
		return err
	}
	defer res.Body.Close()
	if out == nil {
		_, err = io.Copy(io.Discard, io.LimitReader(res.Body, 1<<20))
		return err
	}
	return json.NewDecoder(io.LimitReader(res.Body, 8<<20)).Decode(out)
}
func (c *Client) Login(ctx context.Context, password string) error {
	var out struct{ Token string }
	if err := c.JSON(ctx, "POST", "/api/auth/login", map[string]string{"username": "ARTEX", "password": password}, &out); err != nil {
		return err
	}
	if out.Token == "" {
		return fmt.Errorf("login returned no token")
	}
	c.Token = out.Token
	return nil
}
func taskQuery(id string) string { return "?task=" + url.QueryEscape(id) }

// Stream resumes from the last delivered sequence. Cursors advance only after
// delivery, so cancellation/backpressure never acknowledges an unseen event.
func (c *Client) Stream(ctx context.Context, task string, since int64, emit func(Activity) error, status func(string)) {
	delay := time.Second
	for ctx.Err() == nil {
		status("connecting")
		res, err := c.request(ctx, "GET", "/api/exploration/activity/stream"+taskQuery(task)+fmt.Sprintf("&since=%d", since), nil)
		if err == nil {
			status("live")
			err = readSSE(res.Body, func(a Activity) error {
				if a.Seq <= since {
					return nil
				}
				if err := emit(a); err != nil {
					return err
				}
				since = a.Seq
				delay = time.Second
				return nil
			})
			res.Body.Close()
		}
		if ctx.Err() != nil {
			return
		}
		status(fmt.Sprintf("reconnecting: %v", err))
		timer := time.NewTimer(delay)
		select {
		case <-ctx.Done():
			timer.Stop()
			return
		case <-timer.C:
		}
		if delay < 16*time.Second {
			delay *= 2
		}
	}
}
func readSSE(r io.Reader, emit func(Activity) error) error {
	scanner := bufio.NewScanner(r)
	scanner.Buffer(make([]byte, 4096), 4<<20)
	var data []string
	dispatch := func() error {
		if len(data) == 0 {
			return nil
		}
		var a Activity
		err := json.Unmarshal([]byte(strings.Join(data, "\n")), &a)
		data = nil
		if err != nil {
			return fmt.Errorf("invalid activity frame: %w", err)
		}
		return emit(a)
	}
	for scanner.Scan() {
		line := scanner.Text()
		if line == "" {
			if err := dispatch(); err != nil {
				return err
			}
		} else if strings.HasPrefix(line, "data:") {
			data = append(data, strings.TrimPrefix(strings.TrimPrefix(line, "data:"), " "))
		}
	}
	if err := scanner.Err(); err != nil {
		return err
	}
	return io.EOF
}
