package lsp

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os/exec"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"
)

const (
	maxContentLength = 64 << 20 // 64 MiB
	stderrRingSize   = 200
)

// ErrClientClosed is returned when a call is aborted because the LSP client closed.
var ErrClientClosed = errors.New("lsp client closed")

// RPCError represents a JSON-RPC 2.0 error object returned by the server.
type RPCError struct {
	Code    int             `json:"code"`
	Message string          `json:"message"`
	Data    json.RawMessage `json:"data,omitempty"`
}

func (e *RPCError) Error() string {
	return fmt.Sprintf("jsonrpc error %d: %s", e.Code, e.Message)
}

// Handlers receives asynchronous server notifications and server-initiated requests.
type Handlers struct {
	OnNotification func(method string, params json.RawMessage)
	OnRequest      func(ctx context.Context, method string, params json.RawMessage) (any, error)
}

type rpcRequest struct {
	JSONRPC string `json:"jsonrpc"`
	ID      *int64 `json:"id,omitempty"`
	Method  string `json:"method"`
	Params  any    `json:"params,omitempty"`
}

type rpcResponse struct {
	JSONRPC string          `json:"jsonrpc"`
	ID      json.RawMessage `json:"id"`
	Result  any             `json:"result,omitempty"`
	Error   *RPCError       `json:"error,omitempty"`
}

type rpcIncoming struct {
	JSONRPC string          `json:"jsonrpc"`
	ID      json.RawMessage `json:"id,omitempty"`
	Method  string          `json:"method,omitempty"`
	Params  json.RawMessage `json:"params,omitempty"`
	Result  json.RawMessage `json:"result,omitempty"`
	Error   *RPCError       `json:"error,omitempty"`
}

type rpcResult struct {
	result json.RawMessage
	err    error
}

type writeItem struct {
	frame   []byte
	flushed chan struct{}
}

type rpcNotification struct {
	method string
	params json.RawMessage
}

type stderrRing struct {
	mu    sync.Mutex
	lines [stderrRingSize]string
	head  int
	count int
}

func (s *stderrRing) addLine(line string) {
	line = strings.TrimRight(line, "\r\n")
	s.mu.Lock()
	s.lines[s.head] = line
	s.head = (s.head + 1) % stderrRingSize
	if s.count < stderrRingSize {
		s.count++
	}
	s.mu.Unlock()
}

func (s *stderrRing) tail() []string {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.count == 0 {
		return nil
	}
	out := make([]string, s.count)
	start := (s.head - s.count + stderrRingSize) % stderrRingSize
	for i := 0; i < s.count; i++ {
		out[i] = s.lines[(start+i)%stderrRingSize]
	}
	return out
}

// Client is a JSON-RPC 2.0 Content-Length LSP client.
type Client struct {
	r        io.Reader
	w        io.WriteCloser
	handlers Handlers

	writeCh   chan writeItem
	closedCh  chan struct{}
	closeOnce sync.Once
	shutOnce  sync.Once

	pendingMu sync.Mutex
	pending   map[int64]chan rpcResult
	closed    bool

	nextID atomic.Int64

	notifMu    sync.Mutex
	notifQueue []rpcNotification
	notifSig   chan struct{}

	readDoneCh  chan struct{}
	writeDoneCh chan struct{}
	notifDoneCh chan struct{}
	doneCh      chan struct{}

	exitMu  sync.Mutex
	exitErr error

	cmd         *exec.Cmd
	job         *ProcessJob
	stderr      *stderrRing
	processMode bool
}

// NewPipeClient starts a JSON-RPC 2.0 client over an existing io.Reader / io.WriteCloser pair.
func NewPipeClient(r io.Reader, w io.WriteCloser, handlers Handlers) *Client {
	c := newClient(r, w, handlers, false)
	go c.finishPipeLifecycle()
	return c
}

