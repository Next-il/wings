package shell

import (
	"context"
	"sync"
	"syscall"
	"time"

	"emperror.dev/errors"
	"github.com/docker/docker/api/types"
	"github.com/docker/docker/api/types/container"
	"github.com/docker/docker/client"
	"github.com/google/uuid"
)

const (
	// Script runs bash when the image has it, sh otherwise - both as login shells, so the
	// image's profile runs. The same command NextHost's agent runs.
	Script = "command -v bash >/dev/null && exec bash -l || exec sh -l"

	// IdleTimeout closes a session nobody typed into for this long.
	IdleTimeout = 30 * time.Minute

	// KillGrace is how long a hung-up session gets to exit before it is killed.
	KillGrace = 2 * time.Second

	dockerTimeout = 10 * time.Second
	readBuffer    = 32 * 1024
)

var (
	ErrBusy       = errors.New("shell: a session is already open on this server")
	ErrNotRunning = errors.New("shell: the server's container is not running")
)

// Docker is the part of Docker's client a session uses. *client.Client is one.
type Docker interface {
	ContainerInspect(ctx context.Context, containerID string) (container.InspectResponse, error)
	ContainerExecCreate(ctx context.Context, containerID string, options container.ExecOptions) (container.ExecCreateResponse, error)
	ContainerExecAttach(ctx context.Context, execID string, config container.ExecAttachOptions) (types.HijackedResponse, error)
	ContainerExecResize(ctx context.Context, execID string, options container.ResizeOptions) error
	ContainerExecInspect(ctx context.Context, execID string) (container.ExecInspect, error)
}

var _ Docker = (*client.Client)(nil)

// Options of a session.
type Options struct {
	// Container is the server's container - its uuid. One session per container.
	Container string
	Cols      uint
	Rows      uint
	// Output gets the terminal output in order, from a single goroutine. An error from it
	// ends the session as closed.
	Output func([]byte) error

	// Zero means IdleTimeout, KillGrace and the real /proc. Tests make them shorter.
	IdleTimeout time.Duration
	KillGrace   time.Duration
	Sweeper     *Sweeper
}

// End says how a session ended.
type End struct {
	// Code is the exit code of the shell, nil when Docker could not say.
	Code     *int
	Reason   EndReason
	Duration time.Duration
}

// Session is one shell in a running container, from docker exec until its last process is gone.
type Session struct {
	docker  Docker
	opts    Options
	sweeper Sweeper

	execID       string
	marker       string
	pidNamespace string
	stream       types.HijackedResponse
	startedAt    time.Time

	mu       sync.Mutex
	closing  EndReason
	closedAt time.Time
	exited   bool
	idle     *time.Timer

	writeMu sync.Mutex

	done chan struct{}
	end  End
}

/* Sessions, by container: one each. */

var (
	sessionsMu sync.Mutex
	sessions   = map[string]struct{}{}
)

func acquire(id string) bool {
	sessionsMu.Lock()
	defer sessionsMu.Unlock()
	if _, ok := sessions[id]; ok {
		return false
	}
	sessions[id] = struct{}{}
	return true
}

func release(id string) {
	sessionsMu.Lock()
	delete(sessions, id)
	sessionsMu.Unlock()
}

// IsOpen reports whether a session is open in the container.
func IsOpen(id string) bool {
	sessionsMu.Lock()
	defer sessionsMu.Unlock()
	_, ok := sessions[id]
	return ok
}

// Open starts a shell in the server's running container. The container's slot is taken before
// anything else, so two sockets can never both open one.
//
// It returns ErrBusy when a session is already open on the container and ErrNotRunning when the
// container is not running (or does not exist).
func Open(ctx context.Context, d Docker, opts Options) (*Session, error) {
	if !acquire(opts.Container) {
		return nil, ErrBusy
	}
	s, err := open(ctx, d, opts)
	if err != nil {
		release(opts.Container)
		return nil, err
	}
	return s, nil
}

func open(ctx context.Context, d Docker, opts Options) (*Session, error) {
	if opts.IdleTimeout <= 0 {
		opts.IdleTimeout = IdleTimeout
	}
	if opts.KillGrace <= 0 {
		opts.KillGrace = KillGrace
	}
	sweeper := defaultSweeper()
	if opts.Sweeper != nil {
		sweeper = *opts.Sweeper
	}
	opts.Cols = clamp(float64(opts.Cols), MaxCols, DefaultCols)
	opts.Rows = clamp(float64(opts.Rows), MaxRows, DefaultRows)

	ctx, cancel := context.WithTimeout(ctx, dockerTimeout)
	defer cancel()

	c, err := d.ContainerInspect(ctx, opts.Container)
	if err != nil {
		if client.IsErrNotFound(err) {
			return nil, ErrNotRunning
		}
		return nil, errors.WrapIf(err, "shell: failed to inspect container")
	}
	if c.ContainerJSONBase == nil || c.State == nil || !c.State.Running {
		return nil, ErrNotRunning
	}

	// The user the game runs as: Wings created the container with it.
	var user string
	if c.Config != nil {
		user = c.Config.User
	}

	s := &Session{
		docker:       d,
		opts:         opts,
		sweeper:      sweeper,
		marker:       markerPrefix + uuid.NewString(),
		pidNamespace: sweeper.PidNamespace(c.State.Pid),
		done:         make(chan struct{}),
	}

	size := &[2]uint{opts.Rows, opts.Cols}
	exec, err := d.ContainerExecCreate(ctx, opts.Container, container.ExecOptions{
		User:         user,
		Tty:          true,
		ConsoleSize:  size,
		AttachStdin:  true,
		AttachStdout: true,
		AttachStderr: true,
		Env:          []string{"TERM=xterm-256color", MarkerEnv + "=" + s.marker},
		Cmd:          []string{"sh", "-c", Script},
	})
	if err != nil {
		return nil, errors.WrapIf(err, "shell: failed to create exec")
	}
	s.execID = exec.ID

	// Attaching starts the exec. The stream outlives the context: it only covers the dial.
	stream, err := d.ContainerExecAttach(ctx, exec.ID, container.ExecAttachOptions{Tty: true, ConsoleSize: size})
	if err != nil {
		return nil, errors.WrapIf(err, "shell: failed to attach to exec")
	}
	s.stream = stream
	s.startedAt = time.Now()

	s.mu.Lock()
	s.idle = time.AfterFunc(opts.IdleTimeout, func() { s.Close(ReasonIdle) })
	s.mu.Unlock()

	go s.run()

	return s, nil
}

