// 04-sync-rate.go — continuous sync-rate monitor.
//
// Polls the local btcd RPC (getblockcount) every -interval seconds,
// prints one line per sample on stdout and appends it to a log file,
// recording height, per-interval delta, blocks/s, sync progress % and
// an ETA to the configured target height.
//
// RPC credentials / port are read from the shared runtime ini
// (..\btcd-runtime.ini relative to the exe) by default, so secrets stay out
// of the command line.
//
// Build:  go build -o 04-sync-rate.exe 04-sync-rate.go
// Run:    .\04-sync-rate.exe [-interval 60] [-log sync-rate.log] [-target 43719880]
package main

import (
	"crypto/tls"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"log"
	"net/http"
	"os"
	"os/signal"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"time"
)

const (
	defaultRPCPort = 8334
	defaultRPCUser = "qYjMSzVGbXdgPJiuwxfMAp5EM3M="
	defaultRPCPass = "dSQjlIXRW7ETYcroIhjlTqduT0A="
)

type options struct {
	interval time.Duration
	rpcUser  string
	rpcPass  string
	rpcPort  int
	target   int64
	logFile  string
}

func main() {
	var (
		intervalSec int64
		rpcPort     int
		target      int64
	)
	flag.Int64Var(&intervalSec, "interval", 60, "sample interval in seconds")
	flag.StringVar(&rpcUserData, "user", defaultRPCUser, "RPC username")
	flag.StringVar(&rpcPassData, "pass", defaultRPCPass, "RPC password")
	flag.IntVar(&rpcPort, "port", defaultRPCPort, "RPC port")
	flag.Int64Var(&target, "target", 43719880, "target tip height for progress/ETA")
	flag.StringVar(&logFileData, "log", "sync-rate.log", "log file path (relative to script dir)")
	flag.StringVar(&iniFileData, "ini", "..\\btcd-runtime.ini", "runtime config file (RPC creds/port; resolved relative to exe dir)")
	flag.Parse()

	if intervalSec <= 0 {
		intervalSec = 60
	}
	if rpcPort <= 0 {
		rpcPort = defaultRPCPort
	}
	o := options{
		interval: time.Duration(intervalSec) * time.Second,
		rpcUser:  rpcUserData,
		rpcPass:  rpcPassData,
		rpcPort:  rpcPort,
		target:   target,
		logFile:  logFileData,
	}

	// Resolve relative log path against the directory holding the exe/script.
	if !filepath.IsAbs(o.logFile) {
		if exe, err := os.Executable(); err == nil {
			o.logFile = filepath.Join(filepath.Dir(exe), o.logFile)
		} else if wd, err := os.Getwd(); err == nil {
			o.logFile = filepath.Join(wd, o.logFile)
		}
	}

	// RPC credentials and port come from the shared runtime ini file when it
	// exists (authoritative over flags), keeping secrets out of the command
	// line.  Relative ini paths resolve against the directory holding the exe.
	if !filepath.IsAbs(iniFileData) {
		if exe, err := os.Executable(); err == nil {
			iniFileData = filepath.Join(filepath.Dir(exe), iniFileData)
		}
	}
	if vals := loadIniConfig(iniFileData); len(vals) > 0 {
		if v, ok := vals["rpcuser"]; ok && v != "" {
			o.rpcUser = v
		}
		if v, ok := vals["rpcpass"]; ok && v != "" {
			o.rpcPass = v
		}
		if v, ok := vals["rpclisten"]; ok && v != "" {
			if i := strings.LastIndex(v, ":"); i >= 0 {
				if p, err := strconv.Atoi(v[i+1:]); err == nil {
					o.rpcPort = p
				}
			}
		}
	}

	client := &http.Client{
		Transport: &http.Transport{
			TLSClientConfig: &tls.Config{InsecureSkipVerify: true},
		},
		Timeout: 10 * time.Second,
	}

	logFile, err := os.OpenFile(o.logFile, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o644)
	if err != nil {
		log.Fatalf("cannot open logfile %s: %v", o.logFile, err)
	}
	defer logFile.Close()

	// Signal handling for Ctrl+C / terminate.
	done := make(chan os.Signal, 1)
	signal.Notify(done, syscall.SIGINT, syscall.SIGTERM)
	stop := make(chan struct{})
	go func() {
		<-done
		close(stop)
	}()

	writeLine := func(s string) {
		fmt.Println(s)
		fmt.Fprintln(logFile, s)
	}

	writeLine(fmt.Sprintf("== sync-rate monitor start == interval=%d target=%d log=%s",
		intervalSec, o.target, o.logFile))

	var prev int64 = -1
	ticker := time.NewTicker(o.interval)
	defer ticker.Stop()

	for {
		h, rpcErr := getHeight(client, o)
		now := time.Now().Format("2006-01-02 15:04:05")
		if rpcErr != nil {
			writeLine(fmt.Sprintf("%s  height=<error>  %v", now, rpcErr))
			prev = -1
		} else {
			line := fmt.Sprintf("%s  height=%d", now, h)
			if prev >= 0 {
				delta := h - prev
				rate := float64(delta) / o.interval.Seconds()
				if delta == 0 {
					line += "  delta=+0  rate=0.00 bl/s  (stalled or at tip)"
				} else if rate > 0 {
					line += fmt.Sprintf("  delta=%+d  rate=%.2f bl/s", delta, rate)
				} else {
					line += fmt.Sprintf("  delta=%+d  rate=%.2f bl/s  (reorg?)", delta, rate)
				}
				if rate > 0 && o.target > h {
					pct := float64(h) / float64(o.target) * 100
					etaH := float64(o.target-h) / rate / 3600
					line += fmt.Sprintf("  synced=%.5f%%  ETA=%.1fh", pct, etaH)
				}
			}
			writeLine(line)
			prev = h
		}

		select {
		case <-stop:
			writeLine(fmt.Sprintf("== sync-rate monitor stop == %s", time.Now().Format("2006-01-02 15:04:05")))
			return
		case <-ticker.C:
		}
	}
}

