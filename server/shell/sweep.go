package shell

import (
	"bytes"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
)

// MarkerEnv tags every process a shell session started. The exec carries it in its environment
// and everything started from the shell inherits it. NextHost's node agent uses the same
// variable for its own sessions; Wings' values start with markerPrefix, so neither ever sweeps
// the other's.
const MarkerEnv = "NEXTHOST_SHELL_SESSION"

const markerPrefix = "wings-"

// Sweeper finds the processes of a session in /proc and signals them.
//
// Docker does not end a TTY exec when its client goes away: it only stops copying, and the
// shell sits in the container forever. So on the way out Wings (root, next to Docker) signals
// every process that carries the session's marker - only inside the container's pid namespace.
type Sweeper struct {
	// ProcRoot is where /proc is. Tests point it at a fake one.
	ProcRoot string
	// Kill signals a process. syscall.Kill unless a test replaces it.
	Kill func(pid int, sig syscall.Signal) error
	// Self is Wings' own pid, never signalled. os.Getpid() when zero.
	Self int
}

func defaultSweeper() Sweeper {
	return Sweeper{ProcRoot: "/proc", Kill: syscall.Kill}
}

// PidNamespace returns the pid namespace of a process ("pid:[4026532...]"), or "" when it cannot
// be read.
func (sw Sweeper) PidNamespace(pid int) string {
	if pid < 1 {
		return ""
	}
	ns, err := os.Readlink(filepath.Join(sw.ProcRoot, strconv.Itoa(pid), "ns", "pid"))
	if err != nil {
		return ""
	}
	return ns
}

// hasEnv reports whether a process' environment holds the exact entry, or one starting with
// the prefix when prefix is true.
func (sw Sweeper) hasEnv(entry string, needle string, prefix bool) bool {
	environ, err := os.ReadFile(filepath.Join(sw.ProcRoot, entry, "environ"))
	if err != nil {
		return false
	}
	for _, v := range bytes.Split(environ, []byte{0}) {
		if prefix && bytes.HasPrefix(v, []byte(needle)) {
			return true
		}
		if !prefix && string(v) == needle {
			return true
		}
	}
	return false
}

func (sw Sweeper) self() int {
	if sw.Self > 0 {
		return sw.Self
	}
	return os.Getpid()
}

// each calls fn for every process in /proc but Wings itself.
func (sw Sweeper) each(fn func(entry string, pid int)) {
	entries, err := os.ReadDir(sw.ProcRoot)
	if err != nil {
		return
	}
	self := sw.self()
	for _, e := range entries {
		pid, err := strconv.Atoi(e.Name())
		if err != nil || pid < 1 || pid == self {
			continue
		}
		fn(e.Name(), pid)
	}
}

// KillMarked signals every process in the pid namespace whose environment carries the marker,
// and returns how many it signalled. A process that exits while it is read is skipped. Nothing
// is signalled without a namespace: a session's processes are only ever looked for inside its
// container.
func (sw Sweeper) KillMarked(marker string, pidNamespace string, sig syscall.Signal) int {
	if marker == "" || pidNamespace == "" {
		return 0
	}
	needle := MarkerEnv + "=" + marker
	n := 0
	sw.each(func(entry string, pid int) {
		// The namespace first: one readlink rules out every process on the host.
		if ns, err := os.Readlink(filepath.Join(sw.ProcRoot, entry, "ns", "pid")); err != nil || ns != pidNamespace {
			return
		}
		if !sw.hasEnv(entry, needle, false) {
			return
		}
		if sw.Kill(pid, sig) == nil {
			n++
		}
	})
	return n
}

// KillOrphans kills what shell sessions of an earlier Wings left behind: when Wings stops, the
// sessions' sockets and Docker streams go with it, but their shells stay in the containers.
// No session survives a restart, so at boot every process carrying a Wings marker is an
// orphan. Processes in Wings' own pid namespace (the host's) are never touched.
func (sw Sweeper) KillOrphans() int {
	own := sw.PidNamespace(sw.self())
	if own == "" {
		return 0
	}
	n := 0
	sw.each(func(entry string, pid int) {
		ns, err := os.Readlink(filepath.Join(sw.ProcRoot, entry, "ns", "pid"))
		if err != nil || ns == own || !strings.HasPrefix(ns, "pid:") {
			return
		}
		if !sw.hasEnv(entry, MarkerEnv+"="+markerPrefix, true) {
			return
		}
		if sw.Kill(pid, syscall.SIGKILL) == nil {
			n++
		}
	})
	return n
}

// KillOrphans runs Sweeper.KillOrphans on the real /proc.
func KillOrphans() int {
	return defaultSweeper().KillOrphans()
}
