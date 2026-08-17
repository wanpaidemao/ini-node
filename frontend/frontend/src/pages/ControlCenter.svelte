<script lang="ts">
  // Control center (minimal skeleton). Shows backend status, index progress
  // and start/stop placeholders. Intended as the non-RPC landing page when
  // the node/RPC is not running; polish comes later via the frontend-design
  // skill.
  // 控制中心(最简骨架):展示后端状态、索引进度与启停占位。设计为节点/RPC 未
  // 运行时展示的非 RPC 页面;后续用前端设计 skill 打磨。
  import { onMount } from 'svelte'
  import { Services } from '../lib/services.js'

  type Progress = { height: number; total: number; percent: number; phase?: string }

  let progress: Progress | null = null
  let phase = 'unknown'
  let eta = ''
  let last: { height: number; t: number } | null = null
  let running = false   // node RPC reachable? / 节点 RPC 是否可达
  let busy = false      // start/stop in flight / 启停进行中
  let startErr = ''     // last start failure / 最近一次启动错误

  async function poll() {
    const p = await Services.getIndexProgress()
    running = p !== null
    if (!p) return
    progress = p
    phase = (p as any).phase || 'index'
    if (last && p.height > last.height) {
      const rate = (p.height - last.height) / ((Date.now() - last.t) / 1000)
      if (rate > 0) {
        const remain = Math.round((p.total - p.height) / rate / 60)
        eta = remain > 0 ? `~${remain} min` : '<1 min'
      }
    }
    last = { height: p.height, t: Date.now() }
  }

  async function startNode() {
    busy = true
    startErr = ''
    try {
      const res = await fetch('/api/node-start')
      const d = await res.json().catch(() => ({}))
      if (!d.ok) startErr = d.error || 'Start failed / 启动失败'
      setTimeout(poll, 3000)
    } catch {
      startErr = 'Network error / 网络错误'
    }
    busy = false
  }

  async function stopNode() {
    busy = true
    try {
      await fetch('/api/node-stop')
      setTimeout(poll, 3000)
    } catch { /* ignore */ }
    busy = false
  }

  // Startup params / 启动参数
  let params: Record<string, string> = {}
  async function loadParams() {
    try {
      const res = await fetch('/api/node-config')
      if (res.ok) params = await res.json()
    } catch { /* ignore */ }
  }
  async function saveParams() {
    // Level-C params require a full rebuild: double confirm.
    // 级别 C 参数变更需全量重建：双重确认。
    const levelC = ['txindex', 'sugarindex', 'addcheckpoint']
    const dirty = levelC.filter(k => params[k] !== undefined)
    if (dirty.length > 0) {
      const ok = window.confirm(
        'Rebuild required for: ' + dirty.join(', ') +
        '. Type REBUILD to confirm. / 以下参数变更需重建数据库：' +
        dirty.join(', ') + '。输入 REBUILD 确认。')
      if (!ok) return
    }
    busy = true
    try {
      await fetch('/api/node-config', {
        method: 'POST',
        headers: { 'content-type': 'application/json' },
        body: JSON.stringify(params),
      })
    } catch { /* ignore */ }
    busy = false
  }

  // Logs / 日志
  let logLines: string[] = []
  async function loadLogs() {
    try {
      const res = await fetch('/api/logs?lines=100')
      if (res.ok) {
        const d = await res.json()
        logLines = d.lines || []
      }
    } catch { /* ignore */ }
  }
  async function clearLogs() {
    try {
      await fetch('/api/logs', { method: 'POST' })
      logLines = []
    } catch { /* ignore */ }
  }

  // DB params (tuned values, no rebuild) / 数据库参数（调优值，不重建）
  let dbParams: Record<string, string> = {}
  async function loadDBParams() {
    try {
      const res = await fetch('/api/db-params')
      if (res.ok) dbParams = await res.json()
    } catch { /* ignore */ }
  }

  onMount(() => {
    poll()
    loadParams()
    loadLogs()
    loadDBParams()
    const id = setInterval(poll, 5000)
    const logId = setInterval(loadLogs, 5000)
    return () => { clearInterval(id); clearInterval(logId) }
  })

  const phaseLabel: Record<string, string> = {
    load: 'Loading block index / 加载区块索引',
    index: 'Rebuilding sugar index / 重建索引',
    sync: 'Syncing blocks / 同步区块',
    unknown: 'Waiting / 等待中',
  }
</script>

