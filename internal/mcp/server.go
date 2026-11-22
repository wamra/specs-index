// Package mcp is a hand-written Model Context Protocol server for specs-index:
// newline-delimited JSON-RPC 2.0 over stdio, no SDK.
//
// Protocol revisions (checked 2026-11-21 against the specification sources in
// https://github.com/modelcontextprotocol/modelcontextprotocol, docs/specification/,
// the repository behind https://modelcontextprotocol.io/specification; the
// website itself was unreachable from the build machine):
//
//   - 2026-07-28 is the newest revision. It is stateless: no initialize
//     handshake, every request carries _meta["io.modelcontextprotocol/protocolVersion"],
//     the server must implement server/discover, results carry resultType, list
//     results carry ttlMs and cacheScope, and an unsupported version is answered
//     with error -32022 listing the supported versions.
//   - 2025-11-25 (and 2025-06-18, 2025-03-26, 2024-11-05) use the initialize
//     handshake. The server must echo the client's protocolVersion when it
//     supports it and otherwise answer with its own latest.
//
// This server is dual-era, as the versioning page allows: a request that
// carries the protocol version in _meta is served statelessly, initialize
// selects the handshake revisions. Both eras share the same tools.
//
// stdout carries protocol messages only; everything else goes to stderr.
package mcp

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"sync"
)

// ServerVersion is reported in serverInfo; the CLI sets it from its own version.
var ServerVersion = "dev"

const (
	modernVersion = "2026-07-28"
	// maxLine bounds one message. A longer line is discarded, answered with an
	// error, and the reader carries on with the next line.
	maxLine = 16 << 20
	// queueSize is how many tools/call requests may wait behind a running one.
	queueSize = 256
)

// handshakeVersions are the initialize-era revisions this server speaks,
// newest first; the first is the answer to an unsupported request.
var handshakeVersions = []string{"2025-11-25", "2025-06-18", "2025-03-26", "2024-11-05"}

// supportedVersions is the list sent in -32022 and in server/discover.
func supportedVersions() []string { return append([]string{modernVersion}, handshakeVersions...) }

// JSON-RPC error codes. The -32022 code is the MCP UnsupportedProtocolVersion error.
const (
	codeParse          = -32700
	codeInvalidRequest = -32600
	codeMethodNotFound = -32601
	codeInvalidParams  = -32602
	codeInternal       = -32603
	codeUnsupportedVer = -32022
)

const metaVersionKey = "io.modelcontextprotocol/protocolVersion"

const serverInstructions = "specs-index searches, traces and checks coverage over the project's Markdown requirement and design documents and Gherkin .feature files. " +
	"Register sources with add_source, then run index (and again after sources or files change, or when results are marked stale). " +
	"Use search to find an entity, show to read one with its edges, trace to follow links between requirements, designs and scenarios, coverage to list gaps. " +
	"Source files are never modified; the index lives under .Wammy/."

type request struct {
	JSONRPC string          `json:"jsonrpc"`
	ID      json.RawMessage `json:"id"` // absent for notifications
	Method  string          `json:"method"`
	Params  json.RawMessage `json:"params"`
	// Result and Error identify a response from the client, which is ignored.
	Result json.RawMessage `json:"result"`
	Error  json.RawMessage `json:"error"`
}

type rpcError struct {
	Code    int    `json:"code"`
	Message string `json:"message"`
	Data    any    `json:"data,omitempty"`
}

type response struct {
	JSONRPC string          `json:"jsonrpc"`
	ID      json.RawMessage `json:"id"`
	Result  any             `json:"result,omitempty"`
	Error   *rpcError       `json:"error,omitempty"`
}

type server struct {
	root string
	log  io.Writer // stderr

	wmu sync.Mutex
	enc *json.Encoder

	mu       sync.Mutex
	inflight map[string]*inflight // by raw request id
	version  string               // negotiated handshake revision ("" before initialize)
	fatalErr error                // first stdout write failure
	stop     context.CancelFunc   // ends Serve
}

func newServer(root string, out io.Writer, log io.Writer) *server {
	enc := json.NewEncoder(out)
	enc.SetEscapeHTML(false)
	return &server{root: root, log: log, enc: enc, inflight: map[string]*inflight{}, stop: func() {}}
}

// fatal records a stdout write failure (the client closed the pipe) and ends
// Serve: there is nobody left to answer.
func (s *server) fatal(err error) {
	s.mu.Lock()
	if s.fatalErr == nil {
		s.fatalErr = err
	}
	s.mu.Unlock()
	s.stop()
}

type inflight struct {
	cancel context.CancelFunc
}

type job struct {
	id     json.RawMessage
	params json.RawMessage
	modern bool
	ctx    context.Context
	fl     *inflight
}

