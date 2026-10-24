package router

import (
	"context"
	"encoding/json"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"syscall"
	"testing"
	"time"

	"github.com/apex/log"
	"github.com/docker/docker/api/types"
	"github.com/docker/docker/api/types/container"
	"github.com/gbrlsnchs/jwt/v3"
	"github.com/google/uuid"
	ws "github.com/gorilla/websocket"

	"github.com/pelican/wings/config"
	"github.com/pelican/wings/internal/models"
	"github.com/pelican/wings/router/tokens"
	"github.com/pelican/wings/server/shell"
)

const shellTestUser = "00000000-0000-4000-8000-0000000e7d05"

/* Fakes */

type shellFakeDocker struct {
	mu          sync.Mutex
	running     bool
	execs       int
	env         []string
	execRunning bool
	exitCode    int
	resizes     []container.ResizeOptions
	attached    chan net.Conn
}

func newShellFakeDocker() *shellFakeDocker {
	return &shellFakeDocker{running: true, attached: make(chan net.Conn, 4)}
}

func (f *shellFakeDocker) ContainerInspect(context.Context, string) (container.InspectResponse, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	return container.InspectResponse{
		ContainerJSONBase: &container.ContainerJSONBase{State: &container.State{Running: f.running, Pid: 4242}},
		Config:            &container.Config{User: "988:988"},
	}, nil
}

func (f *shellFakeDocker) ContainerExecCreate(_ context.Context, _ string, o container.ExecOptions) (container.ExecCreateResponse, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.execs++
	f.env = o.Env
	return container.ExecCreateResponse{ID: "exec"}, nil
}

func (f *shellFakeDocker) ContainerExecAttach(context.Context, string, container.ExecAttachOptions) (types.HijackedResponse, error) {
	f.mu.Lock()
	f.execRunning = true
	f.mu.Unlock()
	ours, theirs := net.Pipe()
	f.attached <- theirs
	return types.NewHijackedResponse(ours, ""), nil
}

func (f *shellFakeDocker) ContainerExecResize(_ context.Context, _ string, o container.ResizeOptions) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.resizes = append(f.resizes, o)
	return nil
}

func (f *shellFakeDocker) ContainerExecInspect(context.Context, string) (container.ExecInspect, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	return container.ExecInspect{Running: f.execRunning, ExitCode: f.exitCode}, nil
}

func (f *shellFakeDocker) exit(code int) {
	f.mu.Lock()
	f.execRunning, f.exitCode = false, code
	f.mu.Unlock()
}

func (f *shellFakeDocker) marker() string {
	f.mu.Lock()
	defer f.mu.Unlock()
	for _, e := range f.env {
		if v, ok := strings.CutPrefix(e, shell.MarkerEnv+"="); ok {
			return v
		}
	}
	return ""
}

type shellActivity struct {
	user  string
	event models.Event
	meta  models.ActivityMeta
}

type shellHarness struct {
	t        *testing.T
	server   string
	docker   *shellFakeDocker
	url      string
	proc     string
	ctx      context.Context
	cancel   context.CancelFunc
	activity chan shellActivity

	mu    sync.Mutex
	kills []string
}

const shellTestNs = "pid:[4026532001]"

