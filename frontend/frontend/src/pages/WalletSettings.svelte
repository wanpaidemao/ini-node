<script lang="ts">
  import { t } from "../lib/i18n";
  import { navigate } from "../lib/store.svelte";
  import { setWalletSettings, walletSettings } from "../lib/wallet-settings.svelte";

  // Wallet settings (secondary page of the wallet view): local UI preferences.
  // Every change is applied + persisted immediately; `saved` flashes as
  // feedback (auto-save, no separate save button).
  // 钱包设置(钱包页的二级页面):本地界面偏好。每次修改立即生效并持久化;
  // `saved` 闪现作为反馈(自动保存,无需独立保存按钮)。
  let saved = $state(false);
  let savedTimer: ReturnType<typeof setTimeout> | undefined;

  // apply() — central change handler: persist, re-arm auto-lock, flash badge.
  // apply() — 统一变更处理:持久化、重新武装自动锁定、闪现保存标记。
  function apply() {
    setWalletSettings({ ...walletSettings });
    saved = true;
    clearTimeout(savedTimer);
    savedTimer = setTimeout(() => (saved = false), 1500);
  }

  const autoLockOptions = [0, 1, 5, 10, 30];
  const historyOptions = [10, 25, 50, 100];

  // minutes label: "Never" for 0, otherwise "N minutes"
  // 分钟标签:0 显示"永不",否则显示"N 分钟"
  function minutesLabel(n: number): string {
    return n === 0 ? t("wal.set.never") : t("g.minutes", { n });
  }
</script>

