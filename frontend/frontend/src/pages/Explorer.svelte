<script lang="ts">
  import { onMount } from "svelte";
  import { fmtDateTime, t } from "../lib/i18n";
  import { Services } from "../lib/services";
  import { app } from "../lib/store.svelte";
  import { explorerSettings } from "../lib/explorer-settings.svelte";
  import type { ExplorerBlock, ExplorerChain, ExplorerTx } from "../lib/types";

  // ── Explorer (Step 10): local node RPC, three-level drill-down ──
  // View state machine: "chain" (stats + recent blocks) → "block" (one
  // block's details + tx list) → "tx" (one transaction's details).
  // A tx lookup anywhere requires txindex=1 — without it the page degrades
  // to an error card, matching the wallet history behavior.
  // 浏览器(第 10 步):本节点 RPC,三级下钻。视图状态机:"chain"
  // (链统计 + 近期区块)→ "block"(单区块详情 + 交易列表)→ "tx"(单交易
  // 详情)。任意交易查询需要 txindex=1——未启用时页面降级为错误卡,
  // 与钱包历史行为一致。
  let view = $state<"chain" | "block" | "tx">("chain");
  let chain = $state<ExplorerChain | null>(null);
  let blocks = $state<ExplorerBlock[]>([]);
  let block = $state<(ExplorerBlock & { tx: string[] }) | null>(null);
  let tx = $state<ExplorerTx | null>(null);
  let search = $state("");
  let loadErr = $state<string | null>(null);
  let busy = $state(false);

  // fmtBase renders a satoshi-ish big number readably (supply figures).
  // fmtBase 可读地渲染聪级大数(供应量等)。
  function fmtBase(n: number): string {
    return n.toLocaleString("en-US");
  }

  async function loadChain() {
    view = "chain";
    block = null;
    tx = null;
    loadErr = null;
    busy = true;
    try {
      // Chain stats and the recent block list load together (12 rows,
      // newest first — the list is capped to keep getblock loops light).
      // 链统计与近期区块列表并行加载(12 行,最新在前——限制行数以
      // 保持 getblock 循环轻量)。
      const [c, bs] = await Promise.all([Services.getExplorerChain(), Services.getExplorerBlocks(explorerSettings.recentBlocks)]);
      chain = c;
      blocks = bs;
    } catch (e) {
      loadErr = String(e);
    } finally {
      busy = false;
    }
  }

  async function openBlock(hash: string) {
    view = "block";
    block = null;
    tx = null;
    loadErr = null;
    busy = true;
    try {
      block = await Services.getExplorerBlock(hash);
    } catch (e) {
      loadErr = String(e);
    } finally {
      busy = false;
    }
  }

  async function openTx(txid: string) {
    view = "tx";
    tx = null;
    loadErr = null;
    busy = true;
    try {
      tx = await Services.getExplorerTx(txid);
    } catch (e) {
      loadErr = String(e);
    } finally {
      busy = false;
    }
  }

  // Search: 1-8 digits = block height (getblockhash → detail); 64 hex
  // chars = txid first, block hash on failure.
  // 搜索:1-8 位数字 = 区块高度(getblockhash → 详情);64 位十六进制 =
  // 先交易 id,失败再按区块哈希。
  async function doSearch() {
    const q = search.trim();
    if (!q) return;
    loadErr = null;
    busy = true;
    try {
      if (/^\d{1,8}$/.test(q)) {
        const hash = await Services.getExplorerBlockHash(Number(q));
        await openBlock(hash);
      } else {
        try {
          await openTx(q);
        } catch {
          await openBlock(q);
        }
      }
    } catch (e) {
      loadErr = String(e);
    } finally {
      busy = false;
    }
  }

  onMount(() => {
    // Cross-page deep links: wallet history passes {txid}, a block row
    // drill passes nothing (openBlock is called directly).
    // 跨页深链:钱包历史传 {txid};区块行下钻直接调用 openBlock。
    const p = app.shortcut as { txid?: string; blockHash?: string };
    if (p?.txid) {
      openTx(String(p.txid));
    } else if (p?.blockHash) {
      openBlock(String(p.blockHash));
    } else {
      loadChain();
    }
  });
</script>

