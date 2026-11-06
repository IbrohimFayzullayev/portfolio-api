// Package mcp is a small Model Context Protocol server: the part of the
// protocol a tool server needs - initialize, tools, prompts, ping - over
// stdio and over Streamable HTTP. No resources, no sampling, no server-sent
// event streams, no sessions: every tool call is one request and one answer.
//
// Why by hand and not the official Go SDK: that subset is one JSON object
// per message and five methods, which is less code than the glue around a
// dependency (see the ecosystem rule on dependencies), and every line of it
// is tested here. If the server ever needs what is left out - streaming
// progress, sessions, sampling - that is the moment to move to
// github.com/modelcontextprotocol/go-sdk; the tools are plain functions and
// move over unchanged.
//
// Two transports share one handler: stdio (Serve) for a client that starts
// the server itself, and Streamable HTTP (http.go) for a remote client such
// as a connector in the Claude app. Authorization is not this package's
// business: the HTTP handler is wrapped by internal/mcpauth.
//
// The package knows nothing about the portfolio: internal/portfoliomcp
// registers the tools. The same server can carry a different tool set (the
// Amanat analytics pilot) without a change here.
//
// stdout belongs to the protocol. Anything else a tool wants to say goes to
// stderr (the log package's default), which MCP clients show as server logs.
package mcp

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	"sync"
)

// Protocol versions this server speaks, newest first. A client asking for
// one of them gets it back; anything else gets the newest.
var protocolVersions = []string{"2025-11-25", "2025-06-18", "2025-03-26", "2024-11-05"}

// Annotations are hints to the client about what a tool does; a client may
// use them to decide what to ask the user before running it. Hints, not a
// security boundary - the API behind the tool is.
type Annotations struct {
	Title           string `json:"title,omitempty"`
	ReadOnlyHint    bool   `json:"readOnlyHint"`
	DestructiveHint bool   `json:"destructiveHint"`
	IdempotentHint  bool   `json:"idempotentHint"`
	OpenWorldHint   bool   `json:"openWorldHint"`
}

// Handler runs one tool. A returned error becomes a tool result with
// isError, which the model sees and can act on; it is not a protocol error.
type Handler func(ctx context.Context, args json.RawMessage) (any, error)

type Tool struct {
	Name        string          `json:"name"`
	Title       string          `json:"title,omitempty"`
	Description string          `json:"description"`
	InputSchema json.RawMessage `json:"inputSchema"`
	Annotations *Annotations    `json:"annotations,omitempty"`
	Handler     Handler         `json:"-"`
}

type PromptArgument struct {
	Name        string `json:"name"`
	Description string `json:"description,omitempty"`
	Required    bool   `json:"required,omitempty"`
}

// Prompt is a ready-made request the user can pick in the client (a slash
// command in Claude Code). Render turns its arguments into the text.
type Prompt struct {
	Name        string                                       `json:"name"`
	Title       string                                       `json:"title,omitempty"`
	Description string                                       `json:"description"`
	Arguments   []PromptArgument                             `json:"arguments,omitempty"`
	Render      func(args map[string]string) (string, error) `json:"-"`
}

type Server struct {
	Name         string
	Version      string
	Instructions string // sent once at initialize; the model reads it as context

	tools   []Tool
	prompts []Prompt
	out     io.Writer
	outMu   sync.Mutex
}

func (s *Server) AddTool(t Tool)     { s.tools = append(s.tools, t) }
func (s *Server) AddPrompt(p Prompt) { s.prompts = append(s.prompts, p) }

/* -------------------------------- JSON-RPC -------------------------------- */

type request struct {
	JSONRPC string          `json:"jsonrpc"`
	ID      json.RawMessage `json:"id,omitempty"` // absent: a notification
	Method  string          `json:"method"`
	Params  json.RawMessage `json:"params,omitempty"`
}

type response struct {
	JSONRPC string          `json:"jsonrpc"`
	ID      json.RawMessage `json:"id"`
	Result  any             `json:"result,omitempty"`
	Error   *rpcError       `json:"error,omitempty"`
}

type rpcError struct {
	Code    int    `json:"code"`
	Message string `json:"message"`
}

const (
	codeParse          = -32700
	codeInvalidRequest = -32600
	codeMethodNotFound = -32601
	codeInvalidParams  = -32602
)

// Serve reads requests from in until it closes or ctx ends. Requests are
// handled one at a time, in order: the clients this serves send one tool
// call and wait for it.
func (s *Server) Serve(ctx context.Context, in io.Reader, out io.Writer) error {
	s.out = out
	sc := bufio.NewScanner(in)
	sc.Buffer(make([]byte, 64*1024), 16<<20) // a long post body fits
	for sc.Scan() {
		if ctx.Err() != nil {
			return ctx.Err()
		}
		line := sc.Bytes()
		if len(line) == 0 {
			continue
		}
		if resp := s.process(ctx, line); resp != nil {
			s.write(*resp)
		}
	}
	if err := sc.Err(); err != nil && !errors.Is(err, io.EOF) {
		return err
	}
	return nil
}

