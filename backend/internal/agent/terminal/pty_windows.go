//go:build windows

// Package terminal: Windows stub. ConPTY wiring lands in Phase 3.1.
// Until then, Windows agents return ErrUnsupported and the hub UI
// surfaces a "Terminal not supported on this agent" message instead
// of spawning a session.
package terminal

import "errors"

// ErrUnsupported is returned by NewSession on platforms without a PTY
// implementation. Wraps os.ErrInvalid so callers can use errors.Is.
var ErrUnsupported = errors.New("terminal: PTY not supported on Windows yet (Phase 3.1)")

type Options struct {
	Shell       string
	RunAsUser   string
	AllowRoot   bool
	InitialCols int
	InitialRows int
}

type Session struct{}

func NewSession(id string, opts Options) (*Session, error) { return nil, ErrUnsupported }

func (s *Session) Write(p []byte) (int, error)    { return 0, ErrUnsupported }
func (s *Session) Read(p []byte) (int, error)     { return 0, ErrUnsupported }
func (s *Session) Resize(cols, rows int) error     { return ErrUnsupported }
func (s *Session) Close() error                    { return nil }
func (s *Session) BytesIn() int64                  { return 0 }
func (s *Session) BytesOut() int64                 { return 0 }
func (s *Session) ID() string                      { return "" }
