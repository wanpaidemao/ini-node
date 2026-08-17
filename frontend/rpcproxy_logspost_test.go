package main

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"
)

// TestLogsPost verifies POST /api/logs truncates the real node log.
// os.Args[0] is overridden so handleLogs resolves ../backend/logs relative
// to the frontend package dir (go test binaries live in a temp dir).
func TestLogsPost(t *testing.T) {
	cwd, err := os.Getwd() // frontend/ package dir
	if err != nil {
		t.Fatalf("getwd: %v", err)
	}
	real := os.Args[0]
	defer func() { os.Args[0] = real }()
	os.Args[0] = filepath.Join(cwd, "testbin") // Dir -> frontend/ so ../backend/logs resolves

	req := httptest.NewRequest(http.MethodPost, "/api/logs", nil)
	rec := httptest.NewRecorder()
	handleLogs(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", rec.Code)
	}
	var m struct {
		OK bool `json:"ok"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &m); err != nil {
		t.Fatalf("invalid json: %v", err)
	}
	if !m.OK {
		t.Fatalf("clear failed: ok=false")
	}
	t.Logf("logs cleared (ok=true)")
}
