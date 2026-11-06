package api

import (
	"encoding/json"
	"fmt"
	"net/http"
	"regexp"
	"sort"
	"strings"
	"time"
	"unicode/utf8"
)

// The agent audit log (migration 000017): AI agents report each write they
// made, the bot is told, and anyone can look back at what changed.
//
// The agent reports on itself, but "the agent" here is the MCP server's own
// code, not the model: every write tool records after its API call,
// whatever the model wanted. Raw SQL like the other ops tables
// (system_handler.go): one INSERT, one SELECT.

var (
	agentActorRe = regexp.MustCompile(`^[a-z0-9-]{1,32}$`)
	agentToolRe  = regexp.MustCompile(`^[a-z0-9_]{1,64}$`)
)

var agentTargetTypes = map[string]bool{
	"post": true, "project": true, "message": true, "finance_entry": true, "practice": true,
}

const (
	agentMaxTargets  = 10
	agentMaxLabel    = 200
	agentMaxError    = 1000
	agentMaxChanges  = 512 << 10
	agentNotifyError = 300
)

type agentTarget struct {
	ID    string `json:"id"`
	Label string `json:"label"`
}

type agentChangeInput struct {
	Actor      string          `json:"actor"`
	Tool       string          `json:"tool"`
	TargetType string          `json:"target_type"`
	Targets    []agentTarget   `json:"targets"`
	Changes    json.RawMessage `json:"changes"`
	OK         bool            `json:"ok"`
	Error      string          `json:"error"`
}

func (in *agentChangeInput) validate() error {
	if in.Actor == "" {
		in.Actor = "mcp"
	}
	if !agentActorRe.MatchString(in.Actor) {
		return fmt.Errorf("actor must be lowercase letters, digits and -")
	}
	if !agentToolRe.MatchString(in.Tool) {
		return fmt.Errorf("tool must be lowercase letters, digits and _")
	}
	if !agentTargetTypes[in.TargetType] {
		return fmt.Errorf("target_type must be post, project, message, finance_entry or practice")
	}
	if len(in.Targets) > agentMaxTargets {
		return fmt.Errorf("at most %d targets", agentMaxTargets)
	}
	for _, t := range in.Targets {
		if utf8.RuneCountInString(t.Label) > agentMaxLabel || len(t.ID) > 64 {
			return fmt.Errorf("target id or label is too long")
		}
	}
	if len(in.Changes) == 0 || string(in.Changes) == "null" {
		in.Changes = json.RawMessage("{}")
	}
	if len(in.Changes) > agentMaxChanges {
		return fmt.Errorf("changes are too large")
	}
	var obj map[string]json.RawMessage
	if err := json.Unmarshal(in.Changes, &obj); err != nil {
		return fmt.Errorf("changes must be a JSON object")
	}
	if utf8.RuneCountInString(in.Error) > agentMaxError {
		in.Error = string([]rune(in.Error)[:agentMaxError])
	}
	return nil
}

func (s *Server) handleCreateAgentChange(w http.ResponseWriter, r *http.Request) {
	var in agentChangeInput
	if err := decodeJSON(r, &in); err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	if err := in.validate(); err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	if in.Targets == nil {
		in.Targets = []agentTarget{}
	}
	targets, _ := json.Marshal(in.Targets)

	var (
		id int64
		at time.Time
	)
	err := s.pool.QueryRow(r.Context(), `
		INSERT INTO agent_changes (actor, tool, target_type, targets, changes, ok, error)
		VALUES ($1, $2, $3, $4, $5, $6, $7)
		RETURNING id, created_at`,
		in.Actor, in.Tool, in.TargetType, targets, []byte(in.Changes), in.OK, in.Error,
	).Scan(&id, &at)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "could not record the change")
		return
	}

	// A read mark is bookkeeping, not a change to the site: logged, not announced.
	if in.TargetType != "message" {
		s.queueNotice(agentNotice(in))
	}
	writeJSON(w, http.StatusCreated, map[string]any{"id": id, "created_at": at})
}

// agentNotice is what the ops bot gets: who, which tool, on what, which
// fields - never the values, which can be whole post bodies.
func agentNotice(in agentChangeInput) notice {
	labels := make([]string, 0, len(in.Targets))
	for _, t := range in.Targets {
		labels = append(labels, t.Label)
	}
	var obj map[string]json.RawMessage
	_ = json.Unmarshal(in.Changes, &obj)
	fields := make([]string, 0, len(obj))
	for k := range obj {
		fields = append(fields, k)
	}
	sort.Strings(fields)

	n := notice{kind: "agent.change", payload: map[string]any{
		"actor": in.Actor, "tool": in.Tool, "target_type": in.TargetType,
		"targets": labels, "fields": fields, "ok": in.OK,
	}}
	if !in.OK {
		n.severity = 1
		msg := in.Error
		if utf8.RuneCountInString(msg) > agentNotifyError {
			msg = string([]rune(msg)[:agentNotifyError]) + "…"
		}
		n.payload["error"] = msg
	}
	return n
}

type agentChange struct {
	ID         int64           `json:"id"`
	Actor      string          `json:"actor"`
	Tool       string          `json:"tool"`
	TargetType string          `json:"target_type"`
	Targets    json.RawMessage `json:"targets"`
	Changes    json.RawMessage `json:"changes"`
	OK         bool            `json:"ok"`
	Error      string          `json:"error"`
	CreatedAt  time.Time       `json:"created_at"`
}

func (s *Server) handleListAgentChanges(w http.ResponseWriter, r *http.Request) {
	limit := queryInt(r, "limit", 20, 1, 100)
	actor := strings.TrimSpace(r.URL.Query().Get("actor"))
	if actor != "" && !agentActorRe.MatchString(actor) {
		writeError(w, http.StatusBadRequest, "bad actor")
		return
	}
	rows, err := s.pool.Query(r.Context(), `
		SELECT id, actor, tool, target_type, targets, changes, ok, error, created_at
		FROM agent_changes
		WHERE $1 = '' OR actor = $1
		ORDER BY created_at DESC, id DESC
		LIMIT $2`, actor, limit)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "could not read changes")
		return
	}
	defer rows.Close()
	items := []agentChange{}
	for rows.Next() {
		var c agentChange
		if err := rows.Scan(&c.ID, &c.Actor, &c.Tool, &c.TargetType, &c.Targets, &c.Changes,
			&c.OK, &c.Error, &c.CreatedAt); err != nil {
			writeError(w, http.StatusInternalServerError, "could not read changes")
			return
		}
		items = append(items, c)
	}
	if err := rows.Err(); err != nil {
		writeError(w, http.StatusInternalServerError, "could not read changes")
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"items": items})
}
