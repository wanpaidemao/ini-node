// ── ini-node service adapter (real node) ────────────────────────
// Talks to the running sugarchain-node (btcd fork) over HTTP JSON-RPC.
// The dev server proxies /rpc → 127.0.0.1:8334 and injects the Basic auth
// header read from backend/btcd-runtime.ini, so no credentials live here.
// Wallet/PSBT methods remain mocked (btcd has no built-in wallet yet).
//
// Design docs: NodeService / WalletService / ConfigService / TxBuilder /
// RpcService / UTXO (§06-umami-go-gui-detailed.md).

import type {
  AppConfig,
  NodeInfo,
  NodeInternals,
  Peer,
  RpcResult,
  SyncStatus,
  Tx,
  WalletState,
} from "./types";

// Wails bindings for the in-process wallet service: wallet lifecycle
// (create / unlock / login / lock / next address) runs inside the frontend
// process and works WITHOUT the node. Chain data still goes over RPC below.
// 进程内钱包服务的 Wails bindings:钱包生命周期(创建/解锁/登录/锁定/新地址)
// 在前端进程内运行,无需节点。链上数据仍走下方 RPC。
import * as LocalWallet from "../../bindings/changeme/walletservice.js";

// ── low-level JSON-RPC client ───────────────────────────────────
let seq = 1;

interface RpcEnvelope {
  result?: unknown;
  error?: { code: number; message: string };
  id: number;
}

// rpcTimeout is how long a single JSON-RPC call may take before it is aborted.
// During a stalled or wedged RPC server a request can otherwise hang forever,
// piling up behind every 2s poll and freezing the sync-internals view on stale
// data.  Aborting treats the request as if it was never sent: the caller's
// catch ignores it and the next poll simply retries.
const rpcTimeout = 5000; // ms

async function rpc<T>(method: string, ...params: unknown[]): Promise<T> {
  const id = seq++;
  const ctl = new AbortController();
  const timer = setTimeout(() => ctl.abort(), rpcTimeout);
  try {
    const res = await fetch("/rpc/", {
      method: "POST",
      headers: { "content-type": "application/json" },
      body: JSON.stringify({ jsonrpc: "1.0", id, method, params }),
      signal: ctl.signal,
    });
    // Parse the body first even on non-2xx: the Wails RPC proxy answers
    // transport failures (node down, auth mismatch) with HTTP 502 + a
    // JSON-RPC envelope whose error.message carries the real cause
    // ("dial tcp ...: connectex: ..."). Surfacing it beats a bare
    // "HTTP 502". Bodies that are not JSON (vite dev proxy text errors)
    // fall back to the status code.
    // 先解析响应体,即使非 2xx:Wails RPC 代理对传输层失败(节点未启动、
    // 认证不符)返回 HTTP 502 + JSON-RPC 信封,其 error.message 携带真实
    // 原因("dial tcp ...: connectex: ...")。展示它比光秃秃的 "HTTP 502"
    // 有用。非 JSON 响应体(vite dev 代理的纯文本错误)回退到状态码。
    const env = (await res.json().catch(() => null)) as RpcEnvelope | null;
    if (env?.error) throw new Error(`RPC ${method}: ${env.error.message}`);
    if (!res.ok) throw new Error(`RPC ${method}: HTTP ${res.status}`);
    if (!env) throw new Error(`RPC ${method}: HTTP ${res.status} (no JSON body)`);
    return env.result as T;
  } finally {
    clearTimeout(timer);
  }
}

// numbers can arrive as JSON numbers (blocks) or as strings in odd cases
function toNum(v: unknown): number {
  if (typeof v === "number") return v;
  if (typeof v === "string") return Number(v);
  return 0;
}

// ── history/ETA sampling (kept client-side; node has no rate RPC) ─
const sample = { at: Date.now(), height: 0 };
let lastBlocks = 0;
let lastRate = 0;
const rateSamples: { t: number; blocks: number }[] = [];

