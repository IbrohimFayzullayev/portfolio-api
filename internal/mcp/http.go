package mcp

import (
	"bytes"
	"encoding/json"
	"io"
	"net/http"
)

// Streamable HTTP, the plain half of it (MCP spec 2025-06-18, "Transports"):
// the client POSTs one JSON-RPC message, or a batch, and gets the answer as
// application/json in the same response. The server never opens an event
// stream of its own - nothing here needs to push - so GET answers 405, as
// the spec allows, and there are no sessions to create or delete.

const maxHTTPBody = 8 << 20 // a long post body, with room to spare

// HTTPHandler serves the MCP endpoint. Wrap it in authorization: it trusts
// whoever reaches it.
func (s *Server) HTTPHandler() http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			w.Header().Set("Allow", http.MethodPost)
			http.Error(w, "this endpoint takes POST only; no server-sent event stream is offered", http.StatusMethodNotAllowed)
			return
		}
		body, err := io.ReadAll(io.LimitReader(r.Body, maxHTTPBody+1))
		if err != nil {
			http.Error(w, "could not read the request", http.StatusBadRequest)
			return
		}
		if len(body) > maxHTTPBody {
			http.Error(w, "request too large", http.StatusRequestEntityTooLarge)
			return
		}
		body = bytes.TrimSpace(body)

		var out any
		if len(body) > 0 && body[0] == '[' {
			// A batch: allowed by the 2025-03-26 revision, which some
			// clients still speak.
			var msgs []json.RawMessage
			if err := json.Unmarshal(body, &msgs); err != nil || len(msgs) == 0 {
				writeJSON(w, http.StatusOK, response{JSONRPC: "2.0", ID: json.RawMessage("null"),
					Error: &rpcError{codeInvalidRequest, "invalid batch"}})
				return
			}
			var answers []response
			for _, m := range msgs {
				if resp := s.process(r.Context(), m); resp != nil {
					answers = append(answers, *resp)
				}
			}
			if len(answers) > 0 {
				out = answers
			}
		} else if resp := s.process(r.Context(), body); resp != nil {
			out = *resp
		}

		if out == nil {
			// Only notifications: accepted, nothing to say.
			w.WriteHeader(http.StatusAccepted)
			return
		}
		writeJSON(w, http.StatusOK, out)
	})
}

func writeJSON(w http.ResponseWriter, status int, v any) {
	b, err := json.Marshal(v)
	if err != nil {
		http.Error(w, "encode: "+err.Error(), http.StatusInternalServerError)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Cache-Control", "no-store")
	w.WriteHeader(status)
	w.Write(b)
}