func procEntry(t *testing.T, root string, pid string, env ...string) {
	t.Helper()
	dir := filepath.Join(root, pid)
	if err := os.MkdirAll(filepath.Join(dir, "ns"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(shellTestNs, filepath.Join(dir, "ns", "pid")); err != nil {
		t.Fatal(err)
	}
	if env != nil {
		_ = os.WriteFile(filepath.Join(dir, "environ"), []byte(strings.Join(env, "\x00")+"\x00"), 0o644)
	}
}

func newShellHarness(t *testing.T) *shellHarness {
	h := &shellHarness{
		t:        t,
		server:   uuid.NewString(),
		docker:   newShellFakeDocker(),
		proc:     t.TempDir(),
		activity: make(chan shellActivity, 8),
	}
	h.ctx, h.cancel = context.WithCancel(context.Background())
	t.Cleanup(h.cancel)
	procEntry(t, h.proc, "4242")

	sweeper := &shell.Sweeper{
		ProcRoot: h.proc,
		Self:     1,
		Kill: func(pid int, sig syscall.Signal) error {
			h.mu.Lock()
			h.kills = append(h.kills, sig.String())
			h.mu.Unlock()
			if sig == syscall.SIGHUP {
				h.docker.exit(129)
			}
			return nil
		},
	}

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		conn, err := (&ws.Upgrader{CheckOrigin: func(*http.Request) bool { return true }}).Upgrade(w, r, nil)
		if err != nil {
			return
		}
		defer conn.Close()
		serveShell(h.ctx, conn, shellSocket{
			server: h.server,
			docker: h.docker,
			cols:   100,
			rows:   30,
			authorize: func(token string) (*tokens.ShellPayload, error) {
				return tokens.ParseShellToken([]byte(token), h.server)
			},
			activity: func(user string, event models.Event, meta models.ActivityMeta) {
				h.activity <- shellActivity{user, event, meta}
			},
			log:     log.WithField("test", t.Name()),
			options: shell.Options{KillGrace: 100 * time.Millisecond, Sweeper: sweeper},
			timing:  shellTiming{auth: 300 * time.Millisecond},
		})
	}))
	t.Cleanup(srv.Close)
	h.url = "ws" + strings.TrimPrefix(srv.URL, "http")
	return h
}

// token signs a token with the node token, as NextHost would.
func (h *shellHarness) token(scope string, permissions []string) string {
	h.t.Helper()
	// Rounded up, as NextHost does: Wings refuses a token issued before it booted.
	now := time.Now().Add(time.Second)
	token, err := jwt.Sign(struct {
		jwt.Payload
		UserUUID    string   `json:"user_uuid"`
		ServerUUID  string   `json:"server_uuid"`
		Permissions []string `json:"permissions"`
		Scope       string   `json:"scope"`
		SessionID   string   `json:"session_id"`
	}{
		Payload: jwt.Payload{
			IssuedAt:       jwt.NumericDate(now),
			ExpirationTime: jwt.NumericDate(now.Add(2 * time.Minute)),
			JWTID:          h.t.Name() + now.Format(time.RFC3339Nano) + scope,
		},
		UserUUID:    shellTestUser,
		ServerUUID:  h.server,
		Permissions: permissions,
		Scope:       scope,
		SessionID:   "session-" + h.t.Name(),
	}, config.GetJwtAlgorithm())
	if err != nil {
		h.t.Fatal(err)
	}
	return string(token)
}

func (h *shellHarness) shellToken() string {
	return h.token("shell", []string{"admin.shell"})
}

func (h *shellHarness) dial() *ws.Conn {
	h.t.Helper()
	conn, _, err := ws.DefaultDialer.Dial(h.url+"?cols=100&rows=30", nil)
	if err != nil {
		h.t.Fatal(err)
	}
	h.t.Cleanup(func() { conn.Close() })
	_ = conn.SetReadDeadline(time.Now().Add(5 * time.Second))
	return conn
}

func send(t *testing.T, conn *ws.Conn, v any) {
	t.Helper()
	if err := conn.WriteJSON(v); err != nil {
		t.Fatal(err)
	}
}

// frame reads the next control frame, skipping output.
func frame(t *testing.T, conn *ws.Conn) map[string]any {
	t.Helper()
	for {
		kind, p, err := conn.ReadMessage()
		if err != nil {
			t.Fatalf("reading a frame: %v", err)
		}
		if kind != ws.TextMessage {
			continue
		}
		var m map[string]any
		if err := json.Unmarshal(p, &m); err != nil {
			t.Fatal(err)
		}
		return m
	}
}