// StartProcessClient spawns an LSP server subprocess inside a managed process
// job (Windows Job Object / Unix process group), captures up to 200 lines of
// stderr, and attaches a JSON-RPC 2.0 stdio Client.
func StartProcessClient(ctx context.Context, bin string, args []string, dir string, handlers Handlers) (*Client, error) {
	if ctx != nil && ctx.Err() != nil {
		return nil, ctx.Err()
	}
	cmd := exec.Command(bin, args...)
	if dir != "" {
		cmd.Dir = dir
	}

	stdin, err := cmd.StdinPipe()
	if err != nil {
		return nil, fmt.Errorf("stdin pipe: %w", err)
	}
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		_ = stdin.Close()
		return nil, fmt.Errorf("stdout pipe: %w", err)
	}
	stderrPipe, err := cmd.StderrPipe()
	if err != nil {
		_ = stdin.Close()
		_ = stdout.Close()
		return nil, fmt.Errorf("stderr pipe: %w", err)
	}

	job, err := startManagedProcess(cmd)
	if err != nil {
		_ = stdin.Close()
		_ = stdout.Close()
		_ = stderrPipe.Close()
		return nil, err
	}

	c := newClient(stdout, stdin, handlers, true)
	c.cmd = cmd
	c.job = job
	c.stderr = &stderrRing{}

	stderrDone := make(chan struct{})
	go func() {
		defer close(stderrDone)
		br := bufio.NewReaderSize(stderrPipe, 16*1024)
		for {
			line, rerr := br.ReadString('\n')
			if len(line) > 0 {
				c.stderr.addLine(line)
			}
			if rerr != nil {
				return
			}
		}
	}()

	go func() {
		<-c.readDoneCh
		<-stderrDone
		waitErr := cmd.Wait()
		c.setExitErr(waitErr)
		_ = job.Close()
		c.closeTransport()
		<-c.writeDoneCh
		<-c.notifDoneCh
		close(c.doneCh)
	}()

	return c, nil
}

func newClient(r io.Reader, w io.WriteCloser, handlers Handlers, processMode bool) *Client {
	c := &Client{
		r:           r,
		w:           w,
		handlers:    handlers,
		writeCh:     make(chan writeItem, 64),
		closedCh:    make(chan struct{}),
		pending:     make(map[int64]chan rpcResult),
		notifSig:    make(chan struct{}, 1),
		readDoneCh:  make(chan struct{}),
		writeDoneCh: make(chan struct{}),
		notifDoneCh: make(chan struct{}),
		doneCh:      make(chan struct{}),
		processMode: processMode,
	}
	go c.writeLoop()
	go c.notifLoop()
	go c.readLoop()
	return c
}

func (c *Client) finishPipeLifecycle() {
	<-c.readDoneCh
	c.closeTransport()
	<-c.writeDoneCh
	<-c.notifDoneCh
	close(c.doneCh)
}

func (c *Client) setExitErr(err error) {
	c.exitMu.Lock()
	if c.exitErr == nil && err != nil {
		c.exitErr = err
	}
	c.exitMu.Unlock()
}

// Done returns a channel that closes after the client transport (and managed
// subprocess, if any) has completely exited.
func (c *Client) Done() <-chan struct{} {
	return c.doneCh
}

// ExitErr returns the error that terminated the client or subprocess, if any.
func (c *Client) ExitErr() error {
	c.exitMu.Lock()
	defer c.exitMu.Unlock()
	return c.exitErr
}

// PendingCount returns the number of currently in-flight RPC calls.
func (c *Client) PendingCount() int {
	c.pendingMu.Lock()
	defer c.pendingMu.Unlock()
	return len(c.pending)
}

// StderrTail returns a chronological copy of up to 200 captured stderr lines.
func (c *Client) StderrTail() []string {
	if c.stderr == nil {
		return nil
	}
	return c.stderr.tail()
}

// JobAssigned reports whether a subprocess was attached to an OS process job/group.
func (c *Client) JobAssigned() bool {
	if c.job == nil {
		return false
	}
	return c.job.Assigned()
}

// Call sends a JSON-RPC 2.0 request and waits for its response or ctx cancellation.
func (c *Client) Call(ctx context.Context, method string, params, out any) error {
	if ctx == nil {
		ctx = context.Background()
	}
	if err := ctx.Err(); err != nil {
		return err
	}

	id := c.nextID.Add(1)
	respCh := make(chan rpcResult, 1)

	c.pendingMu.Lock()
	if c.closed {
		c.pendingMu.Unlock()
		return ErrClientClosed
	}
	c.pending[id] = respCh
	c.pendingMu.Unlock()

	defer func() {
		c.pendingMu.Lock()
		delete(c.pending, id)
		c.pendingMu.Unlock()
	}()

	req := rpcRequest{
		JSONRPC: "2.0",
		ID:      &id,
		Method:  method,
		Params:  params,
	}
	frame, err := encodeFrame(req)
	if err != nil {
		return err
	}
	if err := c.enqueueWrite(ctx, frame); err != nil {
		return err
	}

	select {
	case res := <-respCh:
		if res.err != nil {
			return res.err
		}
		if out != nil && len(res.result) > 0 && !bytes.Equal(bytes.TrimSpace(res.result), []byte("null")) {
			if err := json.Unmarshal(res.result, out); err != nil {
				return fmt.Errorf("unmarshal %s response: %w", method, err)
			}
		}
		return nil
	case <-ctx.Done():
		c.sendCancelBestEffort(id)
		return ctx.Err()
	}
}

