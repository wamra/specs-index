package mcp

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/wamra/specs-index/internal/api"
)

func sourcesOf(t *testing.T, root string) int {
	t.Helper()
	st, err := api.OpenExisting(root)
	if err != nil {
		return 0
	}
	defer st.Close()
	r, _ := api.Sources(st)
	return len(r.Sources)
}

func TestCancelledContextRunsNothingAndWritesNothing(t *testing.T) {
	root := newRoot(t)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	var out bytes.Buffer
	in := strings.NewReader(call(1, "add_source", `{"path":"docs"}`) + "\n" + call(2, "index", "") + "\n")
	if err := Serve(ctx, in, &out, root); err != nil {
		t.Fatal(err)
	}
	if out.Len() != 0 {
		t.Errorf("nothing may be written after cancellation, got %q", out.String())
	}
	if n := sourcesOf(t, root); n != 0 {
		t.Errorf("a cancelled add_source registered %d sources", n)
	}
	// The tool itself also refuses with a cancelled context.
	cctx, ccancel := context.WithCancel(context.Background())
	ccancel()
	s := newServer(root, &out, &out)
	for _, name := range []string{"add_source", "remove_source"} {
		for i := range tools {
			if tools[i].name == name {
				if _, err := tools[i].run(cctx, s, json.RawMessage(`{"path":"docs"}`)); !errors.Is(err, context.Canceled) {
					t.Errorf("%s with a cancelled context: want context.Canceled, got %v", name, err)
				}
			}
		}
	}
	if n := sourcesOf(t, root); n != 0 {
		t.Errorf("source tools mutated the index after cancel: %d", n)
	}
}

func TestClientCancelBeforeStartSkipsTheCall(t *testing.T) {
	root := newRoot(t)
	var out bytes.Buffer
	s := newServer(root, &out, os.Stderr)
	jobs := make(chan job, 4)
	ctx := context.Background()
	if err := s.handleLine(ctx, lineMsg{data: []byte(call(5, "add_source", `{"path":"docs"}`))}, jobs); err != nil {
		t.Fatal(err)
	}
	cancelMsg := `{"jsonrpc":"2.0","method":"notifications/cancelled","params":{"requestId":5}}`
	if err := s.handleLine(ctx, lineMsg{data: []byte(cancelMsg)}, jobs); err != nil {
		t.Fatal(err)
	}
	s.runJob(<-jobs)
	if out.Len() != 0 {
		t.Errorf("a cancelled request must get no message, got %q", out.String())
	}
	if n := sourcesOf(t, root); n != 0 {
		t.Errorf("the cancelled add_source ran (%d sources)", n)
	}
	if len(s.inflight) != 0 {
		t.Errorf("in-flight entry leaked: %v", s.inflight)
	}
}

func TestQueueFullIsInternalError(t *testing.T) {
	var out bytes.Buffer
	s := newServer(t.TempDir(), &out, os.Stderr)
	jobs := make(chan job) // no receiver: always full
	if err := s.handleLine(context.Background(), lineMsg{data: []byte(call(9, "status", ""))}, jobs); err != nil {
		t.Fatal(err)
	}
	var m rpcMsg
	if err := json.Unmarshal(out.Bytes(), &m); err != nil || m.Error == nil || m.Error.Code != -32603 || string(m.ID) != "9" {
		t.Fatalf("want -32603 for id 9, got %s (%v)", out.String(), err)
	}
	if len(s.inflight) != 0 {
		t.Error("in-flight entry leaked")
	}
}

func TestPanicInToolIsToolError(t *testing.T) {
	tools = append(tools, tool{name: "boom", description: "x", props: map[string]any{},
		run: func(context.Context, *server, json.RawMessage) (any, error) { panic("kaboom") }})
	t.Cleanup(func() { tools = tools[:len(tools)-1] })
	var errlog bytes.Buffer
	var out bytes.Buffer
	s := newServer(t.TempDir(), &out, &errlog)
	res, rerr := s.callTool(context.Background(), json.RawMessage(`{"name":"boom"}`))
	if rerr != nil || res["isError"] != true || !strings.Contains(res["content"].([]map[string]any)[0]["text"].(string), "kaboom") {
		t.Fatalf("panic must become an isError result: %v %v", res, rerr)
	}
	if !strings.Contains(errlog.String(), "panicked") {
		t.Errorf("panic not logged to the log writer: %q", errlog.String())
	}
	// and through the wire the server keeps serving
	msgs := serve(t, newRoot(t), call(1, "boom", ""), `{"jsonrpc":"2.0","id":2,"method":"ping"}`)
	if r := result(t, byID(t, msgs, "1")); !r.IsError {
		t.Error("boom over the wire must be isError")
	}
	if byID(t, msgs, "2").Error != nil {
		t.Error("server died after a panicking tool")
	}
}

type failWriter struct{}

func (failWriter) Write([]byte) (int, error) { return 0, errors.New("broken pipe") }

func TestStdoutWriteFailureStopsServer(t *testing.T) {
	pr, pw := ioPipeForTest()
	defer pw.Close()
	done := make(chan error, 1)
	go func() { done <- Serve(context.Background(), pr, failWriter{}, newRoot(t)) }()
	pw.Write([]byte(call(1, "status", "") + "\n"))
	select {
	case err := <-done:
		if err == nil || !strings.Contains(err.Error(), "broken pipe") {
			t.Errorf("want the write error, got %v", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("server kept running after stdout failed")
	}
}
