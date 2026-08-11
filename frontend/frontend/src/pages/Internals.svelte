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
        updateHdrHistory(dat);
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

  // Header track zooms into the active download window.  The window is a
  // stepwise conveyor: its right edge tracks the download frontier immediately,
  // while its left edge only advances once the dark "filling" squares have
  // settled into the pile (see hdrWindow).  A completed slice therefore pushes
  // the right line right first; when the dark square reaches the left line the
  // whole frame (both lines included) shifts left by the settled span.
  // peer -> { ts, start, end } of the slice that just completed (drives the
  // dark "filling" square and the lane fill animation, linking top+bottom).
  let hdrJustDone = new Map<string, { ts: number; start: number; end: number }>();
  const hdrWindow = $derived.by(() => {
    if (!dat) return { l: 0, r: 1 };
    const sliceLen = dat.headerTasks.sliceLen || 2000;
    const frontier = Math.max(
      dat.headerTip,
      dat.headerTasks.nextAssign,
      ...dat.headerTasks.hdrPeers.map((p) => p.end),
    );
    const now = Date.now();
    let unsettled = 0;
    for (const v of hdrJustDone.values()) if (now - v.ts < 4500) unsettled++;
    const settledTip = Math.max(0, dat.headerTip - unsettled * sliceLen);
    const l = Math.max(0, settledTip - sliceLen * 2);
    const r = Math.min(dat.headerBoundary, frontier + sliceLen * 6);
    return r > l ? { l, r } : { l, r: Math.max(1, dat.headerTip) };
  });
  const hdrLeft = () => hdrWindow.l;
  const hdrRight = () => hdrWindow.r;
  const hdrFrac = (h: number) => {
    const l = hdrLeft();
    const r = hdrRight();
    return r > l ? Math.min(100, Math.max(0, ((h - l) / (r - l)) * 100)) : 0;
  };
  // Stable color per peer, derived from the sorted lane list so the track bands
  // and the per-IP progress bars below always share the same color.
  const peerColor = $derived.by(() => {
    const m = new Map<string, number>();
    if (!dat) return m;
    const peers = [...new Set(dat.headerTasks.hdrLanes.map((l) => l.peer))].sort();
    peers.forEach((p, i) => m.set(p, i % 8));
    return m;
  });
  // Per-IP header slice history. The backend only reports each peer's *current*
  // header range, and a slice can complete between two polls (so `received` is
  // often never seen). We treat any change of a peer's assigned range as one
  // completed slice, which drives the green "done" squares on the track and the
  // fake per-IP fill animation below.
  interface HdrDoneRec {
    peer: string;
    start: number;
    end: number;
    at: number;
  }
  let hdrPrevStart = new Map<string, number>();
  let hdrDone: HdrDoneRec[] = [];
  // peer -> { ts, start, end } of the slice that just completed (drives the
  // dark "filling" square and the lane fill animation, linking top+bottom).
  function updateHdrHistory(d: NodeInternals) {
    const lanes = d.headerTasks.hdrLanes;
    const sliceLen = d.headerTasks.sliceLen || 2000;
    const seen = new Set(hdrDone.map((x) => `${x.peer}:${x.start}`));
    const fresh = new Map<string, { ts: number; start: number; end: number }>();
    for (const l of lanes) {
      const before = hdrPrevStart.get(l.peer) ?? 0;
      if (before > 0 && (l.received || l.start !== before)) {
        const key = `${l.peer}:${before}`;
        if (!seen.has(key)) {
          hdrDone = [...hdrDone, { peer: l.peer, start: before, end: before + sliceLen, at: Date.now() }].slice(-10);
          fresh.set(l.peer, { ts: Date.now(), start: before, end: before + sliceLen });
        }
      }
      hdrPrevStart.set(l.peer, l.start);
    }
    for (const [peer, start] of hdrPrevStart) {
      if (start > 0 && !lanes.some((l) => l.peer === peer)) {
        const key = `${peer}:${start}`;
        if (!seen.has(key)) {
          hdrDone = [...hdrDone, { peer, start, end: start + sliceLen, at: Date.now() }].slice(-10);
          fresh.set(peer, { ts: Date.now(), start, end: start + sliceLen });
        }
        hdrPrevStart.set(peer, 0);
      }
    }
    if (fresh.size > 0) {
      const now = Date.now();
      for (const [p, v] of fresh) hdrJustDone.set(p, v);
      for (const [p, v] of hdrJustDone) if (now - v.ts > 12000) hdrJustDone.delete(p);
    }
  }
  function hdrJust(peer: string): number {
    return hdrJustDone.get(peer)?.ts ?? 0;
  }
  function hdrJustFresh(peer: string): boolean {
    const j = hdrJust(peer);
    return j > 0 && Date.now() - j < 6000;
  }

  // Conveyor of fixed-size task squares inside the track window:
  // green = recently completed slice, colored = in-flight, gray = next todo.
  const hdrDoneBlocks = $derived.by(() => {
    const l = hdrLeft();
    const r = hdrRight();
    const span = r - l;
    if (span <= 0) return [];
    return hdrDone
      .slice(-6)
      .map((d) => {
        const ls = Math.max(d.start, l);
        const rs = Math.min(d.end, r);
        const lf = ((ls - l) / span) * 100;
        const rf = ((rs - l) / span) * 100;
        if (rf <= lf) return null;
        return { peer: d.peer, l: lf, w: rf - lf, start: d.start };
      })
      .filter((s) => s !== null) as { peer: string; l: number; w: number; start: number }[];
  });
  const hdrTodoBlocks = $derived.by(() => {
    if (!dat) return [];
    const l = hdrLeft();
    const r = hdrRight();
    const span = r - l;
    if (span <= 0) return [];
    const sliceLen = dat.headerTasks.sliceLen || 2000;
    const frontier = Math.max(
      dat.headerTip,
      dat.headerTasks.nextAssign,
      ...dat.headerTasks.hdrPeers.map((p) => p.end),
    );
    const blocks: { l: number; w: number; start: number }[] = [];
    for (let i = 0; i < 6; i++) {
      const start = frontier + i * sliceLen;
      if (start >= r) break;
      const lf = ((Math.max(start, l) - l) / span) * 100;
      const rf = ((Math.min(start + sliceLen, r) - l) / span) * 100;
      if (rf <= lf) continue;
      blocks.push({ start, l: lf, w: rf - lf });
    }
    return blocks;
  });
  // Dark "filling" overlay: for ~6s after a peer's bar fills, the just-finished
  // slice holds its light peer color, then turns dark, then fades to light green
  // once the stepwise frame has shifted it into the pile.  Its position rides
  // the frame (left transitions) so it visibly drifts toward the pile.
  const hdrJustBlocks = $derived.by(() => {
    if (!dat) return [];
    const l = hdrLeft();
    const r = hdrRight();
    const span = r - l;
    if (span <= 0) return [];
    const out: { peer: string; ts: number; l: number; w: number }[] = [];
    for (const [peer, v] of hdrJustDone) {
      if (Date.now() - v.ts < 6000) {
        const ls = Math.max(v.start, l);
        const rs = Math.min(v.end, r);
        const lf = ((ls - l) / span) * 100;
        const rf = ((rs - l) / span) * 100;
        if (rf > lf) out.push({ peer, ts: v.ts, l: lf, w: rf - lf });
      }
    }
    return out;
  });
  // Two guide lines: left = edge of the done (green) pile, right = the active
  // frontier. Both slide as slices complete.
  const hdrLeftLine = $derived.by(() => {
    if (!dat) return 0;
    const minStart = dat.headerTasks.hdrPeers.length > 0 ? Math.min(...dat.headerTasks.hdrPeers.map((p) => p.start)) : dat.headerTip;
    return hdrFrac(minStart);
  });
  const hdrRightLine = $derived.by(() => {
    if (!dat) return 0;
    const frontier = Math.max(
      dat.headerTip,
      dat.headerTasks.nextAssign,
      ...dat.headerTasks.hdrPeers.map((p) => p.end),
    );
    return hdrFrac(frontier);
  });
  const hdrBands = $derived.by(() => {
    if (!dat) return [];
    const l = hdrLeft();
    const r = hdrRight();
    if (r <= l) return [];
    return dat.headerTasks.hdrPeers
      .map((p) => {
        const ls = Math.max(p.start, l);
        const rs = Math.min(p.end, r);
        const lf = ((ls - l) / (r - l)) * 100;
        const rf = ((rs - l) / (r - l)) * 100;
        if (rf <= lf) return null;
        return { ...p, l: lf, w: rf - lf };
      })
      .filter((s) => s !== null) as (typeof dat.headerTasks.hdrPeers[number] & { l: number; w: number })[];
  });

  function hdrTotalPct() {
    if (!dat) return 0;
    return dat.headerBoundary > 0 ? Math.min(100, (dat.headerTip / dat.headerBoundary) * 100) : 0;
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
        <div class="task-total">
          <span class="mono" translate="no">{fmt(dat.headerTip)} / {fmt(dat.headerBoundary)}</span>
          <div class="total-bar" aria-hidden="true">
            <span style:width={`${hdrTotalPct()}%`}></span>
          </div>
          <span class="mono">{hdrTotalPct().toFixed(1)}%</span>
        </div>
        <div class="win-track hdr" role="img" aria-label={t("int.tasks_headers")}>
          {#each hdrTodoBlocks as b}
            <span class="hdr-todo-block" style:left={`${b.l}%`} style:width={`${b.w}%`} title={`${t("int.todo")} ${fmt(b.start)}`} aria-hidden="true"></span>
          {/each}
          <span class="hdr-done-pile" style:left="0%" style:width={`${hdrLeftLine}%`} title={t("int.done")} aria-hidden="true"></span>
          {#each hdrDoneBlocks as b (b.start)}
            <span class="hdr-done-block" style:left={`${b.l}%`} style:width={`${b.w}%`} title={`${b.peer} ${fmt(b.start)}→${fmt(b.start + (dat.headerTasks.sliceLen || 2000))} · ${t("int.done")}`} aria-hidden="true"></span>
          {/each}
          {#each hdrJustBlocks as j (j.peer + ":" + j.ts)}
            <span
              class="hdr-just-block"
              style:--pc={`var(--peer${((peerColor.get(j.peer) ?? 0) % 8) + 1})`}
              style:left={`${j.l}%`}
              style:width={`${j.w}%`}
              title={`${j.peer} filling → ${t("int.done")}`}
              aria-hidden="true"
            ></span>
          {/each}
          {#each hdrBands as b (b.peer)}
            <span
              class="ws hdr-band"
              class:accent={b.received}
              style:--pc={`var(--peer${((peerColor.get(b.peer) ?? 0) % 8) + 1})`}
              style:left={`${b.l}%`}
              style:width={`${b.w}%`}
              title={`${b.peer} ${fmt(b.start)}→${fmt(b.end)} · ${b.received ? t("int.done") : t("int.inflight")}${!b.received ? ` ${b.pct.toFixed(0)}%` : ""}${b.assignedAt ? ` · ${fmtAgo(b.assignedAt)}` : ""}`}
            >
              {#if b.received}
                <span class="ws-done" style:width="100%"></span>
              {:else}
                <span class="ws-done" style:width={`${Math.max(0, Math.min(100, b.pct))}%`}></span>
                <span class="ws-flight" aria-hidden="true"></span>
              {/if}
            </span>
          {/each}
          <span class="hdr-line hdr-right-line" style:left={`${hdrRightLine}%`} title={`${t("int.hdr_next_assign")} ${fmt(dat.headerTasks.nextAssign)}`} aria-hidden="true"></span>
          <span class="hdr-line hdr-left-line" style:left={`${hdrLeftLine}%`} title={`${t("int.done")} edge`} aria-hidden="true"></span>
        </div>
        <div class="win-labels">
          <span class="mono mint">▲ {t("int.hdr_tip")} {fmt(dat.headerTip)}</span>
          <span class="mono straw">↦ {t("int.hdr_next_assign")} {fmt(dat.headerTasks.nextAssign)}</span>
          <span class="mono">◆ target {fmt(dat.headerBoundary)}</span>
        </div>
        {#if dat.headerTasks.hdrLanes.length > 0}
          <p class="legend mono" aria-hidden="true">▓ {t("int.done")} ░ {t("int.inflight")} · {t("int.todo")}</p>
          <div class="hdr-lanes">
            {#each dat.headerTasks.hdrLanes as l}
              <div class="lane">
                <span class="lane-name mono" translate="no">{l.peer}</span>
                <div class="lane-bar" aria-hidden="true">
                  {#key `${l.peer}:${l.start}:${hdrJust(l.peer)}`}
                    <span
                      class="lane-done hi hdr-lane-fill"
                      class:done={l.received || hdrJustFresh(l.peer)}
                      style:--pc={`var(--peer${((peerColor.get(l.peer) ?? 0) % 8) + 1})`}
                      title={`${fmt(l.start)}→${fmt(l.end)} · ${l.received || hdrJustFresh(l.peer) ? t("int.done") : t("int.inflight")} ${l.pct.toFixed(0)}%`}
                    ></span>
                  {/key}
                </div>
                <span class="lane-vals mono" translate="no">{fmt(l.start)}→{fmt(l.end)} · {l.received ? "100%" : `${l.pct.toFixed(0)}%`}</span>
              </div>
            {/each}
          </div>
        {/if}
        <dl class="kv">
          <div><dt>{t("int.hdr_window_start")}</dt><dd class="mono" translate="no">{fmt(hdrLeft())} → {fmt(hdrRight())}</dd></div>
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
    --peer1: #e34d42;
    --peer2: #e8873b;
    --peer3: #ecd034;
    --peer4: #d4a017;
    --peer5: #a2acb8;
    --peer6: #35c3c8;
    --peer7: #4f86e0;
    --peer8: #9b6adf;
    --hdr-pile: #019875;
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

  .hdr-line {
    position: absolute;
    top: 0;
    bottom: 0;
    width: 2px;
    transition: left 1.5s cubic-bezier(0.22, 1, 0.36, 1);
    pointer-events: none;
  }
  .hdr-right-line {
    background: var(--honey);
    opacity: 0.95;
  }
  .hdr-left-line {
    background: #006b54;
    opacity: 1;
  }
  .hdr-done-pile {
    position: absolute;
    top: 6px;
    bottom: 6px;
    left: 0;
    background: var(--hdr-pile);
    border-radius: 5px;
    opacity: 0.92;
    transition: width 1.5s cubic-bezier(0.22, 1, 0.36, 1);
  }
  .hdr-todo-block {
    position: absolute;
    top: 6px;
    bottom: 6px;
    background: repeating-linear-gradient(45deg, #e4e4e4, #e4e4e4 5px, #ededed 5px, #ededed 10px);
    border: 1px dashed #c6c6c6;
    border-radius: 5px;
    transition:
      left 1.5s cubic-bezier(0.22, 1, 0.36, 1),
      width 1.5s cubic-bezier(0.22, 1, 0.36, 1);
  }
  .hdr-done-block {
    position: absolute;
    top: 6px;
    bottom: 6px;
    background: var(--hdr-pile);
    border: 1px solid color-mix(in srgb, var(--hdr-pile) 60%, #0a6b3a);
    border-radius: 5px;
    opacity: 0.92;
    transition:
      left 1.5s cubic-bezier(0.22, 1, 0.36, 1),
      width 1.5s cubic-bezier(0.22, 1, 0.36, 1);
  }
  .hdr-just-block {
    position: absolute;
    top: 6px;
    bottom: 6px;
    border-radius: 5px;
    background: var(--pc);
    border: 1px solid color-mix(in srgb, var(--pc) 65%, #000);
    animation: hdr-settle 6s ease forwards;
    transition: left 1.5s cubic-bezier(0.22, 1, 0.36, 1);
    pointer-events: none;
  }
  @keyframes hdr-settle {
    0% {
      background: var(--pc);
      opacity: 1;
    }
    30% {
      background: var(--pc);
      opacity: 1;
    }
    55% {
      background: color-mix(in srgb, var(--pc) 55%, #000);
      opacity: 1;
    }
    85% {
      background: color-mix(in srgb, var(--pc) 55%, #000);
      opacity: 1;
    }
    100% {
      background: var(--hdr-pile);
      opacity: 0.95;
    }
  }
  .win-track.hdr .ws {
    top: 6px;
    bottom: 6px;
    transition:
      left 1.5s cubic-bezier(0.22, 1, 0.36, 1),
      width 1.5s cubic-bezier(0.22, 1, 0.36, 1),
      background 0.4s,
      border-color 0.4s,
      box-shadow 0.4s;
  }
  .hdr-band.accent {
    box-shadow: 0 0 0 1px var(--pc);
    background: color-mix(in srgb, var(--pc) 55%, #000);
    border-color: color-mix(in srgb, var(--pc) 65%, #000);
  }
  .hdr-band.accent .ws-done {
    background: color-mix(in srgb, var(--pc) 55%, #000);
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
  .hdr-lanes {
    margin-top: 10px;
    border-top: 1px solid var(--line);
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
  .lane-done.hi {
    background: linear-gradient(180deg, var(--pc), color-mix(in srgb, var(--pc) 70%, #000));
  }
  .lane-done.hi.hdr-lane-fill:not(.done) {
    width: 5%;
    min-width: 3px;
    opacity: 0.55;
  }
  .lane-done.hi.hdr-lane-fill.done {
    background: linear-gradient(180deg, var(--pc), color-mix(in srgb, var(--pc) 55%, #000));
    animation: hdr-fill 2s cubic-bezier(0.22, 1, 0.36, 1) both;
    box-shadow: 0 0 6px color-mix(in srgb, var(--pc) 55%, transparent);
  }
  @keyframes hdr-fill {
    from {
      width: 0%;
    }
    to {
      width: 100%;
    }
  }
  .lane-inflight {
    position: absolute;
    top: 1px;
    bottom: 1px;
    min-width: 2px;
    background: var(--honey);
    border-radius: 2px;
  }
  .lane-bar .lane-inflight {
    background: var(--pc);
    opacity: 0.85;
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

  /* The header-track conveyor is a load-bearing status animation; restore its
     durations even under the global prefers-reduced-motion override. */
  @media (prefers-reduced-motion: reduce) {
    .hdr-line,
    .hdr-done-pile,
    .hdr-todo-block,
    .hdr-done-block,
    .hdr-just-block {
      transition-duration: 1.5s !important;
    }
    .hdr-just-block {
      animation-duration: 6s !important;
    }
    .win-track.hdr .ws {
      transition-duration: 1.5s, 1.5s, 0.4s, 0.4s, 0.4s !important;
    }
    .lane-done {
      transition-duration: 0.6s !important;
    }
    .lane-done.hi.hdr-lane-fill.done {
      animation-duration: 2s !important;
    }
  }
</style>