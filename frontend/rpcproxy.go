package main

import (
	"bytes"
	"encoding/base64"
	"encoding/json"
	"fmt"
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
		// The runtime config was renamed from btcd-runtime.ini to
		// runtime.ini; accept both for compatibility.
		// 运行时配置已从 btcd-runtime.ini 改名为 runtime.ini;两者都兼容。
		candidates = append(candidates,
			filepath.Join(cwd, "runtime.ini"),
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

// frontendConfigPath returns the path of the frontend-only client config
// file.  Client-only settings (logredirect, and any future UI flags) live
// HERE instead of the runtime ini, because the ini doubles as btcd's
// --configfile and btcd strictly rejects unknown keys (which would make the
// node exit immediately at startup).
// frontendConfigPath 返回前端客户端专属配置文件路径。前端专属设置
// (logredirect 及未来的 UI 开关)统一存这里,不写进 runtime ini——ini 同时
// 是 btcd 的 --configfile,btcd 严格解析未知键会导致节点启动即退出。
func frontendConfigPath(iniPath string) string {
	return filepath.Join(filepath.Dir(iniPath), "frontend.ini")
}

// readFrontendConfig returns the frontend-only client config key/values.
// readFrontendConfig 读取前端客户端配置的全部键值。
func readFrontendConfig(iniPath string) map[string]string {
	if iniPath == "" {
		return map[string]string{}
	}
	return parseIni(frontendConfigPath(iniPath))
}

// writeFrontendConfig merges val into the frontend-only client config file,
// preserving existing keys.  writeFrontendConfig 把 val 合并写入前端客户端
// 配置文件,保留已有键。
func writeFrontendConfig(iniPath string, val map[string]string) error {
	if iniPath == "" {
		return nil
	}
	writeIni(frontendConfigPath(iniPath), val)
	return nil
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
	// Optional explicit ini path (control center ini picker). / 可选显式 ini 路径（控制中心 ini 选择）。
	if p := strings.TrimSpace(req.URL.Query().Get("path")); p != "" {
		iniPath = p
	}
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
			// logredirect is a frontend-only client setting, NOT a btcd
			// option: keep it in frontend.ini, never in the runtime ini
			// (which doubles as btcd's --configfile; btcd rejects unknown
			// keys and the node would refuse to start).  Future client-only
			// settings should be routed the same way. / logredirect 是前端
			// 客户端专属设置,不是 btcd 选项:统一写入 frontend.ini,绝不写进
			// runtime ini(它是 btcd 的 --configfile,未知键会导致节点拒启)。
			// 以后新增前端专属设置也走同样路由。
			if lr, ok := val["logredirect"]; ok {
				_ = writeFrontendConfig(iniPath, map[string]string{"logredirect": lr})
				delete(val, "logredirect")
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
	// raw=1 returns the raw ini file text (control center "view ini").
	// raw=1 返回 ini 原始文本（控制中心"查看 ini"）。
	if req.URL.Query().Get("raw") == "1" {
		b, err := os.ReadFile(iniPath)
		if err != nil {
			_ = json.NewEncoder(w).Encode(map[string]interface{}{"ok": false, "error": err.Error()})
			return
		}
		_ = json.NewEncoder(w).Encode(map[string]interface{}{"ok": true, "content": string(b)})
		return
	}
	// Return the FULL parsed ini (datadir/headerwindow/rpclisten/rpcuser/...)
	// plus the connection summary, so the control center can show and edit
	// every startup parameter.
	// 返回完整解析的 ini 参数(datadir/headerwindow/rpclisten/rpcuser/...)
	// 外加连接摘要,供控制中心展示和编辑全部启动参数。
	out := map[string]interface{}{
		"rpcEndpoint": rpcEndpoint,
		"credFromIni": true,
		"iniPath":     iniPath,
	}
	for k, v := range o {
		out[k] = v
	}
	// Report the frontend-only client settings (frontend.ini) alongside the
	// node ini, so the control center can show and edit them.  They are kept
	// OUT of the runtime ini, which btcd parses strictly as --configfile.
	// / 把前端客户端专属设置(frontend.ini)与节点 ini 一起返回,供控制中心
	// 展示和编辑。这些键不存 runtime ini(btcd 会严格解析并拒启)。
	fe := readFrontendConfig(iniPath)
	for k, v := range fe {
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
	cmd := exec.Command(btcd, "--configfile="+ini)
	cmd.Dir = filepath.Dir(btcd)
	// Capture stdout/stderr to node.stdout.log/node.stderr.log only when
	// logredirect=1 in the ini.  btcd already writes its own rotated
	// btcd.log, so redirecting is opt-in (avoids an unbounded duplicate
	// copy of the log).  Default: no redirect → child inherits/attaches to
	// the parent's handles (Go attaches os.DevNull when Stdout is nil).
	// 仅当 ini 里 logredirect=1 时才把 stdout/stderr 重定向到
	// node.stdout.log/node.stderr.log。btcd 自身已写轮转的 btcd.log,
	// 重定向默认关闭(避免无界重复日志副本);Stdout 为 nil 时 Go 会挂到
	// os.DevNull。
	if readFrontendConfig(ini)["logredirect"] == "1" {
		logDir := filepath.Join(backendDir, "logs")
		if ld := strings.TrimSpace(parseIni(ini)["logdir"]); ld != "" {
			if filepath.IsAbs(ld) {
				logDir = ld
			} else {
				logDir = filepath.Join(backendDir, ld)
			}
		}
		_ = os.MkdirAll(logDir, 0700)
		out, _ := os.OpenFile(filepath.Join(logDir, "node.stdout.log"),
			os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0600)
		errf, _ := os.OpenFile(filepath.Join(logDir, "node.stderr.log"),
			os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0600)
		cmd.Stdout = out
		cmd.Stderr = errf
	}
	if err := cmd.Start(); err != nil {
		_ = json.NewEncoder(w).Encode(map[string]interface{}{"ok": false, "error": err.Error()})
		return
	}
	_ = json.NewEncoder(w).Encode(map[string]interface{}{
		"ok": true, "running": false, "pid": cmd.Process.Pid,
	})
}

// handleNodeStop gracefully stops the btcd node via its RPC stop command.
// This lets btcd flush the block index, sync and close the database, and
// shut down subsystems cleanly, so the next start does not need an unclean-
// shutdown UTXO reconstruction.  It is idempotent.
// handleNodeStop 优雅停止 btcd 节点(通过其 RPC stop 命令)。btcd 会 flush
// 区块索引、同步并关闭数据库、有序关闭各子系统,下次启动无需重建 UTXO。
// 幂等。
func handleNodeStop(w http.ResponseWriter, req *http.Request) {
	w.Header().Set("content-type", "application/json")

	// Locate the runtime ini to resolve the RPC endpoint and credentials.
	ini := findIniPath()
	if ini == "" {
		_ = json.NewEncoder(w).Encode(map[string]interface{}{
			"ok": false, "error": "runtime.ini not found",
		})
		return
	}
	o := parseIni(ini)
	target := rpcProxyTargetFor(o)
	payload := []byte(`{"jsonrpc":"1.0","id":"1","method":"stop","params":[]}`)
	httpReq, err := http.NewRequest(http.MethodPost, "http://"+target.hostname+":"+target.port+"/", bytes.NewReader(payload))
	if err != nil {
		_ = json.NewEncoder(w).Encode(map[string]interface{}{"ok": false, "error": err.Error()})
		return
	}
	httpReq.Header.Set("Content-Type", "application/json")
	httpReq.Header.Set("Authorization", target.auth)
	resp, err := http.DefaultClient.Do(httpReq)
	if err != nil {
		// If the node is already down the RPC is unreachable; treat that as
		// success (idempotent).
		_ = json.NewEncoder(w).Encode(map[string]interface{}{"ok": true, "stopped": true})
		return
	}
	defer resp.Body.Close()
	_ = json.NewEncoder(w).Encode(map[string]interface{}{"ok": true, "stopped": true})
}

// handleForceNodeStop force-kills the btcd process.  This is the emergency
// stop: it does NOT flush the database, so the next start performs an
// unclean-shutdown repair (Reconstructing UTXO state).  Use handleNodeStop
// (graceful) first whenever possible.
// handleForceNodeStop 强制结束 btcd 进程。这是应急停止:不会 flush 数据库,
// 下次启动会做非正常关闭修复(重建 UTXO)。平时应优先用 handleNodeStop(优雅)。
func handleForceNodeStop(w http.ResponseWriter, req *http.Request) {
	w.Header().Set("content-type", "application/json")
	cmd := exec.Command("powershell", "-NoProfile", "-Command",
		"Stop-Process -Name btcd -Force -ErrorAction SilentlyContinue")
	_ = cmd.Run()
	_ = json.NewEncoder(w).Encode(map[string]interface{}{"ok": true, "force": true})
}

// handleLogs serves the node log tail (GET ?lines=N) and clears it (POST).
// It prefers btcd's own rotated log (<datadir>/logs/<net>/btcd.log) so the
// panel shows live output regardless of whether stdout redirect is enabled;
// falls back to the legacy node.stdout.log capture file.
// handleLogs 返回节点日志尾部(GET ?lines=N)或清空(POST)。优先读 btcd 自身
// 的轮转日志(<datadir>/logs/<net>/btcd.log),这样面板总是显示实时日志,
// 与是否开启 stdout 重定向无关;找不到时回退旧的 node.stdout.log 捕获文件。
func handleLogs(w http.ResponseWriter, req *http.Request) {
	w.Header().Set("content-type", "application/json")
	// Resolve the log file through the same ini lookup as handleNodeStart so
	// the read path always matches where the node's stdout is redirected.
	// 日志目录优先用 ini 的 logdir 配置（相对路径基于 ini 所在目录），
	// 否则默认 ini 同目录下的 logs/。与 handleNodeStart 的写入目录保持一致。
	ini := findIniPath()
	if ini == "" {
		_ = json.NewEncoder(w).Encode(map[string]interface{}{
			"ok": false, "error": "runtime.ini not found",
		})
		return
	}
	iniDir := filepath.Dir(ini)
	logDir := filepath.Join(iniDir, "logs")
	if ld := strings.TrimSpace(parseIni(ini)["logdir"]); ld != "" {
		if filepath.IsAbs(ld) {
			logDir = ld
		} else {
			logDir = filepath.Join(iniDir, ld)
		}
	}
	// Prefer btcd's own rotated log.  btcd appends the network name
	// (sugarmainnet) under <logdir>; glob for it like handleIndexProgress
	// does for the data dir.  / 优先 btcd 自身轮转日志:网络名目录
	// (sugarmainnet)挂在 <logdir> 下,用 glob 查找(同 handleIndexProgress)。
	logPath := filepath.Join(logDir, "node.stdout.log")
	if matches, err := filepath.Glob(filepath.Join(logDir, "*", "btcd.log")); err == nil && len(matches) > 0 {
		best := matches[0]
		var bestMod int64
		for _, m := range matches {
			if st, err := os.Stat(m); err == nil && st.ModTime().Unix() > bestMod {
				best = m
				bestMod = st.ModTime().Unix()
			}
		}
		logPath = best
	}
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

// handleWalletapiStatus probes the walletapi gateway on 8335 (no side effect).
// handleWalletapiStatus 探测 walletapi 网关 8335（无副作用）。
func handleWalletapiStatus(w http.ResponseWriter, req *http.Request) {
	w.Header().Set("content-type", "application/json")
	conn, err := net.DialTimeout("tcp", "127.0.0.1:8335", 800*time.Millisecond)
	running := err == nil
	if running {
		conn.Close()
	}
	_ = json.NewEncoder(w).Encode(map[string]interface{}{"ok": true, "running": running})
}

// handleWalletapiStart starts walletapi.exe unless 8335 is already listening.
// handleWalletapiStart 启动 walletapi.exe（8335 已监听则防双开）。
func handleWalletapiStart(o map[string]string, w http.ResponseWriter, req *http.Request) {
	w.Header().Set("content-type", "application/json")
	if conn, err := net.DialTimeout("tcp", "127.0.0.1:8335", 800*time.Millisecond); err == nil {
		conn.Close()
		_ = json.NewEncoder(w).Encode(map[string]interface{}{"ok": true, "running": true})
		return
	}
	ini := findIniPath()
	if ini == "" {
		_ = json.NewEncoder(w).Encode(map[string]interface{}{"ok": false, "error": "ini not found / ini 未找到"})
		return
	}
	backendDir := filepath.Dir(ini)
	exe := filepath.Join(backendDir, "walletapi.exe")
	cmd := exec.Command(exe, "-rpcpass="+o["rpcpass"])
	cmd.Dir = backendDir
	if err := cmd.Start(); err != nil {
		_ = json.NewEncoder(w).Encode(map[string]interface{}{"ok": false, "error": err.Error()})
		return
	}
	_ = json.NewEncoder(w).Encode(map[string]interface{}{"ok": true, "running": false, "pid": cmd.Process.Pid})
}

// handleWalletapiStop stops the walletapi process (idempotent).
// handleWalletapiStop 停止 walletapi 进程（幂等）。
func handleWalletapiStop(w http.ResponseWriter, req *http.Request) {
	w.Header().Set("content-type", "application/json")
	cmd := exec.Command("powershell", "-NoProfile", "-Command",
		"Stop-Process -Name walletapi -Force -ErrorAction SilentlyContinue")
	_ = cmd.Run()
	_ = json.NewEncoder(w).Encode(map[string]interface{}{"ok": true})
}

// handleLoglevel reads (GET) or sets (POST) the node log level.  POST calls
// btcd's debuglevel RPC for immediate effect (needs RPC reachable) and
// persists the level to the runtime ini so it survives a restart.  GET reads
// the node's actual live level via debuglevel get, falling back to the ini
// value when the RPC is unreachable.
// handleLoglevel 读取(GET)或设置(POST)节点日志等级。POST 调用 btcd 的
// debuglevel RPC 即时生效(需 RPC 可达)并回写 ini 持久化;GET 优先读节点
// 实际级别,RPC 不可达时回退 ini 里的 debuglevel。
func handleLoglevel(o map[string]string, w http.ResponseWriter, req *http.Request) {
	w.Header().Set("content-type", "application/json")
	if req.Method == http.MethodPost {
		var body struct {
			Level string `json:"level"`
		}
		if err := json.NewDecoder(req.Body).Decode(&body); err != nil || body.Level == "" {
			_ = json.NewEncoder(w).Encode(map[string]interface{}{"ok": false, "error": "level required / 缺少 level"})
			return
		}
		if err := setBtcdLogLevel(o, body.Level); err != nil {
			_ = json.NewEncoder(w).Encode(map[string]interface{}{"ok": false, "error": err.Error()})
			return
		}
		// Persist so the level survives a node restart. / 回写 ini,重启后仍生效。
		if iniPath := findIniPath(); iniPath != "" {
			if err := updateIniKey(iniPath, "debuglevel", body.Level); err != nil {
				_ = json.NewEncoder(w).Encode(map[string]interface{}{"ok": false, "error": "applied but failed to persist: " + err.Error()})
				return
			}
		}
		_ = json.NewEncoder(w).Encode(map[string]interface{}{"ok": true, "level": body.Level})
		return
	}
	level, err := callDebugLevel(o, "get")
	if err != nil {
		level = strings.TrimSpace(o["debuglevel"])
		if level == "" {
			level = strings.TrimSpace(o["loglevel"])
		}
	}
	if level == "" {
		level = "info"
	}
	_ = json.NewEncoder(w).Encode(map[string]interface{}{"ok": true, "level": level})
}

// callDebugLevel issues btcd's debuglevel RPC with the given spec ("get",
// "show", or a concrete level such as "warn") and returns the decoded result
// string.  A non-nil error means the RPC was unreachable or returned an error.
// callDebugLevel 调用 btcd 的 debuglevel RPC(spec 为 get/show/具体级别如
// warn),返回解码后的结果字符串;RPC 不可达或返回错误时给出 error。
func callDebugLevel(o map[string]string, spec string) (string, error) {
	host := strings.TrimSpace(o["rpclisten"])
	if host == "" {
		host = "127.0.0.1:8334"
	}
	if !strings.Contains(host, ":") {
		host = "127.0.0.1:" + host
	}
	payload, _ := json.Marshal(map[string]interface{}{
		"jsonrpc": "1.0", "id": "loglevel", "method": "debuglevel",
		"params": []string{spec},
	})
	r, _ := http.NewRequest(http.MethodPost, "http://"+host+"/", bytes.NewReader(payload))
	r.SetBasicAuth(o["rpcuser"], o["rpcpass"])
	resp, err := rpcHTTP.Do(r)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()
	var out struct {
		Result interface{} `json:"result"`
		Error  interface{} `json:"error"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
		return "", err
	}
	if out.Error != nil {
		return "", fmt.Errorf("debuglevel RPC error: %v", out.Error)
	}
	s, _ := out.Result.(string)
	return s, nil
}

// setBtcdLogLevel issues btcd's debuglevel RPC (live, no restart).
// setBtcdLogLevel 调用 btcd 的 debuglevel RPC（即时生效，无需重启）。
func setBtcdLogLevel(o map[string]string, level string) error {
	_, err := callDebugLevel(o, level)
	return err
}

// updateIniKey sets key=value in the ini file at path, preserving comments,
// section headers, and other keys.  The key is inserted right after the first
// section header (or appended at the end if absent) so it belongs to the
// correct section.  A newline-terminated file is always written.
// updateIniKey 在 ini 中写入 key=value,保留注释/section/其他键;键不存在时
// 插到第一个 section 之后(没有 section 则追加到末尾),保证键归属正确。
func updateIniKey(path, key, value string) error {
	b, err := os.ReadFile(path)
	if err != nil {
		return err
	}
	lines := strings.Split(strings.ReplaceAll(string(b), "\r\n", "\n"), "\n")
	re := regexp.MustCompile(`^\s*` + regexp.QuoteMeta(key) + `\s*=`)
	updated := false
	for i, line := range lines {
		if re.MatchString(line) {
			lines[i] = key + "=" + value
			updated = true
		}
	}
	if !updated {
		insertAt := len(lines)
		for i, line := range lines {
			if strings.HasPrefix(strings.TrimSpace(line), "[") {
				insertAt = i + 1
				break
			}
		}
		lines = append(lines, "")
		copy(lines[insertAt+1:], lines[insertAt:])
		lines[insertAt] = key + "=" + value
	}
	return os.WriteFile(path, []byte(strings.Join(lines, "\n")+"\n"), 0600)
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
			case req.URL.Path == "/api/node-stop-force":
				handleForceNodeStop(w, req)
			case req.URL.Path == "/api/logs":
				handleLogs(w, req)
			case req.URL.Path == "/api/db-params":
				handleDBParams(w, req)
			case req.URL.Path == "/api/walletapi-status":
				handleWalletapiStatus(w, req)
			case req.URL.Path == "/api/walletapi-start":
				handleWalletapiStart(opts, w, req)
			case req.URL.Path == "/api/walletapi-stop":
				handleWalletapiStop(w, req)
			case req.URL.Path == "/api/loglevel":
				handleLoglevel(opts, w, req)
			default:
				next.ServeHTTP(w, req)
			}
		})
	}
}