// process handles one JSON-RPC message, whatever carried it (a stdin line,
// an HTTP body). nil means there is nothing to answer: a notification.
func (s *Server) process(ctx context.Context, raw []byte) *response {
	var req request
	if err := json.Unmarshal(raw, &req); err != nil {
		return &response{JSONRPC: "2.0", ID: json.RawMessage("null"), Error: &rpcError{codeParse, "parse error"}}
	}
	if req.JSONRPC != "2.0" || req.Method == "" {
		if len(req.ID) > 0 {
			return &response{JSONRPC: "2.0", ID: req.ID, Error: &rpcError{codeInvalidRequest, "invalid request"}}
		}
		return nil
	}
	result, rerr := s.handle(ctx, req)
	if len(req.ID) == 0 {
		return nil // notifications get no answer, not even an error
	}
	resp := &response{JSONRPC: "2.0", ID: req.ID}
	if rerr != nil {
		resp.Error = rerr
	} else {
		resp.Result = result
	}
	return resp
}

func (s *Server) write(r response) {
	b, err := json.Marshal(r)
	if err != nil {
		b, _ = json.Marshal(response{JSONRPC: "2.0", ID: r.ID, Error: &rpcError{-32603, "encode: " + err.Error()}})
	}
	s.outMu.Lock()
	defer s.outMu.Unlock()
	s.out.Write(append(b, '\n'))
}

func (s *Server) handle(ctx context.Context, req request) (any, *rpcError) {
	switch req.Method {
	case "initialize":
		var p struct {
			ProtocolVersion string `json:"protocolVersion"`
			ClientInfo      struct {
				Name    string `json:"name"`
				Version string `json:"version"`
			} `json:"clientInfo"`
		}
		_ = json.Unmarshal(req.Params, &p)
		version := protocolVersions[0]
		for _, v := range protocolVersions {
			if v == p.ProtocolVersion {
				version = v
			}
		}
		log.Printf("mcp: %s %s connected (protocol %s)", p.ClientInfo.Name, p.ClientInfo.Version, version)
		caps := map[string]any{"tools": map[string]any{"listChanged": false}}
		if len(s.prompts) > 0 {
			caps["prompts"] = map[string]any{"listChanged": false}
		}
		return map[string]any{
			"protocolVersion": version,
			"capabilities":    caps,
			"serverInfo":      map[string]string{"name": s.Name, "version": s.Version},
			"instructions":    s.Instructions,
		}, nil

	case "ping":
		return map[string]any{}, nil

	case "tools/list":
		return map[string]any{"tools": s.tools}, nil

	case "tools/call":
		var p struct {
			Name      string          `json:"name"`
			Arguments json.RawMessage `json:"arguments"`
		}
		if err := json.Unmarshal(req.Params, &p); err != nil {
			return nil, &rpcError{codeInvalidParams, "invalid params: " + err.Error()}
		}
		for _, t := range s.tools {
			if t.Name == p.Name {
				return s.call(ctx, t, p.Arguments), nil
			}
		}
		return nil, &rpcError{codeInvalidParams, "unknown tool: " + p.Name}

	case "prompts/list":
		return map[string]any{"prompts": s.prompts}, nil

	case "prompts/get":
		var p struct {
			Name      string            `json:"name"`
			Arguments map[string]string `json:"arguments"`
		}
		if err := json.Unmarshal(req.Params, &p); err != nil {
			return nil, &rpcError{codeInvalidParams, "invalid params: " + err.Error()}
		}
		for _, pr := range s.prompts {
			if pr.Name != p.Name {
				continue
			}
			for _, a := range pr.Arguments {
				if a.Required && p.Arguments[a.Name] == "" {
					return nil, &rpcError{codeInvalidParams, "missing argument: " + a.Name}
				}
			}
			text, err := pr.Render(p.Arguments)
			if err != nil {
				return nil, &rpcError{codeInvalidParams, err.Error()}
			}
			return map[string]any{
				"description": pr.Description,
				"messages": []map[string]any{{
					"role":    "user",
					"content": map[string]string{"type": "text", "text": text},
				}},
			}, nil
		}
		return nil, &rpcError{codeInvalidParams, "unknown prompt: " + p.Name}

	case "notifications/initialized", "notifications/cancelled":
		return map[string]any{}, nil
	}
	return nil, &rpcError{codeMethodNotFound, "method not found: " + req.Method}
}

// call runs a tool and wraps what it returns: a string as it is, anything
// else as compact JSON text.
func (s *Server) call(ctx context.Context, t Tool, args json.RawMessage) map[string]any {
	if len(args) == 0 || string(args) == "null" {
		args = json.RawMessage("{}")
	}
	v, err := safeCall(ctx, t.Handler, args)
	if err != nil {
		log.Printf("mcp: %s: %v", t.Name, err)
		return map[string]any{
			"content": []map[string]string{{"type": "text", "text": "Error: " + err.Error()}},
			"isError": true,
		}
	}
	text, ok := v.(string)
	if !ok {
		b, err := json.Marshal(v)
		if err != nil {
			return map[string]any{
				"content": []map[string]string{{"type": "text", "text": "Error: encode result: " + err.Error()}},
				"isError": true,
			}
		}
		text = string(b)
	}
	return map[string]any{"content": []map[string]string{{"type": "text", "text": text}}}
}

// safeCall keeps one broken tool from taking the whole session down.
func safeCall(ctx context.Context, h Handler, args json.RawMessage) (v any, err error) {
	defer func() {
		if r := recover(); r != nil {
			err = fmt.Errorf("internal error: %v", r)
		}
	}()
	return h(ctx, args)
}
