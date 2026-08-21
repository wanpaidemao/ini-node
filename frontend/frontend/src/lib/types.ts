// ── ini-node shared types (mirror design docs §data binding) ──

export type Route =
  | "dashboard"
  | "internals"
  | "wallet"
  | "send"
  | "create"
  | "settings"
  | "console"
  | "control";

export type ConnState = "online" | "syncing" | "offline";

export interface SyncStatus {
  blocks: number;
  headers: number;
  bestBlockHash: string;
  difficulty: string;
  rateBlPerSec: number;
  etaMinutes: number | null;
  syncedPct: number;
}

export interface Peer {
  id: number;
  dir: "outbound" | "inbound";
  addr: string;
  version: number;
  height: number;
  syncBlPerSec: number | null;
  latencyMs: number;
}

export interface NodeInfo {
  version: string;
  protocol: number;
  p2pPort: number;
  dataDir: string;
  upnp: boolean;
  proxy: string | null;
  chain: string;
  networkactive: boolean;
  memHeap: number;
  diskWritePerSec: number;
  startedAt: number;
}

export interface NodeInternals {
  chainTip: number;
  headerTip: number;
  chainBoundary: number;
  headerBoundary: number;
  windowSize: number;
  blockTasks: {
    total: number;
    synced: number;
    inflight: number;
    slices: {
      peer: string;
      start: number;
      end: number;
      pct: number;
      complete: boolean;
      inFlight: number;
      syncNode: boolean;
      lastActiveAt: number;
      assignedAt: number;
    }[];
  };
  headerTasks: {
    ranges: { start: number; end: number; state: "done" | "inflight" | "todo" }[];
    hdrPeers: { peer: string; start: number; end: number; pct: number; received: boolean; assignedAt: number }[];
    hdrLanes: { peer: string; start: number; end: number; pct: number; received: boolean; assignedAt: number }[];
    recent: { start: number; end: number; peer: string; assignedAt: number }[];
    sliceLen: number;
    requestedBlocks: number;
    lastReissueAt: number;
    nextAssign: number;
  };
  mem: { gap: number; window: number; inflight: number };
  debugLevel: string;
  // Per-peer connection stats from getpeerinfo, sampled at a low cadence
  // (5 min / manual refresh) so the quality cards do not churn every 2s poll.
  peerStats: {
    id: number;
    addr: string;
    bytesRecv: number;
    bytesSent: number;
    pingMs: number;
    connTime: number; // unix seconds
    startingHeight: number;
    currentHeight: number;
    syncNode: boolean;
    inbound: boolean;
  }[];
}

export interface Tx {
  time: number;
  dir: "in" | "out";
  amount: number; // in S
  status: "confirmed" | "pending";
  hash: string;
}

export interface WalletState {
  locked: boolean;
  total: number;
  confirmed: number;
  pending: number;
  immature: number;
  watchOnly: number;
  address: string | null;
  defaultWalletName: string;
}

export interface AppConfig {
  rpcEndpoint: string;
  rpcUser: string;
  rpcPass: string; // masked unless editing
  credFromIni: boolean;
  walletApi: string;
  parallelPeers: number; // 4–16
  addrType: "bech32" | "segwit" | "legacy";
  defaultWallet: string;
  dataDir: string;
  diskFree: number;
  runNodeOnStart: boolean;
  debugLevel: string;
  upnp: boolean;
  proxy: string | null;
}

export interface RpcResult {
  method: string;
  output: string;
  elapsedMs: number;
  format: "json" | "text";
}