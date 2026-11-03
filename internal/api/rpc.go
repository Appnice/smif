// Package api is Mailroom's wire protocol: newline-delimited JSON-RPC 2.0
// over a Unix socket (RFC 5.4), plus the request and response types of every
// method in RFC 7.1.
package api

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/appnice/mailroom/internal/auth"
)

// ProtocolVersion is sent with every request; the major part must match.
const ProtocolVersion = "1.0"

// MaxLine caps one request or response line (a 25 MiB attachment in base64
// plus framing).
const MaxLine = 40 << 20

// Auth identifies the caller.
type Auth struct {
	Agent  string `json:"agent"`
	Token  string `json:"token"`
	Origin string `json:"origin,omitempty"`
}

// Request is a JSON-RPC request with Mailroom's auth and version members.
type Request struct {
	JSONRPC         string          `json:"jsonrpc"`
	ID              int64           `json:"id"`
	Method          string          `json:"method"`
	Params          json.RawMessage `json:"params,omitempty"`
	Auth            Auth            `json:"auth"`
	ProtocolVersion string          `json:"protocol_version"`
}

// Response is a JSON-RPC response.
type Response struct {
	JSONRPC string          `json:"jsonrpc"`
	ID      int64           `json:"id"`
	Result  json.RawMessage `json:"result,omitempty"`
	Error   *Error          `json:"error,omitempty"`
}

// Error is a JSON-RPC error object.
type Error struct {
	Code    int    `json:"code"`
	Message string `json:"message"`
}

func (e *Error) Error() string { return fmt.Sprintf("%s (code %d)", e.Message, e.Code) }

// Error codes. Negative numbers below -32000 are server-defined.
const (
	CodeParse       = -32700
	CodeInvalid     = -32600
	CodeNoMethod    = -32601
	CodeParams      = -32602
	CodeInternal    = -32603
	CodeAuth        = -32001
	CodeForbidden   = -32002
	CodeRejected    = -32003
	CodeNotFound    = -32004
	CodeUnavailable = -32005
	CodeVersion     = -32006
	CodeRateLimited = -32007
)

// Errf builds an *Error.
func Errf(code int, format string, a ...any) *Error {
	return &Error{Code: code, Message: fmt.Sprintf(format, a...)}
}

// Caller is the authenticated principal of a request.
type Caller = auth.Principal

// Handler serves one method.
type Handler func(ctx context.Context, c Caller, params json.RawMessage) (any, error)

// Server serves JSON-RPC over a Unix socket.
type Server struct {
	Auth     func(a Auth) (Caller, error)
	OnDenied func(method string, a Auth, err error)
	Log      *slog.Logger

	mu       sync.RWMutex
	methods  map[string]Handler
	ln       net.Listener
	wg       sync.WaitGroup
	closed   atomic.Bool
	uid      int
	connsMu  sync.Mutex
	conns    map[net.Conn]struct{}
	baseCtx  context.Context
	cancelFn context.CancelFunc
}

// NewServer returns a server with no methods.
func NewServer() *Server {
	ctx, cancel := context.WithCancel(context.Background())
	return &Server{methods: map[string]Handler{}, uid: os.Getuid(), conns: map[net.Conn]struct{}{}, baseCtx: ctx, cancelFn: cancel}
}

// Handle registers a method.
func (s *Server) Handle(method string, h Handler) {
	s.mu.Lock()
	s.methods[method] = h
	s.mu.Unlock()
}

// Methods lists registered method names.
func (s *Server) Methods() []string {
	s.mu.RLock()
	defer s.mu.RUnlock()
	var out []string
	for m := range s.methods {
		out = append(out, m)
	}
	return out
}

// Listen binds the socket with mode 0600 inside a 0700 directory.
func (s *Server) Listen(path string) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return err
	}
	if c, err := net.DialTimeout("unix", path, 200*time.Millisecond); err == nil {
		c.Close()
		return fmt.Errorf("another daemon is already listening on %s", path)
	}
	_ = os.Remove(path)
	old := syscallUmask(0o177)
	ln, err := net.Listen("unix", path)
	syscallUmask(old)
	if err != nil {
		return err
	}
	if err := os.Chmod(path, 0o600); err != nil {
		ln.Close()
		return err
	}
	s.ln = ln
	return nil
}

// Serve accepts connections until Close.
func (s *Server) Serve() error {
	for {
		c, err := s.ln.Accept()
		if err != nil {
			if s.closed.Load() {
				return nil
			}
			return err
		}
		s.wg.Add(1)
		go func() {
			defer s.wg.Done()
			s.serveConn(c)
		}()
	}
}

// Close stops accepting, cancels in-flight calls and closes connections.
func (s *Server) Close() {
	s.closed.Store(true)
	s.cancelFn()
	if s.ln != nil {
		s.ln.Close()
	}
	s.connsMu.Lock()
	for c := range s.conns {
		c.Close()
	}
	s.connsMu.Unlock()
	s.wg.Wait()
}

