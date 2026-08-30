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
    if (!res.ok) {
      throw new Error(`RPC ${method}: HTTP ${res.status}`);
    }
    const env = (await res.json()) as RpcEnvelope;
    if (env.error) throw new Error(`RPC ${method}: ${env.error.message}`);
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

  // ── Wallet (real RPCs backed by the built-in HD wallet) ────────
  // Lifecycle/key commands always work; balance & history additionally
  // require --sugarindex on the node (they degrade gracefully here).
  // 钱包服务(真实 RPC,由内置 HD 钱包提供):生命周期/密钥命令始终可用;
  // 余额与历史另需节点启用 --sugarindex(此处优雅降级)。

  // getwalletinfo → WalletState; a locked/not-created wallet maps to the
  // locked shell so the page can show the unlock form.
  // getwalletinfo → WalletState;锁定/未创建的钱包映射为锁定外壳状态,
  // 以便页面展示解锁表单。
  async getWallet(): Promise<WalletState> {
    try {
      const r = await rpc<{
        locked: boolean;
        walletname: string;
        address: string;
        total: number;
        confirmed: number;
        pending: number;
        immature: number;
        watchonly: number;
      }>("getwalletinfo");
      return {
        locked: false,
        total: r.total,
        confirmed: r.confirmed,
        pending: r.pending,
        immature: r.immature,
        watchOnly: r.watchonly,
        address: r.address,
        defaultWalletName: r.walletname,
      };
    } catch (e) {
      const msg = String(e);
      // Locked / not created / manager unavailable → locked shell state.
      // 锁定/未创建/管理器不可用 → 锁定外壳状态。
      if (msg.includes("locked") || msg.includes("not created") || msg.includes("not available")) {
        return {
          locked: true,
          total: 0,
          confirmed: 0,
          pending: 0,
          immature: 0,
          watchOnly: 0,
          address: null,
          defaultWalletName: "main",
        };
      }
      throw e;
    }
  },

  // walletpassphrase <pass> 0 → unlock the BIP39 wallet.db.
  // walletpassphrase <pass> 0 → 解锁 BIP39 wallet.db。
  async unlock(pass: string): Promise<boolean> {
    try {
      await rpc("walletpassphrase", pass, 0);
      return true;
    } catch {
      return false;
    }
  },

  // walletlogin <email> <pass> → legacy in-memory login; returns the primary
  // (web-wallet compatible) address. / 邮箱密码登录(纯内存),返回主地址。
  async login(email: string, password: string): Promise<{ address: string }> {
    return rpc<{ address: string }>("walletlogin", email, password);
  },

  // createwallet → fresh BIP39 wallet; the mnemonic is returned once.
  // createwallet → 全新 BIP39 钱包;助记词仅返回一次。
  async createWallet(pass: string): Promise<{
    name: string;
    mnemonic: string;
    address: string;
  }> {
    return rpc("createwallet", "default", false, false, pass, false);
  },

  // walletlock → drop in-memory key material. / walletlock → 丢弃内存密钥。
  async lockWallet(): Promise<void> {
    await rpc("walletlock");
  },

  // getnewaddress → next derived address (advances the persisted index).
  // getnewaddress → 下一个派生地址(推进持久化索引)。
  async getNewAddress(): Promise<string> {
    return rpc<string>("getnewaddress");
  },

  // listtransactions → recent wallet history mapped to the frontend Tx shape.
  // Degrades to [] when the sugar index is disabled so the page stays usable.
  // listtransactions → 映射为前端 Tx 形态的近期历史;sugar 索引未启用时降级为
  // 空数组,页面保持可用。
  async getHistory(): Promise<Tx[]> {
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
      >("listtransactions", "*", 25);
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
