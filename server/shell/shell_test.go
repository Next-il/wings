package shell

import (
	"context"
	"encoding/json"
	"io"
	"net"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"testing"
	"time"

	"github.com/docker/docker/api/types"
	"github.com/docker/docker/api/types/container"
)

/* A fake /proc */

type fakeProc struct {
	t    *testing.T
	root string

	mu     sync.Mutex
	killed []killCall
	onKill func(pid int, sig syscall.Signal)
}

type killCall struct {
	pid int
	sig syscall.Signal
}

func newFakeProc(t *testing.T) *fakeProc {
	return &fakeProc{t: t, root: t.TempDir()}
}

// add makes /proc/<pid> with its pid namespace and environment.
func (p *fakeProc) add(pid int, ns string, env ...string) {
	p.t.Helper()
	dir := filepath.Join(p.root, strconv.Itoa(pid))
	_ = os.RemoveAll(dir)
	if err := os.MkdirAll(filepath.Join(dir, "ns"), 0o755); err != nil {
		p.t.Fatal(err)
	}
	if err := os.Symlink(ns, filepath.Join(dir, "ns", "pid")); err != nil {
		p.t.Fatal(err)
	}
	if env != nil {
		if err := os.WriteFile(filepath.Join(dir, "environ"), []byte(strings.Join(env, "\x00")+"\x00"), 0o644); err != nil {
			p.t.Fatal(err)
		}
	}
}

func (p *fakeProc) sweeper(self int) *Sweeper {
	return &Sweeper{
		ProcRoot: p.root,
		Self:     self,
		Kill: func(pid int, sig syscall.Signal) error {
			p.mu.Lock()
			p.killed = append(p.killed, killCall{pid, sig})
			onKill := p.onKill
			p.mu.Unlock()
			if onKill != nil {
				onKill(pid, sig)
			}
			return nil
		},
	}
}

func (p *fakeProc) kills() []killCall {
	p.mu.Lock()
	defer p.mu.Unlock()
	return append([]killCall(nil), p.killed...)
}

/* A fake Docker */

type notFound struct{}

func (notFound) Error() string { return "No such container" }
func (notFound) NotFound()     {}

type fakeDocker struct {
	mu          sync.Mutex
	exists      bool
	running     bool
	pid         int
	user        string
	execOptions container.ExecOptions
	execRunning bool
	exitCode    int
	resizes     []container.ResizeOptions
	attached    chan net.Conn
}

func newFakeDocker() *fakeDocker {
	return &fakeDocker{exists: true, running: true, pid: 4242, user: "988:988", attached: make(chan net.Conn, 1)}
}

func (f *fakeDocker) ContainerInspect(_ context.Context, _ string) (container.InspectResponse, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if !f.exists {
		return container.InspectResponse{}, notFound{}
	}
	return container.InspectResponse{
		ContainerJSONBase: &container.ContainerJSONBase{State: &container.State{Running: f.running, Pid: f.pid}},
		Config:            &container.Config{User: f.user},
	}, nil
}

func (f *fakeDocker) ContainerExecCreate(_ context.Context, _ string, options container.ExecOptions) (container.ExecCreateResponse, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.execOptions = options
	return container.ExecCreateResponse{ID: "exec-1"}, nil
}

func (f *fakeDocker) ContainerExecAttach(_ context.Context, _ string, _ container.ExecAttachOptions) (types.HijackedResponse, error) {
	f.mu.Lock()
	f.execRunning = true
	f.mu.Unlock()
	ours, theirs := net.Pipe()
	f.attached <- theirs
	return types.NewHijackedResponse(ours, ""), nil
}

func (f *fakeDocker) ContainerExecResize(_ context.Context, _ string, options container.ResizeOptions) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.resizes = append(f.resizes, options)
	return nil
}

func (f *fakeDocker) ContainerExecInspect(_ context.Context, _ string) (container.ExecInspect, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	return container.ExecInspect{ExecID: "exec-1", Running: f.execRunning, ExitCode: f.exitCode, Pid: 5000}, nil
}

// exit makes the exec stop, as the shell would.
func (f *fakeDocker) exit(code int) {
	f.mu.Lock()
	f.execRunning = false
	f.exitCode = code
	f.mu.Unlock()
}

func (f *fakeDocker) marker() string {
	f.mu.Lock()
	defer f.mu.Unlock()
	for _, e := range f.execOptions.Env {
		if v, ok := strings.CutPrefix(e, MarkerEnv+"="); ok {
			return v
		}
	}
	return ""
}

const containerNs = "pid:[4026532001]"

