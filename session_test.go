// +build !windows

package term

import (
	"context"
	"errors"
	"os"
	"testing"
	"time"

	"github.com/pkg/term/termios"
	"golang.org/x/sys/unix"
)

// sessionDev opens a pty-backed terminal and returns the Term together with
// the pty master, which is kept open for the lifetime of the test.
func sessionDev(t *testing.T) (*Term, *os.File) {
	ptm, pts, err := termios.Pty()
	if err != nil {
		t.Fatal(err)
	}
	tt, err := Open(pts.Name())
	if err != nil {
		ptm.Close()
		pts.Close()
		t.Fatal(err)
	}
	pts.Close()
	return tt, ptm
}

func attrOf(t *testing.T, tt *Term) unix.Termios {
	a, err := termios.Tcgetattr(uintptr(tt.fd))
	if err != nil {
		t.Fatal(err)
	}
	return *a
}

func TestSessionRestore(t *testing.T) {
	tt, ptm := sessionDev(t)
	defer ptm.Close()
	defer tt.Close()

	before := attrOf(t, tt)

	s, err := tt.BeginSession(SessionConfig{Options: []func(*Term) error{RawMode, Speed(9600)}})
	if err != nil {
		t.Fatal(err)
	}
	if err := s.Close(); err != nil {
		t.Fatal(err)
	}
	if after := attrOf(t, tt); after != before {
		t.Fatalf("termios not restored after close:\nbefore=%+v\nafter =%+v", before, after)
	}
	if _, err := tt.Write([]byte("x")); err != ErrSessionClosed {
		t.Fatalf("write after close = %v, want ErrSessionClosed", err)
	}
	if _, err := tt.Read(make([]byte, 1)); err != ErrSessionClosed {
		t.Fatalf("read after close = %v, want ErrSessionClosed", err)
	}
}

func TestSessionRollbackOnFailedOption(t *testing.T) {
	tt, ptm := sessionDev(t)
	defer ptm.Close()
	defer tt.Close()

	before := attrOf(t, tt)

	// RawMode is applied first, then an invalid baud rate fails: the raw
	// change must be rolled back to the snapshot.
	_, err := tt.BeginSession(SessionConfig{Options: []func(*Term) error{RawMode, Speed(12345)}})
	if err != unix.EINVAL {
		t.Fatalf("BeginSession err = %v, want EINVAL", err)
	}
	if after := attrOf(t, tt); after != before {
		t.Fatalf("termios not rolled back after failed option:\nbefore=%+v\nafter =%+v", before, after)
	}
}

func TestSessionContextCancel(t *testing.T) {
	tt, ptm := sessionDev(t)
	defer ptm.Close()
	defer tt.Close()

	before := attrOf(t, tt)
	ctx, cancel := context.WithCancel(context.Background())

	s, err := tt.BeginSession(SessionConfig{Ctx: ctx, Options: []func(*Term) error{RawMode}})
	if err != nil {
		t.Fatal(err)
	}

	cancel()
	select {
	case <-s.Done():
	case <-time.After(2 * time.Second):
		t.Fatal("session cleanup did not run after context cancel")
	}
	if after := attrOf(t, tt); after != before {
		t.Fatalf("termios not restored after cancel:\nbefore=%+v\nafter =%+v", before, after)
	}
	if _, err := tt.Write([]byte("x")); err != ErrSessionClosed {
		t.Fatalf("write after cancel = %v, want ErrSessionClosed", err)
	}
	if err := s.SendBreak(); err != ErrSessionClosed {
		t.Fatalf("SendBreak after cancel = %v, want ErrSessionClosed", err)
	}
}

func TestSessionCloseIdempotent(t *testing.T) {
	tt, ptm := sessionDev(t)
	defer ptm.Close()
	defer tt.Close()

	s, err := tt.BeginSession(SessionConfig{Options: []func(*Term) error{RawMode}})
	if err != nil {
		t.Fatal(err)
	}
	if err := s.Close(); err != nil {
		t.Fatal(err)
	}
	if err := s.Close(); err != nil {
		t.Fatalf("second Close = %v, want nil", err)
	}
	if err := s.Err(); err != nil {
		t.Fatalf("clean shutdown Err = %v, want nil", err)
	}
}

func TestSessionSendBreakCloses(t *testing.T) {
	tt, ptm := sessionDev(t)
	defer ptm.Close()
	defer tt.Close()

	before := attrOf(t, tt)
	s, err := tt.BeginSession(SessionConfig{})
	if err != nil {
		t.Fatal(err)
	}
	if err := s.SendBreak(); err != nil {
		t.Fatal(err)
	}
	if after := attrOf(t, tt); after != before {
		t.Fatal("termios not restored after SendBreak")
	}
	if _, err := tt.Read(make([]byte, 1)); err != ErrSessionClosed {
		t.Fatalf("read after SendBreak = %v, want ErrSessionClosed", err)
	}
	// break shares the single cleanup path: it must not run twice.
	if err := s.Close(); err != nil {
		t.Fatalf("Close after SendBreak = %v, want nil", err)
	}
}