async function chainSample(): Promise<{ height: number; rate: number }> {
  const info = await rpc<{
    blocks: number;
    headers: number;
    verificationprogress?: number;
    bestblockhash: string;
    difficulty: number;
  }>("getblockchaininfo");
  const now = Date.now();
  // Two pollers (App.checkHealth + Dashboard.poll) hit chainSample nearly
  // simultaneously every 5s. Only advance the measurement baseline when a real
  // time gap has passed; otherwise the second caller would reset it (rate → 0).
  if (lastBlocks === 0) {
    // first measurement: establish baseline only
    sample.at = now;
    lastBlocks = info.blocks;
    rateSamples.length = 0;
    rateSamples.push({ t: now, blocks: info.blocks });
  } else if (now - sample.at >= 1000) {
    sample.at = now;
    lastBlocks = info.blocks;
    // keep a short window of samples and average across it to smooth spikes
    rateSamples.push({ t: now, blocks: info.blocks });
    while (rateSamples.length > 8) rateSamples.shift();
    const first = rateSamples[0];
    const last = rateSamples[rateSamples.length - 1];
    if (last.t > first.t) {
      const windowRate = (last.blocks - first.blocks) / ((last.t - first.t) / 1000);
      // only a positive rate updates the display; a stalled window (0 growth)
      // keeps the last known speed instead of flickering back to 0
      if (windowRate > 0) lastRate = windowRate;
    }
  }
  return { height: info.blocks, rate: lastRate };
}