func waitDone(t *testing.T, s *Session) End {
	t.Helper()
	select {
	case <-s.Done():
		return s.End()
	case <-time.After(5 * time.Second):
		t.Fatal("the session did not end")
		return End{}
	}
}

func openTestSession(t *testing.T, d *fakeDocker, proc *fakeProc, output func([]byte) error) (*Session, net.Conn) {
	t.Helper()
	proc.add(d.pid, containerNs)
	s, err := Open(context.Background(), d, Options{
		Container:   "server-" + t.Name(),
		Cols:        120,
		Rows:        40,
		Output:      output,
		IdleTimeout: time.Hour,
		KillGrace:   200 * time.Millisecond,
		Sweeper:     proc.sweeper(1),
	})
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	return s, <-d.attached
}

/* Sessions */

func TestOpenRunsTheShellAsTheContainerUser(t *testing.T) {
	d, proc := newFakeDocker(), newFakeProc(t)
	s, theirs := openTestSession(t, d, proc, nil)
	defer theirs.Close()

	o := d.execOptions
	if o.User != "988:988" || !o.Tty || !o.AttachStdin || !o.AttachStdout || !o.AttachStderr {
		t.Fatalf("unexpected exec options: %+v", o)
	}
	if strings.Join(o.Cmd, " ") != "sh -c "+Script {
		t.Fatalf("unexpected command: %v", o.Cmd)
	}
	if o.ConsoleSize == nil || o.ConsoleSize[0] != 40 || o.ConsoleSize[1] != 120 {
		t.Fatalf("unexpected console size: %v", o.ConsoleSize)
	}
	if !strings.HasPrefix(s.Marker(), markerPrefix) || d.marker() != s.Marker() {
		t.Fatalf("the exec does not carry the session marker: %v", o.Env)
	}

	s.Close(ReasonClosed)
	waitDone(t, s)
}

func TestOnlyOneSessionPerContainer(t *testing.T) {
	d, proc := newFakeDocker(), newFakeProc(t)
	s, theirs := openTestSession(t, d, proc, nil)
	defer theirs.Close()

	if _, err := Open(context.Background(), d, Options{Container: "server-" + t.Name()}); err != ErrBusy {
		t.Fatalf("expected ErrBusy, got %v", err)
	}

	s.Close(ReasonClosed)
	waitDone(t, s)
	if IsOpen("server-" + t.Name()) {
		t.Fatal("the container is still held after its session ended")
	}

	// The slot is free again.
	s2, theirs2 := openTestSession(t, d, proc, nil)
	defer theirs2.Close()
	s2.Close(ReasonClosed)
	waitDone(t, s2)
}

func TestOpenRefusesAStoppedContainer(t *testing.T) {
	for name, set := range map[string]func(*fakeDocker){
		"stopped": func(d *fakeDocker) { d.running = false },
		"missing": func(d *fakeDocker) { d.exists = false },
	} {
		t.Run(name, func(t *testing.T) {
			d := newFakeDocker()
			set(d)
			_, err := Open(context.Background(), d, Options{Container: "server-" + t.Name()})
			if err != ErrNotRunning {
				t.Fatalf("expected ErrNotRunning, got %v", err)
			}
			if IsOpen("server-" + t.Name()) {
				t.Fatal("a refused open kept the container's slot")
			}
		})
	}
}

func TestInputOutputAndResize(t *testing.T) {
	d, proc := newFakeDocker(), newFakeProc(t)

	var mu sync.Mutex
	var out strings.Builder
	got := make(chan struct{}, 8)
	s, theirs := openTestSession(t, d, proc, func(b []byte) error {
		mu.Lock()
		out.Write(b)
		mu.Unlock()
		got <- struct{}{}
		return nil
	})
	defer theirs.Close()

	go func() { _, _ = theirs.Write([]byte("$ ")) }()
	<-got
	mu.Lock()
	if out.String() != "$ " {
		t.Fatalf("unexpected output %q", out.String())
	}
	mu.Unlock()

	go s.Write("ls\r")
	buf := make([]byte, 16)
	n, err := theirs.Read(buf)
	if err != nil || string(buf[:n]) != "ls\r" {
		t.Fatalf("the shell got %q (%v)", buf[:n], err)
	}

	s.Resize(1000, 30)
	d.mu.Lock()
	if len(d.resizes) != 1 || d.resizes[0].Width != MaxCols || d.resizes[0].Height != 30 {
		t.Fatalf("unexpected resizes: %+v", d.resizes)
	}
	d.mu.Unlock()

	s.Close(ReasonClosed)
	waitDone(t, s)
}