// Notify sends a JSON-RPC 2.0 notification without waiting for a response.
func (c *Client) Notify(method string, params any) error {
	req := rpcRequest{
		JSONRPC: "2.0",
		Method:  method,
		Params:  params,
	}
	frame, err := encodeFrame(req)
	if err != nil {
		return err
	}
	return c.enqueueWrite(context.Background(), frame)
}

// Close performs a graceful LSP shutdown -> exit handshake, flushes pending
// writes, closes transport pipes, and terminates any managed subprocess.
// Safe and idempotent.
func (c *Client) Close(ctx context.Context) error {
	if ctx == nil {
		ctx = context.Background()
	}
	var closeErr error
	c.shutOnce.Do(func() {
		if !c.isClosed() {
			shutCtx, cancel := context.WithTimeout(ctx, 1500*time.Millisecond)
			_ = c.Call(shutCtx, "shutdown", nil, nil)
			cancel()

			_ = c.Notify("exit", nil)
			c.flushWrites(200 * time.Millisecond)
		}

		c.closeTransport()

		if c.processMode {
			select {
			case <-c.doneCh:
			case <-time.After(1 * time.Second):
				if c.job != nil {
					closeErr = c.job.Close()
				}
				select {
				case <-c.doneCh:
				case <-time.After(2 * time.Second):
				}
			}
		} else {
			select {
			case <-c.doneCh:
			case <-time.After(1 * time.Second):
			}
		}
	})
	return closeErr
}

func (c *Client) sendCancelBestEffort(id int64) {
	req := rpcRequest{
		JSONRPC: "2.0",
		Method:  "$/cancelRequest",
		Params:  CancelParams{ID: id},
	}
	frame, err := encodeFrame(req)
	if err != nil {
		return
	}
	select {
	case <-c.closedCh:
		return
	default:
	}
	select {
	case c.writeCh <- writeItem{frame: frame}:
	default:
	}
}

func (c *Client) enqueueWrite(ctx context.Context, frame []byte) error {
	select {
	case <-c.closedCh:
		return ErrClientClosed
	default:
	}
	select {
	case c.writeCh <- writeItem{frame: frame}:
		return nil
	case <-c.closedCh:
		return ErrClientClosed
	case <-ctx.Done():
		return ctx.Err()
	}
}

func (c *Client) flushWrites(timeout time.Duration) {
	flushed := make(chan struct{})
	select {
	case <-c.closedCh:
		return
	case c.writeCh <- writeItem{flushed: flushed}:
	case <-time.After(timeout):
		return
	}
	select {
	case <-flushed:
	case <-c.closedCh:
	case <-time.After(timeout):
	}
}

func (c *Client) isClosed() bool {
	c.pendingMu.Lock()
	defer c.pendingMu.Unlock()
	return c.closed
}

func (c *Client) closeTransport() {
	c.closeOnce.Do(func() {
		c.failAllPending(ErrClientClosed)
		close(c.closedCh)
		if c.w != nil {
			_ = c.w.Close()
		}
		if rc, ok := c.r.(io.Closer); ok {
			_ = rc.Close()
		}
	})
}

func (c *Client) failAllPending(err error) {
	c.pendingMu.Lock()
	c.closed = true
	pending := c.pending
	c.pending = make(map[int64]chan rpcResult)
	c.pendingMu.Unlock()

	for _, ch := range pending {
		select {
		case ch <- rpcResult{err: err}:
		default:
		}
	}
}

func (c *Client) writeLoop() {
	defer close(c.writeDoneCh)
	for {
		select {
		case item := <-c.writeCh:
			if len(item.frame) > 0 && c.w != nil {
				if _, err := c.w.Write(item.frame); err != nil {
					c.setExitErr(err)
					if item.flushed != nil {
						close(item.flushed)
					}
					c.closeTransport()
					return
				}
			}
			if item.flushed != nil {
				close(item.flushed)
			}
		case <-c.closedCh:
			return
		}
	}
}

func (c *Client) readLoop() {
	defer close(c.readDoneCh)
	br := bufio.NewReaderSize(c.r, 64*1024)
	for {
		payload, err := readFrame(br)
		if err != nil {
			if err != io.EOF {
				c.setExitErr(err)
			}
			c.failAllPending(ErrClientClosed)
			return
		}
		var msg rpcIncoming
		if err := json.Unmarshal(payload, &msg); err != nil {
			continue
		}
		c.dispatchIncoming(msg)
	}
}