<div class="page" style="max-width:760px;margin:0 auto;padding:24px">
  <h1 style="font-size:20px;font-weight:700;margin-bottom:4px">Control Center / 控制中心</h1>
  <p class="dim" style="font-size:12px;margin-bottom:16px">
    Node backend management. Shown when RPC is unavailable.
    <br />后端管理页面,节点/RPC 未运行时展示。
  </p>

  <!-- Status / 状态 -->
  <div class="card" style="border:1px solid var(--c-border);border-radius:10px;padding:14px;margin-bottom:12px">
    <div style="display:flex;justify-content:space-between;align-items:center">
      <span style="font-weight:600">Backend status / 后端状态</span>
      <span class="badge" style="font-size:11px">
        {phaseLabel[phase] || phaseLabel.unknown}
      </span>
    </div>
    {#if progress}
      <div style="margin-top:10px">
        <div style="height:8px;background:var(--c-border);border-radius:4px;overflow:hidden">
          <div style="height:100%;width:{Math.min(progress.percent,100)}%;background:#3b82f6;border-radius:4px"></div>
        </div>
        <div style="display:flex;justify-content:space-between;font-size:11px;margin-top:4px" class="dim">
          <span>{progress.height.toLocaleString()} / {progress.total.toLocaleString()}</span>
          <span>{progress.percent.toFixed(1)}% {eta ? `· ETA ${eta}` : ''}</span>
        </div>
      </div>
    {:else}
      <p class="dim" style="font-size:12px;margin-top:8px">No progress yet / 暂无进度</p>
    {/if}
  </div>

  <!-- Start/Stop / 启停 -->
  <div class="card" style="border:1px solid var(--c-border);border-radius:10px;padding:14px">
    <div style="display:flex;justify-content:space-between;align-items:center;margin-bottom:10px">
      <span style="font-weight:600">Node / 节点</span>
      <span class="badge" class:badge-green={running} style="font-size:11px">
        {running ? 'Running / 运行中' : 'Stopped / 已停止'}
      </span>
    </div>
    <div style="display:flex;gap:8px">
      <button class="btn btn-primary" on:click={startNode} disabled={busy} style="flex:1">Start / 启动</button>
      <button class="btn btn-ghost" on:click={stopNode} disabled={busy} style="flex:1">Stop / 停止</button>
    </div>
    {#if startErr}
      <p style="font-size:12px;margin-top:8px;color:#b91c1c">{startErr}</p>
    {/if}
  </div>

  <!-- Startup params / 启动参数 -->
  <div class="card" style="border:1px solid var(--c-border);border-radius:10px;padding:14px">
    <div style="display:flex;justify-content:space-between;align-items:center;margin-bottom:10px">
      <span style="font-weight:600">Startup params / 启动参数</span>
      <button class="btn btn-primary" on:click={saveParams} disabled={busy} style="font-size:12px">Save / 保存</button>
    </div>
    <div class="col" style="gap:8px">
      {#each Object.entries(params) as [k, v]}
        <label style="font-size:12px">
          <span class="dim" style="display:block">{k}</span>
          <input class="input mono" style="width:100%" bind:value={params[k]} />
        </label>
      {/each}
    </div>
    <p class="dim" style="font-size:11px;margin-top:8px">
      txindex/sugarindex/addcheckpoint changes require a full rebuild (double confirm).
      <br />txindex/sugarindex/addcheckpoint 变更需全量重建（双重确认）。
    </p>
  </div>

  <!-- Logs / 日志 -->
  <div class="card" style="border:1px solid var(--c-border);border-radius:10px;padding:14px">
    <div style="display:flex;justify-content:space-between;align-items:center;margin-bottom:10px">
      <span style="font-weight:600">Logs / 日志</span>
      <div style="display:flex;gap:6px">
        <button class="btn btn-ghost" on:click={loadLogs} style="font-size:12px">Refresh / 刷新</button>
        <button class="btn btn-ghost" on:click={clearLogs} style="font-size:12px">Clear / 清空</button>
      </div>
    </div>
    <pre class="mono" style="max-height:220px;overflow:auto;font-size:11px;background:var(--c-border);border-radius:6px;padding:8px;white-space:pre-wrap">{logLines.join('\n') || 'No log output / 暂无日志'}</pre>
  </div>

  <!-- DB params / 数据库参数 -->
  <div class="card" style="border:1px solid var(--c-border);border-radius:10px;padding:14px">
    <span style="font-weight:600">DB params / 数据库参数</span>
    <div class="col" style="gap:6px;margin-top:10px">
      {#each Object.entries(dbParams) as [k, v]}
        <div class="row between" style="font-size:12px">
          <span class="dim">{k}</span>
          <span class="mono">{v}</span>
        </div>
      {/each}
    </div>
    <p class="dim" style="font-size:11px;margin-top:8px">
      Tuned values — changes do NOT rebuild the index (take effect on restart).
      <br />调优后的当前值——修改不会重建索引（重启后生效）。
    </p>
  </div>
</div>
