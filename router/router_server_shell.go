package router

import (
	"context"
	"net/http"
	"sync"
	"time"

	"emperror.dev/errors"
	"github.com/apex/log"
	"github.com/gin-gonic/gin"
	ws "github.com/gorilla/websocket"
	"golang.org/x/time/rate"

	"github.com/pelican/wings/environment"
	"github.com/pelican/wings/internal/models"
	"github.com/pelican/wings/router/tokens"
	"github.com/pelican/wings/router/websocket"
	"github.com/pelican/wings/server/shell"
)

// NextHost: the staff shell - a Linux shell in a running server's container.

const (
	// How long a socket may stay open without sending its "auth" frame.
	shellAuthTimeout = 10 * time.Second
	// Wings pings the page this often; the browser answers by itself, even from a background tab.
	shellPingInterval = 30 * time.Second
	// A socket nothing arrived on (no frame, no pong) for this long is gone.
	shellReadTimeout  = 90 * time.Second
	shellWriteTimeout = 30 * time.Second
	// A frame of input is at most 8K characters, six bytes each once JSON escapes them.
	shellReadLimit = 64 * 1024
	// Sockets per server, authenticated or not. Only one of them can hold the session.
	shellMaxSockets = 4
)

var (
	shellSocketsMu sync.Mutex
	shellSockets   = map[string]int{}
)

func takeShellSocket(id string) bool {
	shellSocketsMu.Lock()
	defer shellSocketsMu.Unlock()
	if shellSockets[id] >= shellMaxSockets {
		return false
	}
	shellSockets[id]++
	return true
}

func giveShellSocket(id string) {
	shellSocketsMu.Lock()
	defer shellSocketsMu.Unlock()
	if shellSockets[id]--; shellSockets[id] <= 0 {
		delete(shellSockets, id)
	}
}

// getServerShell upgrades to the staff shell of a server: docker exec in its running container
// under a TTY, for as long as the socket stays open.
//
// The socket authenticates with its first frame, {"type":"auth","token":"..."}, like the console
// does - the token never sits in a URL. The token is a JWT signed with the node token, of the
// "shell" scope, carrying the "admin.shell" permission, for this server, and spent on first use
// (tokens.ParseShellToken). NextHost signs one only for its staff. A console token cannot open a
// shell: it has the "websocket" scope, and "*" does not grant "admin.shell".
//
// Refusals are sent on the open socket as an "error" frame, not as an HTTP status: a browser never
// sees the status of an upgrade it was refused, and the page should say why.
func getServerShell(c *gin.Context) {
	s := ExtractServer(c)

	if !takeShellSocket(s.ID()) {
		c.AbortWithStatusJSON(http.StatusTooManyRequests, gin.H{
			"error": "Too many open shell connections.",
		})
		return
	}
	defer giveShellSocket(s.ID())

	docker, err := environment.Docker()
	if err != nil {
		_ = c.AbortWithError(http.StatusInternalServerError, err)
		return
	}

	c.Header("Content-Security-Policy", "default-src 'self'")
	c.Header("X-Frame-Options", "DENY")

	upgrader := ws.Upgrader{EnableCompression: true, CheckOrigin: websocket.CheckOrigin}
	conn, err := upgrader.Upgrade(c.Writer, c.Request, nil)
	if err != nil {
		// The upgrader has answered the request itself.
		return
	}
	defer conn.Close()

	ip := c.ClientIP()
	cols, rows := shell.ClampSize(c.Query("cols"), c.Query("rows"))
	serveShell(s.Context(), conn, shellSocket{
		server: s.ID(),
		docker: docker,
		cols:   cols,
		rows:   rows,
		authorize: func(token string) (*tokens.ShellPayload, error) {
			return tokens.ParseShellToken([]byte(token), s.ID())
		},
		activity: func(user string, event models.Event, metadata models.ActivityMeta) {
			s.SaveActivity(s.NewRequestActivity(user, ip), event, metadata)
		},
		log: s.Log().WithField("subsystem", "shell").WithField("ip", ip),
	})
}

// shellSocket is what serveShell needs from the request, so it can be tested without a server.
type shellSocket struct {
	server    string
	docker    shell.Docker
	cols      uint
	rows      uint
	authorize func(token string) (*tokens.ShellPayload, error)
	activity  func(user string, event models.Event, metadata models.ActivityMeta)
	log       *log.Entry
	// Session options for the tests: shorter timers, a fake /proc.
	options shell.Options
	timing  shellTiming
}

type shellTiming struct {
	auth time.Duration
	ping time.Duration
	read time.Duration
}

func (t shellTiming) orDefaults() shellTiming {
	if t.auth <= 0 {
		t.auth = shellAuthTimeout
	}
	if t.ping <= 0 {
		t.ping = shellPingInterval
	}
	if t.read <= 0 {
		t.read = shellReadTimeout
	}
	return t
}

// shellWriter serializes writes: gorilla's connections take one writer at a time.
type shellWriter struct {
	mu   sync.Mutex
	conn *ws.Conn
}

