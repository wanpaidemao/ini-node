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
  ExplorerBlock,
  ExplorerChain,
  ExplorerTx,
  NodeInfo,
  NodeInternals,
  Peer,
  RpcResult,
  SyncStatus,
  TokenBalance,
  TokenInfo,
  Tx,
  WalletState,
} from "./types";

// Wails bindings for the in-process wallet service: wallet lifecycle
// (create / unlock / login / lock / next address) runs inside the frontend
// process and works WITHOUT the node. Chain data still goes over RPC below.
// 进程内钱包服务的 Wails bindings:钱包生命周期(创建/解锁/登录/锁定/新地址)
// 在前端进程内运行,无需节点。链上数据仍走下方 RPC。
import * as LocalWallet from "../../bindings/changeme/walletservice.js";
import * as LocalGreet from "../../bindings/changeme/greetservice.js";
// Step 8 send pipeline bindings: UTXO query → coin selection → build+sign →
// broadcast, all in-process (two-level UTXO/broadcast chain: node first,
// external REST fallback when the wallet settings enable it).
// 第 8 步发送链路 bindings:UTXO 查询 → 选币 → 构造+签名 → 广播,全在进程内
// (UTXO/广播两级链:先节点,钱包设置启用外部源时降级外部 REST)。
import * as LocalSend from "../../bindings/changeme/sendservice.js";
// Step 6 token layer bindings: balances/transfer/create/issue/burn against
// the off-chain token REST layer, in-process, sharing the wallet session.
// 第 6 步代币层 bindings:对接链外代币 REST 层的余额/转账/创建/增发/销毁,
// 进程内完成,与钱包共享会话。
import * as LocalToken from "../../bindings/changeme/tokenservice.js";
import { chainSource } from "./wallet-registry.svelte";
import { walletSettings } from "./wallet-settings.svelte";

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
  // (never fails); the chain figures come from a two-step source chain —
  // (1) the node's getwalletinfo RPC, (2) if that fails and an external REST
  // source is configured (wallet settings), GET {api}/balance/{addr}
  // (response {"result":{"balance":N}}, same protocol as the original
  // web-wallet's backend). History/UTXO stay node-only (the REST explorer
  // has no history endpoint — same as the original, which linked out to a
  // block explorer).
  // 混合快照:本地 Status() 是 locked/address/name 的权威来源(永不失败);
  // 链上数字走两级数据源——(1) 节点 getwalletinfo RPC;(2) 失败且配置了
  // 外部 REST 源(钱包设置)时,GET {api}/balance/{addr}(响应
  // {"result":{"balance":N}},与原版 web 钱包的后端接口同协议)。历史/UTXO
  // 仅节点提供(REST 浏览器无历史端点——原版也是外链区块浏览器)。
  async getWallet(): Promise<WalletState> {
    const st = await LocalWallet.Status();
    // Step 9: the shown address follows the configured address type
    // (bech32/segwit/legacy). The switch takes effect immediately — no
    // restart, no re-unlock. Falls back to the bech32 default on error.
    // 第 9 步:展示地址跟随配置的地址类型(bech32/segwit/legacy)。
    // 切换即时生效——无需重启、无需重新解锁。出错时回退 bech32 默认值。
    if (st.unlocked) {
      try {
        st.address = await LocalWallet.AddressFor(0, walletSettings.addressType);
      } catch {
        /* locked mid-call — keep the status address / 调用间隙锁定 — 保留状态地址 */
      }
    }
    let chain: { total: number; confirmed: number; pending: number; immature: number; watchonly: number } | null =
      null;
    let external = false;
    if (st.unlocked && st.address) {
      try {
        // Balance is aggregated by the sugar index per address. Query the
        // primary address directly (getaddressbalance) instead of the node's
        // getwalletinfo — the latter requires the NODE's own wallet to be
        // unlocked, a separate instance from the in-process wallet unlocked
        // here through Wails bindings (so it would always fail).
        // 余额由 sugar 索引按地址聚合。直接查主地址(getaddressbalance),
        // 而非节点 getwalletinfo——后者需要节点自身钱包解锁,与本进程经
        // Wails bindings 解锁的钱包是不同实例(因此必然失败)。
        const bal = await rpc<{
          balance: number;
          balance_spendable: number;
          balance_immature: number;
        }>("getaddressbalance", st.address);
        chain = {
          total: bal.balance / 1e8,
          confirmed: bal.balance_spendable / 1e8,
          pending: 0,
          immature: bal.balance_immature / 1e8,
          watchonly: 0,
        };
      } catch {
        /* node offline or sugarindex disabled — try the external source */
        /* 节点离线或 sugarindex 未启用 — 尝试外部数据源 */
      }
      if (!chain && chainSource.mode === "external" && st.address) {
        try {
          const r = await fetch(`${chainSource.api.replace(/\/+$/, "")}/balance/${st.address}`);
          if (r.ok) {
            const env = (await r.json()) as { result?: { balance?: number } };
            const bal = toNum(env.result?.balance);
            // REST explorer reports a single confirmed figure only.
            // REST 浏览器仅提供单一确认余额数字。
            chain = { total: bal, confirmed: bal, pending: 0, immature: 0, watchonly: 0 };
            external = true;
          }
        } catch {
          /* external source also unreachable — figures stay unknown */
          /* 外部数据源也不可达 — 数字保持未知 */
        }
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
      // mark the external source so the UI can badge it / 标记外部数据源供 UI 徽章展示
      chainExternal: external,
    };
  },

  // Unlock a BIP39 wallet by name with the passphrase, in-process.
  // 进程内用口令按名字解锁 BIP39 钱包。
  async unlock(name: string, pass: string): Promise<boolean> {
    try {
      const addr = await LocalWallet.Unlock(name, pass);
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
  async createWallet(name: string, pass: string): Promise<{
    name: string;
    mnemonic: string;
    address: string;
  }> {
    const r = await LocalWallet.Create(name, pass);
    return { name: r.name, mnemonic: r.mnemonic, address: r.address };
  },

  // Restore a BIP39 wallet from its mnemonic, in-process; saved under name
  // and left unlocked. Returns the primary address.
  // 进程内用助记词恢复 BIP39 钱包;以 name 保存并保持解锁。返回主地址。
  async restoreWallet(name: string, mnemonic: string, pass: string): Promise<string> {
    return LocalWallet.Restore(name, mnemonic, pass);
  },

  // Names of all BIP39 wallets on disk (sorted). / 磁盘上所有 BIP39 钱包名(排序)。
  async listWallets(): Promise<string[]> {
    return (await LocalWallet.List()) ?? [];
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
    // binding signature is `[] | null`; normalize to always-array
    // binding 签名为 `[] | null`;归一化为始终数组
    return (await LocalWallet.Addresses()) ?? [];
  },

  // Step 9: address of the given type at a derivation index (immediate
  // effect, no restart). / 第 9 步:派生索引上指定类型的地址(即时生效,
  // 无需重启)。
  async getAddressFor(index: number, addrType: string): Promise<string> {
    return LocalWallet.AddressFor(index, addrType);
  },

  // Step 11: import a WIF private key (in-memory hybrid wallet; index 0 is
  // the imported key's web-wallet address). / 第 11 步:导入 WIF 私钥
  // (纯内存混合钱包;index 0 即导入私钥的 web-wallet 地址)。
  async loginWIF(wif: string): Promise<{ address: string }> {
    const address = await LocalWallet.LoginWIF(wif);
    return { address };
  },

  // Per-address WIF export for backups / migration (Keys tab).
  // 按地址导出 WIF,供备份/迁移(Keys 标签页)。
  async exportWIF(index: number): Promise<string> {
    return LocalWallet.ExportWIF(index);
  },

  // ── Token layer (Step 6, in-process Wails bindings) ───────────
  // Amounts are display-unit floats here; the base-unit conversion uses the
  // token's own decimals (web-wallet amountFormatSatoshis parity).
  // 代币层(第 6 步,进程内 Wails bindings)。此处金额为显示单位浮点;
  // 基本单位换算按代币自身 decimals(对齐 web-wallet amountFormatSatoshis)。
  async getTokenBalances(): Promise<TokenBalance[]> {
    return (await LocalToken.Balances(walletSettings.tokenAPI)) ?? [];
  },

  async getTokenInfo(ticker: string): Promise<TokenInfo> {
    return LocalToken.Info(ticker, walletSettings.tokenAPI);
  },

  // Token transfer: value is display units of the token; markerS is the
  // SUGAR amount riding along with the token output (tokens follow the
  // marker — it must reach the recipient); feeS is the total miner fee.
  // 代币转账:value 为代币显示单位;markerS 为随代币输出携带的 SUGAR 金额
  // (代币跟随 marker——必须到达收款人);feeS 为总矿工费。
  async tokenTransfer(
    ticker: string,
    to: string,
    value: number,
    markerS: number,
    feeS: number,
  ): Promise<{
    txid: string;
    rawHex: string;
    broadcastErr: string;
    fee: number;
    inputCount: number;
  }> {
    const info = await this.getTokenInfo(ticker).catch(() => ({ decimals: 0 } as TokenInfo));
    const sat = (v: number) => Math.round(v * 1e8);
    const base = Math.round(value * Math.pow(10, info.decimals ?? 0));
    const ext = chainSource.mode === "external" ? chainSource.api : "";
    return LocalToken.Transfer(ticker, to, base, sat(markerS), sat(feeS), 0, walletSettings.tokenAPI, ext);
  },

  // Create a new token. / 创建新代币。
  async tokenCreate(
    ticker: string,
    value: number,
    decimals: number,
    reissuable: boolean,
    feeS: number,
  ): Promise<{ txid: string; rawHex: string; broadcastErr: string; fee: number; inputCount: number }> {
    const base = Math.round(value * Math.pow(10, decimals));
    const sat = (v: number) => Math.round(v * 1e8);
    const ext = chainSource.mode === "external" ? chainSource.api : "";
    return LocalToken.Create(ticker, base, decimals, reissuable, sat(feeS), walletSettings.tokenAPI, ext);
  },

  // Issue (mint) additional units. / 增发代币。
  async tokenIssue(
    ticker: string,
    value: number,
    feeS: number,
  ): Promise<{ txid: string; rawHex: string; broadcastErr: string; fee: number; inputCount: number }> {
    const info = await this.getTokenInfo(ticker);
    const base = Math.round(value * Math.pow(10, info.decimals ?? 0));
    const sat = (v: number) => Math.round(v * 1e8);
    const ext = chainSource.mode === "external" ? chainSource.api : "";
    return LocalToken.Issue(ticker, base, sat(feeS), walletSettings.tokenAPI, ext);
  },

  // Burn units. / 销毁代币。
  async tokenBurn(
    ticker: string,
    value: number,
    feeS: number,
  ): Promise<{ txid: string; rawHex: string; broadcastErr: string; fee: number; inputCount: number }> {
    const info = await this.getTokenInfo(ticker);
    const base = Math.round(value * Math.pow(10, info.decimals ?? 0));
    const sat = (v: number) => Math.round(v * 1e8);
    const ext = chainSource.mode === "external" ? chainSource.api : "";
    return LocalToken.Burn(ticker, base, sat(feeS), walletSettings.tokenAPI, ext);
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

  // ── Send pipeline (Step 8, in-process Wails bindings) ─────────
  // Amounts are SUGAR (display unit) here; converted to satoshis for the
  // Go pipeline. Broadcast failures come back as SendResult.broadcastErr
  // with rawHex kept — the UI offers a retry, not an error dead-end.
  // 发送链路(第 8 步,进程内 Wails bindings)。此处金额为 SUGAR(显示单位),
  // 传给 Go 流水线前换算为聪。广播失败以 SendResult.broadcastErr 返回且保留
  // rawHex——UI 提供重试,而非错误死胡同。
  async sendOutputs(
    outputs: { address: string; amount: number }[],
    feeSugar: number,
  ): Promise<{
    txid: string;
    rawHex: string;
    totalIn: number;
    amount: number;
    fee: number;
    change: number;
    inputCount: number;
    broadcastErr: string;
  }> {
    // externalAPI is forwarded only when the wallet settings enable the
    // external chain-data source; otherwise the pipeline is node-only.
    // 仅当钱包设置启用外部链上数据源时转发 externalAPI;否则流水线仅用节点。
    const ext = chainSource.mode === "external" ? chainSource.api : "";
    const sat = (v: number) => Math.round(v * 1e8);
    return LocalSend.Send(
      outputs.map((o) => ({ address: o.address, amount: sat(o.amount) })),
      sat(feeSugar),
      ext,
    );
  },

  // Retry broadcasting a previously signed raw tx (result card retry
  // button). Node first, external fallback. / 重试广播先前已签名裸交易
  // (结果卡重试按钮)。先节点,后外部降级。
  async broadcastRaw(rawHex: string): Promise<string> {
    const ext = chainSource.mode === "external" ? chainSource.api : "";
    return LocalSend.BroadcastRaw(rawHex, ext);
  },

  // Fee suggestion from the external REST /fee endpoint (SUGAR float).
  // Errors when no external source is configured — the UI keeps the
  // manual fee input. / 外部 REST /fee 端点的手续费建议(SUGAR 浮点)。
  // 未配置外部源时报错——UI 保留手动手续费输入。
  async estimateFee(): Promise<number> {
    if (chainSource.mode !== "external") {
      throw new Error("no external API configured / 未配置外部 API");
    }
    return LocalSend.EstimateFee(chainSource.api);
  },

  // Coin-control view: the wallet's spendable outputs (node
  // getaddressutxos, external REST fallback). Read-only.
  // 币控视图:钱包可花费输出(节点 getaddressutxos,外部 REST 降级)。只读。
  async getUTXOs(): Promise<
    { txid: string; index: number; value: number; script: string; address: string }[]
  > {
    const ext = chainSource.mode === "external" ? chainSource.api : "";
    return (await LocalSend.UTXOs(ext)) ?? [];
  },

  // ── Explorer (Step 10): local node RPC data, three-level drill-down ──
  // Requires txindex=1 for tx lookups (getrawtransaction verbose) — the
  // pages degrade with an error card when the index is off, matching the
  // wallet history behavior.
  // 浏览器(第 10 步):本节点 RPC 数据,三级下钻。交易查询(带详细信息的
  // getrawtransaction)需要 txindex=1——索引未启用时页面以错误卡降级,
  // 与钱包历史行为一致。
  async getExplorerChain(): Promise<ExplorerChain> {
    // getblockchaininfo returns blocks/headers/bestblockhash; map them onto
    // the ExplorerChain shape (height/headers/bestHash) the page renders.
    // getblockchaininfo 返回 blocks/headers/bestblockhash;映射到页面渲染的
    // ExplorerChain 形态(height/headers/bestHash)。
    const info = await rpc<{
      chain: string;
      blocks: number;
      headers: number;
      bestblockhash: string;
      difficulty: number;
      mediantime: number;
    }>("getblockchaininfo");
    return {
      height: info.blocks,
      headers: info.headers,
      bestHash: info.bestblockhash,
      chain: info.chain,
      difficulty: info.difficulty,
      mediantime: info.mediantime,
    };
  },

  // Height → hash for the explorer search box. / 高度 → 哈希,浏览器搜索用。
  async getExplorerBlockHash(height: number): Promise<string> {
    return rpc<string>("getblockhash", height);
  },

  // Recent blocks newest-first: getblockhash(h) + getblock(hash, 1) per
  // height (verbosity 1 keeps the list light — tx ids only).
  // 最新在前的区块列表:逐高度 getblockhash(h) + getblock(hash, 1)
  // (verbosity 1 保证列表轻量——只含交易 id)。
  async getExplorerBlocks(count = 12): Promise<ExplorerBlock[]> {
    const info = await rpc<{ blocks: number }>("getblockchaininfo");
    const out: ExplorerBlock[] = [];
    for (let h = info.blocks; h > Math.max(-1, info.blocks - count); h--) {
      const hash = await rpc<string>("getblockhash", h);
      const b = await rpc<{
        hash: string;
        height: number;
        confirmations: number;
        time: number;
        size: number;
        nonce: number;
        bits: string;
        difficulty: number;
        tx: string[];
      }>("getblock", hash, 1);
      out.push({
        hash: b.hash,
        height: b.height,
        confirmations: b.confirmations,
        time: b.time,
        size: b.size,
        txCount: (b.tx ?? []).length,
        nonce: b.nonce,
        bits: b.bits,
        difficulty: b.difficulty,
      });
    }
    return out;
  },

  // Block detail: verbosity 2 carries full transactions (ids + summaries).
  // 区块详情:verbosity 2 携带完整交易(id + 摘要)。
  async getExplorerBlock(hash: string): Promise<ExplorerBlock & { tx: string[] }> {
    const b = await rpc<{
      hash: string;
      height: number;
      confirmations: number;
      time: number;
      size: number;
      nonce: number;
      bits: string;
      difficulty: number;
      // btcd verbosity 1 answers ids in "tx"; verbosity 2 answers verbose
      // objects in "rawtx". Accept both and normalize below.
      // btcd verbosity 1 在 "tx" 里返回 id;verbosity 2 在 "rawtx" 里返回
      // 详述对象。两种都接受,下面归一。
      tx?: string[];
      rawtx?: Array<{ txid: string }>;
    }>("getblock", hash, 2);
    // btcd verbosity 2 answers tx as verbose objects under rawtx; verbosity 1
    // as ids under tx. Normalize both into a plain id list.
    // btcd verbosity 2 的交易在 rawtx 下是详述对象;verbosity 1 的 tx 是 id。
    // 两种形态都归一为纯 id 列表。
    const raw = b.rawtx ?? b.tx ?? [];
    const tx = raw.map((t) => (typeof t === "string" ? t : (t as { txid: string }).txid));
    return {
      hash: b.hash,
      height: b.height,
      confirmations: b.confirmations,
      time: b.time,
      size: b.size,
      txCount: tx.length,
      nonce: b.nonce,
      bits: b.bits,
      difficulty: b.difficulty,
      tx,
    };
  },

  // Transaction detail via getrawtransaction verbose (needs txindex=1).
  // 交易详情:带详细信息的 getrawtransaction(需 txindex=1)。
  async getExplorerTx(txid: string): Promise<ExplorerTx> {
    const tx = await rpc<{
      txid: string;
      blockhash?: string;
      blocktime?: number;
      confirmations?: number;
      time?: number;
      size: number;
      vin: Array<{ txid?: string; vout?: number; addr?: string; address?: string; coinbase?: string }>;
      vout: Array<{ n: number; value: number; scriptPubKey: { addresses?: string[]; address?: string } }>;
    }>("getrawtransaction", txid, 1);
    return {
      txid: tx.txid,
      blockhash: tx.blockhash,
      blocktime: tx.blocktime,
      confirmations: tx.confirmations,
      time: tx.time,
      size: tx.size,
      vinCount: (tx.vin ?? []).length,
      voutCount: (tx.vout ?? []).length,
      totalOut: (tx.vout ?? []).reduce((s, o) => s + o.value, 0),
      outputs: (tx.vout ?? []).map((o) => ({
        n: o.n,
        value: o.value,
        address: o.scriptPubKey?.addresses?.[0] ?? o.scriptPubKey?.address ?? null,
      })),
      inputs: (tx.vin ?? []).map((v) => ({
        txid: v.txid ?? "",
        vout: v.vout ?? 0,
        address: v.addr ?? v.address ?? null,
      })),
    };
  },

  // ── Config ────────────────────────────────────────────────────
  // getConfig returns the REAL node ini state: /api/node-config parses
  // backend/btcd-runtime.ini server-side and returns every key (datadir,
  // maxpeers, upnp, rpcuser, ...), plus frontend-only settings
  // (runnodeonstart) from frontend.ini. Values are mapped into AppConfig;
  // absent keys fall back to btcd defaults. The old version discarded this
  // data and returned hardcoded mock values (a foreign machine's datadir!)
  // — that is gone.
  // getConfig 返回真实的节点 ini 状态:/api/node-config 在服务端解析
  // backend/btcd-runtime.ini 并返回全部键(datadir/maxpeers/upnp/
  // rpcuser/...),以及 frontend.ini 的前端专属设置(runnodeonstart)。
  // 映射进 AppConfig;缺失键回退到 btcd 默认值。旧版本丢弃这些数据并
  // 返回硬编码 mock(还是别人机器的 datadir!)——已移除。
  async getConfig(): Promise<AppConfig> {
    const raw = (await fetch("/api/node-config").then((r) => r.json())) as Record<string, string>;
    const num = (k: string, dflt: number): number => {
      const v = Number(raw[k]);
      return Number.isFinite(v) && v > 0 ? v : dflt;
    };
    const bool = (k: string): boolean => raw[k] === "1" || raw[k] === "true";
    return {
      rpcEndpoint: raw.rpcEndpoint ?? "http://127.0.0.1:8334",
      rpcUser: raw.rpcuser ?? "",
      rpcPass: raw.rpcpass ?? "",
      credFromIni: raw.credFromIni !== "false",
      // node ini keys (btcd options, applied on node restart)
      // 节点 ini 键(btcd 选项,节点重启后生效)
      dataDir: raw.datadir ?? "",
      maxPeers: num("maxpeers", 8),
      upnp: bool("upnp"),
      sugarIndex: bool("sugarindex"),
      // frontend-only keys stored in frontend.ini / 前端专属键,存 frontend.ini
      runNodeOnStart: raw.runnodeonstart !== "0" && raw.runnodeonstart !== "false",
      // iniPath for reference (control center ini picker) / ini 路径(参考)
      iniPath: raw.iniPath ?? "",
    };
  },

  // saveConfig POSTs the changed fields. Server-side routing: rpcendpoint →
  // rpclisten, runnodeonstart → frontend.ini, everything else → runtime ini
  // (btcd applies on next start; debugLevel additionally applies live via
  // the debuglevel RPC when the node is running).
  // saveConfig 提交变更字段。服务端路由:rpcendpoint → rpclisten,
  // runnodeonstart → frontend.ini,其余 → runtime ini(btcd 下次启动生效;
  // debugLevel 在节点运行时还经 debuglevel RPC 即时生效)。
  async saveConfig(cfg: Partial<AppConfig>): Promise<void> {
    const body: Record<string, string> = {};
    if (cfg.rpcEndpoint !== undefined) body.rpcendpoint = cfg.rpcEndpoint;
    if (cfg.rpcUser !== undefined) body.rpcuser = cfg.rpcUser;
    if (cfg.rpcPass !== undefined && cfg.rpcPass !== "") body.rpcpass = cfg.rpcPass;
    if (cfg.dataDir !== undefined) body.datadir = cfg.dataDir;
    if (cfg.maxPeers !== undefined) body.maxpeers = String(cfg.maxPeers);
    if (cfg.upnp !== undefined) body.upnp = cfg.upnp ? "1" : "0";
    if (cfg.runNodeOnStart !== undefined) body.runnodeonstart = cfg.runNodeOnStart ? "1" : "0";
    if (cfg.debugLevel !== undefined) body.debuglevel = cfg.debugLevel;
    if (Object.keys(body).length === 0) return;
    await fetch("/api/node-config", {
      method: "POST",
      headers: { "content-type": "application/json" },
      body: JSON.stringify(body),
    });
  },

  // Open the data directory in the OS file manager (Go binding).
  // 在系统文件管理器中打开数据目录(Go binding)。
  async openDataDir(path: string): Promise<void> {
    await LocalGreet.OpenDataDir(path);
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
