<script lang="ts">
  import { onMount, onDestroy } from "svelte";
  import { fmt, t, fmtBytes, fmtAgo } from "../lib/i18n";
  import { Services } from "../lib/services";
  import { app, navigate } from "../lib/store.svelte";
  import type { NodeInfo, Peer, SyncStatus } from "../lib/types";

  let status = $state<SyncStatus | null>(null);
  let info = $state<NodeInfo | null>(null);
  let peers = $state<Peer[]>([]);
  let curve = $state<{ t: number; height: number }[]>([]);
  let live = $state(false);
  let infoOpen = $state(false);
  let err = $state(false);
  let timer: ReturnType<typeof setInterval> | undefined;
  let lastAt = $state<number | null>(null);

  async function poll() {
    try {
      const [s, i, p, c] = await Promise.all([
        Services.getSyncStatus(),
        Services.getNodeInfo(),
        Services.getPeers(),
        Services.getSyncHistory(60),
      ]);
      status = s;
      info = i;
      peers = p;
      curve = c;
      lastAt = Date.now();
      err = false;
      live = true;
    } catch {
      err = true;
      live = false;
    }
  }

  onMount(() => {
    poll();
    timer = setInterval(poll, 5000);
  });
  onDestroy(() => clearInterval(timer));

  function pct() {
    return status ? Math.min(100, (status.blocks / status.headers) * 100) : 0;
  }
  function totalS() {
    return status ? status.headers : 0;
  }
  // chain ribbon width: window (50k) scaled to full chain
  function ribbon() {
    if (!status || totalS() === 0) return { pct: 0, winPct: 0, caught: false };
    const p = Math.min(100, (status.blocks / totalS()) * 100);
    const winPct = Math.max(1.2, (50000 / totalS()) * 100);
    return { pct: p, winPct, caught: status.blocks >= status.headers - 1 };
  }
  function eta() {
    if (!status?.etaMinutes) return null;
    const m = status.etaMinutes;
    const h = Math.floor(m / 60);
    const r = Math.round(m % 60);
    return h > 0 ? `${h}h ${r}m` : `${r}m`;
  }

  // sparkline path from curve (scaled)
  function sparkPath() {
    if (curve.length < 2) return "";
    const w = 300, h = 60;
    const xs = curve.map((c) => c.height);
    const min = Math.min(...xs), max = Math.max(...xs);
    const span = max - min || 1;
    return curve
      .map((c, i) => {
        const x = (i / (curve.length - 1)) * w;
        const y = h - ((c.height - min) / span) * (h - 8) - 4;
        return `${i === 0 ? "M" : "L"}${x.toFixed(1)},${y.toFixed(1)}`;
      })
      .join(" ");
  }

  const inbound = () => peers.filter((p) => p.dir === "inbound").length;
  const outbound = () => peers.filter((p) => p.dir === "outbound").length;
  const syncingPeers = () => peers.filter((p) => p.syncBlPerSec != null).length;

  function absWin(i: number) {
    // the 50000-block window ends near the tip; highlight ~those links
    const zone = Math.max(2, (50000 / (status?.headers ?? 43_750_000)) * 100);
    return i >= 100 - zone - 2 && i <= 100 - zone + 6 ? true : false;
  }
</script>

