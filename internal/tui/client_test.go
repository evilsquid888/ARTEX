package tui

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

func TestLoginAndTaskRouting(t *testing.T) {
	s := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/api/auth/login" {
			raw, _ := io.ReadAll(r.Body)
			if !strings.Contains(string(raw), `"username":"ARTEX"`) {
				t.Error(string(raw))
			}
			fmt.Fprint(w, `{"token":"session"}`)
			return
		}
		if r.Header.Get("Authorization") != "Bearer session" {
			t.Error("missing session")
		}
		if r.URL.Path != "/api/chat" || r.URL.Query().Get("task") != "42 & lab" {
			t.Error("incorrect task routing", r.URL)
		}
		fmt.Fprint(w, `{"ok":true}`)
	}))
	defer s.Close()
	c, _ := NewClient(s.URL, "")
	if err := c.Login(context.Background(), "password"); err != nil {
		t.Fatal(err)
	}
	if err := c.JSON(context.Background(), "POST", "/api/chat"+taskQuery("42 & lab"), map[string]string{"message": "status?"}, nil); err != nil {
		t.Fatal(err)
	}
}
func TestSSEFrames(t *testing.T) {
	var seqs []int64
	err := readSSE(strings.NewReader(": ping\r\n\r\nid: 8\r\ndata: {\"seq\":8,\r\ndata: \"summary\":\"hello\"}\r\n\r\ndata: {\"seq\":9}\n\n"), func(a Activity) error { seqs = append(seqs, a.Seq); return nil })
	if err != io.EOF || fmt.Sprint(seqs) != "[8 9]" {
		t.Fatalf("seqs=%v err=%v", seqs, err)
	}
	if err := readSSE(strings.NewReader("data: broken\n\n"), func(Activity) error { return nil }); err == nil {
		t.Fatal("malformed frame accepted")
	}
}
func TestStreamReconnectDedupAndCancel(t *testing.T) {
	var calls atomic.Int32
	s := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		if calls.Add(1) == 1 {
			if r.URL.Query().Get("since") != "7" {
				t.Error("initial cursor")
			}
			fmt.Fprint(w, "data: {\"seq\":8}\n\n")
			return
		}
		if r.URL.Query().Get("since") != "8" {
			t.Error("resume cursor", r.URL)
		}
		fmt.Fprint(w, "data: {\"seq\":8}\n\ndata: {\"seq\":9}\n\n")
		w.(http.Flusher).Flush()
		<-r.Context().Done()
	}))
	defer s.Close()
	c, _ := NewClient(s.URL, "token")
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	var got []int64
	c.Stream(ctx, "42", 7, func(a Activity) error {
		got = append(got, a.Seq)
		if a.Seq == 9 {
			cancel()
		}
		return nil
	}, func(string) {})
	if fmt.Sprint(got) != "[8 9]" {
		t.Fatal(got)
	}
}
func TestNoCredentialRedirect(t *testing.T) {
	target := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) { t.Error("followed redirect") }))
	defer target.Close()
	s := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { http.Redirect(w, r, target.URL, 302) }))
	defer s.Close()
	c, _ := NewClient(s.URL, "secret")
	if err := c.JSON(context.Background(), "GET", "/api/tasks", nil, nil); err == nil {
		t.Fatal("redirect accepted")
	}
}
