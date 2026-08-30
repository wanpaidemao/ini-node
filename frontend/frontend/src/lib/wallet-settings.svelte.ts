// ── wallet settings store: local preferences + auto-lock timer ──
// Wallet settings are UI-level preferences (auto-lock, privacy, history size).
// They persist in localStorage (like the nav layout) and stay client-side —
// the node's ini config is NOT touched.
// 钱包设置存储:本地偏好 + 自动锁定计时器。钱包设置是界面级偏好
// (自动锁定/隐私/历史条数),持久化在 localStorage(与导航布局一致),
// 纯客户端保存 — 不触碰节点的 ini 配置。
import { Services } from "./services";
import type { WalletSettings } from "./types";

const KEY = "ini-node.wallet-settings";

// Defaults: auto-lock after 5 min, balance visible, 25 history rows.
// 默认值:5 分钟自动锁定,余额可见,历史 25 条。
const DEFAULTS: WalletSettings = {
  autoLockMinutes: 5,
  hideBalance: false,
  historyCount: 25,
};

function load(): WalletSettings {
  try {
    const raw = localStorage.getItem(KEY);
    if (raw) return { ...DEFAULTS, ...(JSON.parse(raw) as Partial<WalletSettings>) };
  } catch {
    /* corrupted entry — fall back to defaults / 条目损坏 — 回退默认值 */
  }
  return { ...DEFAULTS };
}

// Reactive settings object; mutate through setWalletSettings so the change
// is persisted and the auto-lock timer is re-armed.
// 响应式设置对象;请通过 setWalletSettings 修改,以便持久化并重新武装
// 自动锁定计时器。
export const walletSettings = $state<WalletSettings>(load());

// lockedAt is bumped every time the auto-lock fires, so pages sitting on the
// wallet view can react (refresh into the locked state) via a $effect.
// lockedAt 在每次自动锁定触发时递增,停留在钱包视图的页面可通过 $effect
// 响应(刷新为锁定态)。
export const walletAutoLock = $state({ lockedAt: 0 });

let lockTimer: ReturnType<typeof setTimeout> | null = null;
let unlocked = false;

function clearTimer(): void {
  if (lockTimer !== null) {
    clearTimeout(lockTimer);
    lockTimer = null;
  }
}

// (Re)arm the auto-lock countdown from the current settings.
// 依据当前设置(重新)武装自动锁定倒计时。
function arm(): void {
  clearTimer();
  if (!unlocked || walletSettings.autoLockMinutes <= 0) return;
  lockTimer = setTimeout(fire, walletSettings.autoLockMinutes * 60_000);
}

// Auto-lock fired: call the in-process wallet lock (Wails binding) and
// announce the new locked state. Local operation — works without the node.
// 自动锁定触发:调用进程内钱包锁定(Wails binding)并广播新的锁定状态。
// 纯本地操作——无需节点。
async function fire(): Promise<void> {
  lockTimer = null;
  unlocked = false;
  try {
    await Services.lockWallet();
  } catch {
    /* binding call failed — keys stay in memory; next fire retries */
    /* binding 调用失败 — 密钥留在内存;下次触发时重试 */
  }
  walletAutoLock.lockedAt = Date.now();
}

/** Persist + apply a settings patch (also re-arms the auto-lock timer). */
/** 持久化并应用设置补丁(同时重新武装自动锁定计时器)。 */
export function setWalletSettings(patch: Partial<WalletSettings>): void {
  Object.assign(walletSettings, patch);
  try {
    localStorage.setItem(KEY, JSON.stringify(walletSettings));
  } catch {
    /* storage unavailable — settings stay for this session only */
    /* 存储不可用 — 设置仅本次会话生效 */
  }
  arm();
}

/** Report an unlock (wallet page / login) to start the auto-lock countdown. */
/** 上报解锁事件(钱包页/登录),启动自动锁定倒计时。 */
export function walletUnlocked(): void {
  unlocked = true;
  arm();
}

/** Report a manual lock: cancel the pending auto-lock countdown. */
/** 上报手动锁定:取消挂起的自动锁定倒计时。 */
export function walletLocked(): void {
  unlocked = false;
  clearTimer();
}
