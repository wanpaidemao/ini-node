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

// ── low-level JSON-RPC client ───────────────────────────────────
let seq = 1;

interface RpcEnvelope {
  result?: unknown;
  error?: { code: number; message: string };
  id: number;
}

async function rpc<T>(method: string, ...params: unknown[]): Promise<T> {
  const id = seq++;
  const res = await fetch("/rpc/", {
    method: "POST",
    headers: { "content-type": "application/json" },
    body: JSON.stringify({ jsonrpc: "1.0", id, method, params }),
  });
  if (!res.ok) {
    throw new Error(`RPC ${method}: HTTP ${res.status}`);
  }
  const env = (await res.json()) as RpcEnvelope;
  if (env.error) throw new Error(`RPC ${method}: ${env.error.message}`);
  return env.result as T;
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
    const [info, sync] = await Promise.all([
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
        peers: Array<{
          id: number;
          addr: string;
          sync_node: boolean;
          sync_candidate: boolean;
          current_height: number;
          slice_start: number;
          slice_end: number;
          slice_assigned_at: number;
          header_range_start: number;
          header_range_end: number;
          header_range_received: boolean;
          header_range_assigned_at: number;
          in_flight_blocks: number;
          last_block_at: number;
        }>;
      }>("getblocksyncstatus"),
    ]);
    const chainTip = info.blocks;
    // The node reports the highest known header tip directly; fall back to the
    // local header count / network tip from peers if it ever reports zero.
    const headerTip = Math.max(sync.header_tip, info.headers);
    const windowSize = Math.max(0, sync.block_window);
    // The active download frontier is where the block assignment hands off next.
    const chainBoundary = Math.max(chainTip, sync.block_next_assign - windowSize);

    // Real per-peer slices: each peer owns a disjoint [start, end) range of the
    // chain. Progress is how much of that range the connected chain has caught
    // up to (blocks connect in order, so a slice is done once best_chain_height
    // reaches its end).
    const slices = sync.peers
      .filter((p) => p.slice_end > p.slice_start)
      .map((p) => {
        const len = p.slice_end - p.slice_start;
        const pct = len > 0 ? Math.min(100, Math.max(0, ((chainTip - p.slice_start) / len) * 100)) : 100;
        return {
          peer: p.addr,
          start: p.slice_start,
          end: p.slice_end,
          pct,
          complete: chainTip >= p.slice_end,
          inFlight: p.in_flight_blocks,
          syncNode: p.sync_node,
          lastActiveAt: p.last_block_at * 1000,
        };
      })
      .sort((a, b) => a.start - b.start);

    // Header download state: once the parallel header download has finished the
    // whole reported range is done; while it is active the assigned frontier is
    // in-flight and the rest is pending.
    const headerRanges =
      sync.header_target > chainTip
        ? [
            { start: 0, end: sync.header_next_assign, state: "done" as const },
            { start: sync.header_next_assign, end: sync.header_target, state: "inflight" as const },
          ]
        : [{ start: 0, end: headerTip, state: "done" as const }];

    return {
      chainTip,
      headerTip,
      chainBoundary,
      headerBoundary: sync.header_target > chainTip ? sync.header_target : headerTip,
      windowSize,
      blockTasks: {
        total: Math.max(0, headerTip - chainTip),
        synced: chainTip,
        slices,
      },
      headerTasks: {
        ranges: headerRanges,
        requestedBlocks: Math.max(0, sync.block_target - chainTip),
        lastReissueAt: Date.now() - 3 * 60_000,
      },
      mem: { alloc: 0, heapAlloc: 0, heapObjects: 0, numGC: 0 },
      debugLevel: "info",
      sampling: Array.from({ length: 60 }, (_, i) => ({
        t: Date.now() - (59 - i) * 1000,
        height: chainTip - (59 - i) * Math.max(1, Math.round(lastRate) || 1),
      })),
    };
  },

  async setSyncPeers(_n: number): Promise<number> {
    // blocksyncpeers not implemented on the node yet; keep value client-side.
    return _n;
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

  // ── Wallet (mock until Go wallet layer exists) ────────────────
  async getWallet(): Promise<WalletState> {
    return {
      locked: false,
      total: 0,
      confirmed: 0,
      pending: 0,
      immature: 0,
      watchOnly: 0,
      address: null,
      defaultWalletName: "main",
    };
  },

  async unlock(_pass: string): Promise<boolean> {
    return true;
  },

  async getHistory(): Promise<Tx[]> {
    return [];
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
