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
	"os/exec"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"time"

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

// findIniPath locates btcd-runtime.ini. Prefers an explicit INI_NODE_INI
// override, then a btcd-runtime.ini in the current working directory.
// findIniPath 定位 btcd-runtime.ini。优先显式 INI_NODE_INI 覆盖,其次是
// 当前工作目录下的 btcd-runtime.ini。
func findIniPath() string {
	var cwd string
	if wd, err := os.Getwd(); err == nil {
		cwd = wd
	}
	var candidates []string
	if p := os.Getenv("INI_NODE_INI"); p != "" {
		candidates = append(candidates, p)
	}
	if cwd != "" {
		candidates = append(candidates,
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
	// Return the FULL parsed ini (datadir/headerwindow/rpclisten/rpcuser/...)
	// plus the connection summary, so the control center can show and edit
	// every startup parameter.
	// 返回完整解析的 ini 参数(datadir/headerwindow/rpclisten/rpcuser/...)
	// 外加连接摘要,供控制中心展示和编辑全部启动参数。
	out := map[string]interface{}{
		"rpcEndpoint": rpcEndpoint,
		"credFromIni": true,
	}
	for k, v := range o {
		out[k] = v
	}
	_ = json.NewEncoder(w).Encode(out)
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

// handleNodeStart starts the btcd node unless it is already listening on
// the configured rpclisten (probe first to avoid a double start).
// handleNodeStart 启动 btcd 节点；先探测 rpclisten 防双开。
func handleNodeStart(opts map[string]string, w http.ResponseWriter, req *http.Request) {
	port := opts["rpclisten"]
	if port == "" {
		port = "127.0.0.1:8334"
	}
	if !strings.Contains(port, ":") {
		port = "127.0.0.1:" + port
	}
	if conn, err := net.DialTimeout("tcp", port, 800*time.Millisecond); err == nil {
		conn.Close()
		_ = json.NewEncoder(w).Encode(map[string]interface{}{"ok": true, "running": true})
		return
	}
	// Locate the backend via findIniPath (CWD or INI_NODE_INI); btcd.exe
	// lives next to the ini in backend/.  Do NOT rely on os.Args[0], which
	// points at the Wails binary and is not reliably next to backend/.
	// 用 findIniPath 定位后端目录(CWD 或 INI_NODE_INI);btcd.exe 与 ini 同在
	// backend/。不要依赖 os.Args[0](指向 Wails 二进制,不一定在 backend/ 旁)。
	ini := findIniPath()
	if ini == "" {
		_ = json.NewEncoder(w).Encode(map[string]interface{}{
			"ok":    false,
			"error": "btcd-runtime.ini not found (set INI_NODE_INI or run from backend/) / 未找到 btcd-runtime.ini(设置 INI_NODE_INI 或在 backend/ 目录运行)",
		})
		return
	}
	backendDir := filepath.Dir(ini)
	btcd := filepath.Join(backendDir, "btcd.exe")
	logDir := filepath.Join(backendDir, "logs")
	_ = os.MkdirAll(logDir, 0700)
	out, _ := os.OpenFile(filepath.Join(logDir, "node.stdout.log"),
		os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0600)
	errf, _ := os.OpenFile(filepath.Join(logDir, "node.stderr.log"),
		os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0600)
	cmd := exec.Command(btcd, "--configfile="+ini)
	cmd.Dir = filepath.Dir(btcd)
	cmd.Stdout = out
	cmd.Stderr = errf
	if err := cmd.Start(); err != nil {
		_ = json.NewEncoder(w).Encode(map[string]interface{}{"ok": false, "error": err.Error()})
		return
	}
	_ = json.NewEncoder(w).Encode(map[string]interface{}{
		"ok": true, "running": false, "pid": cmd.Process.Pid,
	})
}

// handleNodeStop stops the btcd process (idempotent).
// handleNodeStop 停止 btcd 进程（幂等）。
func handleNodeStop(w http.ResponseWriter, req *http.Request) {
	cmd := exec.Command("powershell", "-NoProfile", "-Command",
		"Stop-Process -Name btcd -Force -ErrorAction SilentlyContinue")
	_ = cmd.Run()
	_ = json.NewEncoder(w).Encode(map[string]interface{}{"ok": true})
}

// handleLogs serves the node log tail (GET ?lines=N) and clears it (POST).
// handleLogs 返回节点日志尾部(GET ?lines=N)或清空(POST)。
func handleLogs(w http.ResponseWriter, req *http.Request) {
	w.Header().Set("content-type", "application/json")
	exeDir, _ := filepath.Abs(filepath.Dir(os.Args[0]))
	logPath := filepath.Join(exeDir, "..", "backend", "logs", "node.stdout.log")
	if req.Method == http.MethodPost {
		_ = os.Truncate(logPath, 0)
		_ = json.NewEncoder(w).Encode(map[string]bool{"ok": true})
		return
	}
	lines := 200
	if q := req.URL.Query().Get("lines"); q != "" {
		if n, err := strconv.Atoi(q); err == nil && n > 0 {
			lines = n
		}
	}
	data, err := os.ReadFile(logPath)
	if err != nil {
		_ = json.NewEncoder(w).Encode(map[string]interface{}{"ok": false, "error": err.Error()})
		return
	}
	all := strings.Split(strings.TrimRight(string(data), "\n"), "\n")
	if len(all) > lines {
		all = all[len(all)-lines:]
	}
	_ = json.NewEncoder(w).Encode(map[string]interface{}{"ok": true, "lines": all})
}

// handleDBParams returns the currently tuned index/database parameters.
// Changing these does NOT rebuild anything (they take effect on restart).
// handleDBParams 返回当前调优的索引/数据库参数。修改这些参数不会触发重建
// (重启后生效)。
func handleDBParams(w http.ResponseWriter, req *http.Request) {
	w.Header().Set("content-type", "application/json")
	_ = json.NewEncoder(w).Encode(map[string]interface{}{
		"writeBuffer":        "64 MiB",
		"tableSize":          "64 MiB",
		"compactionTotal":    "256 MiB",
		"l0Files":            8,
		"rebuildWorkers":     4,
		"fetchBatchBlocks":   128,
		"rebuildBatchBlocks": 100,
		"headerWindow":       50000,
	})
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
			case req.URL.Path == "/api/node-start":
				handleNodeStart(opts, w, req)
			case req.URL.Path == "/api/node-stop":
				handleNodeStop(w, req)
			case req.URL.Path == "/api/logs":
				handleLogs(w, req)
			case req.URL.Path == "/api/db-params":
				handleDBParams(w, req)
			default:
				next.ServeHTTP(w, req)
			}
		})
	}
}