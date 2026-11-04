package mcp

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"
)

func testServer() *Server {
	s := &Server{Name: "t", Version: "1", Instructions: "be brief"}
	s.AddTool(Tool{
		Name: "echo", Description: "echo", InputSchema: json.RawMessage(`{"type":"object"}`),
		Annotations: &Annotations{ReadOnlyHint: true},
		Handler: func(_ context.Context, args json.RawMessage) (any, error) {
			var in struct{ Text string }
			json.Unmarshal(args, &in)
			return map[string]string{"text": in.Text}, nil
		},
	})
	s.AddTool(Tool{Name: "fail", InputSchema: json.RawMessage(`{}`),
		Handler: func(context.Context, json.RawMessage) (any, error) { return nil, errors.New("no such post") }})
	s.AddTool(Tool{Name: "boom", InputSchema: json.RawMessage(`{}`),
		Handler: func(context.Context, json.RawMessage) (any, error) { panic("oops") }})
	s.AddPrompt(Prompt{Name: "hi", Description: "greet",
		Arguments: []PromptArgument{{Name: "who", Required: true}},
		Render:    func(a map[string]string) (string, error) { return "salom " + a["who"], nil }})
	return s
}

// session sends lines and returns the responses by id.
func session(t *testing.T, lines ...string) map[string]map[string]any {
	t.Helper()
	var out bytes.Buffer
	if err := testServer().Serve(context.Background(), strings.NewReader(strings.Join(lines, "\n")+"\n"), &out); err != nil {
		t.Fatal(err)
	}
	got := map[string]map[string]any{}
	for _, l := range strings.Split(strings.TrimSpace(out.String()), "\n") {
		if l == "" {
			continue
		}
		var m map[string]any
		if err := json.Unmarshal([]byte(l), &m); err != nil {
			t.Fatalf("bad line %q: %v", l, err)
		}
		got[string(mustJSON(m["id"]))] = m
	}
	return got
}

func mustJSON(v any) []byte { b, _ := json.Marshal(v); return b }

func result(t *testing.T, m map[string]any) map[string]any {
	t.Helper()
	r, ok := m["result"].(map[string]any)
	if !ok {
		t.Fatalf("no result in %v", m)
	}
	return r
}

func TestHandshakeAndLists(t *testing.T) {
	got := session(t,
		`{"jsonrpc":"2.0","id":1,"method":"initialize","params":{"protocolVersion":"2025-06-18","clientInfo":{"name":"test","version":"0"}}}`,
		`{"jsonrpc":"2.0","method":"notifications/initialized"}`,
		`{"jsonrpc":"2.0","id":"a","method":"initialize","params":{"protocolVersion":"1999-01-01"}}`,
		`{"jsonrpc":"2.0","id":2,"method":"tools/list"}`,
		`{"jsonrpc":"2.0","id":3,"method":"prompts/list"}`,
		`{"jsonrpc":"2.0","id":4,"method":"ping"}`,
	)
	if len(got) != 5 {
		t.Fatalf("want 5 answers (the notification gets none), got %d: %v", len(got), got)
	}
	init := result(t, got["1"])
	if init["protocolVersion"] != "2025-06-18" || init["instructions"] != "be brief" {
		t.Errorf("initialize: %v", init)
	}
	if v := result(t, got[`"a"`])["protocolVersion"]; v != protocolVersions[0] {
		t.Errorf("unknown version should get the newest, got %v", v)
	}
	tools := result(t, got["2"])["tools"].([]any)
	if len(tools) != 3 || tools[0].(map[string]any)["annotations"].(map[string]any)["readOnlyHint"] != true {
		t.Errorf("tools: %v", tools)
	}
	if ps := result(t, got["3"])["prompts"].([]any); len(ps) != 1 {
		t.Errorf("prompts: %v", ps)
	}
}

func TestToolCalls(t *testing.T) {
	got := session(t,
		`{"jsonrpc":"2.0","id":1,"method":"tools/call","params":{"name":"echo","arguments":{"text":"salom"}}}`,
		`{"jsonrpc":"2.0","id":2,"method":"tools/call","params":{"name":"fail"}}`,
		`{"jsonrpc":"2.0","id":3,"method":"tools/call","params":{"name":"boom"}}`,
		`{"jsonrpc":"2.0","id":4,"method":"tools/call","params":{"name":"nope"}}`,
		`{"jsonrpc":"2.0","id":5,"method":"resources/list"}`,
		`not json`,
	)
	text := func(id string) (string, bool) {
		r := result(t, got[id])
		c := r["content"].([]any)[0].(map[string]any)
		isErr, _ := r["isError"].(bool)
		return c["text"].(string), isErr
	}
	if s, isErr := text("1"); s != `{"text":"salom"}` || isErr {
		t.Errorf("echo: %q %v", s, isErr)
	}
	if s, isErr := text("2"); !isErr || !strings.Contains(s, "no such post") {
		t.Errorf("a tool error is a result with isError: %q %v", s, isErr)
	}
	if s, isErr := text("3"); !isErr || !strings.Contains(s, "oops") {
		t.Errorf("a panic is contained: %q %v", s, isErr)
	}
	if e := got["4"]["error"].(map[string]any); e["code"].(float64) != codeInvalidParams {
		t.Errorf("unknown tool: %v", e)
	}
	if e := got["5"]["error"].(map[string]any); e["code"].(float64) != codeMethodNotFound {
		t.Errorf("unknown method: %v", e)
	}
	if e := got["null"]["error"].(map[string]any); e["code"].(float64) != codeParse {
		t.Errorf("parse error: %v", e)
	}
}

func TestPrompts(t *testing.T) {
	got := session(t,
		`{"jsonrpc":"2.0","id":1,"method":"prompts/get","params":{"name":"hi","arguments":{"who":"Ibrohim"}}}`,
		`{"jsonrpc":"2.0","id":2,"method":"prompts/get","params":{"name":"hi","arguments":{}}}`,
	)
	msg := result(t, got["1"])["messages"].([]any)[0].(map[string]any)
	if msg["content"].(map[string]any)["text"] != "salom Ibrohim" {
		t.Errorf("prompt: %v", msg)
	}
	if _, ok := got["2"]["error"]; !ok {
		t.Errorf("a missing required argument is an error: %v", got["2"])
	}
}
