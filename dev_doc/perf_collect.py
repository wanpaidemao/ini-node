#!/usr/bin/env python3
# -*- coding: utf-8 -*-
"""性能收集脚本：每 30s 采样 CPU/内存/硬盘/网络/RPC，共 60 次（30 分钟），写入文件。
用法: python perf_collect.py <outfile> [interval_sec] [samples]
"""
import json
import os
import subprocess
import sys
import time
import urllib.request
import base64

RPC_URL = "http://127.0.0.1:8334/"
RPC_AUTH = "Basic " + base64.b64encode(b"sugar:sugar").decode()
DATA_DIR = os.path.abspath(os.path.join(os.path.dirname(__file__), "..", "data", "sugarmainnet"))
LOG_FILE = os.path.abspath(os.path.join(os.path.dirname(__file__), "..", "frontend", "bin", "logs", "node.stdout.log"))


def rpc(method, params):
    req = urllib.request.Request(
        RPC_URL,
        data=json.dumps({"jsonrpc": "1.0", "id": "1", "method": method, "params": params}).encode(),
        headers={"Content-Type": "application/json", "Authorization": RPC_AUTH},
    )
    try:
        r = json.load(urllib.request.urlopen(req, timeout=5))
        return r.get("result")
    except Exception:
        return None


def powershell(cmd):
    try:
        out = subprocess.run(
            ["powershell", "-NoProfile", "-Command", cmd],
            capture_output=True, text=True, timeout=15,
        )
        return out.stdout.strip()
    except Exception:
        return ""


def dir_size(path):
    total = 0
    try:
        for root, dirs, files in os.walk(path):
            for f in files:
                try:
                    total += os.path.getsize(os.path.join(root, f))
                except OSError:
                    pass
    except OSError:
        pass
    return total


def collect(f):
    ts = time.strftime("%Y-%m-%d %H:%M:%S")

    # ---- 进程（btcd/ini-node）----
    proc = powershell(
        "Get-Process btcd,ini-node -ErrorAction SilentlyContinue | "
        "ForEach-Object { $_.ProcessName + '|' + $_.CPU + '|' + $_.WorkingSet64 }"
    )
    btcd_cpu = btcd_ws = ininode_cpu = ininode_ws = 0.0
    for line in proc.splitlines():
        parts = line.split("|")
        if len(parts) != 3:
            continue
        name, cpu, ws = parts[0], float(parts[1]), float(parts[2])
        if name == "btcd":
            btcd_cpu, btcd_ws = cpu, ws / 1e6
        elif name == "ini-node":
            ininode_cpu, ininode_ws = cpu, ws / 1e6

    # ---- 系统内存 ----
    mem = powershell(
        "$os=Get-CimInstance Win32_OperatingSystem; "
        "[math]::Round($os.FreePhysicalMemory/1MB,1).ToString() + '|' + "
        "[math]::Round($os.TotalVisibleMemorySize/1MB,1).ToString()"
    )
    mem_parts = mem.split("|")
    free_mem = mem_parts[0] if len(mem_parts) > 0 else "?"
    total_mem = mem_parts[1] if len(mem_parts) > 1 else "?"

    # ---- 磁盘 ----
    disk = powershell(
        "Get-PSDrive -Name C | ForEach-Object { "
        "[math]::Round($_.Free/1GB,1).ToString() + '|' + [math]::Round($_.Used/1GB,1).ToString() }"
    )
    disk_parts = disk.split("|")
    disk_free = disk_parts[0] if len(disk_parts) > 0 else "?"
    disk_used = disk_parts[1] if len(disk_parts) > 1 else "?"
    data_bytes = dir_size(DATA_DIR)
    try:
        log_bytes = os.path.getsize(LOG_FILE)
    except OSError:
        log_bytes = -1

    # ---- RPC / 网络 ----
    bc = rpc("getblockcount", [])
    sync = rpc("getblocksyncstatus", [])
    ht = bh = nxt = None
    if isinstance(sync, dict):
        ht = sync.get("header_tip")
        bh = sync.get("best_chain_height")
        nxt = sync.get("block_next_assign")
    peers = rpc("getpeerinfo", [])
    n_peers = len(peers) if isinstance(peers, list) else 0
    sent = recv = 0
    if isinstance(peers, list):
        sent = sum(p.get("bytessent", 0) for p in peers)
        recv = sum(p.get("bytesrecv", 0) for p in peers)

    line = (
        f"{ts} | cpu_btcd={btcd_cpu:8.1f}s ws_btcd={btcd_ws:7.1f}MB "
        f"cpu_ininode={ininode_cpu:6.1f}s ws_ininode={ininode_ws:6.1f}MB "
        f"mem_free={free_mem}GB/{total_mem}GB "
        f"disk_free={disk_free}GB used={disk_used}GB "
        f"data={data_bytes/1e6:.0f}MB log={log_bytes/1e6:.0f}MB "
        f"peers={n_peers} sent={sent/1e6:.1f}MB recv={recv/1e6:.1f}MB "
        f"blockcount={bc} header_tip={ht} best={bh} next_assign={nxt}"
    )
    print(line, flush=True)
    f.write(line + "\n")
    f.flush()
    return {
        "ts": ts, "btcd_cpu": btcd_cpu, "btcd_ws_mb": btcd_ws,
        "ininode_cpu": ininode_cpu, "ininode_ws_mb": ininode_ws,
        "mem_free_gb": free_mem, "mem_total_gb": total_mem,
        "disk_free_gb": disk_free, "disk_used_gb": disk_used,
        "data_mb": data_bytes / 1e6, "log_mb": log_bytes / 1e6,
        "peers": n_peers, "sent_mb": sent / 1e6, "recv_mb": recv / 1e6,
        "blockcount": bc, "header_tip": ht, "best": bh, "next_assign": nxt,
    }


def main():
    outfile = sys.argv[1] if len(sys.argv) > 1 else "perf.log"
    interval = float(sys.argv[2]) if len(sys.argv) > 2 else 30.0
    samples = int(sys.argv[3]) if len(sys.argv) > 3 else 60

    prev = None
    with open(outfile, "w", encoding="utf-8") as f:
        f.write(f"# perf collect start {time.strftime('%Y-%m-%d %H:%M:%S')} "
                f"interval={interval}s samples={samples}\n")
        for i in range(samples):
            cur = collect(f)
            if prev and cur["ts"] != prev["ts"]:
                dt = interval
                dcpu = cur["btcd_cpu"] - prev["btcd_cpu"]
                drecv = cur["recv_mb"] - prev["recv_mb"]
                dsent = cur["sent_mb"] - prev["sent_mb"]
                rate_line = (
                    f"    >> btcd CPU 利用率 {dcpu/dt*100:5.1f}%  "
                    f"接收 {drecv/dt*1024:6.0f} KB/s  发送 {dsent/dt*1024:5.0f} KB/s"
                )
                print(rate_line, flush=True)
                f.write(rate_line + "\n")
                f.flush()
            prev = cur
            time.sleep(interval)
        f.write(f"# perf collect done {time.strftime('%Y-%m-%d %H:%M:%S')}\n")
    print("DONE")


if __name__ == "__main__":
    main()