func TestSessionRestart(t *testing.T) {
	tt, ptm := sessionDev(t)
	defer ptm.Close()
	defer tt.Close()

	s, err := tt.BeginSession(SessionConfig{Options: []func(*Term) error{RawMode}})
	if err != nil {
		t.Fatal(err)
	}
	s2, err := s.Restart()
	if err != nil {
		t.Fatal(err)
	}
	if err := s.SendBreak(); err != ErrSessionClosed {
		t.Fatalf("old session SendBreak = %v, want ErrSessionClosed", err)
	}
	if err := s2.Close(); err != nil {
		t.Fatal(err)
	}
	if _, err := s2.Restart(); err != ErrSessionClosed {
		t.Fatalf("Restart after close = %v, want ErrSessionClosed", err)
	}
}

func TestSessionNestedConflict(t *testing.T) {
	tt, ptm := sessionDev(t)
	defer ptm.Close()
	defer tt.Close()

	outer, err := tt.BeginSession(SessionConfig{Options: []func(*Term) error{RawMode}})
	if err != nil {
		t.Fatal(err)
	}
	defer outer.Close()

	// An external process (here, a second termios handle on the same fd)
	// changes the terminal behind the session's back.
	a := attrOf(t, tt)
	a.Cc[unix.VMIN] = 0
	a.Cc[unix.VTIME] = 5
	if err := termios.Tcsetattr(uintptr(tt.fd), termios.TCSANOW, &a); err != nil {
		t.Fatal(err)
	}

	if _, err := tt.BeginSession(SessionConfig{}); err != ErrTermiosConflict {
		t.Fatalf("nested BeginSession = %v, want ErrTermiosConflict", err)
	}

	// The external modification is left untouched.
	if got := attrOf(t, tt); got != a {
		t.Fatal("conflicting terminal state was overwritten")
	}
}

func TestSessionCloseConflict(t *testing.T) {
	tt, ptm := sessionDev(t)
	defer ptm.Close()
	defer tt.Close()

	s, err := tt.BeginSession(SessionConfig{Options: []func(*Term) error{RawMode}})
	if err != nil {
		t.Fatal(err)
	}

	// External modification while the session is active: Close must refuse to
	// overwrite and report the conflict instead.
	a := attrOf(t, tt)
	a.Cc[unix.VMIN] = 0
	a.Cc[unix.VTIME] = 7
	if err := termios.Tcsetattr(uintptr(tt.fd), termios.TCSANOW, &a); err != nil {
		t.Fatal(err)
	}

	if err := s.Close(); err != ErrTermiosConflict {
		t.Fatalf("Close = %v, want ErrTermiosConflict", err)
	}
	if got := attrOf(t, tt); got != a {
		t.Fatal("conflicting terminal state was overwritten on close")
	}
}

func TestSessionNestedRestoresOuterState(t *testing.T) {
	tt, ptm := sessionDev(t)
	defer ptm.Close()
	defer tt.Close()

	outer, err := tt.BeginSession(SessionConfig{Options: []func(*Term) error{RawMode}})
	if err != nil {
		t.Fatal(err)
	}
	outerState := attrOf(t, tt)

	inner, err := tt.BeginSession(SessionConfig{Options: []func(*Term) error{Speed(9600)}})
	if err != nil {
		t.Fatal(err)
	}
	if err := inner.Close(); err != nil {
		t.Fatal(err)
	}
	if got := attrOf(t, tt); got != outerState {
		t.Fatalf("inner close did not restore outer session state:\nwant=%+v\ngot =%+v", outerState, got)
	}
	if err := outer.Close(); err != nil {
		t.Fatal(err)
	}
}

func TestSessionDrainTimeoutStillRestores(t *testing.T) {
	tt, ptm := sessionDev(t)
	defer ptm.Close()
	defer tt.Close()

	before := attrOf(t, tt)
	blocked := make(chan struct{})
	cfg := SessionConfig{
		DrainTimeout: 50 * time.Millisecond,
		drain: func(fd uintptr) error {
			<-blocked // simulate a wedged drain that never returns
			return nil
		},
	}
	s, err := tt.BeginSession(cfg)
	if err != nil {
		t.Fatal(err)
	}

	err = s.Close()
	close(blocked)
	if !errors.Is(err, ErrDrainTimeout) {
		t.Fatalf("Close = %v, want ErrDrainTimeout", err)
	}
	// Timeout must not skip the restore, nor pretend the data was delivered.
	if after := attrOf(t, tt); after != before {
		t.Fatal("termios not restored after drain timeout")
	}
	if err := s.Close(); !errors.Is(err, ErrDrainTimeout) {
		t.Fatalf("second Close = %v, want recorded ErrDrainTimeout", err)
	}
}
