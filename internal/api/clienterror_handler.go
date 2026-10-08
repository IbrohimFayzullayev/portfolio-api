package api

import (
	"net/http"
	"strings"
)

// Errors from the browser.
//
// The API's own failures already reach Telegram (errorreport.go). The gap was
// everything that goes wrong one layer up: a page that throws while rendering
// shows the visitor an error screen and tells nobody. The Next.js error
// boundaries post here, and from this point a browser error is one more row in
// error_groups — same grouping, same window, same /xato in the bot.
//
// It is also the only place where the CONTENT of an alert is chosen by a
// stranger, which is what the two limits below are for.

const (
	clientErrorMaxMessage = 300
	clientErrorMaxStack   = 1500
	clientErrorMaxPath    = 200
)

type clientErrorInput struct {
	// Which front-end is reporting. Anything unrecognised is filed as "web".
	Source  string `json:"source"`
	Message string `json:"message"`
	Stack   string `json:"stack"`
	// Next.js gives server-rendered errors a digest instead of a message in
	// production; it is the handle for finding the real error in the logs.
	Digest string `json:"digest"`
	Path   string `json:"path"`
}

func (s *Server) handleClientError(w http.ResponseWriter, r *http.Request) {
	var in clientErrorInput
	if err := decodeJSON(r, &in); err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}

	msg := clampField(in.Message, clientErrorMaxMessage)
	digest := clampField(in.Digest, 64)
	if msg == "" && digest == "" {
		writeError(w, http.StatusBadRequest, "message is required")
		return
	}
	if msg == "" {
		msg = "render error"
	}
	if digest != "" {
		msg += " (digest " + digest + ")"
	}

	service := "web"
	if strings.TrimSpace(in.Source) == "dashboard" {
		service = "dashboard"
	}

	// Stored for context, deliberately left out of the fingerprint: the same
	// broken component on forty pages is one problem, not forty.
	path := clampField(in.Path, clientErrorMaxPath)

	fp := fingerprintError(service, "", msg)
	go s.recordError(
		service, fp, "error.web", path, severityWarning,
		msg, truncateBytes(strings.TrimSpace(in.Stack), clientErrorMaxStack),
		// However many distinct errors arrive, only a handful an hour become
		// messages. All of them are still recorded and visible in /xatolar.
		func() bool { return s.clientErrorBudget.allow("all") },
	)

	w.WriteHeader(http.StatusAccepted)
}
