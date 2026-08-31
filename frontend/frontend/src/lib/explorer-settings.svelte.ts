// ── explorer settings store: client-side UI preferences ─────────
// Local UI preferences for the block explorer (recent block count, etc.).
// Persisted in localStorage, never touching node config.
// 浏览器设置存储:区块浏览器的本地界面偏好(近期区块数量等)。持久化在
// localStorage,不触碰节点配置。

const KEY = "ini-node.explorer-settings";

export interface ExplorerSettings {
  /** how many recent blocks the explorer home lists / 浏览器首页列出的近期区块数量 */
  recentBlocks: number;
}

const DEFAULTS: ExplorerSettings = {
  recentBlocks: 12,
};

function load(): ExplorerSettings {
  try {
    const raw = localStorage.getItem(KEY);
    if (raw) return { ...DEFAULTS, ...(JSON.parse(raw) as Partial<ExplorerSettings>) };
  } catch {
    /* corrupted entry — defaults apply / 条目损坏 — 应用默认值 */
  }
  return { ...DEFAULTS };
}

export const explorerSettings = $state<ExplorerSettings>(load());

/** Persist + apply an explorer-settings patch (values are clamped). */
/** 持久化并应用浏览器设置补丁(数值会被夹取到合法范围)。 */
export function setExplorerSettings(patch: Partial<ExplorerSettings>): void {
  Object.assign(explorerSettings, patch);
  const n = Math.round(explorerSettings.recentBlocks);
  explorerSettings.recentBlocks = Number.isFinite(n) ? Math.min(100, Math.max(1, n)) : 12;
  try {
    localStorage.setItem(KEY, JSON.stringify(explorerSettings));
  } catch {
    /* storage unavailable — settings stay for this session only */
    /* 存储不可用 — 设置仅本次会话生效 */
  }
}