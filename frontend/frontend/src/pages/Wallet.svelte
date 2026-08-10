<script lang="ts">
  import { onMount } from "svelte";
  import { fmtDateTime, t } from "../lib/i18n";
  import { Services } from "../lib/services";
  import { navigate } from "../lib/store.svelte";
  import type { Tx, WalletState } from "../lib/types";

  let w = $state<WalletState | null>(null);
  let txs = $state<Tx[]>([]);
  let tab = $state<"history" | "tokens" | "keys" | "consolidate">("history");
  let locked = $state(true);
  let pass = $state("");
  let copyOk = $state(false);

  onMount(async () => {
    w = await Services.getWallet();
    txs = await Services.getHistory();
    locked = w?.locked ?? true;
  });

  async function unlock() {
    const ok = await Services.unlock(pass);
    if (ok) {
      locked = false;
      pass = "";
    }
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
      <p class="eyebrow">wallet · main</p>
      <h1 class="h-page">{t("wal.title")}</h1>
    </div>
    {#if locked}
      <button class="chip" onclick={() => (locked = false)}>
        <span class="dot lock" aria-hidden="true"></span> {t("wal.unlock")}
      </button>
    {:else}
      <span class="chip"><span class="dot ok" aria-hidden="true"></span> {t("wal.receive")} →</span>
    {/if}
  </div>

  <div class="top-grid">
    <!-- balance -->
    <div class="card balance">
      <p class="eyebrow">{t("wal.total")}</p>
      {#if locked}
        <p class="bal-locked">{t("wal.hidden_balance")}</p>
      {:else}
        <p class="bal mono" translate="no">{(w?.total ?? 0).toFixed(8)} <span class="unit">S</span></p>
      {/if}
      <p class="bal-sub">
        {t("wal.confirmed", { n: ((w?.confirmed ?? 0).toFixed(4)) })} · {t("wal.pending", { n: ((w?.pending ?? 0).toFixed(4)) })}
      </p>
      <div class="bal-actions">
        <button class="btn btn-primary" onclick={() => navigate("send")}>{t("wal.send")}</button>
        <button class="btn">{t("wal.receive")}</button>
      </div>
      {#if (w?.watchOnly ?? 0) > 0}
        <hr class="divider" />
        <p class="watch">{t("wal.watch_only")}: <span class="mono" translate="no">{(w?.watchOnly ?? 0).toFixed(2)} S</span> <span class="dim">({t("wal.pending", { n: "0.1" })})</span></p>
      {/if}
    </div>

    <!-- receive address -->
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
      {#if locked}
        <p class="addr-mask">{t("wal.hidden_balance")}</p>
      {:else}
        <p class="addr mono" translate="no">{w?.address}</p>
      {/if}
      <div class="addr-actions">
        <button class="btn btn-ghost" aria-label={t("g.copy")} onclick={copyAddr}>
          <svg viewBox="0 0 24 24" width="13" height="13" fill="none" stroke="currentColor" stroke-width="2" stroke-linecap="round" aria-hidden="true"><rect x="9" y="9" width="13" height="13" rx="2" /><path d="M5 15H4a2 2 0 0 1-2-2V4a2 2 0 0 1 2-2h9a2 2 0 0 1 2 2v1" /></svg>
          {copyOk ? "✓" : t("g.copy")}
        </button>
        <button class="btn btn-ghost">{t("dash.retry")}</button>
      </div>
      {#if !locked}
        <p class="reuse-warn mono">{t("wal.address_reuse_warning")}</p>
      {/if}
    </div>
  </div>

  <!-- tabs -->
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
      <p class="empty">{t("wal.empty")}</p>
    {:else if tab === "keys"}
      <p class="empty">{t("wal.empty")}</p>
    {:else}
      <p class="empty">{t("wal.empty")}</p>
    {/if}
  </div>
</section>

<style>
  .wal {
    display: flex;
    flex-direction: column;
    gap: 16px;
    max-width: 1080px;
    margin: 0 auto;
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
  .dot.lock {
    background: var(--honey);
  }
  .dot.ok {
    background: var(--mint);
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
  .bal-locked {
    font-size: 30px;
    margin: 6px 0 0;
    letter-spacing: 2px;
    color: var(--ink-dim);
  }
  .unit {
    font-size: 18px;
    color: var(--straw);
    margin-left: 2px;
  }
  .bal-sub {
    font-size: 12px;
    color: var(--ink-dim);
    margin: 4px 0 14px;
  }
  .bal-actions {
    display: flex;
    gap: 8px;
  }
  .divider {
    border: none;
    border-top: 1px dashed var(--line);
    margin: 14px 0 8px;
  }
  .watch {
    font-size: 12px;
    color: var(--ink-dim);
    margin: 0;
  }
  .dim {
    color: var(--mist);
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
  .addr-mask {
    margin: 0;
    letter-spacing: 3px;
    color: var(--ink-dim);
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
    background: #eee;
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