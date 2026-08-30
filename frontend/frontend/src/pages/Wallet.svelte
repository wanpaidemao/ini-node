<script lang="ts">
  import { onMount } from "svelte";
  import { fmtDateTime, t } from "../lib/i18n";
  import { Services } from "../lib/services";
  import { navigate } from "../lib/store.svelte";
  import type { Tx, WalletState } from "../lib/types";

  // Page state: w is the real getwalletinfo snapshot; locked mirrors
  // w.locked so the unlock form replaces the old click-to-unlock mock.
  // 页面状态:w 为真实 getwalletinfo 快照;locked 镜像 w.locked,
  // 解锁表单取代原先"点一下即解锁"的 mock 行为。
  let w = $state<WalletState | null>(null);
  let txs = $state<Tx[]>([]);
  let tab = $state<"history" | "tokens" | "keys" | "consolidate">("history");
  let loadErr = $state<string | null>(null);

  // Unlock form state. / 解锁表单状态。
  let pass = $state("");
  let unlockErr = $state<string | null>(null);
  let busy = $state(false);
  let copyOk = $state(false);

  async function refresh() {
    try {
      w = await Services.getWallet();
      loadErr = null;
    } catch (e) {
      loadErr = String(e);
      w = null;
    }
    if (w && !w.locked) txs = await Services.getHistory();
  }

  onMount(refresh);

  // unlock: real walletpassphrase RPC; on success reload balance + history.
  // unlock: 真实 walletpassphrase RPC;成功后重载余额与历史。
  async function unlock() {
    unlockErr = null;
    busy = true;
    try {
      const ok = await Services.unlock(pass);
      if (!ok) {
        unlockErr = t("wal.unlock_failed");
        return;
      }
      pass = "";
      await refresh();
    } catch {
      unlockErr = t("wal.unlock_failed");
    } finally {
      busy = false;
    }
  }

  // lock: walletlock RPC; drops the in-memory keys.
  // lock: walletlock RPC;丢弃内存密钥。
  async function lock() {
    await Services.lockWallet().catch(() => {});
    await refresh();
  }

  function copyAddr() {
    if (!w?.address) return;
    navigator.clipboard?.writeText(w.address);
    copyOk = true;
    setTimeout(() => (copyOk = false), 1500);
  }

  const tabs: { id: typeof tab; label: string }[] = [
    { id: "history", label: "wal.tab_history" },
    { id: "tokens", label: "wal.tab_tokens" },
    { id: "keys", label: "wal.tab_keys" },
    { id: "consolidate", label: "wal.tab_consolidate" },
  ];
</script>

