package mcp

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func post(t *testing.T, h http.Handler, body string) *httptest.ResponseRecorder {
	t.Helper()
	req := httptest.NewRequest(http.MethodPost, "/mcp", strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "application/json, text/event-stream")
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	return rec
}

func TestHTTPTransport(t *testing.T) {
	h := testServer().HTTPHandler()

	rec := post(t, h, `{"jsonrpc":"2.0","id":1,"method":"initialize","params":{"protocolVersion":"2025-06-18"}}`)
	if rec.Code != http.StatusOK || rec.Header().Get("Content-Type") != "application/json" {
		t.Fatalf("initialize: %d %s", rec.Code, rec.Header().Get("Content-Type"))
	}
	var init response
	json.Unmarshal(rec.Body.Bytes(), &init)
	if init.Result.(map[string]any)["protocolVersion"] != "2025-06-18" {
		t.Errorf("initialize: %s", rec.Body.String())
	}

	if rec := post(t, h, `{"jsonrpc":"2.0","method":"notifications/initialized"}`); rec.Code != http.StatusAccepted || rec.Body.Len() != 0 {
		t.Errorf("a notification gets 202 and no body: %d %q", rec.Code, rec.Body.String())
	}

	rec = post(t, h, `{"jsonrpc":"2.0","id":"x","method":"tools/call","params":{"name":"echo","arguments":{"text":"salom"}}}`)
	if !strings.Contains(rec.Body.String(), `"id":"x"`) || !strings.Contains(rec.Body.String(), `salom`) {
		t.Errorf("tool call: %s", rec.Body.String())
	}

	// A batch: answers for requests only, in order.
	rec = post(t, h, `[{"jsonrpc":"2.0","id":1,"method":"ping"},{"jsonrpc":"2.0","method":"notifications/initialized"},{"jsonrpc":"2.0","id":2,"method":"tools/list"}]`)
	var batch []map[string]any
	if err := json.Unmarshal(rec.Body.Bytes(), &batch); err != nil || len(batch) != 2 || batch[1]["id"] != float64(2) {
		t.Errorf("batch: %s", rec.Body.String())
	}

	if rec := post(t, h, `not json`); !strings.Contains(rec.Body.String(), `"code":-32700`) {
		t.Errorf("parse error: %s", rec.Body.String())
	}

	get := httptest.NewRecorder()
	h.ServeHTTP(get, httptest.NewRequest(http.MethodGet, "/mcp", nil))
	if get.Code != http.StatusMethodNotAllowed || get.Header().Get("Allow") != "POST" {
		t.Errorf("GET: %d %q", get.Code, get.Header().Get("Allow"))
	}

	big := `{"jsonrpc":"2.0","id":1,"method":"ping","params":{"x":"` + strings.Repeat("a", maxHTTPBody) + `"}}`
	if rec := post(t, h, big); rec.Code != http.StatusRequestEntityTooLarge {
		t.Errorf("too large: %d", rec.Code)
	}
}
