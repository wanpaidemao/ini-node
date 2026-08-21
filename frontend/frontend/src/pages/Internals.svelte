<script lang="ts">
  import { onMount, onDestroy } from "svelte";
  import { flip } from "svelte/animate";
  import { fade } from "svelte/transition";
  import { t, fmt, fmtAgo, fmtBytes, fmtUptime } from "../lib/i18n";
  import { Services } from "../lib/services";
  import type { NodeInternals } from "../lib/types";

let dat = $state<NodeInternals | null>(null);
let detail = $state(false);
let metric = $state("height");
let timer: ReturnType<typeof setInterval> | undefined;
// Auto-refresh interval in seconds; the user can change it in the page header.
// Persisted in localStorage so the choice survives navigating away and back.
const REFRESH_KEY = "int.refreshSec";
function loadRefreshSec(): number {
  try {
    const v = Number(localStorage.getItem(REFRESH_KEY));
    if (v >= 1 && v <= 60) return v;
  } catch {
    /* keep default */
  }
  return 5;
}
let refreshSec = $state(loadRefreshSec());

function restartTimer() {
  try {
    localStorage.setItem(REFRESH_KEY, String(refreshSec));
  } catch {
    /* storage may be unavailable; the timer still restarts */
  }
  clearInterval(timer);
  timer = setInterval(poll, refreshSec * 1000);
}

  // Client-side sampling of the poll stream so the speed/ETA numbers are real,
  // computed from the live chain tip / boundary instead of placeholders.
  interface Sample {
    t: number;
    tip: number;
    boundary: number;
  }
  const hist = $state<Sample[]>([]);
  // Traffic sampling for the average rate card: total recv/sent across all
  // peers, captured once per poll.  Kept short (~2 min) so the average is a
  // "recent" figure, and updated only when peerStats itself is refreshed
  // (5-min cadence / manual button), matching the low-frequency design.
  interface TrafficSample {
    t: number;
    recv: number;
    sent: number;
  }
  const traffic = $state<TrafficSample[]>([]);
  let lastTrafficAt = 0;
  const trafficWindowMs = 120_000;
  // Low-frequency snapshot of the per-peer stats consumed by the quality card.
  // Refreshed only on the 5-minute cadence or the manual refresh button, so
  // the card does not churn every 2s poll.
  let peerStatsView = $state<NodeInternals["peerStats"]>([]);

  function pushTraffic(ps: NodeInternals["peerStats"]) {
    const recv = ps.reduce((m, p) => m + p.bytesRecv, 0);
    const sent = ps.reduce((m, p) => m + p.bytesSent, 0);
    const now = Date.now();
    traffic.push({ t: now, recv, sent });
    const cutoff = now - trafficWindowMs;
    while (traffic.length > 0 && traffic[0].t < cutoff) traffic.shift();
  }

  // Average download/upload rate over the sampled window (KB/s).
  const avgRate = $derived.by(() => {
    if (traffic.length < 2) return { down: 0, up: 0 };
    const a = traffic[0];
    const b = traffic[traffic.length - 1];
    const dt = (b.t - a.t) / 1000;
    if (dt <= 0) return { down: 0, up: 0 };
    return {
      down: Math.max(0, (b.recv - a.recv) / dt / 1024),
      up: Math.max(0, (b.sent - a.sent) / dt / 1024),
    };
  });

  async function poll() {
    try {
      dat = await Services.getNodeInternals(detail ? "trace" : "normal");
      if (dat) {
        assignPeerNums(dat.peerStats);
        // A slice's peer may be missing from peerStats (it just disconnected
        // while its range is still pending), so ensure EVERY peer shown in the
        // tasks/hover labels has a stable #N — otherwise the hover shows "?"
        // instead of the number.
        for (const s of dat.blockTasks.slices) {
          if (!peerNumMap.has(s.peer)) peerNumMap.set(s.peer, nextPeerNum++);
        }
        for (const p of dat.headerTasks.hdrPeers) {
          if (!peerNumMap.has(p.peer)) peerNumMap.set(p.peer, nextPeerNum++);
        }
        settleDoneBlocks(dat);
        const now = Date.now();
        hist.push({ t: now, tip: dat.chainTip, boundary: dat.chainBoundary });
        if (hist.length > 30) hist.shift();
        // Enforce the client-side disconnect list: btcd has no timed ban, so
        // a banned peer that reconnects before its duration elapses is dropped
        // again on every poll.
        for (const [addr, until] of [...bannedUntil]) {
          if (now >= until) {
            bannedUntil.delete(addr);
            continue;
          }
          if ((dat.peerStats ?? []).some((p) => p.addr === addr)) {
            Services.disconnectNode(addr).catch(() => {});
          }
        }
        // Traffic samples are pushed on EVERY poll (2s) so the average
        // download/upload rate card stays live and the sample window fills.
        // The peer-quality table itself refreshes on the slow cadence (5 min)
        // or manual refresh only, so it does not churn every 2s poll.
        pushTraffic(dat.peerStats);
        if (now - lastTrafficAt >= 300_000) {
          lastTrafficAt = now;
          peerStatsView = dat.peerStats;
        }
      }
    } catch {
      /* keep */
    }
  }

  // Manual refresh of the traffic / peer-quality cards.
  async function refreshStats() {
    if (!dat) return;
    lastTrafficAt = Date.now();
    peerStatsView = dat.peerStats;
    pushTraffic(dat.peerStats);
    // Ask the node to ping every peer now, then re-poll after one RTT so the
    // latency column fills in even for freshly connected peers (btcd only
    // pings on its own ~2-minute cadence, leaving new peers at pingtime=0).
    Services.ping().catch(() => {});
    setTimeout(async () => {
      try {
        const d = await Services.getNodeInternals(detail ? "trace" : "normal");
        if (d) {
          dat = d;
          assignPeerNums(d.peerStats);
          settleDoneBlocks(d);
          lastTrafficAt = Date.now();
          peerStatsView = d.peerStats;
          pushTraffic(d.peerStats);
        }
      } catch {
        /* keep */
      }
    }, 3000);
  }

  // Client-side disconnect list: addr -> unix ms until which the peer must
  // stay disconnected.  btcd has no timed ban (no setban), so poll() drops a
  // banned peer again whenever it reconnects before the duration elapses.
  const bannedUntil = $state(new Map<string, number>());
  // Disconnect duration choices (ms); default 12 hours.
  const banOptions = [
    { label: "12h", ms: 12 * 3600_000 },
    { label: "1d", ms: 24 * 3600_000 },
    { label: "2d", ms: 48 * 3600_000 },
    { label: "3d", ms: 72 * 3600_000 },
    { label: "7d", ms: 7 * 24 * 3600_000 },
  ];
  let banChoice = $state(banOptions[0].ms);
  // Peer currently offered for disconnect in the confirm dialog (null = closed).
  let banTarget = $state<string | null>(null);

  async function disconnectPeer(addr: string) {
    bannedUntil.set(addr, Date.now() + banChoice);
    await Services.disconnectNode(addr).catch(() => {});
  }
  // Confirm dialog handlers: confirm disconnects the targeted peer with the
  // selected duration, cancel just closes the dialog.
  async function confirmDisconnect() {
    if (banTarget) {
      await disconnectPeer(banTarget);
      banTarget = null;
    }
  }
  function cancelDisconnect() {
    banTarget = null;
  }
  // Remaining ban time for a peer (ms), 0 if not banned.
  function banLeft(addr: string) {
    const until = bannedUntil.get(addr);
    return until && until > Date.now() ? until - Date.now() : 0;
  }
  // Compact remaining-time text for the disconnect badge.
  function fmtBan(ms: number) {
    const s = Math.max(0, Math.floor(ms / 1000));
    const d = Math.floor(s / 86400);
    const h = Math.floor((s % 86400) / 3600);
    const m = Math.floor((s % 3600) / 60);
    if (d > 0) return `${d}d ${h}h`;
    if (h > 0) return `${h}h ${m}m`;
    return `${Math.max(1, m)}m`;
  }

  onMount(() => {
    poll();
    restartTimer();
  });
