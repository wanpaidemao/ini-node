// ── ini-node service adapter ──────────────────────────────────
// Frontend talks to services through one interface. When the Go module is
// bound (wails3 generate bindings) the real services are used; until then a
// deterministic mock keeps the UI alive and demoable in the browser.
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

const MOCK_TOTAL = 43_750_000;

function randInt(min: number, max: number): number {
  return Math.floor(Math.random() * (max - min + 1)) + min;
}

class MockState {
  block = 3_446_323;
  headerTip = MOCK_TOTAL;
  rate = 265;
  startedAt = Date.now() - 3 * 86400_000 - 12 * 3600_000;
  hops = 0;

  get chainBoundary() {
    return this.block - 50_000;
  }

  tick(): void {
    // window catches up on a steeper curve; rate wobbles
    const targetDelta = MOCK_TOTAL - this.block;
    const step =
      targetDelta < 2000
        ? Math.min(targetDelta, 40)
        : randInt(140, 265);
    this.block += step;
    const wobble = Math.sin(++this.hops / 7) * 18;
    this.rate = Math.max(90, Math.round(step + wobble));
    if (this.block >= MOCK_TOTAL) this.block = MOCK_TOTAL - 120_000; // bounce
  }

  peerList(n: number): Peer[] {
    const out: Peer[] = [];
    for (let i = 1; i <= n; i++) {
      const syncing = i % 3 !== 0;
      out.push({
        id: i,
        dir: i % 2 ? "outbound" : "inbound",
        addr: syncing ? `198.51.100.${randInt(2, 250)}:8333` : `192.0.2.${randInt(2, 250)}:${randInt(40000, 60000)}`,
        version: 70016,
        height: syncing ? this.block + randInt(-80, -2) : MOCK_TOTAL,
        syncBlPerSec: syncing ? randInt(30, 265) : null,
        latencyMs: randInt(5, 60),
      });
    }
    return out;
  }
}

const mock = new MockState();

const online = async (): Promise<boolean> => {
  // In a real node window this would TryRPC once; for demos we are "online".
  return true;
};

