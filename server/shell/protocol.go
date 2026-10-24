// Package shell is NextHost's staff shell: a Linux shell in a running server's container,
// started with docker exec under a TTY, one session per server.
//
// The socket (router/router_server_shell.go) speaks the frames NextHost's own shell already
// uses (config/utils/servers/shell.ts in the NextHost monorepo), so the page can talk to Wings
// or to the node agent the same way:
//   - page -> Wings: JSON text frames, ClientMessage. The first one is "auth".
//   - Wings -> page: binary frames are terminal output as raw bytes, text frames are JSON
//     ServerMessage.
package shell

import (
	"encoding/json"
	"math"
	"strconv"

	"github.com/pelican/wings/internal/models"
)

// Activity of a session, sent to NextHost with the rest of the server's activity. Both carry
// the token's session_id; the close also says how long it was open and why it ended. What is
// typed and printed is never logged.
const (
	ActivityOpen  = models.Event("server:shell.open")
	ActivityClose = models.Event("server:shell.close")
)

const (
	DefaultCols = 80
	DefaultRows = 24
	MaxCols     = 500
	MaxRows     = 200

	// MaxInputChars is the longest input a frame may carry. The page splits a paste into
	// frames of this size; longer input is cut, not refused.
	MaxInputChars = 8 * 1024
)

// Error codes sent in an "error" frame. They are a subset of NextHost's SHELL_ERROR_CODES.
const (
	// ErrorUnauthorized: the token was missing, invalid, expired, spent, or not a shell token.
	ErrorUnauthorized = "UNAUTHORIZED"
	// ErrorNotRunning: the container is not running. Wings never starts a container for a
	// shell - NextHost's agent opens a temporary one for a stopped server.
	ErrorNotRunning = "NOT_RUNNING"
	// ErrorBusy: someone already has a shell open on this server.
	ErrorBusy = "BUSY"
	// ErrorFailed: anything else. The Wings log says what.
	ErrorFailed = "FAILED"
)

// EndReason says how a session that opened ended.
type EndReason string

const (
	// ReasonExit: the shell exited by itself (exit, Ctrl-D, the container stopped).
	ReasonExit = EndReason("exit")
	// ReasonIdle: no input for the idle timeout.
	ReasonIdle = EndReason("idle")
	// ReasonClosed: the socket closed, or the server was deleted.
	ReasonClosed = EndReason("closed")
)

// ClientMessage is a frame from the page, as ParseClientMessage leaves it.
type ClientMessage struct {
	Type  string
	Token string
	Data  string
	Cols  uint
	Rows  uint
}

// ServerMessage is a control frame to the page.
type ServerMessage struct {
	Type   string    `json:"type"`
	Mode   string    `json:"mode,omitempty"`
	Code   any       `json:"code,omitempty"`
	Reason EndReason `json:"reason,omitempty"`
}

// Ready is sent once the shell runs. Wings only ever runs one in the server's own container.
func Ready() ServerMessage {
	return ServerMessage{Type: "ready", Mode: "exec"}
}

// Exit is sent when a session that opened ends. A code that is not known is left out.
func Exit(code *int, reason EndReason) ServerMessage {
	m := ServerMessage{Type: "exit", Reason: reason}
	if code != nil {
		m.Code = *code
	}
	return m
}

// Error is sent when a session cannot open.
func Error(code string) ServerMessage {
	return ServerMessage{Type: "error", Code: code}
}

// Pong answers a ping.
func Pong() ServerMessage {
	return ServerMessage{Type: "pong"}
}

func clamp(n float64, max uint, fallback uint) uint {
	if math.IsNaN(n) || n < 1 {
		return fallback
	}
	if n > float64(max) {
		return max
	}
	return uint(n)
}

// ClampSize turns a terminal size from anyone - the query string - into two integers in range.
func ClampSize(cols, rows string) (uint, uint) {
	parse := func(v string) float64 {
		n, err := strconv.ParseFloat(v, 64)
		if err != nil {
			return 0
		}
		return n
	}
	return clamp(parse(cols), MaxCols, DefaultCols), clamp(parse(rows), MaxRows, DefaultRows)
}

// ParseClientMessage reads a frame from the page. It returns false for anything that is not
// one of the frames: auth with a token, input with data, resize, ping. Sizes are clamped and
// input is cut to MaxInputChars.
func ParseClientMessage(raw []byte) (ClientMessage, bool) {
	var f struct {
		Type  string  `json:"type"`
		Token string  `json:"token"`
		Data  string  `json:"data"`
		Cols  float64 `json:"cols"`
		Rows  float64 `json:"rows"`
	}
	if err := json.Unmarshal(raw, &f); err != nil {
		return ClientMessage{}, false
	}

	switch f.Type {
	case "auth":
		if f.Token == "" {
			return ClientMessage{}, false
		}
		return ClientMessage{Type: "auth", Token: f.Token}, true
	case "input":
		if f.Data == "" {
			return ClientMessage{}, false
		}
		return ClientMessage{Type: "input", Data: cut(f.Data, MaxInputChars)}, true
	case "resize":
		return ClientMessage{Type: "resize", Cols: clamp(f.Cols, MaxCols, DefaultCols), Rows: clamp(f.Rows, MaxRows, DefaultRows)}, true
	case "ping":
		return ClientMessage{Type: "ping"}, true
	}
	return ClientMessage{}, false
}

// cut shortens s to at most n characters without splitting one. The page counts UTF-16 units,
// so a frame it split itself is never cut.
func cut(s string, n int) string {
	i := 0
	for pos := range s {
		if i == n {
			return s[:pos]
		}
		i++
	}
	return s
}
