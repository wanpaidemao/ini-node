// ── wallet registry: multiple saved wallet profiles ──────────────
// Mirrors the original web-wallet UX where users bounce between several
// wallets: a local profile list (localStorage) keeps every wallet ever
// opened in this app so reopening is one click + password.
//
// SECURITY: only non-sensitive metadata is stored — name, type, email
// (login hint), and the primary address (display). Passwords and keys are
// NEVER persisted; every open still requires the password.
//
// 钱包档案注册表:多钱包保存。复刻原版 web 钱包的使用习惯——用户会在
// 多个钱包之间来回切换:本地档案列表(localStorage)记住本应用打开过的
// 每个钱包,重开只需一键选中 + 输密码。
//
// 安全:只存非敏感元数据——名字、类型、邮箱(登录提示)、主地址(展示)。
// 密码与密钥永不落盘;每次打开仍需输入密码。
import type { WalletSettings } from "./types";

const KEY = "ini-node.wallet-registry";

/** profile type: web (email/password KDF) or hd (BIP39 wallet.db) */
/** 档案类型:web(邮箱密码 KDF)或 hd(BIP39 wallet.db) */
export type ProfileType = "web" | "hd";

export interface WalletProfile {
  id: string;
  type: ProfileType;
  name: string;
  /** web wallets: the email used for login (hint + one-click fill) */
  /** web 钱包:登录邮箱(提示与一键填充用) */
  email?: string;
  /** primary address (index 0), display only / 主地址(index 0),仅展示 */
  address?: string;
  createdAt: number;
}

// Reactive list; mutations go through the exported functions so the
// persisted copy stays in sync.
// 响应式列表;变更统一走导出函数,持久化副本保持同步。
export const walletRegistry = $state<WalletProfile[]>(load());

function load(): WalletProfile[] {
  try {
    const raw = localStorage.getItem(KEY);
    if (raw) {
      const arr = JSON.parse(raw) as WalletProfile[];
      if (Array.isArray(arr)) return arr;
    }
  } catch {
    /* corrupted entry — start fresh / 条目损坏 — 重新开始 */
  }
  return [];
}

function persist(): void {
  try {
    localStorage.setItem(KEY, JSON.stringify(walletRegistry));
  } catch {
    /* storage unavailable — list stays for this session only */
    /* 存储不可用 — 列表仅本次会话生效 */
  }
}

/**
 * Register (insert or refresh) a profile after a successful open/create.
 * Web wallets key on the email; HD wallets key on the fixed "hd" slot
 * (there is one wallet.db). Existing entries get their address updated.
 *
 * 登记档案(成功打开/创建后插入或刷新)。web 钱包以邮箱为键;HD 钱包固定
 * 占 "hd" 槽位(只有一个 wallet.db)。已有条目会刷新地址。
 */
export function registerWallet(input: {
  type: ProfileType;
  name?: string;
  email?: string;
  address?: string;
}): WalletProfile {
  const id = input.type === "web" ? `web:${input.email ?? ""}` : "hd";
  const name =
    input.name ?? (input.type === "web" ? (input.email ?? "web").split("@")[0] : "HD wallet");
  const existing = walletRegistry.find((p) => p.id === id);
  if (existing) {
    existing.name = name;
    if (input.address) existing.address = input.address;
    if (input.email) existing.email = input.email;
    persist();
    return existing;
  }
  const p: WalletProfile = {
    id,
    type: input.type,
    name,
    email: input.email,
    address: input.address,
    createdAt: Date.now(),
  };
  walletRegistry.push(p);
  persist();
  return p;
}

/** Remove a profile from the list (does NOT delete wallet.db). */
/** 从列表移除档案(不会删除 wallet.db)。 */
export function removeWallet(id: string): void {
  const i = walletRegistry.findIndex((p) => p.id === id);
  if (i >= 0) {
    walletRegistry.splice(i, 1);
    persist();
  }
}

// ── external chain source setting (wallet settings page) ─────────
// ── 外部链数据源设置(钱包设置页) ─────────────────────────────────
// When the local node is unreachable, balance queries can fall back to an
// external REST explorer (same idea as the original web-wallet's
// "wallet-backend" setting: a URL like https://api.sugar.wtf serving
// /balance/<addr>). /balance responses look like {"result":{"balance":N}}.
//
// 本节点不可达时,余额查询可降级到外部 REST 浏览器(与原版 web 钱包的
// "钱包接口"设置同思路:形如 https://api.sugar.wtf 的 URL,提供
// /balance/<addr>)。/balance 响应形如 {"result":{"balance":N}}。

const SOURCE_KEY = "ini-node.chain-source";

export interface ChainSourceSettings {
  /** "local": prefer the bundled node RPC; "external": REST fallback enabled */
  /** "local":优先本节点 RPC;"external":启用外部 REST 降级 */
  mode: "local" | "external";
  /** external REST base URL / 外部 REST 基础 URL */
  api: string;
}

const DEFAULT_SOURCE: ChainSourceSettings = {
  mode: "local",
  api: "https://api.sugar.wtf",
};

function loadSource(): ChainSourceSettings {
  try {
    const raw = localStorage.getItem(SOURCE_KEY);
    if (raw) return { ...DEFAULT_SOURCE, ...(JSON.parse(raw) as Partial<ChainSourceSettings>) };
  } catch {
    /* corrupted — defaults apply / 条目损坏 — 应用默认值 */
  }
  return { ...DEFAULT_SOURCE };
}

export const chainSource = $state<ChainSourceSettings>(loadSource());

/** Persist a chain-source patch. / 持久化链数据源补丁。 */
export function setChainSource(patch: Partial<ChainSourceSettings>): void {
  Object.assign(chainSource, patch);
  try {
    localStorage.setItem(SOURCE_KEY, JSON.stringify(chainSource));
  } catch {
    /* session-only / 仅本次会话 */
  }
}

// re-export WalletSettings type so settings pages import from one module
// 重新导出 WalletSettings 类型,设置页统一从本模块导入
export type { WalletSettings };
