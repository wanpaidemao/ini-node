<script lang="ts">
  import { onMount, onDestroy } from "svelte";
  import { t, fmt, fmtAgo } from "../lib/i18n";
  import { Services } from "../lib/services";
  import type { NodeInternals } from "../lib/types";

  let dat = $state<NodeInternals | null>(null);
  let trace = $state(false);
  let detail = $state(false);
  let debug = $state("info");
  let metric = $state("height");
  let timer: ReturnType<typeof setInterval> | undefined;

  // Client-side sampling of the poll stream so the speed/ETA numbers are real,
  // computed from the live chain tip / boundary instead of placeholders.
  interface Sample {
    t: number;
    tip: number;
    boundary: number;
  }
  const hist = $state<Sample[]>([]);

  async function poll() {
    try {
      dat = await Services.getNodeInternals(detail && debug === "trace" ? "trace" : "normal");
      if (dat) {
        const now = Date.now();
        hist.push({ t: now, tip: dat.chainTip, boundary: dat.chainBoundary });
        if (hist.length > 30) hist.shift();
      }
    } catch {
      /* keep */
    }
  }

  onMount(() => {
    poll();
    timer = setInterval(poll, 2000);
  });
  onDestroy(() => clearInterval(timer));

  function applyDebug(ev: Event) {
    const v = (ev.target as HTMLSelectElement).value;
    debug = v;
    Services.setDebugLevel(v);
  }

  // Text keeps the whole-chain totals; the track zooms into the active window.
  const total = () => dat?.headerBoundary ?? dat?.headerTip ?? 43_750_000;
  const winLeft = () => dat?.chainBoundary ?? 0;
  const winRight = () => {
    const r = winLeft() + (dat?.windowSize ?? 0);
    return Math.max(winLeft() + 1, Math.min(r, total()));
  };
  const winFrac = (h: number) => {
    const l = winLeft();
    const r = winRight();
    return r > l ? Math.min(100, Math.max(0, ((h - l) / (r - l)) * 100)) : 0;
  };
  // Each task slice is drawn as a band spanning its [start, end) inside the
  // window; s.fill is how much of that band its owner has actually downloaded.
  const winSlices = $derived.by(() => {
    if (!dat) return [];
    const l = winLeft();
    const r = winRight();
    if (r <= l) return [];
    const slices = dat.blockTasks.slices;
    return slices
      .map((s) => {
        const ls = Math.max(s.start, l);
        const rs = Math.min(s.end, r);
        const lf = ((ls - l) / (r - l)) * 100;
        const rf = ((rs - l) / (r - l)) * 100;
        if (rf <= lf) return null;
        return { ...s, l: lf, w: rf - lf };
      })
      .filter((s) => s !== null) as (typeof slices[number] & { l: number; w: number })[];
  });

  // overall sync progress across the whole chain
  function taskPct() {
    if (!dat) return 0;
    return dat.headerTip > 0 ? Math.min(100, (dat.blockTasks.synced / dat.headerTip) * 100) : 0;
  }

  function headerPieces() {
    if (!dat || dat.headerTasks.sliceLen <= 0) return 0;
    return Math.max(1, Math.ceil(dat.headerTip / dat.headerTasks.sliceLen));
  }

  // live speed / ETA derived from successive polls
  const rate = $derived.by(() => {
    if (!dat || hist.length < 2) return { v: 0, etaMin: null as number | null };
    const a = hist[0];
    const b = hist[hist.length - 1];
    const dt = (b.t - a.t) / 1000;
    if (dt <= 0) return { v: 0, etaMin: null };
    const dv = metric === "boundary" ? b.boundary - a.boundary : b.tip - a.tip;
    const v = Math.max(0, dv / dt);
    const remaining =
      metric === "boundary"
        ? Math.max(0, dat.headerBoundary - dat.chainBoundary)
        : Math.max(0, dat.headerBoundary - dat.chainTip);
    return { v, etaMin: v > 0 ? remaining / v / 60 : null };
  });

  function etaText(min: number | null): string {
    if (min == null || !Number.isFinite(min)) return "—";
    if (min < 1) return "<1m";
    const h = Math.floor(min / 60);
    const m = Math.round(min % 60);
    return h <= 0 ? `${m}m` : `${h}h${m ? ` ${m}m` : ""}`;
  }

  // sparkline bars fed by the real stream (same count/shape as before)
  const bars = $derived.by(() => {
    if (hist.length < 2) return Array.from({ length: 26 }, (_, i) => (i % 5 === 0 ? 22 : i % 3 === 0 ? 14 : 8));
    const key = metric === "boundary" ? ("boundary" as const) : ("tip" as const);
    const rates: number[] = [];
    for (let i = 1; i < hist.length; i++) {
      const dt = (hist[i].t - hist[i - 1].t) / 1000;
      if (dt > 0) rates.push(Math.max(0, (hist[i][key] - hist[i - 1][key]) / dt));
    }
    if (rates.length === 0) return [8];
    const max = Math.max(...rates, 1);
    return rates.slice(-26).map((r) => Math.max(2, Math.round((r / max) * 22)));
  });