onDestroy(() => clearInterval(timer));

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
  // Completed block-ranges tracked client-side: the backend only reports each
  // peer's *current* range, so once a peer is re-assigned its old band would
  // vanish. We keep finished ranges as dark "settled" nodes on the track that
  // only disappear once the window edge slides past them.
  interface DoneRange {
    start: number;
    end: number;
    peer: string;
  }
  let doneBlocks = $state<DoneRange[]>([]);
  let prevRanges = $state<Map<string, { start: number; end: number }>>(new Map());
  function settleDoneBlocks(d: NodeInternals) {
    const curRanges = new Map<string, { start: number; end: number }>();
    for (const s of d.blockTasks.slices) curRanges.set(s.peer, { start: s.start, end: s.end });
    const completed: DoneRange[] = [];
    for (const [peer, prev] of prevRanges) {
      const cur = curRanges.get(peer);
      if (!cur || cur.start !== prev.start || cur.end !== prev.end) {
        completed.push({ ...prev, peer });
      }
    }
    let list = [...doneBlocks, ...completed].filter((x) => {
      const active = curRanges.get(x.peer);
      if (active && active.start === x.start && active.end === x.end) return false;
      return x.end > winLeft();
    });
    if (list.length > 4000) list = list.slice(-4000);
    doneBlocks = list;
    prevRanges = curRanges;
  }

  // ---- Header conveyor: window over the ACTIVE band area ----------------
  // Each cell is one real slice (sliceLen = 2000 headers), keyed by its
  // ABSOLUTE start height.  The window is anchored to the peers' current
  // ranges ([minStart−2000 … maxEnd+2000]) so the completed (received=true)
  // slices — the dark "finished" progress bars — show their deep color at
  // their real height position; when a range advances N slices the shared
  // cells shift N positions left and flip() slides the whole row.
  type HdrCell =
    | { key: string; kind: "done"; title: string; start: number; end: number }
    | { key: string; kind: "inflight"; title: string; peer: string; start: number; end: number; received: boolean }
    | { key: string; kind: "todo"; title: string; start: number }
    | { key: string; kind: "empty"; title: string };

  const hdrConveyor = $derived.by((): HdrCell[] => {
    if (!dat) return [];
    const sliceLen = dat.headerTasks.sliceLen || 2000;
    const peers = dat.headerTasks.hdrPeers;
    const num = (peer: string) => `#${peerNumOf(peer) ?? "?"}`;
    if (peers.length === 0) return [];
    const minStart = Math.min(...peers.map((p) => p.start));
    const maxEnd = Math.max(...peers.map((p) => p.end));
    const left = Math.floor(minStart / sliceLen) * sliceLen - sliceLen;
    const right = Math.ceil(maxEnd / sliceLen) * sliceLen + sliceLen;
    const cells: HdrCell[] = [];
    for (let start = Math.max(0, left); start < right; start += sliceLen) {
      const end = start + sliceLen;
      const p = peers.find((x) => x.start < end && x.end > start);
      if (p && p.received) {
        cells.push({ key: `h${start}`, kind: "done", title: `${num(p.peer)} ${fmt(start)}→${fmt(end)} · ${t("int.done")}`, start, end });
      } else if (p) {
        cells.push({ key: `h${start}`, kind: "inflight", title: `${num(p.peer)} ${fmt(start)}→${fmt(end)} · ${t("int.inflight")}`, peer: p.peer, start, end, received: false });
      } else {
        cells.push({ key: `h${start}`, kind: "todo", title: `${t("int.todo")} ${fmt(start)}`, start });
      }
    }
    return cells;
  });

  // Each task slice is drawn as a band spanning its [start, end) inside the
  // window; s.fill is how much of that band its owner has actually downloaded.
  const winSlices = $derived.by(() => {
    if (!dat) return [];
    const l = winLeft();
    const r = winRight();
    if (r <= l) return [];
    const active = dat.blockTasks.slices;
    const src: typeof active = [
      ...active,
      ...doneBlocks.map((x) => ({
        peer: x.peer,
        start: x.start,
        end: x.end,
        pct: 100,
        complete: true,
        inFlight: 0,
        syncNode: false,
        lastActiveAt: 0,
        assignedAt: 0,
      })),
    ].sort((a, b) => a.start - b.start);
    return src
      .map((s) => {
        const ls = Math.max(s.start, l);
        const rs = Math.min(s.end, r);
        const lf = ((ls - l) / (r - l)) * 100;
        const rf = ((rs - l) / (r - l)) * 100;
        if (rf <= lf) return null;
        return { ...s, l: lf, w: rf - lf };
      })
      .filter((s) => s !== null) as (typeof src[number] & { l: number; w: number })[];
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

  // Most recent block-slice assignment (mirrors headerTasks.lastReissueAt).
  function lastBlockAssign() {
    if (!dat) return 0;
    return dat.blockTasks.slices.reduce((m, s) => Math.max(m, s.assignedAt), 0);
  }

  // Header track zooms into the active download window.  The window is
  // derived purely from the node's real state: its right edge tracks the
  // download frontier (tip / next assignment / farthest in-flight range),
  // its left edge trails two slices behind the connected tip, and it slides
  // as the tip advances.  No simulated "settling" offset is applied.
  const hdrWindow = $derived.by(() => {
    if (!dat) return { l: 0, r: 1 };
    const sliceLen = dat.headerTasks.sliceLen || 2000;
    const peers = dat.headerTasks.hdrPeers;
    // Anchor the window to the ACTIVE band area only: the node's header_tip
    // races far ahead of the slices it hands out (the assigned ranges sit up
    // to a meg or more behind), so a tip/frontier-anchored window would show
    // mostly empty space with the bands crushed into a sliver.  Keep the
    // slice window plus a little lookahead/lookback on either side — just
    // enough to see the track advance.
    if (peers.length === 0) {
      const l = Math.max(0, dat.headerTip - sliceLen * 2);
      const r = Math.min(dat.headerBoundary, dat.headerTip + sliceLen * 6);
      return r > l ? { l, r } : { l, r: Math.max(1, dat.headerTip) };
    }
    const minStart = Math.min(...peers.map((p) => p.start));
    const maxEnd = Math.max(...peers.map((p) => p.end));
    const l = Math.max(0, minStart - sliceLen * 2);
    const r = Math.min(dat.headerBoundary, maxEnd + sliceLen * 4);
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
  // Stable color per peer, derived from the session-stable #N number so a
  // peer keeps its color across reconnects and new peers never reshuffle
  // existing colors (a sort-index map would drift when the peer set changes).
  // All surfaces share this: conveyor cells, lane bars, track bands, quality
  // cards — color = (num−1) % 8, same as the quality-card rows.
  const peerColor = $derived.by(() => {
    const m = new Map<string, number>();
    for (const [addr, n] of peerNumMap) m.set(addr, (n - 1) % 8);
    return m;
  });
  // Same stable-per-peer color scheme for the block-task slices, shared by the
  // window track bands and the per-IP progress bars so they always match.
  const blockPeerColor = $derived.by(() => {
    const m = new Map<string, number>();
    for (const [addr, n] of peerNumMap) m.set(addr, (n - 1) % 8);
    return m;
  });
  // Persistent per-IP number: #1, #2, ...  Assigned once when a peer is first
  // seen and kept for the whole session — a peer keeps its number across
  // reconnects, and a slot is never reused by another IP after a disconnect
  // (the node stays reserved until that peer reconnects).
  const peerNumMap = $state(new Map<string, number>());
  let nextPeerNum = $state(1);
  function assignPeerNums(ps: NodeInternals["peerStats"]) {
    for (const p of ps) {
      if (!peerNumMap.has(p.addr)) {
        peerNumMap.set(p.addr, nextPeerNum++);
      }
    }
  }
  const peerNumOf = (addr: string) => peerNumMap.get(addr);
  // Short lane label: "#N" by default, "addr" (or "#N addr") with details.
  const peerLabel = (addr: string) => {
    const n = peerNumOf(addr);
    return detail ? `#${n ?? "?"} ${addr}` : `#${n ?? "?"}`;
  };
  // Header download state is rendered from the node's real data only:
  // each peer's current [start,end) range (hdrLanes / hdrPeers) and the
  // received flag the node sets when the slice is fully downloaded.  The
  // old client-side "simulated completion" state machine (inferring a slice
  // completed from a peer's range changing between polls) has been removed:
  // it drove fake done/filling animations that never matched the node.

  // Conveyor of fixed-size task squares inside the track window:
  // gray = the next slices waiting to be handed out (frontier onwards).
  const hdrTodoBlocks = $derived.by(() => {
    if (!dat) return [];
    const l = hdrLeft();
    const r = hdrRight();
    const span = r - l;
    if (span <= 0) return [];
    const sliceLen = dat.headerTasks.sliceLen || 2000;
    const peers = dat.headerTasks.hdrPeers;
    // The real frontier (tip-led) usually sits far off-window to the right, so
    // anchor the lookahead squares to the band area's right edge instead of an
    // invisible line — the conveyor stays visible and slides as ranges advance.
    const todoStart =
      peers.length > 0
        ? Math.max(...peers.map((p) => p.end))
        : Math.max(dat.headerTip, dat.headerTasks.nextAssign);
    const blocks: { l: number; w: number; start: number }[] = [];
    for (let i = 0; i < 6; i++) {
      const start = todoStart + i * sliceLen;
      if (start >= r) break;
      const lf = ((Math.max(start, l) - l) / span) * 100;
      const rf = ((Math.min(start + sliceLen, r) - l) / span) * 100;
      if (rf <= lf) continue;
      blocks.push({ start, l: lf, w: rf - lf });
    }
    return blocks;
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
    const peers = dat.headerTasks.hdrPeers;
    // The tip-led frontier usually sits off-window; clamp the line to the band
    // area's right edge so it stays visible and slides with the assignment.
    const line = peers.length > 0 ? Math.max(...peers.map((p) => p.end)) : dat.headerTip;
    return hdrFrac(line);
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
    <span class="head-controls">
      <span class="live">
        <span class="dot" aria-hidden="true"></span> {refreshSec}s
      </span>
      <select class="refresh-select mono" aria-label="auto refresh interval" bind:value={refreshSec} onchange={restartTimer}>
        <option value={1}>1s</option>
        <option value={2}>2s</option>
        <option value={5}>5s</option>
        <option value={10}>10s</option>
        <option value={30}>30s</option>
        <option value={60}>60s</option>
      </select>
    </span>
  </div>

  {#if dat}
    <!-- per-peer quality (top of page, under the title): numbered by the
         persistent per-IP slot, sorted by number, always showing the full
         address so the #N mapping is self-explanatory -->
    <div class="card">
      <div class="card-head">
        <span class="h-card">{t("int.peers_quality")}</span>
        <span class="head-controls">
          <button class="chip" onclick={refreshStats}>{t("int.refresh")}</button>
        </span>
      </div>
      {#if peerStatsView.length === 0}
        <p class="dim" style="font-size:12px">—</p>
      {:else}
        <div class="peer-q">
          {#each [...peerStatsView].sort((a, b) => (peerNumOf(a.addr) ?? 9999) - (peerNumOf(b.addr) ?? 9999)) as p}
            <div class="peer-q-row" style:--pc={`var(--peer${((peerNumOf(p.addr) ?? 1) - 1) % 8 + 1})`}>
              <span class="mono peer-num" title={p.addr}>#{peerNumOf(p.addr) ?? "?"}</span>
              <span class="mono peer-ip" translate="no" title={p.addr}>{p.addr}{#if p.syncNode}<span class="lane-star" title="sync node"> ★</span>{/if}</span>
              <span class="mono" title={t("int.peer_uptime")}>{fmtUptime(p.connTime)}</span>
              <span class="mono" title={t("int.peer_recv")}>↓{fmtBytes(p.bytesRecv)}</span>
              <span class="mono" title={t("int.peer_sent")}>↑{fmtBytes(p.bytesSent)}</span>
              <span class="mono" title={t("int.peer_ping")}>{p.pingMs > 0 ? `${p.pingMs.toFixed(0)} ms` : "—"}</span>
              <span class="mono dim" title={t("int.peer_height")}>{p.currentHeight > 0 ? fmt(p.currentHeight) : "—"}</span>
              <span class="peer-q-actions">
                {#if banLeft(p.addr) > 0}
                  <span class="chip ban-left" title="disconnect until">⏳ {fmtBan(banLeft(p.addr))}</span>
                {:else}
                  <button class="chip ban" onclick={() => (banTarget = p.addr)}>{t("int.disconnect")}</button>
                {/if}
              </span>
            </div>
          {/each}
        </div>
      {/if}
    </div>

    <!-- tasks -->
    <div class="two">
      <div class="card">
        <div class="card-head">
          <span class="h-card">{t("int.tasks_blocks")}</span>
          <span class="chip mono" translate="no">{t("int.window_size", { n: fmt(dat.windowSize) })}</span>
        </div>
        <div class="task-total">
          <span class="mono" translate="no">{fmt(dat.blockTasks.synced)} / {fmt(dat.headerTip)}</span>
          <div class="total-bar" aria-hidden="true">
            <span style:width={`${taskPct()}%`}></span>
          </div>
          <span class="mono">{taskPct().toFixed(1)}%</span>
        </div>
        <div class="win-track" role="img" aria-label={t("int.window_status")}>
          <span class="ws-tip" style:left={`${winFrac(dat.chainTip)}%`} aria-hidden="true"></span>
          {#each winSlices as s (s.peer + ":" + s.start)}
            <span
              class="ws"
              class:accent={s.syncNode}
              style:--pc={`var(--peer${((blockPeerColor.get(s.peer) ?? 0) % 8) + 1})`}
              style:left={`${s.l}%`}
              style:width={`${s.w}%`}
              title={`#{peerNumOf(s.peer) ?? "?"} ${fmt(s.start)}→${fmt(s.end)}${s.inFlight > 0 ? ` · ${s.inFlight} in-flight` : ""}`}
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
        <div class="legend-row">
          <p class="legend mono" aria-hidden="true">▓ downloaded  ░ in-flight</p>
          <button
            class="btn btn-ghost se"
            class:on={detail}
            title={t("int.trace_hint")}
            onclick={() => {
              detail = !detail;
              poll();
            }}
          >{t("int.details")}</button>
        </div>
        <div class="block-lanes">
          {#each dat.blockTasks.slices as s}
            <div class="lane" style:--pc={`var(--peer${((blockPeerColor.get(s.peer) ?? 0) % 8) + 1})`}>
              <span class="lane-name mono" title={`#{peerNumOf(s.peer) ?? "?"} ${s.peer}`}>
                <span class="ln-num">#{peerNumOf(s.peer) ?? "?"}</span>{#if s.syncNode}<span class="lane-star" title="sync node"> ★</span>{/if}
              </span>
              <div class="lane-bar" aria-hidden="true">
                <span class="lane-done hi" style:transform={`scaleX(${Math.max(0, Math.min(1, s.pct / 100)).toFixed(3)})`}></span>
                {#if s.inFlight > 0}<span class="lane-inflight" style:width={`${Math.min(100 - s.pct, 12)}%`} style:left={`${s.pct}%`}></span>{/if}
              </div>
              <span class="lane-vals mono" translate="no">{fmt(s.start)}→{fmt(s.end)}</span>
              {#if detail}
                <div class="lane-meta">
                  <span class="ln-ip mono" translate="no">{s.peer}</span>
                  <span class="lane-time mono" title="in-flight / last block at">{s.inFlight}↕ · {s.lastActiveAt ? fmtAgo(s.lastActiveAt) : "—"}</span>
                </div>
              {/if}
            </div>
          {/each}
        </div>
        <dl class="kv">
          <div><dt>{t("int.hdr_window_start")}</dt><dd class="mono" translate="no">{fmt(winLeft())} → {fmt(winRight())}</dd></div>
          <div><dt>{t("int.requested_blocks")}</dt><dd class="mono" translate="no">{fmt(dat.blockTasks.total)}</dd></div>
          <div><dt>{t("int.last_reissue")}</dt><dd class="mono">{lastBlockAssign() > 0 ? fmtAgo(lastBlockAssign()) : "—"}</dd></div>
        </dl>
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
          <div class="hdr-conveyor">
            {#each hdrConveyor as c (c.key)}
              <span
                class="hdr-cell {c.kind}"
                class:got={c.kind === "inflight" && c.received}
                animate:flip={{ duration: 450 }}
                transition:fade={{ duration: 250 }}
                style:--pc={c.kind === "inflight" ? `var(--peer${((peerColor.get(c.peer) ?? 0) % 8) + 1})` : undefined}
                title={c.title}
              >
                {#if c.kind === "inflight" && !c.received}<span class="hdr-scan" aria-hidden="true"></span>{/if}
              </span>
            {/each}
          </div>
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
                <span class="lane-name mono" title={`#{peerNumOf(l.peer) ?? "?"} ${l.peer}`} translate="no">
                  <span class="ln-num">#{peerNumOf(l.peer) ?? "?"}</span>
                </span>
                <div class="lane-bar" aria-hidden="true">
                  {#key `${l.peer}:${l.start}:${l.received}`}
                    <span
                      class="lane-done hi hdr-lane-fill"
                      class:done={l.received}
                      style:--pc={`var(--peer${((peerColor.get(l.peer) ?? 0) % 8) + 1})`}
                      title={`${fmt(l.start)}→${fmt(l.end)} · ${l.received ? t("int.done") : t("int.inflight")} ${l.pct.toFixed(0)}%`}
                    ></span>
                  {/key}
                </div>
                <span class="lane-vals mono" translate="no">{fmt(l.start)}→{fmt(l.end)}</span>
                {#if detail}
                  <div class="lane-meta">
                    <span class="ln-ip mono" translate="no">{l.peer}</span>
                  </div>
                {/if}
              </div>
            {/each}
          </div>
        {/if}
        <dl class="kv">
          <div><dt>{t("int.hdr_window_start")}</dt><dd class="mono" translate="no">{fmt(hdrLeft())} → {fmt(hdrRight())}</dd></div>
          <div><dt>{t("int.requested_headers")}</dt><dd class="mono" translate="no">{fmt(Math.max(0, dat.headerBoundary - dat.headerTip))}</dd></div>
          <div><dt>{t("int.last_reissue")}</dt><dd class="mono">{fmtAgo(dat.headerTasks.lastReissueAt)}</dd></div>
        </dl>
      </div>
    </div>

    <!-- speed + mem -->
    <div class="mem-zone">
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

      <div class="card">
        <div class="card-head"><span class="h-card">{t("int.memory")}</span></div>
        <dl class="kv">
          <div><dt>{t("int.prefetch")}</dt><dd class="mono" translate="no">{fmt(dat.mem.window)} bl</dd></div>
          <div><dt>{t("int.to_go")}</dt><dd class="mono" translate="no">{fmt(dat.mem.gap)} bl</dd></div>
          <div><dt>{t("int.inflight")}</dt><dd class="mono" translate="no">{fmt(dat.mem.inflight)} bl</dd></div>
        </dl>
        <div class="mem-bar" aria-hidden="true"><span style:width={`${dat.mem.gap > 0 ? Math.min(100, (dat.mem.window / dat.mem.gap) * 100) : 100}%`}></span></div>
      </div>
    </div>

    <!-- average traffic / recent download speed (low-frequency refresh) -->
    <div class="card">
      <div class="card-head">
        <span class="h-card">{t("int.traffic")}</span>
        <button class="chip" onclick={refreshStats}>{t("int.refresh")}</button>
      </div>
      <dl class="kv">
        <div><dt>{t("int.avg_down")}</dt><dd class="mono" translate="no">{avgRate.down.toFixed(1)} KB/s</dd></div>
        <div><dt>{t("int.avg_up")}</dt><dd class="mono" translate="no">{avgRate.up.toFixed(1)} KB/s</dd></div>
        <div><dt>{t("int.sample_win")}</dt><dd class="mono" translate="no">{traffic.length} · {Math.min(2, trafficWindowMs / 60_000).toFixed(0)} min</dd></div>
      </dl>
    </div>
  {/if}

  <!-- disconnect confirm dialog -->
  {#if banTarget}
    <div class="ban-overlay" role="dialog" aria-modal="true" aria-label={t("int.disconnect")}>
      <div class="ban-modal">
        <div class="ban-modal-head">
          <span class="h-card">{t("int.disconnect")}</span>
          <span class="ban-target mono" translate="no">{banTarget}</span>
        </div>
        <div class="ban-options">
          {#each banOptions as o}
            <label class="ban-opt" class:sel={banChoice === o.ms}>
              <input type="radio" name="ban-dur" value={o.ms} bind:group={banChoice} />
              <span>{o.label}</span>
            </label>
          {/each}
        </div>
        <div class="ban-actions">
          <button class="chip" onclick={cancelDisconnect}>{t("int.cancel")}</button>
          <button class="chip ban" onclick={confirmDisconnect}>{t("int.confirm")}</button>
        </div>
      </div>
    </div>
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
  .head-controls {
    display: flex;
    align-items: center;
    gap: 8px;
  }
  .refresh-select {
    font-size: 12px;
    padding: 2px 6px;
    border-radius: 6px;
    border: 1px solid var(--line);
    background: var(--ink);
    color: var(--ink-fg);
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
    transition:
      left 0.5s cubic-bezier(0.77, 0, 0.175, 1),
      width 0.5s cubic-bezier(0.77, 0, 0.175, 1),
      background 0.4s,
      border-color 0.4s;
  }
  .ws.accent {
    box-shadow: 0 0 0 1px var(--pc);
  }
  .ws-done {
    position: absolute;
    inset: 0 auto 0 0;
    width: 100%;
    transform-origin: left;
    background: var(--pc);
    border-radius: 4px;
    transition: transform 0.4s linear;
  }
  .ws-flight {
    position: absolute;
    top: 0;
    bottom: 0;
    width: 2px;
    background: #fff;
    box-shadow: 0 0 4px rgba(0, 0, 0, 0.35);
    animation: ws-flight 1.2s ease-in-out infinite alternate;
  }
  @keyframes ws-flight {
    from {
      transform: translateX(0);
      opacity: 0.9;
    }
    to {
      transform: translateX(28px);
      opacity: 0.4;
    }
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

  /* Header conveyor: fixed-size 20px cells — green done pile (left, ≤8),
     colorless → peer-color inflight cells (middle, ≤8), dashed todo cells
     (right, 6).  flip() animates the left shift when a slice completes. */
  .hdr-conveyor {
    display: flex;
    align-items: stretch;
    gap: 2px;
    padding: 6px 8px;
    height: 100%;
    min-width: 0;
    overflow: hidden;
  }
  .hdr-cell {
    position: relative;
    flex: 1 1 0;
    min-width: 0;
    border-radius: 5px;
    transition: background 0.4s, border-color 0.4s, box-shadow 0.4s;
  }
  .hdr-cell.done {
    background: linear-gradient(180deg, var(--hdr-pile), color-mix(in srgb, var(--hdr-pile) 75%, #0f5c33));
    border: 1px solid color-mix(in srgb, var(--hdr-pile) 65%, #000);
    box-shadow: inset 0 1px 0 rgba(255, 255, 255, 0.25);
  }
  .hdr-cell.inflight {
    background: color-mix(in srgb, var(--pc) 16%, transparent);
    border: 1px solid color-mix(in srgb, var(--pc) 55%, transparent);
  }
  .hdr-cell.inflight.got {
    background: var(--pc);
    border: 1px solid color-mix(in srgb, var(--pc) 60%, #000);
    box-shadow: 0 0 6px color-mix(in srgb, var(--pc) 45%, transparent);
    /* "Light-up" sweep: plays whenever a cell turns completed, INCLUDING the
       very first render when several slices already arrived received=true on
       refresh — a plain transition would not fire for a fresh element.  The
       scale pop + glow pulse make the completion obvious even when every
       other cell is already dark. */
    animation: hdr-got 0.6s ease-out both;
  }
  @keyframes hdr-got {
    0% {
      background: color-mix(in srgb, var(--pc) 16%, transparent);
      transform: scale(0.8);
      box-shadow: none;
    }
    55% {
      transform: scale(1.14);
      box-shadow: 0 0 12px color-mix(in srgb, var(--pc) 80%, transparent);
    }
    100% {
      background: var(--pc);
      transform: scale(1);
      box-shadow: 0 0 6px color-mix(in srgb, var(--pc) 45%, transparent);
    }
  }
  .hdr-cell.todo {
    background: repeating-linear-gradient(45deg, #e4e4e4, #e4e4e4 5px, #ededed 5px, #ededed 10px);
    border: 1px dashed #c6c6c6;
  }
  .hdr-cell.empty {
    border: 1px dashed #d8d8d8;
    opacity: 0.55;
  }
  .hdr-scan {
    position: absolute;
    top: 3px;
    bottom: 3px;
    left: 3px;
    width: 2px;
    background: #fff;
    box-shadow: 0 0 4px rgba(0, 0, 0, 0.35);
    animation: hdr-scan 1.2s ease-in-out infinite alternate;
  }
  @keyframes hdr-scan {
    from {
      transform: translateX(0);
      opacity: 0.9;
    }
    to {
      transform: translateX(12px);
      opacity: 0.35;
    }
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
    grid-template-columns: auto 1fr 150px;
    align-items: center;
    gap: 6px 10px;
    padding: 6px 0;
    font-size: 12px;
  }
  .hdr-lanes {
    margin-top: 10px;
    border-top: 1px solid var(--line);
  }
  .block-lanes {
    margin-top: 10px;
    border-top: 1px solid var(--line);
    padding-top: 6px;
  }
  .block-lanes .lane {
    padding: 9px 0;
  }
  .lane-name {
    color: var(--ink-dim);
    overflow: hidden;
    text-overflow: ellipsis;
    white-space: nowrap;
  }
  /* In detail mode the IP address is dropped onto its own line spanning the
     full lane width, so it never stretches the grid row that holds the bar. */
  .lane-name .ln-num {
    font-weight: 600;
  }
  .ln-ip {
    font-size: 10px;
    opacity: 0.6;
    line-height: 1.2;
    white-space: nowrap;
    overflow: hidden;
    text-overflow: ellipsis;
  }
  /* Detail mode second row: IP on the left, in-flight/last-active on the
     right, both on the line below the progress bar. */
  .lane-meta {
    grid-column: 1 / -1;
    display: flex;
    align-items: baseline;
    justify-content: space-between;
    gap: 10px;
    margin-top: 2px;
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
    width: 100%;
    transform-origin: left;
    background: linear-gradient(180deg, var(--straw), color-mix(in srgb, var(--straw) 75%, #c07f00));
    border-radius: 8px;
    transition: transform 0.4s linear;
  }
  .lane-done.hi {
    background: linear-gradient(180deg, var(--pc), color-mix(in srgb, var(--pc) 70%, #000));
  }
  .lane-done.hi.hdr-lane-fill:not(.done) {
    transform: scaleX(0.06);
    min-width: 3px;
    opacity: 0.55;
  }
  .lane-done.hi.hdr-lane-fill.done {
    background: linear-gradient(180deg, var(--pc), color-mix(in srgb, var(--pc) 55%, #000));
    animation: hdr-fill 0.5s ease-out both;
    box-shadow: 0 0 6px color-mix(in srgb, var(--pc) 55%, transparent);
  }
  @keyframes hdr-fill {
    from {
      transform: scaleX(0);
    }
    to {
      transform: scaleX(1);
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
    margin: 0;
    font-size: 11px;
    color: var(--ink-dim);
  }
  .legend-row {
    display: flex;
    align-items: center;
    justify-content: space-between;
    gap: 8px;
    margin-top: 8px;
  }
  .btn.se {
    padding: 4px 10px;
    min-height: 28px;
    font-size: 12px;
    font-weight: 500;
  }
  .btn.se.on {
    border-color: var(--mint);
    color: var(--mint);
    background: rgba(3, 152, 118, 0.08);
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

  .mem-zone {
    display: grid;
    grid-template-columns: 1fr 1fr;
    gap: 14px;
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

  .peer-q {
    margin: 14px 0 0;
    display: grid;
    gap: 6px;
  }
  .peer-q-row {
    display: grid;
    grid-template-columns: 0.5fr 1.7fr 0.8fr 1fr 1fr 1fr 1fr 0.9fr;
    gap: 8px;
    align-items: center;
    padding: 6px 8px;
    border-radius: 8px;
    background: color-mix(in srgb, var(--peer1, #888) 6%, transparent);
  }
  .peer-q-row .peer-num {
    font-weight: 700;
    text-align: left;
  }
  .peer-q-row .peer-ip {
    font-weight: 400;
    opacity: 0.85;
    font-size: 11px;
    text-align: left;
  }
  .peer-q-row .mono {
    font-size: 12px;
    text-align: right;
  }
  .peer-q-actions {
    display: flex;
    justify-content: flex-end;
  }
  .chip.ban {
    border-color: #d97706;
    color: #d97706;
  }
  .chip.ban:hover {
    background: #d97706;
    color: #fff;
  }
  .chip.ban-left {
    border-color: #7a7a7a;
    color: #7a7a7a;
    cursor: default;
  }
  .ban-overlay {
    position: fixed;
    inset: 0;
    z-index: 60;
    display: flex;
    align-items: center;
    justify-content: center;
    background: rgba(0, 0, 0, 0.45);
  }
  .ban-modal {
    min-width: 280px;
    padding: 16px 18px;
    border-radius: 12px;
    border: 1px solid var(--line);
    background: var(--ink);
    box-shadow: 0 12px 32px rgba(0, 0, 0, 0.35);
  }
  .ban-modal-head {
    display: flex;
    align-items: baseline;
    justify-content: space-between;
    gap: 12px;
    margin-bottom: 12px;
  }
  .ban-target {
    font-size: 12px;
    opacity: 0.8;
  }
  .ban-options {
    display: grid;
    grid-template-columns: repeat(5, 1fr);
    gap: 6px;
    margin-bottom: 14px;
  }
  .ban-opt {
    display: flex;
    align-items: center;
    justify-content: center;
    padding: 6px 4px;
    border-radius: 8px;
    border: 1px solid var(--line);
    cursor: pointer;
    font-size: 12px;
    color: var(--ink-fg);
  }
  .ban-opt.sel {
    border-color: #d97706;
    color: #d97706;
    font-weight: 700;
  }
  .ban-opt input {
    position: absolute;
    opacity: 0;
    pointer-events: none;
  }
  .ban-actions {
    display: flex;
    justify-content: flex-end;
    gap: 8px;
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

  /* The header-track conveyor is a load-bearing status animation; under
     prefers-reduced-motion keep the same gentle durations but stop the
     decorative flight sweeps (movement removed, state colors retained). */
  @media (prefers-reduced-motion: reduce) {
    .hdr-scan,
    .ws-flight {
      animation: none !important;
    }
    .lane-done {
      transition-duration: 0.4s !important;
    }
    .lane-done.hi.hdr-lane-fill.done {
      animation-duration: 0.5s !important;
    }
  }
</style>