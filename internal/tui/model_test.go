package tui

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	tea "charm.land/bubbletea/v2"
)

func key(r rune) tea.KeyPressMsg                 { return tea.KeyPressMsg{Code: r, Text: string(r)} }
func step(m Model, msg tea.Msg) (Model, tea.Cmd) { out, cmd := m.Update(msg); return out.(Model), cmd }
func TestChatAndWorkerRouting(t *testing.T) {
	var path string
	var body map[string]string
	s := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		path = r.URL.RequestURI()
		raw, _ := io.ReadAll(r.Body)
		json.Unmarshal(raw, &body)
		w.Write([]byte(`{"ok":true}`))
	}))
	defer s.Close()
	c, _ := NewClient(s.URL, "token")
	m := NewModel(context.Background(), c)
	m.task = "42"
	m.tasks = []Task{{ID: "42", Name: "Lab"}}
	m, cmd := step(m, key('c'))
	if !m.composing || cmd == nil {
		t.Fatal("composer not focused")
	}
	m.input.SetValue("explain progress")
	m, cmd = step(m, tea.KeyPressMsg{Code: tea.KeyEnter})
	if cmd == nil {
		t.Fatal("no chat command")
	}
	m, _ = step(m, cmd())
	if path != "/api/chat?task=42" || body["message"] != "explain progress" || m.composing {
		t.Fatalf("%s %v", path, body)
	}
	m.tab = 1
	m.rows = []map[string]any{{"id": "17", "state": "running"}}
	m, _ = step(m, key('w'))
	if m.composing {
		t.Fatal("running worker accepted")
	}
	m.rows[0]["state"] = "paused"
	m, _ = step(m, key('w'))
	m.input.SetValue("operator answer")
	m, cmd = step(m, tea.KeyPressMsg{Code: tea.KeyEnter})
	m, _ = step(m, cmd())
	if path != "/api/tasks/42/intents/17/messages" || body["request_id"] == "" {
		t.Fatalf("%s %v", path, body)
	}
}
func TestStaleEventsAndTerminalText(t *testing.T) {
	c, _ := NewClient("http://localhost:8787", "")
	m := NewModel(context.Background(), c)
	m.task = "42"
	m.generation = 3
	m, _ = step(m, eventMsg{task: "42", generation: 2, activity: &Activity{Seq: 99}})
	if len(m.activity) != 0 {
		t.Fatal("stale stream accepted")
	}
	m, _ = step(m, tea.WindowSizeMsg{Width: 110, Height: 32})
	m.tasks = []Task{{ID: "42", Name: "Lab"}}
	m.activity = []Activity{{Seq: 1, Worker: "mainagent", Summary: "hello\x1b]52;c;bad\a"}}
	m.content()
	view := m.View().Content
	if !strings.Contains(view, "TERMINAL OPERATIONS") || strings.Contains(view, "\x1b]52") {
		t.Fatal("unsafe/missing view")
	}
	m, _ = step(m, tea.WindowSizeMsg{Width: 40, Height: 10})
	if !strings.Contains(m.View().Content, "enlarge") {
		t.Fatal("small terminal fallback missing")
	}
}
