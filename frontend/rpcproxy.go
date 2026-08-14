package main

import (
	"bytes"
	"encoding/base64"
	"encoding/json"
	"io"
	"net"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"regexp"
	"strings"

	"github.com/wailsapp/wails/v3/pkg/application"
)

// Production counterpart of the vite dev proxy (frontend/frontend/vite.config.ts).
// The WebView2 loads the embedded asset server in release builds, so the /rpc and
// /api/node-config routes the dev server fake-implemented must be served here instead.
// The node (btcd fork) listens on rpclisten (HTTP, --notls); credentials come from
// backend/btcd-runtime.ini so no secrets are baked into the frontend.

const (
	defaultRPCUser = "ini"
	defaultRPCPass = "ini"
	defaultRPCHost = "127.0.0.1"
	defaultRPCPort = "8334"
)

var (
	iniKeyRe    = regexp.MustCompile(`^\s*([a-zA-Z0-9]+)\s*=\s*(.*)$`)
	iniUpdateRe = regexp.MustCompile(`^([a-zA-Z0-9]+)\s*=`)
	rpcHTTP     = &http.Client{}
)

// findIniPath locates backend/btcd-runtime.ini. Prefers an explicit override,
// then paths relative to the executable (bin/../../backend), then the CWD.
func findIniPath() string {
	var exeDir, cwd string
	if exe, err := os.Executable(); err == nil {
		exeDir = filepath.Dir(exe)
	}
	if wd, err := os.Getwd(); err == nil {
		cwd = wd
	}
	var candidates []string
	if p := os.Getenv("INI_NODE_INI"); p != "" {
		candidates = append(candidates, p)
	}
	if exeDir != "" {
		candidates = append(candidates,
			filepath.Join(exeDir, "..", "..", "backend", "btcd-runtime.ini"),
			filepath.Join(exeDir, "..", "backend", "btcd-runtime.ini"),
			filepath.Join(exeDir, "backend", "btcd-runtime.ini"),
		)
	}
	if cwd != "" {
		candidates = append(candidates,
			filepath.Join(cwd, "backend", "btcd-runtime.ini"),
			filepath.Join(cwd, "btcd-runtime.ini"),
		)
	}
	for _, p := range candidates {
		if fi, err := os.Stat(p); err == nil && !fi.IsDir() {
			return p
		}
	}
	return ""
}

func parseIni(path string) map[string]string {
	out := map[string]string{}
	if path == "" {
		return out
	}
	b, err := os.ReadFile(path)
	if err != nil {
		return out
	}
	for _, line := range strings.Split(string(b), "\n") {
		line = strings.TrimRight(line, "\r")
		m := iniKeyRe.FindStringSubmatch(line)
		if m != nil {
			if _, ok := out[m[1]]; !ok {
				out[m[1]] = strings.TrimSpace(m[2])
			}
		}
	}
	return out
}

type rpcProxyTarget struct {
	hostname string
	port     string
	auth     string
}

func rpcProxyTargetFor(o map[string]string) rpcProxyTarget {
	user := o["rpcuser"]
	if user == "" {
		user = defaultRPCUser
	}
	pass := o["rpcpass"]
	if pass == "" {
		pass = defaultRPCPass
	}
	auth := "Basic " + base64.StdEncoding.EncodeToString([]byte(user+":"+pass))
	raw := strings.TrimSpace(o["rpclisten"])
	if raw == "" {
		raw = net.JoinHostPort(defaultRPCHost, defaultRPCPort)
	}
	if !strings.Contains(raw, "://") {
		raw = "http://" + raw
	}
	u, err := url.Parse(raw)
	hostname, port := defaultRPCHost, defaultRPCPort
	if err == nil && u.Host != "" {
		if h := u.Hostname(); h != "" {
			hostname = h
		}
		if p := u.Port(); p != "" {
			port = p
		}
	}
	return rpcProxyTarget{hostname: hostname, port: port, auth: auth}
}

func writeRPCError(w http.ResponseWriter, msg string) {
	w.Header().Set("content-type", "application/json")
	w.WriteHeader(http.StatusBadGateway)
	_ = json.NewEncoder(w).Encode(map[string]interface{}{
		"jsonrpc": "1.0",
		"error": map[string]interface{}{
			"code":    -32603,
			"message": msg,
		},
		"result": nil,
		"id":     nil,
	})
}

func handleRPCProxy(o map[string]string, w http.ResponseWriter, req *http.Request) {
	target := rpcProxyTargetFor(o)
	body, err := io.ReadAll(req.Body)
	if err != nil {
		writeRPCError(w, "failed to read request body: "+err.Error())
		return
	}
	up, err := http.NewRequest(req.Method, "http://"+net.JoinHostPort(target.hostname, target.port)+"/", bytes.NewReader(body))
	if err != nil {
		writeRPCError(w, err.Error())
		return
	}
	up.Header = req.Header.Clone()
	for _, h := range []string{"Connection", "Keep-Alive", "Proxy-Connection", "Transfer-Encoding", "Upgrade"} {
		up.Header.Del(h)
	}
	up.Header.Set("authorization", target.auth)
	up.Header.Set("content-type", "application/json")
	up.Host = net.JoinHostPort(target.hostname, target.port)

	resp, err := rpcHTTP.Do(up)
	if err != nil {
		writeRPCError(w, err.Error())
		return
	}
	defer resp.Body.Close()
	for k, vv := range resp.Header {
		for _, v := range vv {
			w.Header().Add(k, v)
		}
	}
	w.WriteHeader(resp.StatusCode)
	_, _ = io.Copy(w, resp.Body)
}

