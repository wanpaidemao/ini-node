// ── ini-node shared types (mirror design docs §data binding) ──

export type Route =
  | "dashboard"
  | "internals"
  | "wallet"
  | "wallet-settings"
  | "send"
  | "create"
  | "settings"
  | "console"
  | "control"
  | "explorer";

// Address type for the Step 9 three-type derivation (same key material,
// three Sugarchain encodings). / 第 9 步三型派生的地址类型(同一密钥
// 材料,三种糖链编码)。
export type AddressType = "bech32" | "segwit" | "legacy";

// Wallet settings — local UI preferences for the wallet pages, persisted
// client-side (localStorage), not part of the node RPC config.
// 钱包设置 — 钱包页面的本地界面偏好,客户端持久化(localStorage),
// 不属于节点 RPC 配置。
export interface WalletSettings {
  /** auto-lock the wallet N minutes after unlock (0 = never) */
  /** 解锁 N 分钟后自动锁定钱包(0 = 永不) */
  autoLockMinutes: number;
  /** privacy: mask balances on the wallet page / 隐私:钱包页余额遮蔽显示 */
  hideBalance: boolean;
  /** how many recent transactions listtransactions fetches / 拉取的近期交易条数 */
  historyCount: number;
  /** Step 9: address type shown/used by the wallet page (immediate effect) */
  /** 第 9 步:钱包页展示/使用的地址类型(即时生效) */
  addressType: AddressType;
  /** Step 6: token layer REST endpoint (default tokenstest.sugar.wtf) */
  /** 第 6 步:代币层 REST 端点(默认 tokenstest.sugar.wtf) */
  tokenAPI: string;
}

// Token balance entry of the wallet (Step 6 Tokens tab).
// 钱包的代币余额条目(第 6 步 Tokens 标签页)。
export interface TokenBalance {
  ticker: string;
  value: number; // base units / 基本单位
  decimals: number;
}

// Token metadata from the layer's /layer/token/{ticker}.
// 代币层 /layer/token/{ticker} 返回的代币元数据。
export interface TokenInfo {
  ticker: string;
  decimals: number;
  reissuable: boolean;
  supply: number; // base units / 基本单位
}

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
    hdrPeers: { peer: string; start: number; end: number; received: boolean; applied: number; assignedAt: number }[];
    hdrLanes: { peer: string; start: number; end: number; received: boolean; applied: number; assignedAt: number }[];
    recent: { start: number; end: number; peer: string; assignedAt: number }[];
    sliceLen: number;
    paused: boolean;
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
  /** false when the node RPC was unreachable — chain figures unknown (UI shows "—"), wallet itself still usable */
  /** 节点 RPC 不可达时为 false — 链上数字未知(UI 显示 "—"),钱包本身仍可用 */
  chainOnline: boolean;
  /** true when figures came from the external REST source instead of the node (badge in UI) */
  /** 数字来自外部 REST 数据源而非本节点时为 true(UI 徽章提示) */
  chainExternal?: boolean;
}

export interface AppConfig {
  rpcEndpoint: string;
  rpcUser: string;
  rpcPass: string;
  credFromIni: boolean;
  /** node ini keys — applied on next node start / 节点 ini 键,下次启动生效 */
  dataDir: string;
  maxPeers: number;
  upnp: boolean;
  sugarIndex: boolean;
  /** frontend-only keys stored in frontend.ini / 前端专属键,存 frontend.ini */
  runNodeOnStart: boolean;
  /** runtime ini file location (read-only reference) / runtime ini 位置(只读参考) */
  iniPath: string;
  debugLevel?: string;
}

export interface RpcResult {
  method: string;
  output: string;
  elapsedMs: number;
  format: "json" | "text";
}

// ── Explorer (Step 10): chain / block / tx view shapes ──
// ── 浏览器(第 10 步):链/区块/交易视图数据形态 ──
export interface ExplorerChain {
  height: number;
  bestHash: string;
  chain: string;
  difficulty: number;
  headers: number;
  mediantime: number;
}

export interface ExplorerBlock {
  hash: string;
  height: number;
  confirmations: number;
  time: number;
  size: number;
  txCount: number;
  nonce: number;
  bits: string;
  difficulty: number;
}

export interface ExplorerTx {
  txid: string;
  blockhash?: string;
  blocktime?: number;
  confirmations?: number;
  time?: number;
  size: number;
  vinCount: number;
  voutCount: number;
  totalOut: number; // SUGAR / SUGAR
  outputs: { n: number; value: number; address: string | null }[];
  inputs: { txid: string; vout: number; address: string | null }[];
}