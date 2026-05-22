//go:build !windows

// Package terminal owns the agent-side PTY: spawning a shell, plumbing
// bytes in/out, handling resize, and tearing down cleanly when the hub
// closes the session.
//
// This file is the Unix path. Windows ships a stub in pty_windows.go
// that returns ErrUnsupported until ConPTY is wired in Phase 3.1.
package terminal

import (
	"errors"
	"io"
	"os"
	"os/exec"
	"strconv"
	"sync"
	"sync/atomic"

	"github.com/creack/pty"
)

// ErrUnsupported is returned by NewSession on platforms without a PTY
// implementation. Wraps os.ErrInvalid so callers can use errors.Is.
var ErrUnsupported = errors.New("terminal: PTY not supported on this platform")

// Options configure a new session. Zero values pick reasonable defaults.
type Options struct {
	Shell      string // path to shell binary; "" → /bin/bash → /bin/sh
	RunAsUser  string // ignored on Unix in Phase 3; honored once setuid plumbing lands
	AllowRoot  bool   // if false, refuses to spawn when the agent runs as root
	InitialCols int
	InitialRows int
}

// Session is a live PTY attached to a child shell.
type Session struct {
	id    string
	pty   *os.File
	cmd   *exec.Cmd
	mu    sync.Mutex
	closed atomic.Bool

	bytesIn  atomic.Int64
	bytesOut atomic.Int64
}

// NewSession spawns a shell on a fresh PTY. The caller is responsible for
// reading from Output() and forwarding to whoever owns the session.
func NewSession(id string, opts Options) (*Session, error) {
	if !opts.AllowRoot && os.Geteuid() == 0 {
		// Hard fail rather than silently giving the browser-side user a
		// root shell — the operator should opt in by setting
		// permissions.allowRoot=true.
		return nil, errors.New("terminal: refusing to spawn as root; set permissions.allowRoot=true to override")
	}

	shell := opts.Shell
	if shell == "" {
		if _, err := os.Stat("/bin/bash"); err == nil {
			shell = "/bin/bash"
		} else {
			shell = "/bin/sh"
		}
	}

	cmd := exec.Command(shell, "-l")
	// Set a sensible default environment: most shells need TERM to do
	// anything useful (color, line editing). Caller env still flows
	// through.
	cmd.Env = append(os.Environ(),
		"TERM=xterm-256color",
		"COLUMNS="+strconv.Itoa(defaultI(opts.InitialCols, 80)),
		"LINES="+strconv.Itoa(defaultI(opts.InitialRows, 24)),
	)

	ptmx, err := pty.Start(cmd)
	if err != nil {
		return nil, err
	}

	s := &Session{id: id, pty: ptmx, cmd: cmd}
	if opts.InitialCols > 0 && opts.InitialRows > 0 {
		_ = pty.Setsize(ptmx, &pty.Winsize{Cols: uint16(opts.InitialCols), Rows: uint16(opts.InitialRows)})
	}
	return s, nil
}

// Write forwards bytes from the browser side into the PTY (the shell sees
// them as stdin).
func (s *Session) Write(p []byte) (int, error) {
	if s.closed.Load() {
		return 0, io.ErrClosedPipe
	}
	n, err := s.pty.Write(p)
	s.bytesIn.Add(int64(n))
	return n, err
}

// Read pulls PTY output (shell stdout/stderr merged by the kernel) so the
// caller can forward it back to the browser. Blocks until data or EOF.
func (s *Session) Read(p []byte) (int, error) {
	n, err := s.pty.Read(p)
	s.bytesOut.Add(int64(n))
	return n, err
}

// Resize updates the PTY's reported window size. Required for ncurses
// apps (htop, vim) to redraw correctly when the browser pane resizes.
func (s *Session) Resize(cols, rows int) error {
	if cols <= 0 || rows <= 0 {
		return errors.New("invalid resize")
	}
	return pty.Setsize(s.pty, &pty.Winsize{Cols: uint16(cols), Rows: uint16(rows)})
}

// Close kills the child shell and reclaims the PTY. Safe to call
// repeatedly; subsequent calls are no-ops.
func (s *Session) Close() error {
	if !s.closed.CompareAndSwap(false, true) {
		return nil
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.cmd != nil && s.cmd.Process != nil {
		_ = s.cmd.Process.Kill()
		// Reap so we don't leak a zombie. Ignore the wait error — the
		// process is already dead.
		_, _ = s.cmd.Process.Wait()
	}
	return s.pty.Close()
}

// BytesIn / BytesOut expose counters for the audit log row.
func (s *Session) BytesIn() int64  { return s.bytesIn.Load() }
func (s *Session) BytesOut() int64 { return s.bytesOut.Load() }
func (s *Session) ID() string      { return s.id }

func defaultI(v, fallback int) int {
	if v <= 0 {
		return fallback
	}
	return v
}