func TestCloseHangsUpThenKillsTheSessionsProcesses(t *testing.T) {
	d, proc := newFakeDocker(), newFakeProc(t)
	s, theirs := openTestSession(t, d, proc, nil)
	defer theirs.Close()
	go func() { _, _ = io.Copy(io.Discard, theirs) }()

	marker := MarkerEnv + "=" + s.Marker()
	proc.add(100, containerNs, "HOME=/home/container", marker)  // the shell
	proc.add(101, containerNs, marker)                          // a job it started
	proc.add(102, containerNs, "HOME=/home/container")          // the game
	proc.add(103, "pid:[4026531836]", marker)                   // the same marker outside the container
	proc.add(104, containerNs, MarkerEnv+"=wings-someone-else") // another session
	proc.onKill = func(pid int, sig syscall.Signal) {
		if pid == 100 && sig == syscall.SIGHUP {
			d.exit(129)
		}
	}

	s.Close(ReasonClosed)
	end := waitDone(t, s)

	if end.Reason != ReasonClosed || end.Code == nil || *end.Code != 129 {
		t.Fatalf("unexpected end: %+v", end)
	}
	want := []killCall{{100, syscall.SIGHUP}, {101, syscall.SIGHUP}, {100, syscall.SIGKILL}, {101, syscall.SIGKILL}}
	if got := proc.kills(); !equalKills(got, want) {
		t.Fatalf("signalled %v, want %v", got, want)
	}
}

func TestShellExitingEndsTheSession(t *testing.T) {
	d, proc := newFakeDocker(), newFakeProc(t)
	s, theirs := openTestSession(t, d, proc, func([]byte) error { return nil })

	marker := MarkerEnv + "=" + s.Marker()
	proc.add(101, containerNs, marker) // a background job the shell left

	d.exit(3)
	_ = theirs.Close()
	end := waitDone(t, s)

	if end.Reason != ReasonExit || end.Code == nil || *end.Code != 3 {
		t.Fatalf("unexpected end: %+v", end)
	}
	if got := proc.kills(); !equalKills(got, []killCall{{101, syscall.SIGKILL}}) {
		t.Fatalf("signalled %v", got)
	}

	// Closing after the end does nothing.
	s.Close(ReasonClosed)
}

func TestIdleSessionIsClosed(t *testing.T) {
	d, proc := newFakeDocker(), newFakeProc(t)
	proc.add(d.pid, containerNs)
	s, err := Open(context.Background(), d, Options{
		Container:   "server-" + t.Name(),
		IdleTimeout: 150 * time.Millisecond,
		KillGrace:   50 * time.Millisecond,
		Sweeper:     proc.sweeper(1),
	})
	if err != nil {
		t.Fatal(err)
	}
	theirs := <-d.attached
	defer theirs.Close()
	go func() { _, _ = io.Copy(io.Discard, theirs) }()

	// Typing keeps it open.
	for i := 0; i < 4; i++ {
		time.Sleep(60 * time.Millisecond)
		s.Write("x")
	}
	select {
	case <-s.Done():
		t.Fatal("a session with input was closed as idle")
	default:
	}

	if end := waitDone(t, s); end.Reason != ReasonIdle {
		t.Fatalf("unexpected end: %+v", end)
	}
}

func TestOutputErrorClosesTheSession(t *testing.T) {
	d, proc := newFakeDocker(), newFakeProc(t)
	s, theirs := openTestSession(t, d, proc, func([]byte) error { return io.ErrClosedPipe })
	defer theirs.Close()

	go func() { _, _ = theirs.Write([]byte("output nobody reads")) }()
	if end := waitDone(t, s); end.Reason != ReasonClosed {
		t.Fatalf("unexpected end: %+v", end)
	}
}

func TestWithoutANamespaceOnlyTheExecIsSignalled(t *testing.T) {
	d, proc := newFakeDocker(), newFakeProc(t)
	// No /proc entry for the container: its pid namespace cannot be read.
	s, err := Open(context.Background(), d, Options{
		Container: "server-" + t.Name(),
		KillGrace: 50 * time.Millisecond,
		Sweeper:   proc.sweeper(1),
	})
	if err != nil {
		t.Fatal(err)
	}
	theirs := <-d.attached
	defer theirs.Close()
	proc.add(101, containerNs, MarkerEnv+"="+s.Marker())

	s.Close(ReasonClosed)
	waitDone(t, s)
	for _, k := range proc.kills() {
		if k.pid != 5000 {
			t.Fatalf("signalled %d without knowing the container's namespace", k.pid)
		}
	}
}

func equalKills(a, b []killCall) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}

/* Sweeping */

