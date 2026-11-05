package portfoliomcp

import (
	"context"
	"encoding/json"
	"fmt"
	"log"
	"net/http"
	"net/url"
	"strconv"
	"time"
)

// Every write tool reports what it did to the API's agent log
// (/api/v1/agent-changes, migration 000017): which tool, on what, the
// values before and after, and whether the API accepted it. The API keeps
// the row and tells the ops bot. A failed write is reported too.
//
// Recording happens here, in the server's code, after the API call - not
// because the model asked - so a change cannot slip past the log. If the
// log itself cannot be written, the write still stands and the tool result
// says so (audit_error), rather than pretending nothing happened.

const actor = "mcp"

type target struct {
	ID    string `json:"id"`
	Label string `json:"label"`
}

type change struct {
	Tool       string         `json:"tool"`
	TargetType string         `json:"target_type"`
	Targets    []target       `json:"targets"`
	Changes    map[string]any `json:"changes"`
	OK         bool           `json:"ok"`
	Error      string         `json:"error,omitempty"`
}

func label(locale, slug string) string { return locale + "/" + slug }

// record sends one change. Its own short context: the tool call may be
// cancelled right after the write, and the log should still get the row.
func (t *tools) record(ctx context.Context, ch change) error {
	ctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 5*time.Second)
	defer cancel()
	if ch.Targets == nil {
		ch.Targets = []target{}
	}
	if ch.Changes == nil {
		ch.Changes = map[string]any{}
	}
	body := struct {
		Actor string `json:"actor"`
		change
	}{actor, ch}
	if err := t.c.Do(ctx, http.MethodPost, "/api/v1/agent-changes", nil, body, nil); err != nil {
		log.Printf("audit: %s: %v", ch.Tool, err)
		return err
	}
	return nil
}

// audited records the outcome of a write and shapes the tool's answer: the
// write's own error wins; a logging failure is added to a good result.
func (t *tools) audited(ctx context.Context, ch change, res map[string]any, writeErr error) (any, error) {
	ch.OK = writeErr == nil
	if writeErr != nil {
		ch.Error = writeErr.Error()
	}
	logErr := t.record(ctx, ch)
	if writeErr != nil {
		return nil, writeErr
	}
	if logErr != nil {
		res["audit_error"] = "the change was made but not logged: " + logErr.Error()
	}
	return res, nil
}

// fieldDiff turns two API inputs into {"field": {"from": .., "to": ..}} for
// the named fields, by their JSON names.
func fieldDiff(before, after any, fields []string) map[string]any {
	toMap := func(v any) map[string]any {
		b, _ := json.Marshal(v)
		var m map[string]any
		_ = json.Unmarshal(b, &m)
		return m
	}
	bm, am := toMap(before), toMap(after)
	out := map[string]any{}
	for _, f := range fields {
		out[f] = map[string]any{"from": bm[f], "to": am[f]}
	}
	return out
}

/* --------------------------------- reading --------------------------------- */

func (t *tools) listChanges(ctx context.Context, args json.RawMessage) (any, error) {
	var in struct{ Limit int }
	if err := decode(args, &in); err != nil {
		return nil, err
	}
	var res struct {
		Items []map[string]any `json:"items"`
	}
	q := url.Values{"limit": {strconv.Itoa(limitArg(in.Limit, 20))}}
	if err := t.c.Do(ctx, http.MethodGet, "/api/v1/agent-changes", q, nil, &res); err != nil {
		return nil, err
	}
	// Bodies stay in the log; a summary of a long value is enough to talk
	// about. get_post shows the current text.
	for _, it := range res.Items {
		if c, ok := it["changes"]; ok {
			it["changes"] = shorten(c)
		}
	}
	return res, nil
}

const shortenAt = 300

func shorten(v any) any {
	switch x := v.(type) {
	case string:
		if r := []rune(x); len(r) > shortenAt {
			return fmt.Sprintf("%s… (%d chars)", string(r[:120]), len(r))
		}
		return x
	case map[string]any:
		for k, e := range x {
			x[k] = shorten(e)
		}
		return x
	case []any:
		for i, e := range x {
			x[i] = shorten(e)
		}
		return x
	}
	return v
}