export const Services = {
  // ── Node ──────────────────────────────────────────────────────
  /** Index rebuild progress (served by the app's own /api/index-progress,
   *  NOT btcd RPC, which is not listening during the rebuild). */
  async getIndexProgress(): Promise<{
    height: number;
    total: number;
    percent: number;
  } | null> {
    try {
      const res = await fetch("/api/index-progress");
      if (!res.ok) return null;
      return (await res.json()) as { height: number; total: number; percent: number };
    } catch {
      return null;
    }
  },

  async getSyncStatus(): Promise<SyncStatus> {
    const [info, peers] = await Promise.all([
      rpc<{
        chain: string;
        blocks: number;
        headers: number;
        bestblockhash: string;
        difficulty: number;
        verificationprogress?: number;
      }>("getblockchaininfo"),
      rpc<Array<{ currentheight: number }>>("getpeerinfo").catch(() => []),
    ]);
    const { rate } = await chainSample();
    // This fork keeps its own headers within a window (headerwindow=50000), so
    // local headers ≈ blocks even mid-sync. The real target is the network tip
    // as reported by peers.
    const networkTip = peers.reduce((m, p) => Math.max(m, p.currentheight ?? 0), 0);
    const target = Math.max(info.headers, networkTip);
    const gap = Math.max(0, target - info.blocks);
    const etaMinutes = rate > 0 ? gap / rate / 60 : null;
    const syncedPct =
      info.verificationprogress !== undefined && info.headers >= target
        ? info.verificationprogress * 100
        : target > 0
          ? Math.min(100, (info.blocks / target) * 100)
          : 0;
    return {
      blocks: info.blocks,
      headers: target,
      bestBlockHash: info.bestblockhash,
      difficulty: String(info.difficulty),
      rateBlPerSec: Math.max(0, rate),
      etaMinutes,
      syncedPct,
    };
  },

  async getNodeInfo(): Promise<NodeInfo> {
    const [up, peers] = await Promise.all([
      rpc<number>("uptime").catch(() => 0),
      rpc<
        Array<{
          subver: string;
          version: number;
          inbound: boolean;
          pingtime: number;
        }>
      >("getpeerinfo").catch(() => []),
    ]);
    const subver = peers.find((p) => !p.inbound)?.subver ?? peers[0]?.subver;
    return {
      version: subver || "v?",
      protocol: peers[0]?.version ?? 0,
      p2pPort: 8333,
      dataDir: "C:\\Users\\adest\\AppData\\Local\\Btcd",
      upnp: false,
      proxy: null,
      chain: "sugarmainnet",
      networkactive: true,
      memHeap: 0,
      diskWritePerSec: 0,
      startedAt: Date.now() / 1000 - up,
    };
  },

  async getPeers(): Promise<Peer[]> {
    const peers = await rpc<
      Array<{
        id: number;
        addr: string;
        version: number;
        startingheight: number;
        currentheight: number;
        inbound: boolean;
        pingtime: number;
        syncnode: boolean;
      }>
    >("getpeerinfo");
    return peers.map((p) => ({
      id: p.id,
      dir: p.inbound ? ("inbound" as const) : ("outbound" as const),
      addr: p.addr,
      version: p.version,
      height: p.currentheight ?? p.startingheight,
      syncBlPerSec: p.syncnode ? Math.max(0, lastRate) : null,
      latencyMs: Math.round(p.pingtime ?? 0),
    }));
  },

  async getSyncHistory(n: number): Promise<{ t: number; height: number }[]> {
    const { height } = await chainSample();
    const now = Date.now();
    // Build a plausible recent curve: sample height at the tip, decay backwards
    // by the observed rate (or a small fallback step) — the live tip is real.
    const step = Math.max(1, Math.round(lastRate) || 1);
    return Array.from({ length: n }, (_, i) => ({
      t: now - (n - 1 - i) * 1000,
      height: Math.max(0, height - (n - 1 - i) * step),
    }));
  },

  async getNodeInternals(_detail?: "normal" | "trace"): Promise<NodeInternals> {
    const [info, sync, peers] = await Promise.all([
      rpc<{
        blocks: number;
        headers: number;
        bestblockhash: string;
      }>("getblockchaininfo"),
      rpc<{
        current: boolean;
        ibd: boolean;
        best_chain_height: number;
        header_tip: number;
        header_target: number;
        header_next_assign: number;
        block_target: number;
        block_next_assign: number;
        block_window: number;
        header_slice_len: number;
        header_paused: boolean;
        header_recent_ranges: Array<{
          start: number;
          end: number;
          peer: string;
          assigned_at: number;
        }>;
        peers: Array<{
          id: number;
          addr: string;
          sync_node: boolean;
          sync_candidate: boolean;
          current_height: number;
          slice_start: number;
          slice_end: number;
          slice_assigned_at: number;
          slice_received: number;
          header_range_start: number;
          header_range_end: number;
          header_range_received: boolean;
          header_range_applied: number;
          header_range_assigned_at: number;
          in_flight_blocks: number;
          last_block_at: number;
        }>;
      }>("getblocksyncstatus"),
      // Low-cadence per-peer connection stats for the quality / traffic cards.
      // Sampled here (every poll) but the UI refreshes them at its own pace
      // (5 min / manual), so the extra RPC cost is negligible.
      rpc<
        Array<{
          id: number;
          addr: string;
          bytessent: number;
          bytesrecv: number;
          pingtime: number;
          conntime: number;
          startingheight: number;
          currentheight: number;
          syncnode: boolean;
          inbound: boolean;
        }>
      >("getpeerinfo"),
    ]);
    const chainTip = info.blocks;
    // The node reports the highest known header tip directly; fall back to the
    // local header count / network tip from peers if it ever reports zero.
    const headerTip = Math.max(sync.header_tip, info.headers);
    const windowSize = Math.max(0, sync.block_window);
    // The active download frontier is where the block assignment hands off next:
    // every height below it has been handed out to some peer (received or still
    // in flight), everything above it has not been requested yet.
    const chainBoundary = Math.max(chainTip, sync.block_next_assign - windowSize);

    // Real per-peer slices: each peer owns a disjoint [start, end) range of the
    // chain whose blocks it fetches in parallel.  The node reports slice_received
    // (the highest height this peer has actually delivered in its slice), so a
    // lane fills as *that peer* downloads — every bar moves instead of only the
    // one holding the connected chain tip.  Blocks connect in order, which is
    // why connected progress (chainTip) looks sequential while downloads run
    // ahead of it in parallel.
    const slices = (sync.peers ?? [])
      .filter((p) => p.slice_end > p.slice_start)
      .map((p) => {
        const len = p.slice_end - p.slice_start;
        const low = p.slice_start;
        const recv = p.slice_received ?? low - 1;
        const pct = len > 0 ? Math.min(100, Math.max(0, ((recv - low + 1) / len) * 100)) : 100;
        return {
          peer: p.addr,
          start: p.slice_start,
          end: p.slice_end,
          pct,
          complete: chainTip >= p.slice_end,
          inFlight: p.in_flight_blocks,
          syncNode: p.sync_node,
          lastActiveAt: p.last_block_at * 1000,
          assignedAt: p.slice_assigned_at * 1000,
        };
      })
      .sort((a, b) => a.start - b.start);

    // Header download state is exposed per peer: each peer carries the range it
    // is currently receiving. Received ranges are done, the ones still being
    // pulled are in-flight, and whatever has not been handed out yet is pending.
    const hdrPeers = (sync.peers ?? []).filter((p) => p.header_range_start > 0);
    const headerRanges: { start: number; end: number; state: "done" | "inflight" | "todo" }[] = [];
    if (sync.header_target > chainTip) {
      if (hdrPeers.length > 0) {
        for (const p of hdrPeers) {
          headerRanges.push({
            start: p.header_range_start,
            end: p.header_range_end,
            state: p.header_range_received ? "done" : "inflight",
          });
        }
        const assignedMax = Math.max(chainTip, ...hdrPeers.map((p) => p.header_range_end));
        if (sync.header_target > assignedMax) {
          headerRanges.push({ start: assignedMax, end: sync.header_target, state: "todo" });
        }
      } else if (sync.header_next_assign < sync.header_target) {
        headerRanges.push(
          { start: 0, end: sync.header_next_assign, state: "done" },
          { start: sync.header_next_assign, end: sync.header_target, state: "inflight" },
        );
      } else {
        // Header download already finished: the whole span is done.
        headerRanges.push({ start: 0, end: headerTip, state: "done" });
      }
    } else {
      headerRanges.push({ start: 0, end: headerTip, state: "done" });
    }

    // "lastReissue" = the most recent time any slice/header range was handed to
    // a peer (the node reports it per peer in unix seconds).
    const lastAssignAt = (sync.peers ?? []).reduce(
      (m, p) => Math.max(m, Math.max(p.header_range_assigned_at, p.slice_assigned_at)),
      0,
    );
    const inflightTotal = (sync.peers ?? []).reduce((m, p) => m + p.in_flight_blocks, 0);

    // Merge getpeerinfo rows by addr: btcd opens one connection per peer but
    // can report the same host twice (or three times) with separate ids (one
    // carrying the sync slice, the others idle).  Deduping keeps the quality
    // card to one row per host: bytes sum across connections, connTime is the
    // earliest, ping the most recent non-zero, sync flag ORs together.
    const peerByAddr = new Map<string, {
      id: number;
      addr: string;
      bytesrecv: number;
      bytessent: number;
      pingtime: number;
      conntime: number;
      startingheight: number;
      currentheight: number;
      syncnode: boolean;
      inbound: boolean;
    }>();
    for (const p of peers ?? []) {
      const cur = peerByAddr.get(p.addr);
      if (!cur) {
        peerByAddr.set(p.addr, { ...p });
        continue;
      }
      cur.bytesrecv += p.bytesrecv ?? 0;
      cur.bytessent += p.bytessent ?? 0;
      if (p.pingtime && p.pingtime > 0) cur.pingtime = p.pingtime;
      if (p.conntime && (cur.conntime === 0 || p.conntime < cur.conntime)) cur.conntime = p.conntime;
      if ((p.currentheight ?? 0) > (cur.currentheight ?? 0)) cur.currentheight = p.currentheight ?? 0;
      cur.syncnode = cur.syncnode || p.syncnode;
    }

    return {
      chainTip,
      headerTip,
      chainBoundary,
      headerBoundary: sync.header_target > chainTip ? sync.header_target : headerTip,
      windowSize,
      blockTasks: {
        total: Math.max(0, headerTip - chainTip),
        synced: chainTip,
        inflight: inflightTotal,
        slices,
      },
      headerTasks: {
        ranges: headerRanges,
        hdrPeers: hdrPeers.map((p) => {
          const sliceLen = sync.header_slice_len || 2000;
          return {
            peer: p.addr,
            start: p.header_range_start,
            end: Math.max(p.header_range_start + 1, p.header_range_start + sliceLen),
            received: p.header_range_received,
            applied: p.header_range_applied ?? 0,
            assignedAt: p.header_range_assigned_at ? p.header_range_assigned_at * 1000 : 0,
          };
        }),
        recent: (sync.header_recent_ranges ?? []).map((r) => ({
          start: r.start,
          end: r.end,
          peer: r.peer,
          assignedAt: r.assigned_at * 1000,
        })),
        hdrLanes: (sync.peers ?? [])
          .filter((p) => p.header_range_start > 0 || p.header_range_received)
          .map((p) => {
            const sliceLen = sync.header_slice_len || 2000;
            return {
              peer: p.addr,
              start: p.header_range_start,
              end: Math.max(p.header_range_start + 1, p.header_range_start + sliceLen),
              received: p.header_range_received,
              applied: p.header_range_applied ?? 0,
              assignedAt: p.header_range_assigned_at ? p.header_range_assigned_at * 1000 : 0,
            };
          })
          .sort((a, b) => a.peer.localeCompare(b.peer)),
        sliceLen: sync.header_slice_len ?? 0,
        paused: sync.header_paused ?? false,
        requestedBlocks: Math.max(0, sync.block_target - chainTip),
        lastReissueAt: lastAssignAt > 0 ? lastAssignAt * 1000 : Date.now(),
        nextAssign: sync.header_next_assign ?? 0,
      },
      mem: {
        gap: Math.max(0, headerTip - chainTip),
        window: windowSize,
        inflight: inflightTotal,
      },
      peerStats: [...peerByAddr.values()].map((p) => ({
        id: p.id,
        addr: p.addr,
        bytesRecv: p.bytesrecv ?? 0,
        bytesSent: p.bytessent ?? 0,
        // btcd reports pingtime in microseconds (LastPingMicros); convert to
        // milliseconds for display.  0 means no ping has completed yet (the
        // peer pings every 2 minutes), shown as "—" in the UI.
        pingMs: (p.pingtime ?? 0) / 1000,
        connTime: p.conntime ?? 0,
        startingHeight: p.startingheight ?? 0,
        currentHeight: p.currentheight ?? 0,
        syncNode: p.syncnode ?? false,
        inbound: p.inbound ?? false,
      })),
      debugLevel: await Services.getDebugLevel().catch(() => "info"),
    };
  },

  async setSyncPeers(_n: number): Promise<number> {
    // blocksyncpeers not implemented on the node yet; keep value client-side.
    return _n;
  },

  // ping asks the node to broadcast a ping to every connected peer right now.
  // btcd only pings peers on its own ~2-minute cadence, so a freshly connected
  // peer reports pingtime=0 until the first cycle; triggering a ping lets the
  // quality card fill in the latency column on demand.
  async ping(): Promise<void> {
    await rpc<unknown>("ping");
  },

  // Disconnect a peer by address.  btcd has no timed ban (no setban), so a
  // disconnected peer may reconnect on its own; the internals view keeps a
  // client-side banned list and re-disconnects such peers on each poll until
  // the chosen duration elapses.
  async disconnectNode(addr: string): Promise<void> {
    await rpc<unknown>("node", "disconnect", addr);
  },

  async getDebugLevel(): Promise<string> {
    try {
      const res = await rpc<{ message: string }>("debuglevel", "show");
      // btcd returns "Results: { "level": "info" }-ish text
      const text = typeof res === "string" ? res : JSON.stringify(res ?? "");
      const m = text.match(/(\bdebug\b|\binfo\b|\bwarn\b|\berror\b|\btrace\b|\boff\b)/);
      return (m?.[1] ?? "info") as string;
    } catch {
      return "info";
    }
  },

  async setDebugLevel(spec: string): Promise<void> {
    await rpc("debuglevel", spec);
  },

  async disconnectPeer(id: number): Promise<void> {
    // btcd: `node disconnect <id>` accepts a numeric node id directly.
    try {
      await rpc("node", "disconnect", String(id));
    } catch {
      /* peer may already be gone */
    }
  },

  async resetPeers(): Promise<void> {
    try {
      await rpc("ping");
    } catch {
      /* ignore */
    }
  },

  async testNode(): Promise<{ ok: boolean; ms: number }> {
    const t0 = performance.now();
    try {
      await rpc<number>("getblockcount");
      return { ok: true, ms: Math.round(performance.now() - t0) };
    } catch {
      return { ok: false, ms: Math.round(performance.now() - t0) };
    }
  },

  // ── Wallet (local Wails bindings + node RPC for chain data) ────
  // Lifecycle/key commands run in-process (node NOT required); balance &
  // history come from the node's sugar index over RPC and degrade to
  // placeholders when the node is offline or the index is disabled.
  // 钱包服务(本地 Wails bindings + 节点 RPC 链上数据):生命周期/密钥命令在
  // 进程内运行(无需节点);余额与历史走节点 sugar index RPC,节点离线或索引
  // 未启用时降级为占位显示。

  // Hybrid snapshot: local Status() is authoritative for locked/address/name
  // (never fails); getwalletinfo RPC only enriches the balance figures. With
  // the node down the wallet page still renders — balance shows "—".
  // 混合快照:本地 Status() 是 locked/address/name 的权威来源(永不失败);
  // getwalletinfo RPC 仅补充余额数字。节点未启动时钱包页照常渲染——余额
  // 显示 "—"。
  async getWallet(): Promise<WalletState> {
    const st = await LocalWallet.Status();
    let chain: { total: number; confirmed: number; pending: number; immature: number; watchonly: number } | null =
      null;
    if (st.unlocked) {
      try {
        chain = await rpc<{
          total: number;
          confirmed: number;
          pending: number;
          immature: number;
          watchonly: number;
        }>("getwalletinfo");
      } catch {
        /* node offline or sugarindex disabled — figures stay unknown */
        /* 节点离线或 sugarindex 未启用 — 数字保持未知 */
      }
    }
    return {
      locked: !st.unlocked,
      total: chain?.total ?? 0,
      confirmed: chain?.confirmed ?? 0,
      pending: chain?.pending ?? 0,
      immature: chain?.immature ?? 0,
      watchOnly: chain?.watchonly ?? 0,
      address: st.address || null,
      defaultWalletName: st.name,
      chainOnline: chain !== null,
    };
  },

  // Unlock wallet.db with the passphrase, in-process. / 进程内用口令解锁 wallet.db。
  async unlock(pass: string): Promise<boolean> {
    try {
      const addr = await LocalWallet.Unlock(pass);
      return !!addr;
    } catch {
      return false;
    }
  },

  // Legacy email/password login (in-memory KDF); returns the primary
  // (web-wallet compatible) address. / 邮箱密码登录(纯内存 KDF),返回主地址。
  async login(email: string, password: string): Promise<{ address: string }> {
    const address = await LocalWallet.Login(email, password);
    return { address };
  },

  // Fresh BIP39 wallet, in-process; the mnemonic is returned once.
  // 进程内创建全新 BIP39 钱包;助记词仅返回一次。
  async createWallet(pass: string): Promise<{
    name: string;
    mnemonic: string;
    address: string;
  }> {
    const r = await LocalWallet.Create(pass);
    return { name: r.name, mnemonic: r.mnemonic, address: r.address };
  },

  // Lock: drop in-process key material. / 锁定:丢弃进程内密钥材料。
  async lockWallet(): Promise<void> {
    await LocalWallet.Lock();
  },

  // Next derived address (advances the persisted index), in-process.
  // 进程内获取下一个派生地址(推进持久化索引)。
  async getNewAddress(): Promise<string> {
    return LocalWallet.NextAddress();
  },

  // Read-only list of derived addresses (index 0 .. next-1) for the Keys
  // tab; does NOT advance the index. Local, node not required.
  // Keys 标签页的只读地址列表(index 0 .. next-1);不推进索引。
  // 纯本地,无需节点。
  async getAddresses(): Promise<{ index: number; address: string }[]> {
    return LocalWallet.Addresses();
  },

  // listtransactions → recent wallet history mapped to the frontend Tx shape.
  // Degrades to [] when the sugar index is disabled so the page stays usable.
  // `n` is the row count requested (wallet settings → history rows).
  // listtransactions → 映射为前端 Tx 形态的近期历史;sugar 索引未启用时降级为
  // 空数组,页面保持可用。n 为请求条数(钱包设置 → 历史记录条数)。
  async getHistory(n = 25): Promise<Tx[]> {
    try {
      const rs = await rpc<
        Array<{
          category: "send" | "receive";
          address?: string;
          amount: number;
          txid: string;
          blocktime: number;
          confirmations: number;
        }>
      >("listtransactions", "*", n);
      return rs.map((r) => ({
        time: r.blocktime,
        dir: r.category === "send" ? "out" : "in",
        amount: r.amount,
        status: r.confirmations > 0 ? "confirmed" : "pending",
        hash: r.txid,
      }));
    } catch {
      return []; // sugarindex off / wallet locked → empty history / 索引未启用或锁定 → 空历史
    }
  },

  // ── Config ────────────────────────────────────────────────────
  async getConfig(): Promise<AppConfig> {
    const cfg = (await fetch("/api/node-config").then((r) => r.json())) as {
      rpcEndpoint: string;
      rpcUser: string;
      rpcPass: string;
      credFromIni: boolean;
    };
    return {
      ...cfg,
      walletApi: "http://127.0.0.1:8080",
      parallelPeers: 8,
      addrType: "bech32",
      defaultWallet: "main",
      dataDir: "C:\\Users\\adest\\AppData\\Local\\Btcd",
      diskFree: 0,
      runNodeOnStart: true,
      debugLevel: "info",
      upnp: false,
      proxy: null,
    };
  },

  async saveConfig(cfg: AppConfig): Promise<AppConfig> {
    try {
      await fetch("/api/node-config", {
        method: "POST",
        headers: { "content-type": "application/json" },
        body: JSON.stringify({
          rpcuser: cfg.rpcUser,
          rpcpass: cfg.rpcPass,
          rpcendpoint: cfg.rpcEndpoint,
        }),
      });
    } catch {
      /* ini write failure — keep values client-side */
    }
    return cfg;
  },

  async pickDataDir(): Promise<string | null> {
    return "C:\\Users\\adest\\AppData\\Local\\Btcd";
  },

  async migrateDataDir(_from: string, _to: string): Promise<number> {
    return 0;
  },

  async estimateFee(_target: number): Promise<number> {
    try {
      return await rpc<number>("estimatesmartfee", _target);
    } catch {
      return 0;
    }
  },

  async buildPsbt(_to: string, _amountS: number, _feeS: number) {
    throw new Error("buildPsbt requires the Go wallet layer (not yet wired)");
  },

  async broadcast(_hex: string): Promise<string> {
    return rpc<string>("sendrawtransaction", _hex);
  },

  async rpcCall(method: string, params: unknown[]): Promise<RpcResult> {
    const started = performance.now();
    const raw = (await rpc<unknown>(method, ...(params ?? []))) as unknown;
    const isSimple = ["getbestblockhash", "getblockcount", "uptime", "ping"].includes(method);
    const output =
      isSimple && typeof raw === "number" ? String(raw) : JSON.stringify(raw, null, 2);
    return {
      method,
      output: output ?? "null",
      elapsedMs: Math.round(performance.now() - started),
      format: isSimple ? "text" : "json",
    };
  },
};