// Serve speaks MCP over in and out for the project at root until in reaches
// end of file or ctx is cancelled. Requests with an id get exactly one
// response; notifications get none. A malformed line is answered with a parse
// error and the server keeps running. tools/call requests run one at a time,
// in arrival order, on a worker so that notifications/cancelled and ping stay
// responsive while a long tool (index) runs. On end of file the queued calls
// are finished before returning; on ctx cancellation they are abandoned.
func Serve(ctx context.Context, in io.Reader, out io.Writer, root string) error {
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()
	s := newServer(root, out, os.Stderr)
	s.stop = cancel

	lines := make(chan lineMsg)
	go readLines(ctx, in, lines)

	jobs := make(chan job, queueSize)
	workerDone := make(chan struct{})
	go func() {
		defer close(workerDone)
		for j := range jobs {
			s.runJob(j)
		}
	}()

	var werr error
loop:
	for {
		select {
		case <-ctx.Done():
			break loop
		case m, ok := <-lines:
			if !ok {
				break loop
			}
			if m.err != nil {
				if !errors.Is(m.err, io.EOF) {
					werr = m.err
				}
				break loop
			}
			if err := s.handleLine(ctx, m, jobs); err != nil {
				werr = err
				break loop
			}
		}
	}
	if ctx.Err() != nil || werr != nil {
		cancel()
	}
	close(jobs)
	<-workerDone
	s.mu.Lock()
	defer s.mu.Unlock()
	if werr == nil {
		werr = s.fatalErr
	}
	return werr
}

type lineMsg struct {
	data    []byte
	tooLong bool
	err     error
}

// readLines splits in into lines of any length up to maxLine (no 64 KiB
// scanner limit) and sends them until in ends or ctx is done.
func readLines(ctx context.Context, in io.Reader, out chan<- lineMsg) {
	defer close(out)
	r := bufio.NewReaderSize(in, 64<<10)
	send := func(m lineMsg) bool {
		select {
		case out <- m:
			return true
		case <-ctx.Done():
			return false
		}
	}
	var buf []byte
	tooLong := false
	for {
		chunk, err := r.ReadSlice('\n')
		if !tooLong {
			buf = append(buf, chunk...)
			if len(buf) > maxLine {
				tooLong, buf = true, nil
			}
		}
		if errors.Is(err, bufio.ErrBufferFull) {
			continue
		}
		if err != nil {
			if len(bytes.TrimSpace(buf)) > 0 || tooLong {
				if !send(lineMsg{data: buf, tooLong: tooLong}) {
					return
				}
			}
			send(lineMsg{err: err})
			return
		}
		line := bytes.TrimRight(buf, "\r\n")
		if tooLong || len(bytes.TrimSpace(line)) > 0 {
			if !send(lineMsg{data: line, tooLong: tooLong}) {
				return
			}
		}
		buf, tooLong = nil, false
	}
}

func (s *server) write(v any) error {
	s.wmu.Lock()
	defer s.wmu.Unlock()
	return s.enc.Encode(v)
}

func (s *server) reply(id json.RawMessage, result any) error {
	return s.write(response{JSONRPC: "2.0", ID: id, Result: result})
}

func (s *server) fail(id json.RawMessage, code int, msg string, data any) error {
	if len(id) == 0 {
		id = json.RawMessage("null")
	}
	return s.write(response{JSONRPC: "2.0", ID: id, Error: &rpcError{Code: code, Message: msg, Data: data}})
}

// handleLine processes one message. It returns an error only when stdout
// cannot be written, which ends the server.
func (s *server) handleLine(ctx context.Context, m lineMsg, jobs chan<- job) error {
	if m.tooLong {
		return s.fail(nil, codeInvalidRequest, fmt.Sprintf("message longer than %d bytes was discarded", maxLine), nil)
	}
	trimmed := bytes.TrimSpace(m.data)
	if len(trimmed) > 0 && trimmed[0] == '[' {
		return s.fail(nil, codeInvalidRequest, "JSON-RPC batches are not supported", nil)
	}
	var req request
	if err := json.Unmarshal(trimmed, &req); err != nil {
		var te *json.UnmarshalTypeError
		if errors.As(err, &te) {
			return s.fail(nil, codeInvalidRequest, "invalid request: "+err.Error(), nil)
		}
		return s.fail(nil, codeParse, "parse error: "+err.Error(), nil)
	}
	if req.Method == "" {
		if len(req.Result) > 0 || len(req.Error) > 0 {
			return nil // a response from the client; this server sends no requests
		}
		return s.fail(req.ID, codeInvalidRequest, "invalid request: no method", nil)
	}
	if string(req.ID) == "null" {
		return s.fail(nil, codeInvalidRequest, "invalid request: id must not be null", nil)
	}
	isNotification := len(req.ID) == 0
	if isNotification {
		s.notification(req)
		return nil
	}
	modern, version := metaVersion(req.Params)
	if modern && version != modernVersion {
		return s.fail(req.ID, codeUnsupportedVer, "Unsupported protocol version",
			map[string]any{"supported": supportedVersions(), "requested": version})
	}
	switch req.Method {
	case "initialize":
		return s.reply(req.ID, s.initialize(req.Params))
	case "ping":
		return s.reply(req.ID, map[string]any{})
	case "server/discover":
		return s.reply(req.ID, s.decorate(map[string]any{
			"supportedVersions": supportedVersions(),
			"capabilities":      map[string]any{"tools": map[string]any{}},
			"instructions":      serverInstructions,
			"ttlMs":             3600000,
			"cacheScope":        "public",
		}, true))
	case "tools/list":
		return s.reply(req.ID, s.decorate(map[string]any{
			"tools":      toolList(modern || s.annotationsAllowed()),
			"ttlMs":      3600000,
			"cacheScope": "public",
		}, modern))
	case "tools/call":
		jctx, jcancel := context.WithCancel(ctx)
		fl := &inflight{cancel: jcancel}
		s.mu.Lock()
		s.inflight[string(req.ID)] = fl
		s.mu.Unlock()
		select {
		case jobs <- job{id: req.ID, params: req.Params, modern: modern, ctx: jctx, fl: fl}:
			return nil
		default:
			s.done(req.ID)
			return s.fail(req.ID, codeInternal, "too many tool calls are queued", nil)
		}
	}
	return s.fail(req.ID, codeMethodNotFound, "method not found: "+req.Method, nil)
}