</script>

<section class="int">
  <div class="head">
    <div>
      <p class="eyebrow">sync internals</p>
      <h1 class="h-page">{t("int.title")}</h1>
    </div>
    <span class="live">
      <span class="dot" aria-hidden="true"></span> 2s
    </span>
  </div>

  {#if dat}
    <!-- window status -->
    <div class="card">
      <div class="card-head">
        <span class="h-card">{t("int.window_status")}</span>
        <span class="chip mono" translate="no">{fmt(winLeft())} → {fmt(winRight())} · {t("int.window_size", { n: fmt(dat.windowSize) })}</span>
      </div>
      <div class="win-track" role="img" aria-label={t("int.window_status")}>
        <span class="ws-tip" style:left={`${winFrac(dat.chainTip)}%`} aria-hidden="true"></span>
        {#each winSlices as s, i}
          <span
            class="ws"
            class:accent={s.syncNode}
            style:--pc={`var(--peer${(i % 6) + 1})`}
            style:left={`${s.l}%`}
            style:width={`${s.w}%`}
            title={`${s.peer} ${fmt(s.start)}→${fmt(s.end)} · ${s.pct.toFixed(0)}%${s.inFlight > 0 ? ` · ${s.inFlight} in-flight` : ""}`}
          >
            <span class="ws-done" style:width={`${Math.max(0, Math.min(100, s.pct))}%`}></span>
            {#if s.inFlight > 0}<span class="ws-flight" aria-hidden="true"></span>{/if}
          </span>
        {/each}
        <span class="ws-boundary" style:left={`${winFrac(dat.chainBoundary)}%`} aria-hidden="true"></span>
      </div>
      <div class="win-labels">
        <span class="mono">▲ {t("int.boundary")} {fmt(dat.chainBoundary)}</span>
        <span class="mono">▲ tip {fmt(dat.chainTip)}</span>
        <span class="mono straw">{t("int.window_size", { n: fmt(dat.windowSize) })}</span>
        <span class="mono mint">▲ headerTip {t("int.caught_up")}</span>
      </div>
    </div>

    <!-- speed -->
    <div class="card">
      <div class="card-head">
        <span class="h-card">{t("int.window_speed")}</span>
        <span class="corner">
          <select aria-label={t("int.options")} bind:value={metric}>
            <option value="height">{t("int.height")}</option>
            <option value="boundary">{t("int.boundary")}</option>
          </select>
        </span>
      </div>
      <div class="speed-bars" aria-hidden="true">
        {#each bars as h}
          <span class="bar" style:height={`${h}px`}></span>
        {/each}
      </div>
      <p class="speed-cap mono">▲ {t("int.blocks_per_s", { n: fmt(Math.round(rate.v)) })} · {t("int.eta", { eta: etaText(rate.etaMin) })}</p>
    </div>

    <!-- tasks -->
    <div class="two">
      <div class="card">
        <div class="card-head">
          <span class="h-card">{t("int.tasks_blocks")}</span>
          <span class="chip mono" translate="no">{fmt(dat.blockTasks.total)} blocks to go</span>
        </div>
        <div class="task-total">
          <span class="mono" translate="no">{fmt(dat.blockTasks.synced)} / {fmt(dat.headerTip)}</span>
          <div class="total-bar" aria-hidden="true">
            <span style:width={`${taskPct()}%`}></span>
          </div>
          <span class="mono">{taskPct().toFixed(1)}%</span>
        </div>
        {#each dat.blockTasks.slices as s}
          <div class="lane">
            <span class="lane-name mono">{s.peer}{#if s.syncNode}<span class="lane-star" title="sync node"> ★</span>{/if}</span>
            <div class="lane-bar" aria-hidden="true">
              <span class="lane-done" style:width={`${s.pct}%`}></span>
              {#if s.inFlight > 0}<span class="lane-inflight" style:width={`${Math.min(100 - s.pct, 12)}%`} style:left={`${s.pct}%`}></span>{/if}
            </div>
            <span class="lane-vals mono" translate="no">{fmt(s.start)}→{fmt(s.end)} · {s.pct.toFixed(0)}%</span>
            {#if detail}
              <span class="lane-time mono" title="in-flight / last block at">{s.inFlight}↕ · {s.lastActiveAt ? fmtAgo(s.lastActiveAt) : "—"}</span>
            {/if}
          </div>
        {/each}
        <p class="legend mono" aria-hidden="true">▓ downloaded  ░ in-flight</p>
      </div>

      <div class="card">
        <div class="card-head">
          <span class="h-card">{t("int.tasks_headers")}</span>
          {#if dat.headerTasks.sliceLen > 0}
            <span class="chip mono" translate="no">{t("int.task_segments", { n: fmt(headerPieces()), len: fmt(dat.headerTasks.sliceLen) })}</span>
          {/if}
        </div>
        <div class="seg-grid">
          {#each dat.headerTasks.ranges as r}
            <span class="seg" class:done={r.state === "done"} class:inflight={r.state === "inflight"} class:todo={r.state === "todo"} title={`${r.start}-${r.end}`}>
              <span class="mono" translate="no">{fmt(r.start)}-{fmt(r.end)}</span>
              {r.state === "done" ? "✓" : r.state === "inflight" ? "●" : "·"}
            </span>
          {/each}
        </div>
        {#if dat.headerTasks.recent.length > 0}
          <div class="seg-grid recent">
            {#each [...dat.headerTasks.recent].reverse() as w}
              <span class="seg done" title={`${fmt(w.start)}→${fmt(w.end)} · ${w.peer} · ${fmtAgo(w.assignedAt)}`}>
                <span class="mono" translate="no">{fmt(w.start)}→{fmt(w.end)}</span>✓
              </span>
            {/each}
          </div>
        {/if}
        <dl class="kv">
          <div><dt>{t("int.window_start")}</dt><dd class="mono" translate="no">{fmt(winLeft())}</dd></div>
          <div><dt>{t("int.window_end")}</dt><dd class="mono" translate="no">{fmt(winRight())}</dd></div>
          <div><dt>{t("int.window_size_label")}</dt><dd class="mono" translate="no">{fmt(dat.windowSize)}</dd></div>
          <div><dt>{t("int.requested_blocks")}</dt><dd class="mono" translate="no">{fmt(dat.headerTasks.requestedBlocks)}</dd></div>
          <div><dt>{t("int.last_reissue")}</dt><dd class="mono">{fmtAgo(dat.headerTasks.lastReissueAt)}</dd></div>
        </dl>
      </div>
    </div>

    <!-- mem + debug -->
    <div class="two">
      <div class="card">
        <div class="card-head"><span class="h-card">{t("int.memory")}</span></div>
        <dl class="kv">
          <div><dt>{t("int.prefetch")}</dt><dd class="mono" translate="no">{fmt(dat.mem.window)} bl</dd></div>
          <div><dt>{t("int.to_go")}</dt><dd class="mono" translate="no">{fmt(dat.mem.gap)} bl</dd></div>
          <div><dt>{t("int.inflight")}</dt><dd class="mono" translate="no">{fmt(dat.mem.inflight)} bl</dd></div>
        </dl>
        <div class="mem-bar" aria-hidden="true"><span style:width={`${dat.mem.gap > 0 ? Math.min(100, (dat.mem.window / dat.mem.gap) * 100) : 100}%`}></span></div>
      </div>

      <div class="card">
        <div class="card-head">
          <span class="h-card">{t("int.debug_level")}</span>
          <span class="chip mono" translate="no">{dat.debugLevel}</span>
        </div>
        <div class="debug-row">
          <label class="field-label" for="dbg">{t("int.current")}: {debug}</label>
          <select id="dbg" value={debug} onchange={applyDebug}>
            <option>info</option>
            <option>debug</option>
            <option>trace</option>
            <option>warn</option>
            <option>off</option>
          </select>
          <label class="field-label" for="dbgapply">{t("g.apply")}</label>
        </div>
        <div class="quick-row">
          {#each ["debug", "trace", "info", "warn", "off"] as q}
            <button class="btn btn-ghost se" onclick={() => { debug = q; Services.setDebugLevel(q); }}>{q}</button>
          {/each}
        </div>
        <label class="check">
          <input type="checkbox" bind:checked={detail} onchange={() => poll()} />
          <span>{t("int.details")} — <span class="dim">{t("int.trace_hint")}</span></span>
        </label>
      </div>
    </div>
  {:else}
    <div class="card"><p>{t("int.not_connected")}</p></div>
  {/if}
</section>

<style>
  .int {
    display: flex;
    flex-direction: column;
    gap: 16px;
    max-width: 1080px;
    margin: 0 auto;
  }
  .int {
    --peer1: var(--straw);
    --peer2: var(--honey);
    --peer3: var(--mint);
    --peer4: #9a7bd8;
    --peer5: #4aa3c7;
    --peer6: #d86a8a;
  }
  .head {
    display: flex;
    align-items: flex-end;
    justify-content: space-between;
  }
  .h-page {
    font-family: var(--font-display);
    font-size: 24px;
    margin: 2px 0 0;
  }
  .live {
    display: inline-flex;
    align-items: center;
    gap: 6px;
    font-size: 12px;
    color: var(--mint);
  }
  .live .dot {
    background: var(--mint);
  }
  .card-head {
    display: flex;
    align-items: center;
    justify-content: space-between;
    gap: 10px;
    margin-bottom: 12px;
  }
  .corner select {
    padding: 4px 24px 4px 8px;
    min-height: 28px;
    font-size: 12px;
  }

  .win-track {
    position: relative;
    height: 44px;
    border-radius: var(--r-12);
    background: #f0f0f0;
    border: 1px solid var(--line);
    overflow: hidden;
    margin-bottom: 8px;
  }
  .ws {
    position: absolute;
    top: 4px;
    bottom: 4px;
    min-width: 3px;
    background: color-mix(in srgb, var(--pc) 16%, transparent);
    border: 1px solid color-mix(in srgb, var(--pc) 45%, transparent);
    border-radius: 5px;
    overflow: hidden;
  }
  .ws.accent {
    box-shadow: 0 0 0 1px var(--pc);
  }
  .ws-done {
    position: absolute;
    inset: 0 auto 0 0;
    background: var(--pc);
    border-radius: 4px;
  }
  .ws-flight {
    position: absolute;
    top: 0;
    bottom: 0;
    width: 2px;
    background: #fff;
    box-shadow: 0 0 4px rgba(0, 0, 0, 0.35);
  }
  .ws-boundary {
    position: absolute;
    top: 0;
    bottom: 0;
    width: 2px;
    background: var(--mist);
    opacity: 0.8;
  }
  .ws-tip {
    position: absolute;
    top: 0;
    bottom: 0;
    width: 2px;
    background: var(--ink-fg);
    opacity: 0.6;
  }
  .win-labels {
    display: flex;
    gap: 16px;
    font-size: 11px;
    color: var(--ink-dim);
    flex-wrap: wrap;
  }
  .straw {
    color: var(--straw);
  }
  .mint {
    color: var(--mint);
  }

  .speed-bars {
    display: flex;
    align-items: flex-end;
    gap: 5px;
    height: 40px;
  }
  .bar {
    flex: 1;
    background: var(--straw);
    border-radius: 2px 2px 0 0;
    opacity: 0.85;
  }
  .speed-cap {
    margin: 10px 0 0;
    font-size: 12px;
    color: var(--ink-dim);
  }

  .two {
    display: grid;
    grid-template-columns: 1fr 1fr;
    gap: 14px;
  }
  .task-total {
    display: grid;
    grid-template-columns: 1fr 1fr;
    align-items: center;
    gap: 6px 10px;
    font-size: 12px;
    color: var(--ink-dim);
    margin-bottom: 10px;
  }
  .total-bar {
    grid-column: 1 / -1;
    grid-row: 1;
    height: 8px;
    border-radius: 4px;
    background: #e0e0e0;
    overflow: hidden;
  }
  .total-bar span {
    display: block;
    height: 100%;
    background: var(--straw);
    border-radius: 4px;
  }
  .task-total > .mono:nth-child(2) {
    justify-self: end;
    color: var(--straw);
    font-weight: 700;
  }
  .lane {
    display: grid;
    grid-template-columns: 150px 1fr 150px;
    align-items: center;
    gap: 10px;
    padding: 6px 0;
    font-size: 12px;
  }
  .lane-name {
    color: var(--ink-dim);
    overflow: hidden;
    text-overflow: ellipsis;
    white-space: nowrap;
  }
  .lane-star {
    color: var(--honey);
  }
  .lane-bar {
    position: relative;
    height: 16px;
    border-radius: 8px;
    background: linear-gradient(#0003, #0003), #f0f0f0;
    box-shadow: inset 0 1px 2px rgba(0, 0, 0, 0.08);
    overflow: hidden;
  }
  .lane-done {
    position: absolute;
    inset: 0 auto 0 0;
    background: linear-gradient(180deg, var(--straw), color-mix(in srgb, var(--straw) 75%, #c07f00));
    border-radius: 8px;
    transition: width 0.6s ease;
  }
  .lane-inflight {
    position: absolute;
    top: 1px;
    bottom: 1px;
    min-width: 2px;
    background: var(--honey);
    border-radius: 2px;
  }
  .lane-vals {
    color: var(--ink-dim);
    font-variant-numeric: tabular-nums;
    min-width: 0;
    text-align: right;
  }
  .lane-time {
    color: var(--mist);
    text-align: right;
  }
  .legend {
    margin: 8px 0 0;
    font-size: 11px;
    color: var(--ink-dim);
  }

  .seg-grid {
    display: flex;
    flex-wrap: wrap;
    gap: 6px;
  }
  .seg {
    display: inline-flex;
    align-items: center;
    gap: 6px;
    border-radius: 6px;
    padding: 4px 8px;
    font-size: 11px;
    border: 1px solid var(--line);
    color: var(--ink-dim);
  }
  .seg.done {
    border-color: var(--mint);
    color: var(--mint);
  }
  .seg.inflight {
    border-color: var(--honey);
    color: var(--honey);
  }
  .kv {
    margin: 14px 0 0;
    display: grid;
    gap: 6px;
  }
  .kv > div {
    display: flex;
    justify-content: space-between;
  }
  .kv dt {
    font-size: 12px;
    color: var(--ink-dim);
  }
  .kv dd {
    margin: 0;
    font-size: 13px;
  }

  .mem-bar {
    height: 8px;
    border-radius: 4px;
    background: #e0e0e0;
    margin-top: 12px;
    overflow: hidden;
  }
  .mem-bar span {
    display: block;
    height: 100%;
    background: var(--straw);
    border-radius: 4px;
  }

  .debug-row {
    display: flex;
    align-items: center;
    gap: 8px;
    flex-wrap: wrap;
  }
  .debug-row .field-label {
    margin: 0;
  }
  .quick-row {
    display: flex;
    flex-wrap: wrap;
    gap: 6px;
    margin: 10px 0;
  }
  .btn.se {
    padding: 4px 10px;
    min-height: 28px;
    font-size: 12px;
  }
  .check {
    display: flex;
    align-items: center;
    gap: 8px;
    font-size: 13px;
    cursor: pointer;
  }
  .check input {
    accent-color: var(--straw);
  }
  .dim {
    color: var(--ink-dim);
  }

  @media (max-width: 760px) {
    .two {
      grid-template-columns: 1fr;
    }
    .lane {
      grid-template-columns: 120px 1fr;
    }
    .lane-vals {
      grid-column: 2;
      text-align: left;
    }
    .lane-time {
      grid-column: 2;
    }
  }
</style>