<section class="exp">
  <div class="head">
    <div>
      <p class="eyebrow">chain · explorer</p>
      <!-- breadcrumb reflects the drill level / 面包屑反映下钻层级 -->
      <h1 class="h-page">
        {#if view === "chain"}
          {t("exp.title")}
        {:else if view === "block"}
          {t("exp.block")} <span class="mono crumb" translate="no">#{block?.height ?? "…"}</span>
        {:else}
          {t("exp.tx")}
        {/if}
      </h1>
    </div>
    <div class="head-actions">
      <form
        class="search"
        onsubmit={(e) => {
          e.preventDefault();
          doSearch();
        }}
      >
        <input
          type="text"
          bind:value={search}
          placeholder={t("exp.search_hint")}
          aria-label={t("exp.search_hint")}
          spellcheck="false"
          translate="no"
        />
        <button class="btn btn-primary" type="submit" disabled={busy}>
          {#if busy}<span class="spin" aria-hidden="true"></span>{/if}
          {t("exp.search")}
        </button>
      </form>
      {#if view !== "chain"}
        <button class="btn btn-ghost" onclick={loadChain}>{t("exp.back_chain")}</button>
      {/if}
      {#if view === "tx"}
        {#if tx?.blockhash}
          <button class="btn btn-ghost" onclick={() => tx?.blockhash && openBlock(tx.blockhash)}>
            {t("exp.back_block")}
          </button>
        {/if}
      {/if}
    </div>
  </div>

  {#if loadErr}
    <div class="card err-card" role="alert">
      <p class="err"><span class="dot" aria-hidden="true"></span>{loadErr}</p>
      <p class="hint">{t("exp.err_hint")}</p>
    </div>
  {:else if view === "chain"}
    <!-- ── level 1: chain stats + recent blocks ── -->
    <!-- ── 第一级:链统计 + 近期区块 ── -->
    <div class="stats-grid">
      {#if chain}
        <div class="card stat">
          <p class="eyebrow">{t("exp.stat_height")}</p>
          <p class="stat-v mono" translate="no">{fmtBase(chain.height)}</p>
        </div>
        <div class="card stat">
          <p class="eyebrow">{t("exp.stat_headers")}</p>
          <p class="stat-v mono" translate="no">{fmtBase(chain.headers)}</p>
        </div>
        <div class="card stat">
          <p class="eyebrow">{t("exp.stat_difficulty")}</p>
          <p class="stat-v mono" translate="no">{chain.difficulty.toExponential(3)}</p>
        </div>
        <div class="card stat">
          <p class="eyebrow">{t("exp.stat_chain")}</p>
          <p class="stat-v" translate="no">{chain.chain}</p>
        </div>
      {:else}
        <div class="card stat"><p class="empty">{t("wal.loading")}</p></div>
      {/if}
    </div>

    {#if chain}
      <div class="card tip-card">
        <p class="tip mono" translate="no" title={chain.bestHash}>
          {t("exp.tip")} <span class="hash">{chain.bestHash}</span>
        </p>
      </div>
    {/if}

    <div class="card tabled-card">
      <div class="table-title">{t("exp.recent_blocks")}</div>
      {#if blocks.length === 0}
        <p class="empty">{busy ? t("wal.loading") : t("exp.no_blocks")}</p>
      {:else}
        <table class="blk-table">
          <thead>
            <tr>
              <th scope="col">{t("exp.col_height")}</th>
              <th scope="col">{t("exp.col_hash")}</th>
              <th scope="col">{t("wal.col_time")}</th>
              <th scope="col">{t("exp.col_txs")}</th>
              <th scope="col">{t("exp.col_size")}</th>
              <th scope="col">{t("wal.col_status")}</th>
            </tr>
          </thead>
          <tbody>
            {#each blocks as b (b.hash)}
              <tr class="row" onclick={() => openBlock(b.hash)} title={t("exp.open_block")}>
                <td class="mono" translate="no">#{fmtBase(b.height)}</td>
                <td class="mono blk-hash" translate="no" title={b.hash}>{b.hash}</td>
                <td class="mono" translate="no">{fmtDateTime(b.time)}</td>
                <td class="mono" translate="no">{fmtBase(b.txCount)}</td>
                <td class="mono" translate="no">{(b.size / 1024).toFixed(1)} KB</td>
                <td><span class="st confirmed"><span class="dot" aria-hidden="true"></span>{t("wal.confirmed_status")}</span></td>
              </tr>
            {/each}
          </tbody>
        </table>
      {/if}
    </div>
  {:else if view === "block" && block}
    <!-- ── level 2: block detail ── -->
    <!-- ── 第二级:区块详情 ── -->
    <div class="card detail-card">
      <div class="kv"><span>{t("exp.col_height")}</span><span class="mono" translate="no">#{fmtBase(block.height)}</span></div>
      <div class="kv"><span>{t("exp.col_hash")}</span><span class="mono kv-v" translate="no" title={block.hash}>{block.hash}</span></div>
      <div class="kv"><span>{t("exp.col_confirmations")}</span><span class="mono" translate="no">{fmtBase(block.confirmations)}</span></div>
      <div class="kv"><span>{t("wal.col_time")}</span><span class="mono" translate="no">{fmtDateTime(block.time)}</span></div>
      <div class="kv"><span>{t("exp.col_size")}</span><span class="mono" translate="no">{(block.size / 1024).toFixed(1)} KB</span></div>
      <div class="kv"><span>{t("exp.col_difficulty")}</span><span class="mono" translate="no">{block.difficulty.toExponential(3)}</span></div>
      <div class="kv"><span>{t("exp.col_nonce")}</span><span class="mono" translate="no">{block.nonce}</span></div>
      <div class="kv"><span>{t("exp.col_bits")}</span><span class="mono" translate="no">{block.bits}</span></div>
    </div>

    <div class="card tabled-card">
      <div class="table-title">{t("exp.block_txs", { n: fmtBase(block.txCount) })}</div>
      {#if block.tx.length === 0}
        <p class="empty">{t("exp.no_txs")}</p>
      {:else}
        <table class="blk-table">
          <thead>
            <tr>
              <th scope="col">{t("exp.col_txid")}</th>
              <th scope="col">{t("exp.col_action")}</th>
            </tr>
          </thead>
          <tbody>
            {#each block.tx as txid (txid)}
              <tr class="row" onclick={() => openTx(txid)} title={t("exp.open_tx")}>
                <td class="mono blk-hash" translate="no" title={txid}>{txid}</td>
                <td><span class="link">{t("exp.open_tx")} →</span></td>
              </tr>
            {/each}
          </tbody>
        </table>
      {/if}
    </div>
  {:else if view === "tx" && tx}
    <!-- ── level 3: transaction detail ── -->
    <!-- ── 第三级:交易详情 ── -->
    <div class="card detail-card">
      <div class="kv"><span>{t("exp.col_txid")}</span><span class="mono kv-v" translate="no" title={tx.txid}>{tx.txid}</span></div>
      {#if tx.blockhash}
        <div class="kv">
          <span>{t("exp.col_blockhash")}</span>
          <span class="mono kv-v link" translate="no" title={tx.blockhash} role="button" tabindex="0"
            onclick={() => openBlock(tx.blockhash!)}
            onkeydown={(e) => e.key === "Enter" && openBlock(tx.blockhash!)}
          >{tx.blockhash}</span>
        </div>
      {/if}
      {#if tx.confirmations !== undefined}
        <div class="kv"><span>{t("exp.col_confirmations")}</span><span class="mono" translate="no">{fmtBase(tx.confirmations)}</span></div>
      {/if}
      <div class="kv">
        <span>{t("wal.col_time")}</span>
        <span class="mono" translate="no">{fmtDateTime(tx.blocktime ?? tx.time ?? 0)}</span>
      </div>
      <div class="kv"><span>{t("exp.col_size")}</span><span class="mono" translate="no">{tx.size} B</span></div>
      <div class="kv"><span>{t("exp.col_total_out")}</span><span class="mono" translate="no">{tx.totalOut.toFixed(8)} S</span></div>
      <div class="kv"><span>{t("exp.col_vin_vout")}</span><span class="mono" translate="no">{tx.vinCount} / {tx.voutCount}</span></div>
    </div>

    <div class="top-grid">
      <div class="card tabled-card">
        <div class="table-title">{t("exp.inputs", { n: tx.vinCount })}</div>
        {#if tx.inputs.length === 0}
          <p class="empty">{t("exp.coinbase")}</p>
        {:else}
          <table class="io-table">
            <tbody>
              {#each tx.inputs as vin, i (i)}
                <tr class="row" class:clickable={!!vin.txid} onclick={() => vin.txid && openTx(vin.txid)} title={vin.txid ? t("exp.open_tx") : undefined}>
                  <td class="mono io-ref" translate="no" title={vin.txid || "coinbase"}>
                    {vin.txid ? `${vin.txid.slice(0, 16)}…:${vin.vout}` : "coinbase"}
                  </td>
                  <td class="mono io-addr" translate="no">{vin.address ?? "—"}</td>
                </tr>
              {/each}
            </tbody>
          </table>
        {/if}
      </div>

      <div class="card tabled-card">
        <div class="table-title">{t("exp.outputs", { n: tx.voutCount })}</div>
        <table class="io-table">
          <tbody>
            {#each tx.outputs as vout (vout.n)}
              <tr>
                <td class="mono" translate="no">#{vout.n}</td>
                <td class="mono amount" translate="no">{vout.value.toFixed(8)} S</td>
                <td class="mono io-addr" translate="no" title={vout.address ?? ""}>{vout.address ?? "—"}</td>
              </tr>
            {/each}
          </tbody>
        </table>
      </div>
    </div>
  {:else}
    <div class="card"><p class="empty">{busy ? t("wal.loading") : "—"}</p></div>
  {/if}
</section>

<style>
  .exp {
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
    .exp {
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
  .crumb {
    font-size: 18px;
    color: var(--ink-dim);
  }
  .head-actions {
    display: inline-flex;
    align-items: center;
    gap: 8px;
    flex-wrap: wrap;
  }
  .search {
    display: flex;
    gap: 8px;
  }
  .search input {
    min-width: 260px;
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
    display: flex;
    flex-direction: column;
    gap: 8px;
  }
  .hint {
    font-size: 11px;
    color: var(--ink-dim);
    margin: 0;
    line-height: 1.45;
  }

  /* ── level 1: stats grid + tip ── */
  .stats-grid {
    display: grid;
    grid-template-columns: repeat(4, 1fr);
    gap: 14px;
  }
  .stat {
    display: flex;
    flex-direction: column;
    gap: 4px;
    padding: 14px;
  }
  .stat-v {
    font-size: 20px;
    font-weight: 700;
    margin: 0;
    overflow: hidden;
    text-overflow: ellipsis;
    white-space: nowrap;
  }
  .tip-card {
    padding: 12px 14px;
  }
  .tip {
    margin: 0;
    font-size: 12px;
    color: var(--ink-dim);
    display: flex;
    gap: 8px;
    align-items: center;
    min-width: 0;
  }
  .tip .hash {
    overflow: hidden;
    text-overflow: ellipsis;
    white-space: nowrap;
    color: var(--ink-fg);
  }

  /* ── tables ── */
  .tabled-card {
    padding: 0;
    overflow: hidden;
  }
  .table-title {
    padding: 12px 14px 4px;
    font-size: 10px;
    text-transform: uppercase;
    letter-spacing: 0.08em;
    color: var(--ink-dim);
    font-weight: 700;
  }
  .blk-table,
  .io-table {
    width: 100%;
    border-collapse: collapse;
    font-size: 13px;
  }
  .blk-table th {
    text-align: left;
    font-size: 11px;
    font-weight: 700;
    letter-spacing: 0.8px;
    text-transform: uppercase;
    color: var(--mist);
    padding: 10px 14px;
  }
  .blk-table td,
  .io-table td {
    padding: 9px 14px;
    border-top: 1px solid var(--line);
    font-variant-numeric: tabular-nums;
  }
  .row {
    cursor: pointer;
  }
  .row:hover td {
    background: var(--violet);
  }
  .blk-hash,
  .io-ref,
  .io-addr {
    max-width: 300px;
    overflow: hidden;
    text-overflow: ellipsis;
    white-space: nowrap;
  }
  .io-addr {
    color: var(--ink-dim);
    font-size: 12px;
  }
  .amount {
    color: var(--straw);
  }
  .link {
    color: var(--straw);
    font-size: 12px;
    font-weight: 600;
  }
  .kv-v.link {
    cursor: pointer;
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
    background: var(--mint);
  }

  /* ── level 2/3: detail card ── */
  .detail-card {
    display: grid;
    grid-template-columns: repeat(2, 1fr);
    gap: 6px 24px;
    padding: 14px 16px;
  }
  .kv {
    display: flex;
    justify-content: space-between;
    gap: 12px;
    font-size: 12px;
    padding: 6px 0;
    border-bottom: 1px solid var(--violet);
  }
  .kv span:first-child {
    color: var(--ink-dim);
    flex: none;
  }
  .kv-v {
    overflow: hidden;
    text-overflow: ellipsis;
    white-space: nowrap;
    text-align: right;
  }

  .top-grid {
    display: grid;
    grid-template-columns: 1fr 1fr;
    gap: 14px;
  }
  .empty {
    color: var(--ink-dim);
    padding: 28px;
    margin: 0;
    text-align: center;
    font-size: 13px;
  }

  @media (max-width: 860px) {
    .stats-grid {
      grid-template-columns: repeat(2, 1fr);
    }
    .top-grid {
      grid-template-columns: 1fr;
    }
    .detail-card {
      grid-template-columns: 1fr;
    }
  }
</style>