func (w *shellWriter) message(m shell.ServerMessage) error {
	w.mu.Lock()
	defer w.mu.Unlock()
	_ = w.conn.SetWriteDeadline(time.Now().Add(shellWriteTimeout))
	return w.conn.WriteJSON(m)
}

func (w *shellWriter) output(b []byte) error {
	w.mu.Lock()
	defer w.mu.Unlock()
	_ = w.conn.SetWriteDeadline(time.Now().Add(shellWriteTimeout))
	return w.conn.WriteMessage(ws.BinaryMessage, b)
}

func (w *shellWriter) ping() {
	w.mu.Lock()
	defer w.mu.Unlock()
	_ = w.conn.WriteControl(ws.PingMessage, nil, time.Now().Add(shellWriteTimeout))
}

func (w *shellWriter) close() {
	w.mu.Lock()
	defer w.mu.Unlock()
	_ = w.conn.WriteControl(ws.CloseMessage, ws.FormatCloseMessage(ws.CloseNormalClosure, ""), time.Now().Add(time.Second))
}

// serveShell runs one shell socket from its auth frame to the end of its session. ctx is the
// server's: deleting the server ends the session.
func serveShell(ctx context.Context, conn *ws.Conn, sock shellSocket) {
	timing := sock.timing.orDefaults()
	conn.SetReadLimit(shellReadLimit)
	w := &shellWriter{conn: conn}

	refuse := func(code string) {
		_ = w.message(shell.Error(code))
		w.close()
	}

	// The auth frame, and nothing before it.
	_ = conn.SetReadDeadline(time.Now().Add(timing.auth))
	t, p, err := conn.ReadMessage()
	if err != nil {
		return
	}
	m, ok := shell.ParseClientMessage(p)
	if t != ws.TextMessage || !ok || m.Type != "auth" {
		refuse(shell.ErrorUnauthorized)
		return
	}
	token, err := sock.authorize(m.Token)
	if err != nil {
		sock.log.WithField("error", err).Warn("refused a shell socket: invalid token")
		refuse(shell.ErrorUnauthorized)
		return
	}

	sessionID := token.SessionID
	if sessionID == "" {
		sessionID = token.JWTID
	}
	logger := sock.log.WithField("session_id", sessionID)

	opts := sock.options
	opts.Container = sock.server
	opts.Cols = sock.cols
	opts.Rows = sock.rows
	opts.Output = w.output

	session, err := shell.Open(ctx, sock.docker, opts)
	if err != nil {
		switch {
		case errors.Is(err, shell.ErrBusy):
			refuse(shell.ErrorBusy)
		case errors.Is(err, shell.ErrNotRunning):
			refuse(shell.ErrorNotRunning)
		default:
			logger.WithField("error", err).Error("failed to open a shell")
			refuse(shell.ErrorFailed)
		}
		return
	}

	sock.activity(token.UserUUID, shell.ActivityOpen, models.ActivityMeta{"session_id": sessionID, "mode": "exec"})
	logger.Info("opened a shell in the server container")
	_ = w.message(shell.Ready())

	// Anything that arrives - a frame or the pong to Wings' ping - proves the page is still there.
	alive := func() { _ = conn.SetReadDeadline(time.Now().Add(timing.read)) }
	conn.SetPongHandler(func(string) error {
		alive()
		return nil
	})
	alive()

	reading := make(chan struct{})
	go func() {
		defer close(reading)
		// Typing is one frame per key: far above the console's limits, like NextHost's own relay.
		limiter := rate.NewLimiter(rate.Every(20*time.Millisecond), 300)
		for {
			t, p, err := conn.ReadMessage()
			if err != nil {
				return
			}
			alive()
			if t != ws.TextMessage || !limiter.Allow() {
				continue
			}
			m, ok := shell.ParseClientMessage(p)
			if !ok {
				continue
			}
			switch m.Type {
			case "input":
				session.Write(m.Data)
			case "resize":
				session.Resize(m.Cols, m.Rows)
			case "ping":
				_ = w.message(shell.Pong())
			}
		}
	}()

	ticker := time.NewTicker(timing.ping)
	defer ticker.Stop()
wait:
	for {
		select {
		case <-ticker.C:
			w.ping()
		case <-reading:
			session.Close(shell.ReasonClosed)
			break wait
		case <-ctx.Done():
			session.Close(shell.ReasonClosed)
			break wait
		case <-session.Done():
			break wait
		}
	}

	end := session.End()
	_ = w.message(shell.Exit(end.Code, end.Reason))
	w.close()
	_ = conn.Close()
	<-reading

	meta := models.ActivityMeta{
		"session_id":       sessionID,
		"mode":             "exec",
		"reason":           string(end.Reason),
		"duration_seconds": int64(end.Duration.Round(time.Second) / time.Second),
	}
	if end.Code != nil {
		meta["exit_code"] = *end.Code
	}
	sock.activity(token.UserUUID, shell.ActivityClose, meta)
	logger.WithField("reason", end.Reason).WithField("duration", end.Duration.Round(time.Second).String()).
		Info("closed a shell in the server container")
}