export const Services = {
  // ── Node ──────────────────────────────────────────────
  async getSyncStatus(): Promise<SyncStatus> {
    mock.tick();
    const gap = MOCK_TOTAL - mock.block;
    const eta = mock.rate > 0 ? gap / mock.rate / 60 : null;
    return {
      blocks: mock.block,
      headers: mock.headerTip,
      bestBlockHash:
        "0000000000000000000" + mock.block.toString(16).padStart(14, "0"),
      difficulty: "1041628735714725.1",
      rateBlPerSec: mock.rate,
      etaMinutes: eta,
      syncedPct: Math.min(100, (mock.block / MOCK_TOTAL) * 100),
    };
  },

  async getNodeInfo(): Promise<NodeInfo> {
    return {
      version: "v1.14.1",
      protocol: 70016,
      p2pPort: 8333,
      dataDir: "C:\\Users\\ad\\AppData\\Local\\Btcd",
      upnp: true,
      proxy: null,
      chain: "main",
      networkactive: true,
      memHeap: 1.05 * 1024 * 1024 * 1024,
      diskWritePerSec: 41 * 1024,
      startedAt: mock.startedAt,
    };
  },

  async getPeers(): Promise<Peer[]> {
    void (await online());
    return mock.peerList(8);
  },

  async getSyncHistory(n: number): Promise<{ t: number; height: number }[]> {
    const now = Date.now();
    return Array.from({ length: n }, (_, i) => ({
      t: now - (n - 1 - i) * 1000,
      height: mock.block - ((randInt(0, 3) + (n - 1 - i) * 3)) * 120,
    }));
  },

  async getNodeInternals(_detail?: "normal" | "trace"): Promise<NodeInternals> {
    mock.tick();
    const window = 50_000;
    return {
      chainTip: mock.block,
      headerTip: mock.headerTip,
      chainBoundary: mock.block - window,
      headerBoundary: mock.headerTip - Math.max(1000, window / 10),
      windowSize: window,
      blockTasks: {
        slices: ["A", "B", "C", "D"].map((peer, i) => {
          const start = mock.chainBoundary + i * (window / 4);
          const applied = start + randInt(2000, 11000);
          return {
            peer,
            start,
            end: start + window / 4,
            assignedAt: Date.now() - randInt(20_000, 90_000),
            applied,
            complete: applied >= start + window / 4 - 400,
          };
        }),
      },
      headerTasks: {
        ranges: [
          { start: 0, end: 2_000, state: "done" as const },
          { start: 2_000, end: 4_000, state: "done" as const },
          { start: 4_000, end: 6_000, state: "inflight" as const },
          { start: 6_000, end: 12_000, state: "todo" as const },
        ],
        requestedBlocks: randInt(900, 1500),
        lastReissueAt: Date.now() - 3 * 60_000,
      },
      mem: {
        alloc: 1.05 * 1024 * 1024 * 1024,
        heapAlloc: 1.01 * 1024 * 1024 * 1024,
        heapObjects: 12.2e6,
        numGC: 5421,
      },
      debugLevel: "info",
      sampling: Array.from({ length: 60 }, (_, i) => ({
        t: Date.now() - (59 - i) * 1000,
        height: mock.block - ((59 - i) * 265 - randInt(0, 200)),
      })),
    };
  },

  async setSyncPeers(_n: number): Promise<number> {
    return _n;
  },

  async getDebugLevel(): Promise<string> {
    return "info";
  },

  async setDebugLevel(spec: string): Promise<void> {
    void spec;
  },

  async disconnectPeer(_id: number): Promise<void> {
    void _id;
  },

  async resetPeers(): Promise<void> {
    return;
  },

  async testNode(): Promise<{ ok: boolean; ms: number }> {
    return { ok: true, ms: randInt(2, 12) };
  },

  // ── Wallet ─────────────────────────────────────────────
  async getWallet(): Promise<WalletState> {
    return {
      locked: false,
      total: 1234.56789001,
      confirmed: 1230.0,
      pending: 4.56,
      immature: 0,
      watchOnly: 12.3,
      address: "sugar1qk95y3l2vuk8f0n9z9gx22mrt70y9j6k7f97lg",
      defaultWalletName: "main",
    };
  },

  async unlock(_pass: string): Promise<boolean> {
    return true;
  },

  async getHistory(): Promise<Tx[]> {
    return [
      { time: Date.now() - 2_400_000, dir: "out", amount: -0.5, status: "confirmed", hash: "f3a21c…" },
      { time: Date.now() - 5_500_000, dir: "in", amount: 10.0, status: "pending", hash: "beef09…" },
      { time: Date.now() - 86_400_000, dir: "in", amount: 42.0, status: "confirmed", hash: "a1e2c3…" },
    ];
  },

  // ── Config ─────────────────────────────────────────────
  async getConfig(): Promise<AppConfig> {
    return {
      rpcEndpoint: "http://127.0.0.1:8334",
      rpcUser: "qYjMSzVGbXdgPJiuwxfMAp5EM3M=",
      rpcPass: "dSQjlIXRW7ETYcroIhjlTqduT0A=",
      credFromIni: true,
      walletApi: "http://127.0.0.1:8080",
      parallelPeers: 8,
      addrType: "bech32",
      defaultWallet: "main",
      dataDir: "C:\\Users\\ad\\AppData\\Local\\Btcd",
      diskFree: 120 * 1024 ** 3,
      runNodeOnStart: true,
      debugLevel: "info",
      upnp: true,
      proxy: null,
    };
  },

  async saveConfig(cfg: AppConfig): Promise<AppConfig> {
    return cfg;
  },

  async pickDataDir(): Promise<string | null> {
    return "C:\\Users\\ad\\AppData\\Local\\Btcd";
  },

  async migrateDataDir(_from: string, _to: string): Promise<number> {
    return 100;
  },

  async estimateFee(_target: number): Promise<number> {
    return 0.000001;
  },

  async buildPsbt(_to: string, _amountS: number, _feeS: number) {
    return {
      psbt:
        "cHNidP8BAAoBAAAAAdWVNKnsqXH6edQmNSW3F0KZ9vMQwrR6oAzGMyzPzyogAQAAAAF1WXV5q1k95y3l2vuk8f0n9z9gx22mrt70y9j6k7f97lgAAAAAAAAAAA=",
      hex: "0200000001d59534a9eca971fa",
      size: 225,
      feeS: 0.000001,
    };
  },

  async broadcast(_hex: string): Promise<string> {
    return "f3a21c22ab…";
  },

  async rpcCall(method: string, params: unknown[]): Promise<RpcResult> {
    const started = performance.now();
    const table: Record<string, unknown> = {
      getblockchaininfo: {
        chain: "main", blocks: mock.block, headers: mock.headerTip,
        syncheight: mock.block, difficulty: "1041628735714725.1", pruned: false,
        bestblockhash: "0000000000000000000" + method.length,
      },
      getnetworkinfo: { version: 110100, subversion: "/Saber:0.20.1/", protocolversion: 70016, networkactive: true, relayfee: 0.000001 },
      getpeerinfo: mock.peerList(8),
      getblocktemplate: { capabilities: ["proposal", "coinbasetxn"], version: 536870912, rules: ["csv", "segwit"] },
      getmempoolinfo: { size: randInt(120, 900), bytes: randInt(1e6, 4e6) },
      uptime: 302_400,
    };
    const payload: unknown = table[method] ?? `0x${method.length}42`;
    await new Promise((r) => setTimeout(r, 8));
    const isSimple = ["getbestblockhash", "getblockcount", "uptime"].includes(method);
    const output =
      isSimple && typeof payload === "number"
        ? String(payload)
        : JSON.stringify(payload, null, isSimple ? 0 : 2);
    return {
      method,
      output,
      elapsedMs: Math.round(performance.now() - started),
      format: isSimple ? "text" : "json",
    };
  },
};