func (h *shellHarness) nextActivity() shellActivity {
	h.t.Helper()
	select {
	case a := <-h.activity:
		return a
	case <-time.After(5 * time.Second):
		h.t.Fatal("no activity was logged")
		return shellActivity{}
	}
}

/* Tests */

func TestShellSocketSession(t *testing.T) {
	h := newShellHarness(t)
	conn := h.dial()

	send(t, conn, map[string]string{"type": "auth", "token": h.shellToken()})
	if m := frame(t, conn); m["type"] != "ready" || m["mode"] != "exec" {
		t.Fatalf("expected ready, got %v", m)
	}
	container := <-h.docker.attached
	procEntry(t, h.proc, "100", shell.MarkerEnv+"="+h.docker.marker())

	open := h.nextActivity()
	if open.event != shell.ActivityOpen || open.user != shellTestUser || open.meta["session_id"] != "session-"+t.Name() {
		t.Fatalf("unexpected open activity: %+v", open)
	}

	// Output goes down as raw bytes.
	go func() { _, _ = container.Write([]byte("root@game:~$ ")) }()
	kind, p, err := conn.ReadMessage()
	if err != nil || kind != ws.BinaryMessage || string(p) != "root@game:~$ " {
		t.Fatalf("expected the output as a binary frame, got %d %q %v", kind, p, err)
	}

	// Input goes up to the shell.
	send(t, conn, map[string]string{"type": "input", "data": "id\r"})
	buf := make([]byte, 16)
	if n, err := container.Read(buf); err != nil || string(buf[:n]) != "id\r" {
		t.Fatalf("the shell got %q (%v)", buf[:n], err)
	}

	send(t, conn, map[string]any{"type": "resize", "cols": 132, "rows": 43})
	send(t, conn, map[string]string{"type": "ping"})
	if m := frame(t, conn); m["type"] != "pong" {
		t.Fatalf("expected pong, got %v", m)
	}
	h.docker.mu.Lock()
	if len(h.docker.resizes) != 1 || h.docker.resizes[0].Width != 132 || h.docker.resizes[0].Height != 43 {
		t.Fatalf("unexpected resizes %+v", h.docker.resizes)
	}
	h.docker.mu.Unlock()

	// The page goes away: the shell is hung up, then killed, and the close is logged.
	_ = conn.Close()
	closed := h.nextActivity()
	if closed.event != shell.ActivityClose || closed.meta["reason"] != "closed" || closed.meta["exit_code"] != 129 ||
		closed.meta["session_id"] != "session-"+t.Name() {
		t.Fatalf("unexpected close activity: %+v", closed)
	}
	h.mu.Lock()
	defer h.mu.Unlock()
	if strings.Join(h.kills, ",") != "hangup,killed" {
		t.Fatalf("the shell's processes got %v", h.kills)
	}
}

func TestShellSocketRefusesConsoleTokens(t *testing.T) {
	h := newShellHarness(t)

	for name, token := range map[string]string{
		"console token":           h.token("websocket", []string{"*", "admin.websocket.errors", "admin.websocket.install", "admin.websocket.transfer"}),
		"wildcard in shell scope": h.token("shell", []string{"*"}),
		"garbage":                 "not-a-jwt",
	} {
		t.Run(name, func(t *testing.T) {
			conn := h.dial()
			send(t, conn, map[string]string{"type": "auth", "token": token})
			if m := frame(t, conn); m["type"] != "error" || m["code"] != "UNAUTHORIZED" {
				t.Fatalf("expected UNAUTHORIZED, got %v", m)
			}
		})
	}

	h.docker.mu.Lock()
	defer h.docker.mu.Unlock()
	if h.docker.execs != 0 {
		t.Fatal("an exec was created for a refused token")
	}
}

