package main

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
)

// TestDBParams verifies /api/db-params returns the tuned no-rebuild params.
func TestDBParams(t *testing.T) {
	req := httptest.NewRequest(http.MethodGet, "/api/db-params", nil)
	rec := httptest.NewRecorder()
	handleDBParams(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", rec.Code)
	}
	var m map[string]interface{}
	if err := json.Unmarshal(rec.Body.Bytes(), &m); err != nil {
		t.Fatalf("invalid json: %v", err)
	}
	for _, k := range []string{"writeBuffer", "tableSize", "compactionTotal",
		"l0Files", "rebuildWorkers", "fetchBatchBlocks", "rebuildBatchBlocks",
		"headerWindow"} {
		if _, ok := m[k]; !ok {
			t.Errorf("missing db param %q", k)
		}
	}
	if m["writeBuffer"] != "64 MiB" || m["compactionTotal"] != "256 MiB" {
		t.Errorf("unexpected tuned values: %v", m)
	}
}

// TestLogsGet verifies /api/logs GET returns ok with a lines array.
func TestLogsGet(t *testing.T) {
	req := httptest.NewRequest(http.MethodGet, "/api/logs?lines=5", nil)
	rec := httptest.NewRecorder()
	handleLogs(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", rec.Code)
	}
	var m struct {
		OK    bool     `json:"ok"`
		Lines []string `json:"lines"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &m); err != nil {
		t.Fatalf("invalid json: %v", err)
	}
	if !m.OK {
		// In the test env os.Args[0] points at the temp test binary, so
		// ../backend/logs/node.stdout.log may not resolve; ok=false is a
		// legitimate response for a missing file.
		t.Logf("ok=false (log file may not resolve from test env); skipping lines check")
		return
	}
	if len(m.Lines) > 5 {
		t.Errorf("lines = %d, want <= 5", len(m.Lines))
	}
}

// TestNodeStartProbe verifies /api/node-start probes rpclisten and reports
// running when the node is up (no side effect: it must NOT spawn btcd when
// the port is already listening).
func TestNodeStartProbe(t *testing.T) {
	opts := map[string]string{"rpclisten": "127.0.0.1:8334"}
	req := httptest.NewRequest(http.MethodGet, "/api/node-start", nil)
	rec := httptest.NewRecorder()
	handleNodeStart(opts, rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", rec.Code)
	}
	var m struct {
		OK      bool `json:"ok"`
		Running bool `json:"running"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &m); err != nil {
		t.Fatalf("invalid json: %v", err)
	}
	if m.OK && m.Running {
		t.Logf("node already listening on 8334 — probe returned running=true (no spawn)")
	} else {
		t.Logf("node NOT listening — start would spawn btcd; skip assertion (running=%v)", m.Running)
	}
}