<div class="dash">
  <div class="head">
    <div>
      <p class="eyebrow">node sync</p>
      <h1 class="h-page">{t("nav.dashboard")}</h1>
    </div>
    <span class="live-badge" class:off={!live}>
      <span class="dot" aria-hidden="true"></span>
      {live ? t("dash.rate", { rate: status?.rateBlPerSec ?? 0 }) : t("dash.not_connected")}
    </span>
  </div>

  {#if err || !live}
    <div class="card offline-card" role="status">
      <div class="dot off" aria-hidden="true"></div>
      <span>{t("dash.not_connected")}</span>
      <button class="btn" onclick={poll}>{t("dash.retry")}</button>
    </div>
  {/if}

  <!-- stat cards -->
  <div class="stat-grid">
    <div class="card stat">
      <p class="eyebrow">{t("dash.current_height")}</p>
      <p class="stat-num mono" translate="no">{fmt(status?.blocks ?? 0)}</p>
      <p class="stat-sub">{t("dash.sync_pct", { pct: pct().toFixed(2) })}</p>
    </div>
    <div class="card stat">
      <p class="eyebrow">{t("dash.target_height")}</p>
      <p class="stat-num mono" translate="no">{fmt(totalS())}</p>
      <p class="stat-sub mint">{t("dash.headers_caught")}</p>
    </div>
    <div class="card stat">
      <p class="eyebrow">{t("dash.gap")}</p>
      <p class="stat-num stat-num-warn mono" translate="no">{fmt((status?.headers ?? 0) - (status?.blocks ?? 0))}</p>
      <p class="stat-sub">{eta() ? t("dash.eta", { eta: eta()! }) : "—"}</p>
    </div>
  </div>

  <!-- signature: Chain Ribbon -->
  <div class="card ribbon-card" aria-label={t("dash.sync_progress")}>
    <div class="ribbon-head">
      <span class="h-card">{t("dash.sync_progress")}</span>
      <span class="chip mono" translate="no">
        {(status?.blocks ?? 0).toLocaleString()} / {(status?.headers ?? 0).toLocaleString()}
      </span>
    </div>
    <div class="ribbon-scale">
      <span class="scale-label">0</span>
      <span class="scale-label scale-end">
        <span class="mono" translate="no">{(status?.headers ?? 0).toLocaleString()}</span>
        <span>100%</span>
      </span>
    </div>
    <div class="ribbon" role="img" aria-label={t("dash.sync_progress")}>
      {#each Array.from({ length: 100 }, (_, i) => i) as i}
        <span class="link" class:filled={i < Math.floor(pct())} class:winning={absWin(i)} aria-hidden="true"></span>
      {/each}
      <span class="drag-ghost" style:left={`calc(${pct()}% - 1px)`} aria-hidden="true"></span>
      <span class="win-mark" style:left={`calc(${(pct() + ribbon().winPct).toFixed(3)}% )`} aria-hidden="true"></span>
    </div>
    <div class="ribbon-footer">
      <span class="mono" translate="no">blocks {fmt(status?.blocks ?? 0)}</span>
      {#if status}
        <span class="chip">
          <span class="dot mint" aria-hidden="true"></span>
          {t("dash.rate", { rate: status.rateBlPerSec })}
        </span>
        <span class="chip">{t("dash.eta", { eta: eta() ?? "—" })}</span>
      {/if}
    </div>
  </div>

  <!-- middle row -->
  <div class="mid-grid">
    <div class="card">
      <div class="card-head">
        <span class="h-card">{t("dash.height_curve")}</span>
        <span class="corner">{lastAt ? t("dash.last_update", { ago: fmtAgo(lastAt) }) : ""}</span>
      </div>
      <svg class="spark" viewBox="0 0 300 60" preserveAspectRatio="none" aria-hidden="true">
        <path d={sparkPath()} fill="none" stroke="var(--straw)" stroke-width="1.6" />
      </svg>
      <p class="spark-caption mono" translate="no">▂▃▄▅▆▇ ▂▃▄▅▆▇ ▅▆▇ {status?.rateBlPerSec ?? 0} bl/s</p>
    </div>

    <div class="card">
      <div class="card-head">
        <span class="h-card">{t("dash.peers")}</span>
        <span class="corner mono" translate="no">{outbound()}↓ / {inbound()}↑</span>
      </div>
      <div class="peer-sum">
        <span class="chip">{t("dash.outbound", { n: outbound() })}</span>
        <span class="chip">{t("dash.inbound", { n: inbound() })}</span>
        <span class="chip honey">{t("dash.syncing_peers", { n: syncingPeers() })}</span>
      </div>
      {#if peers.length === 0}
        <p class="empty">{t("dash.empty_peers")}</p>
      {:else}
        <ul class="peer-list">
          {#each peers.slice(0, 5) as p}
            <li class="peer-row">
              <span class="dot" class:in={p.dir === "inbound"} class:out={p.dir === "outbound"} aria-hidden="true"></span>
              <span class="mono peer-addr" translate="no">{p.addr}</span>
              <span class="peer-sync mono" translate="no">{p.syncBlPerSec != null ? `+${p.syncBlPerSec}` : "—"}</span>
            </li>
          {/each}
        </ul>
      {/if}
    </div>
  </div>

  <!-- bottom row -->
  <div class="mid-grid">
    <div class="card">
      <div class="card-head"><span class="h-card">{t("dash.runtime")}</span></div>
      <dl class="kv">
        <div><dt>{t("dash.mem_heap")}</dt><dd class="mono" translate="no">{fmtBytes(info?.memHeap ?? 0)}</dd></div>
        <div><dt>{t("dash.disk_writes")}</dt><dd class="mono" translate="no">~{fmtBytes(info?.diskWritePerSec ?? 0)}</dd></div>
        <div><dt>{t("dash.uptime")}</dt><dd class="mono">{fmtAgo(info?.startedAt ?? Date.now())}</dd></div>
      </dl>
    </div>

    <div class="card">
      <div class="card-head"><span class="h-card">{t("dash.shortcuts")}</span></div>
      <div class="shortcuts">
        <button class="btn" onclick={() => navigate("internals")}>{t("dash.view_internals")}</button>
        <button class="btn" onclick={() => navigate("settings")}>
          {t("dash.sync_peers")} <span class="mono">{app.connected ? "8▾" : "—"}</span>
        </button>
        <button class="btn" onclick={() => navigate("settings")}>DebugLevel: info▾</button>
      </div>
    </div>
  </div>

  <!-- Information -->
  <section class="card info">
    <button class="info-toggle" onclick={() => (infoOpen = !infoOpen)} aria-expanded={infoOpen}>
      <span class="h-card">{t("dash.info")}</span>
      <span class="chip">{info?.version ?? "—"} · {info?.chain ?? "—"} · {infoOpen ? t("dash.fold") : t("dash.unfold")}</span>
      <svg class="chev" class:open={infoOpen} viewBox="0 0 24 24" width="14" height="14" fill="none" stroke="currentColor" stroke-width="2" stroke-linecap="round" aria-hidden="true"><polyline points="6 9 12 15 18 9" /></svg>
    </button>
    {#if infoOpen}
      <dl class="kv info-grid">
        <div><dt>version</dt><dd class="mono" translate="no">{info?.version} · protocol {info?.protocol}</dd></div>
        <div><dt>P2P</dt><dd class="mono" translate="no">{info?.p2pPort}</dd></div>
        <div><dt>network</dt><dd class="mono" translate="no">{info?.chain} · active {info?.networkactive ? "✓" : "✕"}</dd></div>
        <div><dt>data dir</dt><dd class="mono dir" translate="no">{info?.dataDir} <button class="mini-btn" onclick={() => navigate("settings")}>{t("g.open")}</button></dd></div>
        <div><dt>connections</dt><dd class="mono">{t("dash.peers")}: {outbound()}↓ {inbound()}↑ · syc {syncingPeers()}</dd></div>
        <div><dt>consensus</dt><dd class="mono" translate="no">BIP34..BIP141 ✓</dd></div>
      </dl>
    {/if}
  </section>
</div>

<style>
  .dash {
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
    text-wrap: balance;
  }
  .live-badge {
    display: inline-flex;
    align-items: center;
    gap: 7px;
    font-size: 12px;
    color: var(--mint);
    border: 1px solid rgba(74, 222, 128, 0.4);
    border-radius: 999px;
    padding: 4px 12px;
  }
  .live-badge .dot {
    background: var(--mint);
    box-shadow: 0 0 8px rgba(74, 222, 128, 0.9);
  }
  .live-badge.off {
    color: var(--mist);
    border-color: var(--line);
  }
  .live-badge.off .dot {
    background: var(--mist);
    box-shadow: none;
  }

  .offline-card {
    display: flex;
    align-items: center;
    gap: 12px;
    border-color: rgba(255, 107, 157, 0.5);
  }
  .dot.off {
    background: var(--straw);
  }

  .stat-grid {
    display: grid;
    grid-template-columns: repeat(3, 1fr);
    gap: 14px;
  }
  .stat {
    position: relative;
    overflow: hidden;
  }
  .stat::after {
    content: "";
    position: absolute;
    inset: auto 0 0 0;
    height: 2px;
    background: linear-gradient(90deg, var(--straw), transparent 70%);
    opacity: 0.6;
  }
  .stat-num {
    font-family: var(--font-display);
    font-weight: 800;
    font-size: 26px;
    margin: 6px 0 2px;
    font-variant-numeric: tabular-nums;
    letter-spacing: -0.5px;
  }
  .stat-num-warn {
    color: var(--honey);
  }
  .stat-sub {
    margin: 0;
    font-size: 12px;
    color: var(--ink-dim);
  }
  .stat-sub.mint {
    color: var(--mint);
  }

  /* chain ribbon — signature element */
  .ribbon-card {
    position: relative;
  }
  .ribbon-head,
  .ribbon-footer,
  .ribbon-scale {
    display: flex;
    align-items: center;
    justify-content: space-between;
    gap: 10px;
  }
  .ribbon-scale {
    padding: 14px 4px 6px;
    font-size: 11px;
    color: var(--ink-dim);
  }
  .scale-end {
    display: inline-flex;
    gap: 6px;
  }
  .ribbon {
    position: relative;
    display: flex;
    gap: 2px;
    height: 34px;
    align-items: center;
    padding: 0 4px;
    border-radius: var(--r-12);
    background: linear-gradient(180deg, rgba(160, 156, 181, 0.08), rgba(160, 156, 181, 0.03));
    border: 1px solid var(--line);
    overflow: hidden;
  }
  .link {
    flex: 1 1 0;
    height: 12px;
    border-radius: 3px;
    background: rgba(160, 156, 181, 0.14);
    transition: background 0.3s ease;
  }
  .link.filled {
    background: linear-gradient(180deg, var(--straw), #d94f7d);
  }
  .link.winning {
    background: linear-gradient(180deg, var(--honey), #dca31f);
    box-shadow: 0 0 10px rgba(255, 201, 60, 0.5);
  }
  .drag-ghost {
    position: absolute;
    top: 4px;
    bottom: 4px;
    width: 2px;
    background: #fff;
    opacity: 0.85;
    border-radius: 2px;
    pointer-events: none;
    animation: pulse 1.2s ease-in-out infinite;
  }
  .win-mark {
    position: absolute;
    top: 2px;
    bottom: 2px;
    width: 2px;
    background: var(--honey);
    opacity: 0.55;
    pointer-events: none;
  }
  @keyframes pulse {
    0%, 100% { opacity: 0.9; }
    50% { opacity: 0.4; }
  }
  .ribbon-footer {
    margin-top: 10px;
    font-size: 12px;
    color: var(--ink-dim);
  }
  .dot.mint {
    background: var(--mint);
  }
  .chip.honey {
    color: var(--honey);
    border-color: rgba(255, 201, 60, 0.4);
  }

  .mid-grid {
    display: grid;
    grid-template-columns: 1fr 1fr;
    gap: 14px;
  }
  .card-head {
    display: flex;
    align-items: center;
    justify-content: space-between;
    gap: 10px;
    margin-bottom: 10px;
  }
  .corner {
    font-size: 11px;
    color: var(--ink-dim);
  }
  .spark {
    width: 100%;
    height: 60px;
    display: block;
    overflow: visible;
  }
  .spark-caption {
    margin: 8px 0 0;
    font-size: 11px;
    color: var(--ink-dim);
    overflow: hidden;
    white-space: nowrap;
  }
  .peer-sum {
    display: flex;
    gap: 8px;
    flex-wrap: wrap;
    margin-bottom: 10px;
  }
  .peer-list {
    list-style: none;
    margin: 0;
    padding: 0;
  }
  .peer-row {
    display: flex;
    align-items: center;
    gap: 8px;
    padding: 6px 0;
    font-size: 12px;
  }
  .peer-row + .peer-row {
    border-top: 1px dashed var(--line);
  }
  .dot.in {
    background: var(--mint);
  }
  .dot.out {
    background: var(--straw);
  }
  .peer-addr {
    flex: 1;
    min-width: 0;
    overflow: hidden;
    text-overflow: ellipsis;
    white-space: nowrap;
  }
  .peer-sync {
    color: var(--mint);
    font-variant-numeric: tabular-nums;
  }
  .empty {
    color: var(--ink-dim);
    font-size: 12px;
    margin: 8px 0 0;
  }

  .kv {
    margin: 0;
    display: grid;
    gap: 6px;
  }
  .kv > div {
    display: flex;
    align-items: baseline;
    justify-content: space-between;
    gap: 10px;
  }
  .kv dt {
    font-size: 12px;
    color: var(--ink-dim);
  }
  .kv dd {
    margin: 0;
    font-size: 13px;
  }
  .shortcuts {
    display: flex;
    flex-wrap: wrap;
    gap: 8px;
  }

  .info {
    padding: 0;
    overflow: hidden;
  }
  .info-toggle {
    display: flex;
    align-items: center;
    gap: 12px;
    width: 100%;
    background: none;
    border: none;
    padding: 16px;
    cursor: pointer;
    text-align: left;
  }
  .info-toggle .chip {
    margin-left: auto;
  }
  .chev {
    color: var(--mist);
    transition: transform 0.15s ease;
    flex: none;
  }
  .chev.open {
    transform: rotate(180deg);
  }
  .info-toggle:focus-visible {
    outline: none;
    box-shadow: var(--focus);
  }
  .info-grid {
    padding: 0 16px 16px;
  }
  .dir {
    display: inline-flex;
    align-items: center;
    gap: 6px;
    min-width: 0;
  }
  .mini-btn {
    background: none;
    border: 1px solid var(--line);
    border-radius: 6px;
    color: var(--ink-fg);
    padding: 1px 8px;
    font-size: 11px;
    cursor: pointer;
  }
  .mini-btn:hover {
    border-color: var(--straw);
  }

  @media (max-width: 760px) {
    .stat-grid,
    .mid-grid {
      grid-template-columns: 1fr;
    }
  }
</style>