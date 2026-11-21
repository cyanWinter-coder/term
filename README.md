
# term
    import "github.com/pkg/term"

Package term manages POSIX terminals. As POSIX terminals are connected to,
or emulate, a UART, this package also provides control over the various
UART and serial line parameters.






## func CBreakMode
``` go
func CBreakMode(t *Term) error
```
CBreakMode places the terminal into cbreak mode.


## func RawMode
``` go
func RawMode(t *Term) error
```
RawMode places the terminal into raw mode.


## func Speed
``` go
func Speed(baud int) func(*Term) error
```
Speed sets the baud rate option for the terminal.



## type Term
``` go
type Term struct {
    // contains filtered or unexported fields
}
```
Term represents an asynchronous communications port.









### func Open
``` go
func Open(name string, options ...func(*Term) error) (*Term, error)
```
Open opens an asynchronous communications port.




### func (\*Term) Available
``` go
func (t *Term) Available() (int, error)
```
Available returns how many bytes are unused in the buffer.



### func (\*Term) Buffered
``` go
func (t *Term) Buffered() (int, error)
```
Buffered returns the number of bytes that have been written into the current buffer.



### func (\*Term) Close
``` go
func (t *Term) Close() error
```
Close closes the device and releases any associated resources.



### func (\*Term) DTR
``` go
func (t *Term) DTR() (bool, error)
```
DTR returns the state of the DTR (data terminal ready) signal.



### func (\*Term) Flush
``` go
func (t *Term) Flush() error
```
Flush flushes both data received but not read, and data written but not transmitted.



### func (\*Term) RTS
``` go
func (t *Term) RTS() (bool, error)
```
RTS returns the state of the RTS (data terminal ready) signal.



### func (\*Term) Read
``` go
func (t *Term) Read(b []byte) (int, error)
```
Read reads up to len(b) bytes from the terminal. It returns the number of
bytes read and an error, if any. EOF is signaled by a zero count with
err set to io.EOF.



### func (\*Term) Restore
``` go
func (t *Term) Restore() error
```
Restore restores the state of the terminal captured at the point that
the terminal was originally opened.



### func (\*Term) SendBreak
``` go
func (t *Term) SendBreak() error
```
SendBreak sends a break signal.



### func (\*Term) SetCbreak
``` go
func (t *Term) SetCbreak() error
```
SetCbreak sets cbreak mode.



### func (\*Term) SetDTR
``` go
func (t *Term) SetDTR(v bool) error
```
SetDTR sets the DTR (data terminal ready) signal.



### func (\*Term) SetOption
``` go
func (t *Term) SetOption(options ...func(*Term) error) error
```
SetOption takes one or more option function and applies them in order to Term.



### func (\*Term) SetRTS
``` go
func (t *Term) SetRTS(v bool) error
```
SetRTS sets the RTS (data terminal ready) signal.



### func (\*Term) SetRaw
``` go
func (t *Term) SetRaw() error
```
SetRaw sets raw mode.



### func (\*Term) SetSpeed
``` go
func (t *Term) SetSpeed(baud int) error
```
SetSpeed sets the receive and transmit baud rates.



### func (\*Term) Write
``` go
func (t *Term) Write(b []byte) (int, error)
```
Write writes len(b) bytes to the terminal. It returns the number of bytes
written and an error, if any. Write returns a non-nil error when n !=
len(b).




### func (\*Term) BeginSession
``` go
func (t *Term) BeginSession(cfg SessionConfig) (*Session, error)
```
BeginSession starts a transactional terminal session. The complete current
termios is captured first, then cfg.Options are applied in order; if any
option fails the captured state is rolled back and no session is created.
The session restores the captured termios through a single cleanup path when
Close is called, the context is cancelled, SendBreak is used, or Restart
replaces the session. Nested sessions reject the operation with
ErrTermiosConflict when another process has modified the terminal.




## type SessionConfig
``` go
type SessionConfig struct {
    Ctx          context.Context
    Options      []func(*Term) error
    DrainTimeout time.Duration
}
```
SessionConfig configures a transactional terminal session. DrainTimeout
bounds how long Close waits for queued output to drain; on timeout the local
attributes are still restored and Close returns ErrDrainTimeout.




## type Session
``` go
type Session struct {
    // contains filtered or unexported fields
}
```
Session is a transactional terminal session. Close is idempotent; after the
cleanup path has run, Read and Write return ErrSessionClosed.


### func (\*Session) Close
``` go
func (s *Session) Close() error
```
Close ends the session, optionally drains output, and restores the termios
captured when the session began. It is safe to call more than once.


### func (\*Session) SendBreak
``` go
func (s *Session) SendBreak() error
```
SendBreak transmits a break and then runs the same single cleanup path as
Close.


### func (\*Session) Restart
``` go
func (s *Session) Restart() (*Session, error)
```
Restart tears the session down through the single cleanup path and starts a
fresh session with the same configuration.


### func (\*Session) Done
``` go
func (s *Session) Done() <-chan struct{}
```
Done returns a channel closed once the cleanup path has completed.


### func (\*Session) Err
``` go
func (s *Session) Err() error
```
Err returns the error recorded by the cleanup run, or nil while active and
after a clean shutdown.









- - -
Generated by [godoc2md](http://godoc.org/github.com/davecheney/godoc2md)