func TestKillOrphansOnlyKillsWingsMarkersInContainers(t *testing.T) {
	proc := newFakeProc(t)
	host := "pid:[4026531836]"
	proc.add(1, host)
	proc.add(200, containerNs, MarkerEnv+"=wings-0b6e")        // a shell of an earlier Wings
	proc.add(201, "pid:[4026532002]", MarkerEnv+"=wings-77aa") // in another container
	proc.add(202, containerNs, MarkerEnv+"=4c1d9b52")          // the node agent's own session
	proc.add(203, host, MarkerEnv+"=wings-0b6e")               // on the host itself
	proc.add(204, containerNs, "HOME=/home/container")         // the game

	if n := proc.sweeper(1).KillOrphans(); n != 2 {
		t.Fatalf("killed %d processes, want 2", n)
	}
	if got := proc.kills(); !equalKills(got, []killCall{{200, syscall.SIGKILL}, {201, syscall.SIGKILL}}) {
		t.Fatalf("killed %v", got)
	}
}

func TestKillMarkedNeedsMarkerAndNamespace(t *testing.T) {
	proc := newFakeProc(t)
	proc.add(100, containerNs, MarkerEnv+"=wings-1")
	sw := proc.sweeper(1)

	if sw.KillMarked("", containerNs, syscall.SIGKILL) != 0 || sw.KillMarked("wings-1", "", syscall.SIGKILL) != 0 {
		t.Fatal("swept without a marker or a namespace")
	}
	if sw.KillMarked("wings-1", containerNs, syscall.SIGKILL) != 1 {
		t.Fatal("did not sweep the marked process")
	}
	// Wings never signals itself.
	if proc.sweeper(100).KillMarked("wings-1", containerNs, syscall.SIGKILL) != 0 {
		t.Fatal("signalled its own pid")
	}
}

/* Frames */

func TestParseClientMessage(t *testing.T) {
	long := strings.Repeat("é", MaxInputChars+10)
	cases := []struct {
		raw  string
		ok   bool
		want ClientMessage
	}{
		{`{"type":"auth","token":"abc"}`, true, ClientMessage{Type: "auth", Token: "abc"}},
		{`{"type":"auth"}`, false, ClientMessage{}},
		{`{"type":"input","data":"ls\r"}`, true, ClientMessage{Type: "input", Data: "ls\r"}},
		{`{"type":"input","data":""}`, false, ClientMessage{}},
		{`{"type":"input","data":"` + long + `"}`, true, ClientMessage{Type: "input", Data: strings.Repeat("é", MaxInputChars)}},
		{`{"type":"resize","cols":9999,"rows":-3}`, true, ClientMessage{Type: "resize", Cols: MaxCols, Rows: DefaultRows}},
		{`{"type":"resize","cols":100.7,"rows":30}`, true, ClientMessage{Type: "resize", Cols: 100, Rows: 30}},
		{`{"type":"ping"}`, true, ClientMessage{Type: "ping"}},
		{`{"type":"exec","data":"rm -rf /"}`, false, ClientMessage{}},
		{`not json`, false, ClientMessage{}},
	}
	for _, c := range cases {
		got, ok := ParseClientMessage([]byte(c.raw))
		if ok != c.ok || got != c.want {
			t.Errorf("%.60s: got %+v %v, want %+v %v", c.raw, got, ok, c.want, c.ok)
		}
	}
}

func TestClampSize(t *testing.T) {
	if c, r := ClampSize("", "abc"); c != DefaultCols || r != DefaultRows {
		t.Fatalf("got %dx%d", c, r)
	}
	if c, r := ClampSize("100000", "0"); c != MaxCols || r != DefaultRows {
		t.Fatalf("got %dx%d", c, r)
	}
	if c, r := ClampSize("132", "43"); c != 132 || r != 43 {
		t.Fatalf("got %dx%d", c, r)
	}
}

func TestServerMessagesMatchNextHostFrames(t *testing.T) {
	code := 0
	for _, c := range []struct {
		m    ServerMessage
		want string
	}{
		{Ready(), `{"type":"ready","mode":"exec"}`},
		{Exit(&code, ReasonExit), `{"type":"exit","code":0,"reason":"exit"}`},
		{Exit(nil, ReasonIdle), `{"type":"exit","reason":"idle"}`},
		{Error(ErrorBusy), `{"type":"error","code":"BUSY"}`},
		{Pong(), `{"type":"pong"}`},
	} {
		b, _ := json.Marshal(c.m)
		if string(b) != c.want {
			t.Errorf("got %s, want %s", b, c.want)
		}
	}
}