var (
	rpcUserData string
	rpcPassData string
	logFileData string
	iniFileData string
)

// loadIniConfig parses the shared btcd runtime ini file ([Application Options]
// section, key=value lines, ';' comments) into a map.
func loadIniConfig(path string) map[string]string {
	cfg := make(map[string]string)
	data, err := os.ReadFile(path)
	if err != nil {
		return cfg
	}
	for _, raw := range strings.Split(string(data), "\n") {
		line := strings.TrimSpace(raw)
		if line == "" || strings.HasPrefix(line, ";") || strings.HasPrefix(line, "#") {
			continue
		}
		if strings.HasPrefix(line, "[") && strings.HasSuffix(line, "]") {
			continue
		}
		if eq := strings.IndexByte(line, '='); eq > 0 {
			cfg[strings.TrimSpace(line[:eq])] = strings.TrimSpace(line[eq+1:])
		}
	}
	return cfg
}

func getHeight(client *http.Client, o options) (int64, error) {
	body := `{"jsonrpc":"1.0","id":"x","method":"getblockcount"}` + "\n"
	req, err := http.NewRequest("POST", fmt.Sprintf("https://127.0.0.1:%d/", o.rpcPort),
		strings.NewReader(body))
	if err != nil {
		return -1, err
	}
	req.Header.Set("Content-Type", "application/json")
	req.SetBasicAuth(o.rpcUser, o.rpcPass)

	resp, err := client.Do(req)
	if err != nil {
		return -1, err
	}
	defer resp.Body.Close()

	var reply struct {
		Result int64 `json:"result"`
		Error  *struct {
			Code    int    `json:"code"`
			Message string `json:"message"`
		} `json:"error"`
	}
	data, err := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if err != nil {
		return -1, err
	}
	if err := json.Unmarshal(data, &reply); err != nil {
		return -1, fmt.Errorf("json decode failed (http %d): %q: %w", resp.StatusCode, data, err)
	}
	if reply.Error != nil {
		return -1, fmt.Errorf("rpc error: %s", reply.Error.Message)
	}
	if reply.Result < 0 {
		return -1, fmt.Errorf("rpc reply malformed: height=%d", reply.Result)
	}
	return reply.Result, nil
}