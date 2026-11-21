// +build !windows

package term

import (
	"context"
	"errors"
	"sync"
	"time"

	"github.com/pkg/term/termios"
	"golang.org/x/sys/unix"
)

var (
	// ErrSessionClosed is returned by Read, Write, SendBreak and Restart
	// after the single cleanup path of a transactional session has run.
	ErrSessionClosed = errors.New("term: session is closed")

	// ErrTermiosConflict is returned when the terminal attributes currently
	// applied to the device no longer match the attributes the session last
	// committed, i.e. another process changed the terminal underneath us.
	// The conflicting state is left untouched and must be resolved by the
	// caller rather than being overwritten.
	ErrTermiosConflict = errors.New("term: terminal was modified by another process")

	// ErrDrainTimeout is returned by Close when queued output could not be
	// drained within the configured deadline. The local terminal attributes
	// are still restored, but no guarantee is made that the buffered data
	// reached the wire.
	ErrDrainTimeout = errors.New("term: timed out waiting for output to drain")
)

// Shutdown reasons. Every way of ending a session funnels into the single
// shutdown path; the reason only documents how it was entered.
type shutdownReason int

const (
	reasonClose shutdownReason = iota
	reasonCancel
	reasonBreak
	reasonRestart
)

// SessionConfig configures a transactional terminal session.
type SessionConfig struct {
	// Ctx, when non-nil, tears the session down (running the same single
	// cleanup path as Close) as soon as it is cancelled. A nil context means
	// the session lives until Close, SendBreak or Restart.
	Ctx context.Context

	// Options are applied in order against a snapshot of the original
	// termios when the session starts. If any option fails, every change
	// already made is rolled back and BeginSession returns the error.
	Options []func(*Term) error

	// DrainTimeout, when positive, bounds how long Close waits for queued
	// output to be transmitted before restoring the terminal attributes. On
	// timeout the attributes are restored anyway and Close returns
	// ErrDrainTimeout; the data is never reported as successfully delivered.
	// A zero value restores the attributes without draining.
	DrainTimeout time.Duration

	// drain is overridable in tests to simulate a slow or wedged device.
	drain func(fd uintptr) error
}

// Session is a transactional terminal session. It owns the terminal for the
// duration between BeginSession and Close and guarantees that the termios
// captured at the beginning is restored exactly once through a single
// cleanup path, regardless of how the session ends.
type Session struct {
	t *Term

	ctx       context.Context
	drain     func(fd uintptr) error
	drainTo   time.Duration
	stopWatch chan struct{} // closed to stop the context watcher
	done      chan struct{} // closed once the cleanup path has completed

	// mu guards the fields below. It is always taken after Term.mu, never
	// before, so the lock order across the package is Term.mu -> Session.mu.
	mu sync.Mutex

	closed   bool
	closeErr error

	parent *Session // enclosing session, nil for the outermost session

	// baseline is the termios to restore on cleanup: the state captured when
	// this session began. committed is the termios last successfully applied
	// by this session; it is used to detect external modifications.
	baseline  unix.Termios
	committed unix.Termios
}

// BeginSession starts a transactional session on t. It first reads the
// complete current termios, then applies cfg.Options in order. If any option
// (or the ioctl behind it) fails, all changes are rolled back to the
// snapshot and the error is returned with no session created.
//
// Sessions may be nested: when starting a nested session the current
// terminal attributes are compared against the value the enclosing session
// last committed. If they differ, an external process has touched the
// terminal and ErrTermiosConflict is returned without modifying anything.
func (t *Term) BeginSession(cfg SessionConfig) (*Session, error) {
	t.mu.Lock()
	defer t.mu.Unlock()

	// A closed session stays attached so reads and writes keep failing with
	// ErrSessionClosed, but a fresh session pops it; a still-active session
	// becomes the parent of the new, nested session.
	if t.session != nil && t.session.closedErr() != nil {
		t.session = nil
	}

	current, err := termios.Tcgetattr(uintptr(t.fd))
	if err != nil {
		return nil, err
	}

	parent := t.session
	if parent != nil {
		parent.mu.Lock()
		stale := parent.committed != *current
		parent.mu.Unlock()
		if stale {
			return nil, ErrTermiosConflict
		}
	}

	s := &Session{
		t:         t,
		ctx:       cfg.Ctx,
		drain:     cfg.drain,
		drainTo:   cfg.DrainTimeout,
		stopWatch: make(chan struct{}),
		done:      make(chan struct{}),
		parent:    parent,
		baseline:  *current,
		committed: *current,
	}
	if s.drain == nil {
		s.drain = termios.Tcdrain
	}

	if err := t.SetOption(cfg.Options...); err != nil {
		// Reverse recovery: restore the snapshot immediately. A failure here
		// is reported alongside the original error rather than hiding it.
		if rerr := termios.Tcsetattr(uintptr(t.fd), termios.TCSANOW, current); rerr != nil {
			return nil, errors.New(err.Error() + "; rollback failed: " + rerr.Error())
		}
		return nil, err
	}

	// Re-read what the kernel actually committed so later conflict checks
	// compare against the real on-device state rather than our request.
	applied, err := termios.Tcgetattr(uintptr(t.fd))
	if err != nil {
		_ = termios.Tcsetattr(uintptr(t.fd), termios.TCSANOW, current)
		return nil, err
	}
	s.committed = *applied
	t.session = s

	if s.ctx != nil {
		go s.watch()
	}
	return s, nil
}

