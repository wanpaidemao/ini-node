package main

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestNodeConfig_GetFromIni(t *testing.T) {
	ini := filepath.Join(t.TempDir(), "btcd-runtime.ini")
	if err := os.WriteFile(ini, []byte("[Application Options]\nrpcuser=sugar\nrpcpass=secret\nrpclisten=127.0.0.1:8334\nnotls=1\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	opts := parseIni(ini)
	rr := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/api/node-config", nil)
	handleNodeConfig(ini, opts, rr, req)

	var got map[string]interface{}
	if err := json.Unmarshal(rr.Body.Bytes(), &got); err != nil {
		t.Fatalf("bad json: %v", err)
	}
	if got["rpcUser"] != "sugar" || got["rpcPass"] != "secret" {
		t.Errorf("unexpected creds: %v", got)
	}
	if got["rpcEndpoint"] != "http://127.0.0.1:8334" {
		t.Errorf("unexpected endpoint: %v", got["rpcEndpoint"])
	}
	if got["credFromIni"] != true {
		t.Errorf("credFromIni should be true: %v", got["credFromIni"])
	}
}

func TestNodeConfig_PostUpdatesIni(t *testing.T) {
	ini := filepath.Join(t.TempDir(), "btcd-runtime.ini")
	if err := os.WriteFile(ini, []byte("[Application Options]\nrpcuser=old\nrpcpass=oldpass\nnotls=1\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	opts := parseIni(ini)
	rr := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/api/node-config", strings.NewReader(`{"rpcuser":"new","rpcpass":"newpass","rpcendpoint":"http://1.2.3.4:9999"}`))
	handleNodeConfig(ini, opts, rr, req)

	if rr.Code != http.StatusOK {
		t.Fatalf("want 200 got %d", rr.Code)
	}
	b, err := os.ReadFile(ini)
	if err != nil {
		t.Fatal(err)
	}
	content := string(b)
	for _, want := range []string{"rpcuser=new", "rpcpass=newpass", "rpclisten=1.2.3.4:9999", "notls=1"} {
		if !strings.Contains(content, want) {
			t.Errorf("ini missing %q:\n%s", want, content)
		}
	}
}

func TestRPCProxy_InjectAuthAndForward(t *testing.T) {
	var receivedAuth, receivedBody string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		receivedAuth = r.Header.Get("authorization")
		body, _ := io.ReadAll(r.Body)
		receivedBody = string(body)
		if r.URL.Path != "/" {
			t.Errorf("want path / got %s", r.URL.Path)
		}
		w.Header().Set("content-type", "application/json")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{"result":"ok","id":1}`))
	}))
	defer srv.Close()

	ini := filepath.Join(t.TempDir(), "btcd-runtime.ini")
	u := strings.TrimPrefix(srv.URL, "http://")
	if err := os.WriteFile(ini, []byte("rpcuser=sugar\nrpcpass=secret\nrpclisten="+u+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	opts := parseIni(ini)

	rr := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/rpc/", strings.NewReader(`{"jsonrpc":"1.0","id":1,"method":"getblockcount"}`))
	handleRPCProxy(opts, rr, req)

	if rr.Code != http.StatusOK {
		t.Fatalf("want 200 got %d: %s", rr.Code, rr.Body.String())
	}
	if receivedAuth != "Basic c3VnYXI6c2VjcmV0" { // sugar:secret
		t.Errorf("expected Basic auth header, got %q", receivedAuth)
	}
	if !strings.Contains(receivedBody, "getblockcount") {
		t.Errorf("body not forwarded: %q", receivedBody)
	}
	if !strings.Contains(rr.Body.String(), `"result":"ok"`) {
		t.Errorf("response not streamed back: %q", rr.Body.String())
	}
}

func TestRPCProxy_NodeDownReturns502JSONRPC(t *testing.T) {
	ini := filepath.Join(t.TempDir(), "btcd-runtime.ini")
	if err := os.WriteFile(ini, []byte("rpcuser=u\nrpcpass=p\nrpclisten=127.0.0.1:1\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	opts := parseIni(ini)
	rr := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/rpc/", strings.NewReader(`{"jsonrpc":"1.0","id":1,"method":"getblockcount"}`))
	handleRPCProxy(opts, rr, req)

	if rr.Code != http.StatusBadGateway {
		t.Fatalf("want 502 got %d", rr.Code)
	}
	if !strings.Contains(rr.Body.String(), `"error"`) {
		t.Errorf("expected jsonrpc error envelope, got %q", rr.Body.String())
	}
}