func (s *Server) serveConn(c net.Conn) {
	defer c.Close()
	s.connsMu.Lock()
	s.conns[c] = struct{}{}
	s.connsMu.Unlock()
	defer func() {
		s.connsMu.Lock()
		delete(s.conns, c)
		s.connsMu.Unlock()
	}()
	if uc, ok := c.(*net.UnixConn); ok {
		uid, err := auth.PeerUID(uc)
		if err != nil || uid != s.uid {
			if s.OnDenied != nil {
				s.OnDenied("connect", Auth{}, fmt.Errorf("peer uid %d is not daemon uid %d (%v)", uid, s.uid, err))
			}
			return
		}
	}
	r := bufio.NewReaderSize(c, 64<<10)
	var wmu sync.Mutex
	write := func(resp *Response) {
		b, _ := json.Marshal(resp)
		wmu.Lock()
		c.Write(append(b, '\n'))
		wmu.Unlock()
	}
	var inflight sync.WaitGroup
	defer inflight.Wait()
	for {
		line, err := readLine(r)
		if err != nil {
			return
		}
		var req Request
		if err := json.Unmarshal(line, &req); err != nil {
			write(&Response{JSONRPC: "2.0", Error: Errf(CodeParse, "parse error: %v", err)})
			continue
		}
		inflight.Add(1)
		go func() {
			defer inflight.Done()
			write(s.dispatch(&req))
		}()
	}
}

func readLine(r *bufio.Reader) ([]byte, error) {
	var buf []byte
	for {
		chunk, isPrefix, err := r.ReadLine()
		if err != nil {
			return nil, err
		}
		buf = append(buf, chunk...)
		if len(buf) > MaxLine {
			return nil, errors.New("line too long")
		}
		if !isPrefix {
			return buf, nil
		}
	}
}

func (s *Server) dispatch(req *Request) *Response {
	resp := &Response{JSONRPC: "2.0", ID: req.ID}
	major := strings.SplitN(req.ProtocolVersion, ".", 2)[0]
	if major != strings.SplitN(ProtocolVersion, ".", 2)[0] {
		resp.Error = Errf(CodeVersion, "unsupported protocol_version %q (daemon speaks %s)", req.ProtocolVersion, ProtocolVersion)
		return resp
	}
	s.mu.RLock()
	h := s.methods[req.Method]
	s.mu.RUnlock()
	if h == nil {
		resp.Error = Errf(CodeNoMethod, "unknown method %q", req.Method)
		return resp
	}
	caller, err := s.Auth(req.Auth)
	if err != nil {
		if s.OnDenied != nil {
			s.OnDenied(req.Method, req.Auth, err)
		}
		resp.Error = Errf(CodeAuth, "%v", err)
		return resp
	}
	out, err := h(s.baseCtx, caller, req.Params)
	if err != nil {
		var e *Error
		if errors.As(err, &e) {
			resp.Error = e
		} else {
			resp.Error = Errf(CodeInternal, "%v", err)
		}
		return resp
	}
	b, err := json.Marshal(out)
	if err != nil {
		resp.Error = Errf(CodeInternal, "encode: %v", err)
		return resp
	}
	resp.Result = b
	return resp
}

// Client is a connection to the daemon.
type Client struct {
	conn net.Conn
	r    *bufio.Reader
	auth Auth
	mu   sync.Mutex
	next int64
}

// Dial connects with a timeout.
func Dial(socket string, a Auth, timeout time.Duration) (*Client, error) {
	c, err := net.DialTimeout("unix", socket, timeout)
	if err != nil {
		return nil, fmt.Errorf("mailroom daemon is not reachable at %s: %w", socket, err)
	}
	return &Client{conn: c, r: bufio.NewReaderSize(c, 64<<10), auth: a}, nil
}

// Close closes the connection.
func (c *Client) Close() error { return c.conn.Close() }

// Call invokes method and decodes the result into out (if non-nil).
// timeout 0 means no deadline.
func (c *Client) Call(method string, params, out any, timeout time.Duration) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.next++
	var raw json.RawMessage
	if params != nil {
		b, err := json.Marshal(params)
		if err != nil {
			return err
		}
		raw = b
	}
	req := Request{JSONRPC: "2.0", ID: c.next, Method: method, Params: raw, Auth: c.auth, ProtocolVersion: ProtocolVersion}
	b, _ := json.Marshal(req)
	if timeout > 0 {
		c.conn.SetDeadline(time.Now().Add(timeout))
		defer c.conn.SetDeadline(time.Time{})
	}
	if _, err := c.conn.Write(append(b, '\n')); err != nil {
		return err
	}
	for {
		line, err := readLine(c.r)
		if err != nil {
			if errors.Is(err, io.EOF) {
				return errors.New("daemon closed the connection")
			}
			return err
		}
		var resp Response
		if err := json.Unmarshal(line, &resp); err != nil {
			return err
		}
		if resp.ID != c.next {
			continue
		}
		if resp.Error != nil {
			return resp.Error
		}
		if out != nil && len(resp.Result) > 0 {
			return json.Unmarshal(resp.Result, out)
		}
		return nil
	}
}

// IsCode reports whether err is an *Error with the given code.
func IsCode(err error, code int) bool {
	var e *Error
	return errors.As(err, &e) && e.Code == code
}