func (c *Client) dispatchIncoming(msg rpcIncoming) {
	trimmedID := bytes.TrimSpace(msg.ID)
	hasID := len(trimmedID) > 0 && !bytes.Equal(trimmedID, []byte("null"))

	if msg.Method != "" && hasID {
		rawID := append(json.RawMessage(nil), trimmedID...)
		params := append(json.RawMessage(nil), msg.Params...)
		go c.handleServerRequest(rawID, msg.Method, params)
		return
	}

	if msg.Method != "" {
		params := append(json.RawMessage(nil), msg.Params...)
		c.enqueueNotification(rpcNotification{method: msg.Method, params: params})
		return
	}

	if hasID {
		id, err := strconv.ParseInt(string(trimmedID), 10, 64)
		if err != nil {
			return
		}
		c.pendingMu.Lock()
		ch, ok := c.pending[id]
		if ok {
			delete(c.pending, id)
		}
		c.pendingMu.Unlock()
		if ok {
			if msg.Error != nil {
				ch <- rpcResult{err: msg.Error}
			} else {
				ch <- rpcResult{result: msg.Result}
			}
		}
	}
}

func (c *Client) handleServerRequest(rawID json.RawMessage, method string, params json.RawMessage) {
	var result any
	var reqErr error

	if c.handlers.OnRequest != nil {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		result, reqErr = c.handlers.OnRequest(ctx, method, params)
		cancel()
	} else {
		result = defaultServerRequestResult(method, params)
	}

	resp := rpcResponse{
		JSONRPC: "2.0",
		ID:      rawID,
	}
	if reqErr != nil {
		if rpcErr, ok := reqErr.(*RPCError); ok {
			resp.Error = rpcErr
		} else {
			resp.Error = &RPCError{Code: -32603, Message: reqErr.Error()}
		}
	} else {
		resp.Result = result
	}

	frame, err := encodeFrame(resp)
	if err != nil {
		return
	}
	_ = c.enqueueWrite(context.Background(), frame)
}

func defaultServerRequestResult(method string, params json.RawMessage) any {
	if method == "workspace/configuration" {
		var cp ConfigurationParams
		if err := json.Unmarshal(params, &cp); err == nil {
			out := make([]any, len(cp.Items))
			return out
		}
		return []any{}
	}
	return nil
}

func (c *Client) enqueueNotification(n rpcNotification) {
	c.notifMu.Lock()
	c.notifQueue = append(c.notifQueue, n)
	c.notifMu.Unlock()

	select {
	case c.notifSig <- struct{}{}:
	default:
	}
}

func (c *Client) notifLoop() {
	defer close(c.notifDoneCh)
	for {
		c.drainNotifications()
		select {
		case <-c.notifSig:
		case <-c.readDoneCh:
			c.drainNotifications()
			return
		case <-c.closedCh:
			return
		}
	}
}

func (c *Client) drainNotifications() {
	for {
		c.notifMu.Lock()
		if len(c.notifQueue) == 0 {
			c.notifMu.Unlock()
			return
		}
		n := c.notifQueue[0]
		copy(c.notifQueue, c.notifQueue[1:])
		c.notifQueue[len(c.notifQueue)-1] = rpcNotification{}
		c.notifQueue = c.notifQueue[:len(c.notifQueue)-1]
		c.notifMu.Unlock()

		if c.handlers.OnNotification != nil {
			c.handlers.OnNotification(n.method, n.params)
		}
	}
}

func encodeFrame(v any) ([]byte, error) {
	body, err := json.Marshal(v)
	if err != nil {
		return nil, err
	}
	header := fmt.Sprintf("Content-Length: %d\r\n\r\n", len(body))
	frame := make([]byte, len(header)+len(body))
	copy(frame, header)
	copy(frame[len(header):], body)
	return frame, nil
}

func readFrame(br *bufio.Reader) ([]byte, error) {
	contentLength := -1
	for {
		line, err := br.ReadString('\n')
		if err != nil {
			if err == io.EOF && line == "" && contentLength < 0 {
				return nil, io.EOF
			}
			return nil, err
		}
		trimmed := strings.TrimRight(line, "\r\n")
		if trimmed == "" {
			break
		}
		colon := strings.IndexByte(trimmed, ':')
		if colon <= 0 {
			continue
		}
		key := strings.TrimSpace(trimmed[:colon])
		val := strings.TrimSpace(trimmed[colon+1:])
		if strings.EqualFold(key, "Content-Length") {
			n, perr := strconv.Atoi(val)
			if perr != nil || n < 0 || n > maxContentLength {
				return nil, fmt.Errorf("invalid Content-Length %q", val)
			}
			contentLength = n
		}
	}

	if contentLength < 0 {
		return nil, errors.New("missing Content-Length header")
	}

	payload := make([]byte, contentLength)
	if _, err := io.ReadFull(br, payload); err != nil {
		return nil, err
	}
	return payload, nil
}