func writeIni(iniPath string, val map[string]string) {
	if iniPath == "" || len(val) == 0 {
		return
	}
	upd := map[string]string{}
	var updOrder []string
	for k, v := range val {
		if v == "" {
			continue
		}
		if _, ok := upd[k]; !ok {
			updOrder = append(updOrder, k)
		}
		upd[k] = k + "=" + v
	}
	existing, err := os.ReadFile(iniPath)
	var out []string
	if err == nil && len(existing) > 0 {
		for _, line := range strings.Split(string(existing), "\n") {
			line = strings.TrimRight(line, "\r")
			m := iniUpdateRe.FindStringSubmatch(line)
			if m != nil {
				if rep, ok := upd[m[1]]; ok {
					out = append(out, rep)
					delete(upd, m[1])
					continue
				}
			}
			out = append(out, line)
		}
	}
	for _, k := range updOrder {
		if v, ok := upd[k]; ok {
			out = append(out, v)
		}
	}
	content := strings.TrimRight(strings.Join(out, "\n"), "\n") + "\n"
	_ = os.WriteFile(iniPath, []byte(content), 0o600)
}

func handleNodeConfig(iniPath string, o map[string]string, w http.ResponseWriter, req *http.Request) {
	w.Header().Set("content-type", "application/json")
	if req.Method == http.MethodPost {
		var cfg map[string]interface{}
		body, err := io.ReadAll(req.Body)
		if err == nil && json.Unmarshal(body, &cfg) == nil {
			val := map[string]string{}
			for k, v := range cfg {
				if s, ok := v.(string); ok {
					val[k] = s
				}
			}
			if ep, ok := val["rpcendpoint"]; ok && ep != "" {
				if u, err := url.Parse(ep); err == nil && u.Host != "" {
					val["rpclisten"] = u.Host
				}
				delete(val, "rpcendpoint")
			}
			writeIni(iniPath, val)
		}
		_ = json.NewEncoder(w).Encode(map[string]bool{"ok": true})
		return
	}
	rpcEndpoint := "http://" + net.JoinHostPort(defaultRPCHost, defaultRPCPort)
	if rl := strings.TrimSpace(o["rpclisten"]); rl != "" {
		if strings.Contains(rl, "://") {
			rpcEndpoint = rl
		} else {
			rpcEndpoint = "http://" + rl
		}
	}
	_ = json.NewEncoder(w).Encode(map[string]interface{}{
		"rpcEndpoint": rpcEndpoint,
		"rpcUser":     o["rpcuser"],
		"rpcPass":     o["rpcpass"],
		"credFromIni": true,
	})
}

// handleIndexProgress serves the sugarindex rebuild progress written by the
// node to <datadir>/*/index/progress.json.  It does NOT go through btcd's RPC,
// because the RPC server is not listening while the index is being rebuilt.
// Returns {"height":N,"total":N,"percent":F} or 404 when absent.
func handleIndexProgress(iniPath string, o map[string]string, w http.ResponseWriter, req *http.Request) {
	w.Header().Set("content-type", "application/json")
	datadir := strings.TrimSpace(o["datadir"])
	if datadir == "" {
		// Default data directory (matches btcd's default). / 默认数据目录。
		home, err := os.UserHomeDir()
		if err != nil {
			_ = json.NewEncoder(w).Encode(map[string]interface{}{"height": 0, "total": 0, "percent": 0})
			return
		}
		datadir = filepath.Join(home, "AppData", "Local", "Btcd")
	}
	// The network subdir (e.g. sugarmainnet) is unknown here; glob for it.
	// 网络子目录(如 sugarmainnet)此处未知,用 glob 查找。
	matches, err := filepath.Glob(filepath.Join(datadir, "*", "index", "progress.json"))
	if err != nil || len(matches) == 0 {
		// Also try the bare datadir (no network subdir). / 也尝试裸数据目录。
		matches, err = filepath.Glob(filepath.Join(datadir, "index", "progress.json"))
	}
	if err != nil || len(matches) == 0 {
		w.WriteHeader(http.StatusNotFound)
		_ = json.NewEncoder(w).Encode(map[string]interface{}{"height": 0, "total": 0, "percent": 0})
		return
	}
	// Pick the newest matching file. / 取最新的匹配文件。
	best := matches[0]
	var bestMod int64
	for _, m := range matches {
		if st, err := os.Stat(m); err == nil && st.ModTime().Unix() > bestMod {
			best = m
			bestMod = st.ModTime().Unix()
		}
	}
	raw, err := os.ReadFile(best)
	if err != nil {
		w.WriteHeader(http.StatusInternalServerError)
		_ = json.NewEncoder(w).Encode(map[string]interface{}{"height": 0, "total": 0, "percent": 0})
		return
	}
	var p map[string]interface{}
	if err := json.Unmarshal(raw, &p); err != nil {
		p = map[string]interface{}{"height": 0, "total": 0, "percent": 0}
	}
	_ = json.NewEncoder(w).Encode(p)
}

// rpcProxyMiddleware intercepts the RPC proxy and node-config routes that the
// frontend hits relative to its own origin; everything else falls through to the
// embedded asset server.
func rpcProxyMiddleware() application.Middleware {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
			iniPath := findIniPath()
			opts := parseIni(iniPath)
			switch {
			case req.URL.Path == "/rpc" || strings.HasPrefix(req.URL.Path, "/rpc/"):
				handleRPCProxy(opts, w, req)
			case req.URL.Path == "/api/node-config":
				handleNodeConfig(iniPath, opts, w, req)
			case req.URL.Path == "/api/index-progress":
				handleIndexProgress(iniPath, opts, w, req)
			default:
				next.ServeHTTP(w, req)
			}
		})
	}
}