// watch tears the session down through the single cleanup path when the
// session context is cancelled.
func (s *Session) watch() {
	if s.ctx == nil {
		return
	}
	select {
	case <-s.ctx.Done():
		_ = s.shutdown(reasonCancel)
	case <-s.stopWatch:
	}
}

// shutdown is the one and only cleanup path. Context cancellation, SendBreak,
// Restart and Close all enter here; the closed flag makes it run at most
// once and every later call returns the result of the first run.
func (s *Session) shutdown(why shutdownReason) error {
	t := s.t
	t.mu.Lock()
	s.mu.Lock()
	if s.closed {
		err := s.closeErr
		s.mu.Unlock()
		t.mu.Unlock()
		return err
	}
	s.closed = true
	s.mu.Unlock()
	t.mu.Unlock()

	close(s.stopWatch)

	var err error

	// SendBreak must complete before the terminal state is restored, and it
	// shares this cleanup path with every other teardown trigger.
	if why == reasonBreak {
		if berr := termios.Tcsendbreak(uintptr(t.fd), 0); berr != nil {
			err = berr
		}
	}

	// Optionally wait for queued output, bounded by DrainTimeout. A timeout
	// never skips the attribute restore and never pretends the data landed.
	if s.drainTo > 0 {
		if derr := s.drainWithTimeout(); derr != nil {
			if err == nil {
				err = derr
			}
		}
	}

	// Re-read the live attributes before restoring: if an external process
	// changed them while the session was active, refuse to overwrite that
	// state and report the conflict.
	live, gerr := termios.Tcgetattr(uintptr(t.fd))
	if gerr == nil {
		s.mu.Lock()
		conflict := *live != s.committed
		s.mu.Unlock()
		if conflict {
			err = ErrTermiosConflict
		} else if rerr := termios.Tcsetattr(uintptr(t.fd), termios.TCSANOW, &s.baseline); rerr != nil && err == nil {
			err = rerr
		}
	} else if err == nil {
		err = gerr
	}

	s.mu.Lock()
	s.closeErr = err
	s.mu.Unlock()
	close(s.done)
	return err
}

// drainWithTimeout invokes the platform drain (TIOCDRAIN on BSD, TCSADRAIN
// on Linux/Solaris — the dialect difference lives in the termios package)
// and caps the wait at the configured DrainTimeout. The fd is captured up
// front so a drain still blocked when the timer fires never touches Term
// state that a concurrent Close may be tearing down.
func (s *Session) drainWithTimeout() error {
	fd := uintptr(s.t.fd)
	done := make(chan error, 1)
	go func() {
		done <- s.drain(fd)
	}()
	timer := time.NewTimer(s.drainTo)
	defer timer.Stop()
	select {
	case <-timer.C:
		return ErrDrainTimeout
	case err := <-done:
		return err
	}
}

// Close ends the session, draining output when configured and restoring the
// termios captured when the session began. It is idempotent: calling Close
// more than once, or after a context cancellation, SendBreak or Restart,
// returns the error recorded by the single cleanup run (nil on a clean
// shutdown) without touching the terminal again.
func (s *Session) Close() error {
	return s.shutdown(reasonClose)
}

// SendBreak transmits a break and then tears the session down through the
// same single cleanup path as Close. After it returns the session is closed
// and further reads and writes fail with ErrSessionClosed.
func (s *Session) SendBreak() error {
	if err := s.closedErr(); err != nil {
		return err
	}
	return s.shutdown(reasonBreak)
}

// Restart ends the session through the single cleanup path and immediately
// starts a fresh session with the same configuration. When called on a
// session that was already torn down it returns ErrSessionClosed without
// creating anything; if the fresh session cannot be started, the terminal
// retains the restored baseline attributes and the rollback/conflict error
// is returned.
func (s *Session) Restart() (*Session, error) {
	// shutdown itself is the single, idempotent cleanup path. If the session
	// had already been torn down (Close, cancel, break, an earlier restart),
	// there is nothing to restart.
	if s.closedErr() != nil {
		return nil, ErrSessionClosed
	}
	if err := s.shutdown(reasonRestart); err != nil {
		return nil, err
	}
	return s.t.BeginSession(SessionConfig{
		Ctx:          s.ctx,
		Options:      nil,
		DrainTimeout: s.drainTo,
		drain:        s.drain,
	})
}

// Done returns a channel that is closed once the session's cleanup path has
// completed.
func (s *Session) Done() <-chan struct{} {
	return s.done
}

// Err returns nil while the session is active and, after shutdown, the error
// recorded by the single cleanup run (nil for a clean shutdown).
func (s *Session) Err() error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if !s.closed {
		return nil
	}
	return s.closeErr
}

// closedErr returns ErrSessionClosed once shutdown has run, otherwise nil.
func (s *Session) closedErr() error {
	s.mu.Lock()
	closed := s.closed
	s.mu.Unlock()
	if closed {
		return ErrSessionClosed
	}
	return nil
}