<section class="wal">
  <div class="head">
    <div>
      <p class="eyebrow">wallet · {w?.defaultWalletName ?? "main"}</p>
      <h1 class="h-page">{t("wal.title")}</h1>
    </div>
    {#if w && !w.locked}
      <button class="chip btn-chip" onclick={lock} title={t("wal.lock")}>
        <span class="dot ok" aria-hidden="true"></span> {t("wal.lock")}
      </button>
    {/if}
  </div>

  {#if loadErr}
    <div class="card err-card" role="alert">
      <p class="err"><span class="dot" aria-hidden="true"></span>{loadErr}</p>
    </div>
  {:else if w && w.locked}
    <!-- ── locked: real unlock form (BIP39 passphrase) + login shortcut ── -->
    <!-- ── 锁定:真实解锁表单(BIP39 口令)+ 登录入口 ── -->
    <div class="card unlock-card">
      <h2 class="h-card">{t("wal.unlock")}</h2>
      <form class="unlock-form" onsubmit={(e) => { e.preventDefault(); unlock(); }}>
        <input
          type="password"
          bind:value={pass}
          placeholder={t("g.password")}
          aria-label={t("g.password")}
          autocomplete="off"
        />
        <button class="btn btn-primary" type="submit" disabled={busy}>
          {#if busy}<span class="spin" aria-hidden="true"></span>{/if}
          {t("wal.unlock")}
        </button>
      </form>
      {#if unlockErr}
        <p class="err" role="alert"><span class="dot" aria-hidden="true"></span>{unlockErr}</p>
      {/if}
      <p class="hint">{t("wal.login_hint")}</p>
      <button class="btn btn-ghost" onclick={() => navigate("create")}>{t("wal.go_login")} →</button>
    </div>
  {:else if w}
    <div class="top-grid">
      <!-- ── balance card ── -->
      <!-- ── 余额卡片 ── -->
      <div class="card balance">
        <p class="eyebrow">{t("wal.total")}</p>
        <p class="bal mono" translate="no">{(w.total).toFixed(8)} <span class="unit">S</span></p>
        <div class="bal-sub">
          <span class="part"><span class="dot ok" aria-hidden="true"></span>{t("wal.confirmed", { n: w.confirmed.toFixed(4) })}</span>
          {#if w.pending > 0}
            <span class="part"><span class="dot wait" aria-hidden="true"></span>{t("wal.pending", { n: w.pending.toFixed(4) })}</span>
          {/if}
          {#if w.immature > 0}
            <span class="part"><span class="dot lock" aria-hidden="true"></span>{t("wal.immature", { n: w.immature.toFixed(4) })}</span>
          {/if}
        </div>
        <div class="bal-actions">
          <button class="btn btn-primary" onclick={() => navigate("send")}>{t("wal.send")}</button>
          <button class="btn btn-ghost" onclick={copyAddr}>{copyOk ? "✓" : t("wal.receive")}</button>
        </div>
      </div>

      <!-- ── receive address card ── -->
      <!-- ── 收款地址卡片 ── -->
      <div class="card receive">
        <p class="eyebrow">{t("wal.receive_addr")}</p>
        <div class="qr" aria-hidden="true">
          <svg viewBox="0 0 21 21" width="92" height="92" aria-hidden="true">
            <rect width="21" height="21" fill="#ffffff" />
            <g fill="#1a1a1a" shape-rendering="crispEdges">
              <!-- pseudo QR pattern -->
              {#each [1,2,3,0,1,4,5,6,7,0,3,2,1,0,5,7,6,3,2,4,1,6,0,7,5,2,3,1,4,6,7,5,0,2,1,3,6,5,4] as c, i}
                <rect x={(i % 7) + c} y={Math.floor(i / 7)} width="1" height="1" />
              {/each}
            </g>
          </svg>
        </div>
        <p class="addr mono" translate="no">{w.address}</p>
        <div class="addr-actions">
          <button class="btn btn-ghost" aria-label={t("g.copy")} onclick={copyAddr}>
            <svg viewBox="0 0 24 24" width="13" height="13" fill="none" stroke="currentColor" stroke-width="2" stroke-linecap="round" aria-hidden="true"><rect x="9" y="9" width="13" height="13" rx="2" /><path d="M5 15H4a2 2 0 0 1-2-2V4a2 2 0 0 1 2-2h9a2 2 0 0 1 2 2v1" /></svg>
            {copyOk ? "✓" : t("g.copy")}
          </button>
          <button class="btn btn-ghost" aria-label={t("dash.retry")} onclick={refresh}>{t("dash.retry")}</button>
        </div>
        <p class="reuse-warn mono">{t("wal.address_reuse_warning")}</p>
      </div>
    </div>

    <!-- ── tabs ── -->
    <!-- ── 标签页 ── -->
    <div class="card tabled-card">
      <div class="tabs" role="tablist" aria-label={t("wal.title")}>
        {#each tabs as tb}
          <button
            class="tab"
            class:active={tab === tb.id}
            role="tab"
            aria-selected={tab === tb.id}
            onclick={() => (tab = tb.id)}
          >
            {t(tb.label)}
          </button>
        {/each}
      </div>

      {#if tab === "history"}
        {#if txs.length === 0}
          <p class="empty">{t("wal.empty")}</p>
        {:else}
          <table class="tx-table">
            <thead>
              <tr>
                <th scope="col">{t("wal.col_time")}</th>
                <th scope="col">{t("wal.col_dir")}</th>
                <th scope="col">{t("wal.col_amount")}</th>
                <th scope="col">{t("wal.col_status")}</th>
              </tr>
            </thead>
            <tbody>
              {#each txs as x}
                <tr>
                  <td class="mono" translate="no">{fmtDateTime(x.time)}</td>
                  <td>{x.dir === "out" ? t("wal.out") : t("wal.in")}</td>
                  <td class={`mono amount ${x.dir}`} translate="no">{x.dir === "out" ? "−" : "+"}{(x.amount < 0 ? -x.amount : x.amount).toFixed(8)} S</td>
                  <td>
                    <span class={`st ${x.status}`}>
                      <span class="dot" aria-hidden="true"></span>
                      {x.status === "confirmed" ? t("wal.confirmed_status") : t("wal.pending_status")}
                    </span>
                    <span class="mono hash" translate="no">({x.hash})</span>
                  </td>
                </tr>
              {/each}
            </tbody>
          </table>
        {/if}
      {:else if tab === "tokens"}
        <p class="empty">{t("wal.tab_tokens_hint")}</p>
      {:else if tab === "keys"}
        <p class="empty">{t("wal.empty")}</p>
      {:else}
        <p class="empty">{t("wal.empty")}</p>
      {/if}
    </div>
  {/if}
</section>

<style>
  .wal {
    display: flex;
    flex-direction: column;
    gap: 16px;
    max-width: 1080px;
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
    .wal {
      animation: none;
    }
  }
  .head {
    display: flex;
    align-items: flex-end;
    justify-content: space-between;
    gap: 12px;
  }
  .h-page {
    font-family: var(--font-display);
    font-size: 24px;
    margin: 2px 0 0;
  }
  .btn-chip {
    cursor: pointer;
  }
  .btn-chip:hover {
    background: var(--violet-2);
    color: var(--ink-fg);
  }
  .dot.ok {
    background: var(--mint);
  }
  .dot.wait {
    background: var(--honey);
  }
  .dot.lock {
    background: var(--mist);
  }

  /* ── locked state: centered unlock form ── */
  .unlock-card {
    max-width: 440px;
    margin: 12px auto 0;
    width: 100%;
    display: flex;
    flex-direction: column;
    gap: 12px;
    padding: 18px;
  }
  .unlock-form {
    display: flex;
    gap: 8px;
  }
  .unlock-form input {
    flex: 1;
    min-width: 0;
  }
  .hint {
    font-size: 11px;
    color: var(--ink-dim);
    margin: 0;
    line-height: 1.45;
  }
  .err {
    display: flex;
    align-items: center;
    gap: 7px;
    color: var(--honey);
    font-size: 12px;
    margin: 0;
  }
  .err .dot {
    background: var(--honey);
    width: 6px;
    height: 6px;
    border-radius: 50%;
    flex: none;
  }
  .err-card {
    max-width: 640px;
    margin: 12px auto 0;
    width: 100%;
  }

  .top-grid {
    display: grid;
    grid-template-columns: 1.2fr 1fr;
    gap: 14px;
  }
  .balance {
    display: flex;
    flex-direction: column;
  }
  .bal {
    font-size: 30px;
    font-family: var(--font-display);
    margin: 6px 0 0;
    letter-spacing: -0.5px;
  }
  .unit {
    font-size: 18px;
    color: var(--straw);
    margin-left: 2px;
  }
  .bal-sub {
    display: flex;
    gap: 12px;
    flex-wrap: wrap;
    margin: 6px 0 14px;
  }
  .part {
    display: inline-flex;
    align-items: center;
    gap: 5px;
    font-size: 12px;
    color: var(--ink-dim);
  }
  .part .dot {
    width: 6px;
    height: 6px;
  }
  .bal-actions {
    display: flex;
    gap: 8px;
    margin-top: auto;
  }

  .receive {
    display: flex;
    flex-direction: column;
    align-items: center;
    text-align: center;
    gap: 8px;
  }
  .qr {
    border-radius: var(--r-8);
    padding: 6px;
    background: #fff;
    border: 1px solid var(--line);
  }
  .addr {
    max-width: 100%;
    word-break: break-all;
    font-size: 12px;
    font-variant-numeric: tabular-nums;
    margin: 0;
  }
  .addr-actions {
    display: flex;
    gap: 8px;
  }
  .reuse-warn {
    font-size: 11px;
    color: var(--honey);
    margin: 0;
  }

  .tabled-card {
    padding: 0;
    overflow: hidden;
  }
  .tabs {
    display: flex;
    gap: 4px;
    padding: 10px 12px 0;
    border-bottom: 1px solid var(--line);
  }
  .tab {
    background: none;
    border: none;
    color: var(--ink-dim);
    padding: 8px 14px;
    font-size: 13px;
    font-weight: 600;
    cursor: pointer;
    border-radius: var(--r-8) var(--r-8) 0 0;
    transition: color 0.12s ease, background 0.12s ease;
  }
  .tab:hover {
    color: var(--ink-fg);
    background: var(--violet-2);
  }
  .tab.active {
    color: var(--straw);
    box-shadow: inset 0 -2px 0 var(--straw);
  }

  .tx-table {
    width: 100%;
    border-collapse: collapse;
    font-size: 13px;
  }
  .tx-table th {
    text-align: left;
    font-size: 11px;
    font-weight: 700;
    letter-spacing: 0.8px;
    text-transform: uppercase;
    color: var(--mist);
    padding: 10px 14px;
  }
  .tx-table td {
    padding: 9px 14px;
    border-top: 1px solid var(--line);
    font-variant-numeric: tabular-nums;
  }
  .amount.in {
    color: var(--mint);
  }
  .amount.out {
    color: var(--straw);
  }
  .st {
    display: inline-flex;
    align-items: center;
    gap: 6px;
    font-size: 12px;
  }
  .st .dot {
    width: 7px;
    height: 7px;
  }
  .st.confirmed .dot {
    background: var(--mint);
  }
  .st.pending .dot {
    background: var(--honey);
  }
  .st.pending {
    color: var(--honey);
  }
  .hash {
    color: var(--ink-dim);
    font-size: 11px;
    margin-left: 6px;
  }
  .empty {
    color: var(--ink-dim);
    padding: 28px;
    margin: 0;
    text-align: center;
    font-size: 13px;
  }

  @media (max-width: 760px) {
    .top-grid {
      grid-template-columns: 1fr;
    }
  }
</style>