<section class="wset">
  <div class="head">
    <div>
      <p class="eyebrow">wallet · settings</p>
      <h1 class="h-page">{t("wal.set.title")}</h1>
    </div>
    <div class="head-actions">
      {#if saved}
        <span class="chip save-chip" role="status"><span class="dot ok" aria-hidden="true"></span>{t("wal.set.saved")}</span>
      {/if}
      <button class="btn btn-ghost" onclick={() => navigate("wallet")}>
        <svg viewBox="0 0 24 24" width="13" height="13" fill="none" stroke="currentColor" stroke-width="2.5" stroke-linecap="round" aria-hidden="true">
          <polyline points="15 18 9 12 15 6" />
        </svg>
        {t("wal.set.back")}
      </button>
    </div>
  </div>

  <div class="card">
    <!-- auto-lock: after unlock, lock the wallet RPC-side automatically -->
    <!-- 自动锁定:解锁后到期自动调用 walletlock 锁定钱包 -->
    <div class="field">
      <div class="field-text">
        <label class="field-label" for="auto-lock">{t("wal.set.auto_lock")}</label>
        <p class="field-hint">{t("wal.set.auto_lock_hint")}</p>
      </div>
      <select id="auto-lock" bind:value={walletSettings.autoLockMinutes} onchange={apply}>
        {#each autoLockOptions as n}
          <option value={n}>{minutesLabel(n)}</option>
        {/each}
      </select>
    </div>

    <div class="divider" aria-hidden="true"></div>

    <!-- privacy: mask balances on the wallet page -->
    <!-- 隐私:钱包页余额遮蔽显示 -->
    <div class="field">
      <div class="field-text">
        <label class="field-label" for="hide-balance">{t("wal.set.hide_balance")}</label>
        <p class="field-hint">{t("wal.set.hide_balance_hint")}</p>
      </div>
      <label class="switch">
        <input id="hide-balance" type="checkbox" bind:checked={walletSettings.hideBalance} onchange={apply} />
        <span class="knob" aria-hidden="true"></span>
      </label>
    </div>

    <div class="divider" aria-hidden="true"></div>

    <!-- history rows: rows fetched by listtransactions -->
    <!-- 历史条数:listtransactions 拉取的交易条数 -->
    <div class="field">
      <div class="field-text">
        <label class="field-label" for="history-count">{t("wal.set.history_count")}</label>
        <p class="field-hint">{t("wal.set.history_count_hint")}</p>
      </div>
      <select id="history-count" bind:value={walletSettings.historyCount} onchange={apply}>
        {#each historyOptions as n}
          <option value={n}>{n}</option>
        {/each}
      </select>
    </div>
  </div>

  <p class="note">{t("wal.set.note")}</p>
</section>

<style>
  .wset {
    display: flex;
    flex-direction: column;
    gap: 16px;
    max-width: 720px;
    margin: 0 auto;
    animation: rise 0.28s ease both;
  }
  @keyframes rise {
    from {
      opacity: 0;
      transform: translateY(6px);
    }
    to {
      opacity: 1;
      transform: none;
    }
  }
  @media (prefers-reduced-motion: reduce) {
    .wset {
      animation: none;
    }
  }
  .head {
    display: flex;
    align-items: flex-end;
    justify-content: space-between;
    gap: 12px;
    flex-wrap: wrap;
  }
  .h-page {
    font-family: var(--font-display);
    font-size: 24px;
    margin: 2px 0 0;
  }
  .head-actions {
    display: inline-flex;
    align-items: center;
    gap: 8px;
  }
  .save-chip {
    display: inline-flex;
    align-items: center;
    gap: 6px;
    font-size: 12px;
    font-weight: 700;
    color: var(--mint);
  }
  .save-chip .dot {
    width: 6px;
    height: 6px;
    background: var(--mint);
    animation: pop 0.2s ease;
  }
  @keyframes pop {
    from {
      transform: scale(0.4);
    }
    to {
      transform: scale(1);
    }
  }

  .card {
    display: flex;
    flex-direction: column;
    padding: 4px 18px;
  }
  .field {
    display: flex;
    align-items: center;
    justify-content: space-between;
    gap: 16px;
    padding: 14px 0;
  }
  .field-text {
    display: flex;
    flex-direction: column;
    gap: 4px;
    min-width: 0;
  }
  .field-label {
    font-size: 14px;
    font-weight: 700;
    color: var(--ink-fg);
    cursor: pointer;
  }
  .field-hint {
    font-size: 11px;
    line-height: 1.5;
    color: var(--ink-dim);
    margin: 0;
  }
  .divider {
    height: 1px;
    background: var(--line);
  }

  select {
    flex: none;
    min-width: 130px;
    background: var(--ink);
    color: var(--ink-fg);
    border: 1px solid var(--line);
    border-radius: var(--r-8);
    padding: 7px 10px;
    font-size: 13px;
    font-family: var(--font-body);
    cursor: pointer;
    font-variant-numeric: tabular-nums;
  }
  select:focus-visible {
    outline: none;
    box-shadow: var(--focus);
  }

  /* toggle switch / 开关 */
  .switch {
    position: relative;
    display: inline-flex;
    flex: none;
    width: 40px;
    height: 22px;
    cursor: pointer;
  }
  .switch input {
    position: absolute;
    opacity: 0;
    width: 100%;
    height: 100%;
    margin: 0;
    cursor: pointer;
  }
  .switch input:focus-visible + .knob {
    box-shadow: var(--focus);
  }
  .knob {
    position: absolute;
    inset: 0;
    background: var(--ink);
    border: 1px solid var(--line);
    border-radius: 999px;
    transition: background 0.15s ease, border-color 0.15s ease;
    pointer-events: none;
  }
  .knob::after {
    content: "";
    position: absolute;
    top: 2px;
    left: 2px;
    width: 16px;
    height: 16px;
    border-radius: 50%;
    background: var(--mist);
    transition: transform 0.15s ease, background 0.15s ease;
  }
  .switch input:checked + .knob {
    background: var(--straw);
    border-color: var(--straw);
  }
  .switch input:checked + .knob::after {
    transform: translateX(18px);
    background: var(--ink);
  }

  .note {
    font-size: 11px;
    color: var(--ink-dim);
    margin: 0;
    line-height: 1.5;
  }

  @media (max-width: 760px) {
    .field {
      flex-direction: column;
      align-items: flex-start;
    }
  }
</style>