// Done is closed once the session has ended and its processes are gone.
func (s *Session) Done() <-chan struct{} {
	return s.done
}

// End is how the session ended. Only meaningful once Done is closed.
func (s *Session) End() End {
	<-s.done
	return s.end
}

// Marker is the value of MarkerEnv the session's processes carry.
func (s *Session) Marker() string {
	return s.marker
}

func (s *Session) live() bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.closing == "" && !s.exited
}

// Write sends keyboard input to the shell and resets the idle timer.
func (s *Session) Write(data string) {
	if data == "" || !s.live() {
		return
	}
	s.mu.Lock()
	if s.idle != nil {
		s.idle.Reset(s.opts.IdleTimeout)
	}
	s.mu.Unlock()

	s.writeMu.Lock()
	defer s.writeMu.Unlock()
	// A shell that stops reading its input must not hold the socket's reader forever.
	_ = s.stream.Conn.SetWriteDeadline(time.Now().Add(dockerTimeout))
	_, _ = s.stream.Conn.Write([]byte(data))
}

// Resize changes the size of the shell's terminal.
func (s *Session) Resize(cols, rows uint) {
	if !s.live() {
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), dockerTimeout)
	defer cancel()
	_ = s.docker.ContainerExecResize(ctx, s.execID, container.ResizeOptions{
		Width:  clamp(float64(cols), MaxCols, DefaultCols),
		Height: clamp(float64(rows), MaxRows, DefaultRows),
	})
}

// Close hangs the session up: the shell gets SIGHUP, and whatever is left of it SIGKILL after
// KillGrace. It returns at once; Done is closed once it is over. Closing an ended session, or
// closing twice, does nothing.
func (s *Session) Close(reason EndReason) {
	s.mu.Lock()
	if s.closing != "" || s.exited {
		s.mu.Unlock()
		return
	}
	s.closing = reason
	s.closedAt = time.Now()
	if s.idle != nil {
		s.idle.Stop()
	}
	s.mu.Unlock()

	// The shell in the container first - closing the stream only ends Docker's copy of it.
	s.signal(syscall.SIGHUP)
	s.stream.Close()
}

// signal sends sig to every process of the session. Without the container's pid namespace
// only the exec's own process is reached.
func (s *Session) signal(sig syscall.Signal) {
	if s.pidNamespace != "" {
		s.sweeper.KillMarked(s.marker, s.pidNamespace, sig)
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), dockerTimeout)
	defer cancel()
	if i, err := s.docker.ContainerExecInspect(ctx, s.execID); err == nil && i.Running && i.Pid > 0 {
		_ = s.sweeper.Kill(i.Pid, sig)
	}
}

func (s *Session) run() {
	defer close(s.done)
	defer release(s.opts.Container)

	// Output until the stream ends: the shell exited, or Close closed the stream.
	buf := make([]byte, readBuffer)
	for {
		n, err := s.stream.Reader.Read(buf)
		if n > 0 && s.opts.Output != nil {
			if werr := s.opts.Output(buf[:n]); werr != nil {
				s.Close(ReasonClosed)
			}
		}
		if err != nil {
			break
		}
	}

	s.mu.Lock()
	reason := s.closing
	if reason == "" {
		s.exited = true
		reason = ReasonExit
	}
	if s.idle != nil {
		s.idle.Stop()
	}
	closedAt := s.closedAt
	s.mu.Unlock()
	s.stream.Close()

	// A hung-up shell gets its grace period, cut short once Docker says it is gone.
	if reason != ReasonExit {
		deadline := closedAt.Add(s.opts.KillGrace)
		for time.Now().Before(deadline) && s.execRunning() {
			time.Sleep(min(100*time.Millisecond, time.Until(deadline)))
		}
	}

	// Whatever the shell left behind: a background job, a process that ignored the hangup.
	s.signal(syscall.SIGKILL)

	s.end = End{Code: s.exitCode(), Reason: reason, Duration: time.Since(s.startedAt)}
}

func (s *Session) execRunning() bool {
	ctx, cancel := context.WithTimeout(context.Background(), dockerTimeout)
	defer cancel()
	i, err := s.docker.ContainerExecInspect(ctx, s.execID)
	return err == nil && i.Running
}

func (s *Session) exitCode() *int {
	ctx, cancel := context.WithTimeout(context.Background(), dockerTimeout)
	defer cancel()
	i, err := s.docker.ContainerExecInspect(ctx, s.execID)
	if err != nil || i.Running {
		return nil
	}
	code := i.ExitCode
	return &code
}
