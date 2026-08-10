<script lang="ts">
  import { onMount, onDestroy } from "svelte";
  import { t, fmt, fmtBytes, fmtAgo } from "../lib/i18n";
  import { Services } from "../lib/services";
  import type { NodeInternals } from "../lib/types";

  let dat = $state<NodeInternals | null>(null);
  let trace = $state(false);
  let detail = $state(false);
  let debug = $state("info");
  let timer: ReturnType<typeof setInterval> | undefined;

  async function poll() {
    try {
      dat = await Services.getNodeInternals(detail && debug === "trace" ? "trace" : "normal");
      debug = await Services.getDebugLevel();
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
    Services.setDebugLevel(v).then(() => {
      debug = v;
    });
  }

  // position of things on 0..total
  const total = () => dat?.headerTip ?? 43_750_000;
  const pos = (h: number) => (total() ? (h / total()) * 100 : 0);
  // block task lane fill
  function sliceFill(s: { start: number; end: number; applied: number; complete: boolean }) {
    const w = s.end - s.start || 1;
    return Math.min(100, ((s.applied - s.start) / w) * 100);
  }
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
    <!-- window position -->
    <div class="card">
      <div class="card-head">
        <span class="h-card">{t("int.window_position")}</span>
        <span class="chip mono" translate="no">0 → 43,750,000</span>
      </div>
      <div class="win-track" role="img" aria-label={t("int.window_position")}>
        <span class="b-chain" aria-hidden="true"></span>
        <span class="b-boundary" style:left={`${pos(dat.chainBoundary)}%`} aria-hidden="true"></span>
        <span class="b-window" style:left={`${pos(dat.chainBoundary)}%`} style:width={`${pos(dat.windowSize)}%`} aria-hidden="true"></span>
        <span class="b-tip" style:left={`${pos(dat.chainTip)}%`} aria-hidden="true"></span>
        <span class="b-hmark" style:left={`${pos(dat.headerTip)}%`} aria-hidden="true"></span>
      </div>
      <div class="win-labels">
        <span class="mono">▲ chainBoundary</span>
        <span class="mono straw">{t("int.window_size", { n: fmt(dat.windowSize) })}</span>
        <span class="mono">▲ chainTip {fmt(dat.chainTip)}</span>
        <span class="mono mint">▲ headerTip {t("int.caught_up")}</span>
      </div>
    </div>

    <!-- speed -->
    <div class="card">
      <div class="card-head">
        <span class="h-card">{t("int.window_speed")}</span>
        <span class="corner">
          <select aria-label={t("int.options")}>
            <option>{t("int.height")}</option>
            <option>{t("int.boundary")}</option>
          </select>
        </span>
      </div>
      <div class="speed-bars" aria-hidden="true">
        {#each Array.from({ length: 26 }, (_, i) => (i % 5 === 0 ? 22 : i % 3 === 0 ? 14 : 8)) as h}
          <span class="bar" style:height={`${h}px`}></span>
        {/each}
      </div>
      <p class="speed-cap mono">▲ 265 bl/s · boundary +12/h · ETA 37h</p>
    </div>

    <!-- tasks -->
    <div class="two">
      <div class="card">
        <div class="card-head">
          <span class="h-card">{t("int.tasks_blocks")}</span>
        </div>
        {#each dat.blockTasks.slices as s}
          <div class="lane">
            <span class="lane-name mono">peer {s.peer}</span>
            <div class="lane-bar" aria-hidden="true">
              <span class="lane-done" style:width={`${sliceFill(s)}%`}></span>
              <span class="lane-slot"></span>
            </div>
            <span class="lane-vals mono" translate="no">{fmt(s.start)}→{fmt(s.end)} {s.complete ? "✓" : "…"}</span>
            {#if detail}
              <span class="lane-time mono" translate="no">{fmtAgo(s.assignedAt)} ago</span>
            {/if}
          </div>
        {/each}
        <p class="legend mono" aria-hidden="true">▓ allocated  ░ in-flight</p>
      </div>

      <div class="card">
        <div class="card-head"><span class="h-card">{t("int.tasks_headers")}</span></div>
        <div class="seg-grid">
          {#each dat.headerTasks.ranges as r}
            <span class="seg" class:done={r.state === "done"} class:inflight={r.state === "inflight"} class:todo={r.state === "todo"} title={`${r.start}-${r.end}`}>
              <span class="mono" translate="no">{fmt(r.start)}-{fmt(r.end)}</span>
              {r.state === "done" ? "✓" : r.state === "inflight" ? "●" : "·"}
            </span>
          {/each}
        </div>
        <dl class="kv">
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
          <div><dt>{t("int.heap")}</dt><dd class="mono" translate="no">{fmtBytes(dat.mem.heapAlloc)}</dd></div>
          <div><dt>HeapObjects</dt><dd class="mono" translate="no">{fmt(dat.mem.heapObjects)}</dd></div>
          <div><dt>NumGC</dt><dd class="mono" translate="no">{fmt(dat.mem.numGC)}</dd></div>
          <div><dt>{t("int.resident")}</dt><dd class="mono">~{Math.round(dat.windowSize / 1000)}k</dd></div>
        </dl>
        <div class="mem-bar" aria-hidden="true"><span style:width="88%"></span></div>
      </div>

      <div class="card">
        <div class="card-head">
          <span class="h-card">{t("int.debug_level")}</span>
          <span class="chip mono" translate="no">info</span>
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
    height: 46px;
    border-radius: var(--r-12);
    background: #f0f0f0;
    border: 1px solid var(--line);
    overflow: hidden;
    margin-bottom: 8px;
  }
  .b-chain {
    position: absolute;
    inset: 0;
    background: repeating-linear-gradient(90deg, var(--straw) 0, var(--straw) 2px, transparent 2px, transparent 12%);
    opacity: 0.15;
  }
  .b-boundary {
    position: absolute;
    top: 6px;
    bottom: 6px;
    width: 2px;
    background: var(--mist);
    opacity: 0.7;
  }
  .b-window {
    position: absolute;
    top: 8px;
    bottom: 8px;
    min-width: 3px;
    background: var(--straw);
    opacity: 0.85;
    border-radius: 4px;
  }
  .b-tip {
    position: absolute;
    top: 4px;
    bottom: 4px;
    width: 2px;
    background: var(--ink-fg);
    opacity: 0.6;
  }
  .b-hmark {
    position: absolute;
    top: 4px;
    bottom: 4px;
    width: 2px;
    background: var(--mint);
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
  .lane {
    display: grid;
    grid-template-columns: 62px 1fr 170px;
    align-items: center;
    gap: 10px;
    padding: 6px 0;
    font-size: 12px;
  }
  .lane-name {
    color: var(--ink-dim);
  }
  .lane-bar {
    position: relative;
    height: 14px;
    border-radius: 4px;
    background: #e0e0e0;
    overflow: hidden;
  }
  .lane-done {
    position: absolute;
    inset: 0 auto 0 0;
    background: var(--straw);
    border-radius: 4px;
  }
  .lane-slot {
    position: absolute;
    top: 2px;
    bottom: 2px;
    left: 78%;
    width: 2px;
    background: var(--ink-fg);
    opacity: 0.3;
  }
  .lane-vals {
    color: var(--ink-dim);
    font-variant-numeric: tabular-nums;
    min-width: 0;
  }
  .lane-time {
    grid-column: 3;
    color: var(--mist);
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
      grid-template-columns: 52px 1fr;
    }
    .lane-vals {
      grid-column: 2;
    }
    .lane-time {
      grid-column: 2;
    }
  }
</style>