// metaVersion reads params._meta["io.modelcontextprotocol/protocolVersion"].
func metaVersion(params json.RawMessage) (modern bool, version string) {
	if len(params) == 0 {
		return false, ""
	}
	var p struct {
		Meta map[string]json.RawMessage `json:"_meta"`
	}
	if json.Unmarshal(params, &p) != nil {
		return false, ""
	}
	raw, ok := p.Meta[metaVersionKey]
	if !ok {
		return false, ""
	}
	_ = json.Unmarshal(raw, &version)
	return true, version
}

// decorate adds the fields the stateless revision requires on every result.
func (s *server) decorate(res map[string]any, modern bool) map[string]any {
	if !modern {
		delete(res, "ttlMs")
		delete(res, "cacheScope")
		return res
	}
	res["resultType"] = "complete"
	res["_meta"] = map[string]any{
		"io.modelcontextprotocol/serverInfo": map[string]any{"name": "specs-index", "version": ServerVersion},
	}
	return res
}

// annotationsAllowed says whether tool annotations exist in the revision
// negotiated by initialize (added in 2025-03-26; 2024-11-05 has none).
func (s *server) annotationsAllowed() bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.version != "2024-11-05"
}

func (s *server) initialize(params json.RawMessage) map[string]any {
	var p struct {
		ProtocolVersion string `json:"protocolVersion"`
	}
	_ = json.Unmarshal(params, &p)
	v := handshakeVersions[0]
	for _, sv := range handshakeVersions {
		if p.ProtocolVersion == sv {
			v = sv
		}
	}
	s.mu.Lock()
	s.version = v
	s.mu.Unlock()
	return map[string]any{
		"protocolVersion": v,
		"capabilities":    map[string]any{"tools": map[string]any{"listChanged": false}},
		"serverInfo":      map[string]any{"name": "specs-index", "version": ServerVersion},
		"instructions":    serverInstructions,
	}
}

// notification handles the notifications that matter; all others are ignored.
func (s *server) notification(req request) {
	if req.Method != "notifications/cancelled" {
		return
	}
	var p struct {
		RequestID json.RawMessage `json:"requestId"`
	}
	if json.Unmarshal(req.Params, &p) != nil || len(p.RequestID) == 0 {
		return
	}
	s.mu.Lock()
	fl := s.inflight[string(p.RequestID)]
	s.mu.Unlock()
	if fl != nil {
		fl.cancel()
	}
}

func (s *server) done(id json.RawMessage) {
	s.mu.Lock()
	delete(s.inflight, string(id))
	s.mu.Unlock()
}

// runJob executes one tools/call and writes its single response. A call that
// was cancelled (by the client, or because Serve is shutting down) before it
// started does not run, and one cancelled while running gets no response.
func (s *server) runJob(j job) {
	defer j.fl.cancel()
	defer s.done(j.id)
	if j.ctx.Err() != nil {
		return
	}
	resp, rerr := s.callTool(j.ctx, j.params)
	if j.ctx.Err() != nil {
		return
	}
	var err error
	if rerr != nil {
		err = s.fail(j.id, rerr.Code, rerr.Message, rerr.Data)
	} else {
		err = s.reply(j.id, s.decorate(resp, j.modern))
	}
	if err != nil {
		fmt.Fprintf(s.log, "specs-index mcp: write failed, stopping: %v\n", err)
		s.fatal(err)
	}
}