func TestShellSocketNeedsAuthFirst(t *testing.T) {
	h := newShellHarness(t)

	conn := h.dial()
	send(t, conn, map[string]string{"type": "input", "data": "id\r"})
	if m := frame(t, conn); m["type"] != "error" || m["code"] != "UNAUTHORIZED" {
		t.Fatalf("expected UNAUTHORIZED, got %v", m)
	}

	// Silence is closed after the auth timeout.
	silent := h.dial()
	if _, _, err := silent.ReadMessage(); err == nil {
		t.Fatal("expected a socket without auth to be closed")
	}
}

func TestShellSocketTokenIsSpent(t *testing.T) {
	h := newShellHarness(t)
	token := h.shellToken()

	first := h.dial()
	send(t, first, map[string]string{"type": "auth", "token": token})
	if m := frame(t, first); m["type"] != "ready" {
		t.Fatalf("expected ready, got %v", m)
	}
	<-h.docker.attached
	_ = first.Close()
	h.nextActivity()
	h.nextActivity()

	again := h.dial()
	send(t, again, map[string]string{"type": "auth", "token": token})
	if m := frame(t, again); m["code"] != "UNAUTHORIZED" {
		t.Fatalf("expected a spent token to be refused, got %v", m)
	}
}

func TestShellSocketOneSessionPerServer(t *testing.T) {
	h := newShellHarness(t)

	first := h.dial()
	send(t, first, map[string]string{"type": "auth", "token": h.shellToken()})
	if m := frame(t, first); m["type"] != "ready" {
		t.Fatalf("expected ready, got %v", m)
	}
	<-h.docker.attached

	second := h.dial()
	send(t, second, map[string]string{"type": "auth", "token": h.shellToken()})
	if m := frame(t, second); m["type"] != "error" || m["code"] != "BUSY" {
		t.Fatalf("expected BUSY, got %v", m)
	}
}

func TestShellSocketShellExits(t *testing.T) {
	h := newShellHarness(t)
	conn := h.dial()
	send(t, conn, map[string]string{"type": "auth", "token": h.shellToken()})
	frame(t, conn)
	container := <-h.docker.attached
	h.nextActivity()

	h.docker.exit(0)
	_ = container.Close()

	if m := frame(t, conn); m["type"] != "exit" || m["reason"] != "exit" || m["code"] != float64(0) {
		t.Fatalf("expected exit, got %v", m)
	}
	if _, _, err := conn.ReadMessage(); err == nil {
		t.Fatal("expected the socket to close after the exit")
	}
	if a := h.nextActivity(); a.event != shell.ActivityClose || a.meta["reason"] != "exit" || a.meta["exit_code"] != 0 {
		t.Fatalf("unexpected close activity: %+v", a)
	}
}

func TestShellSocketServerDeleted(t *testing.T) {
	h := newShellHarness(t)
	conn := h.dial()
	send(t, conn, map[string]string{"type": "auth", "token": h.shellToken()})
	frame(t, conn)
	<-h.docker.attached
	h.nextActivity()

	h.cancel()
	if m := frame(t, conn); m["type"] != "exit" || m["reason"] != "closed" {
		t.Fatalf("expected exit, got %v", m)
	}
}

func TestShellSocketNotRunning(t *testing.T) {
	h := newShellHarness(t)
	h.docker.running = false

	conn := h.dial()
	send(t, conn, map[string]string{"type": "auth", "token": h.shellToken()})
	if m := frame(t, conn); m["type"] != "error" || m["code"] != "NOT_RUNNING" {
		t.Fatalf("expected NOT_RUNNING, got %v", m)
	}
}

func TestShellSocketsPerServerAreCapped(t *testing.T) {
	id := "cap-test"
	for i := 0; i < shellMaxSockets; i++ {
		if !takeShellSocket(id) {
			t.Fatalf("socket %d refused", i)
		}
	}
	if takeShellSocket(id) {
		t.Fatal("expected the socket over the cap to be refused")
	}
	for i := 0; i < shellMaxSockets; i++ {
		giveShellSocket(id)
	}
	if !takeShellSocket(id) {
		t.Fatal("expected a socket once the others closed")
	}
	giveShellSocket(id)
}
