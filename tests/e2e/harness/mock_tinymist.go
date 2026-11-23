package harness

import (
	"bufio"
	"encoding/json"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
)

// MockTraceRecord mirrors the JSON-line records emitted by mocktinymist.
type MockTraceRecord struct {
	TS      string          `json:"ts"`
	Event   string          `json:"event,omitempty"`
	Method  string          `json:"method,omitempty"`
	PID     int             `json:"pid"`
	ID      json.RawMessage `json:"id,omitempty"`
	URI     string          `json:"uri,omitempty"`
	Version int             `json:"version,omitempty"`
	Text    string          `json:"text,omitempty"`
	Params  json.RawMessage `json:"params,omitempty"`
	Note    string          `json:"note,omitempty"`
}

// MockOptions configures environment variables for mock-tinymist.exe.
type MockOptions struct {
	IgnoreStdinEOF bool
	CrashOn        string
	DiagOnOpen     bool
	VersionFail    bool
}

// MockEnv creates temp files for PID/trace/state and returns env overrides for mocktinymist.
func MockEnv(t testing.TB, opts MockOptions) (env map[string]string, pidFile, traceFile, stateFile string) {
	t.Helper()
	dir := t.TempDir()
	pidFile = filepath.Join(dir, "mock.pid")
	traceFile = filepath.Join(dir, "mock.trace.jsonl")
	stateFile = filepath.Join(dir, "mock.state")

	env = map[string]string{
		"TYPX_MOCK_PID_FILE":   pidFile,
		"TYPX_MOCK_TRACE_FILE": traceFile,
		"TYPX_MOCK_STATE_FILE": stateFile,
	}
	if opts.IgnoreStdinEOF {
		env["TYPX_MOCK_IGNORE_STDIN_EOF"] = "1"
	}
	if opts.CrashOn != "" {
		env["TYPX_MOCK_CRASH_ON"] = opts.CrashOn
	}
	if opts.DiagOnOpen {
		env["TYPX_MOCK_DIAG_ON_OPEN"] = "1"
	}
	if opts.VersionFail {
		env["TYPX_MOCK_VERSION_FAIL"] = "1"
	}
	return env, pidFile, traceFile, stateFile
}

// ReadMockPIDs reads all integer PIDs recorded in pidFile.
func ReadMockPIDs(path string) ([]int, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	var pids []int
	for _, line := range strings.Split(string(data), "\n") {
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}
		pid, err := strconv.Atoi(line)
		if err == nil && pid > 0 {
			pids = append(pids, pid)
		}
	}
	return pids, nil
}

// ReadMockTrace reads all valid JSON-line trace records from traceFile.
func ReadMockTrace(path string) ([]MockTraceRecord, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer f.Close()

	var recs []MockTraceRecord
	sc := bufio.NewScanner(f)
	sc.Buffer(make([]byte, 0, 64*1024), 4*1024*1024)
	for sc.Scan() {
		line := strings.TrimSpace(sc.Text())
		if line == "" {
			continue
		}
		var rec MockTraceRecord
		if err := json.Unmarshal([]byte(line), &rec); err == nil {
			recs = append(recs, rec)
		}
	}
	return recs, sc.Err()
}

// HasTraceMethod reports whether any record has the given JSON-RPC method.
func HasTraceMethod(recs []MockTraceRecord, method string) bool {
	for _, r := range recs {
		if r.Method == method {
			return true
		}
	}
	return false
}

// HasTraceEvent reports whether any record has the given lifecycle event name.
func HasTraceEvent(recs []MockTraceRecord, event string) bool {
	for _, r := range recs {
		if r.Event == event {
			return true
		}
	}
	return false
}
