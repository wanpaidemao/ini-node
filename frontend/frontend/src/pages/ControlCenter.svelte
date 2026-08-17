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
  let paramsOpen = false    // collapsed by default / 默认折叠
  let iniPath = ''          // current ini file path / 当前 ini 文件路径
  let iniContent = ''       // raw ini text (shown on demand) / ini 原始文本（按需显示）
  async function loadParams() {
    try {
      const res = await fetch('/api/node-config')
      if (res.ok) {
        const d = await res.json()
        params = d
        iniPath = d.iniPath || ''
      }
    } catch { /* ignore */ }
  }
  async function viewIni() {
    iniContent = ''
    try {
      const res = await fetch('/api/node-config?raw=1&path=' + encodeURIComponent(iniPath))
      const d = await res.json()
      if (d.ok) iniContent = d.content || ''
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

  // Logs / 日志（默认不显示内容，只显示等级；点击查看才加载）
  let logLines: string[] = []
  let logLevel = 'info'
  let logsOpen = false
  async function loadLogs() {
    try {
      const res = await fetch('/api/logs?lines=100')
      if (res.ok) {
        const d = await res.json()
        logLines = d.lines || []
      }
    } catch { /* ignore */ }
  }
  async function loadLogLevel() {
    try {
      const res = await fetch('/api/loglevel')
      const d = await res.json()
      if (d.ok) logLevel = d.level || 'info'
    } catch { /* ignore */ }
  }
  async function applyLogLevel() {
    try {
      await fetch('/api/loglevel', {
        method: 'POST',
        headers: { 'content-type': 'application/json' },
        body: JSON.stringify({ level: logLevel }),
      })
    } catch { /* ignore */ }
  }
  function toggleLogs() {
    logsOpen = !logsOpen
    if (logsOpen) loadLogs()
  }

  // DB params (tuned values, no rebuild) / 数据库参数（调优值，不重建）
  let dbParams: Record<string, string> = {}
  async function loadDBParams() {
    try {
      const res = await fetch('/api/db-params')
      if (res.ok) dbParams = await res.json()
    } catch { /* ignore */ }
  }

  // Wallet API (walletapi gateway 8335) / 钱包网关
  let wapiRunning = false
  let wapiBusy = false
  let wapiErr = ''
  async function pollWapi() {
    try {
      const res = await fetch('/api/walletapi-status')
      const d = await res.json()
      wapiRunning = !!(d.ok && d.running)
    } catch { wapiRunning = false }
  }
  async function startWapi() {
    wapiBusy = true
    wapiErr = ''
    try {
      const res = await fetch('/api/walletapi-start')
      const d = await res.json().catch(() => ({}))
      if (!d.ok) wapiErr = d.error || 'Start failed / 启动失败'
      setTimeout(pollWapi, 3000)
    } catch { wapiErr = 'Network error / 网络错误' }
    wapiBusy = false
  }
  async function stopWapi() {
    wapiBusy = true
    try {
      await fetch('/api/walletapi-stop')
      setTimeout(pollWapi, 3000)
    } catch { /* ignore */ }
    wapiBusy = false
  }

  onMount(() => {
    poll()
    pollWapi()
    loadParams()
    loadLogLevel()
    loadDBParams()
    const id = setInterval(poll, 5000)
    const wapiId = setInterval(pollWapi, 5000)
    return () => { clearInterval(id); clearInterval(wapiId) }
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
      <button class="btn btn-primary" onclick={startNode} disabled={busy} style="flex:1">Start / 启动</button>
      <button class="btn btn-ghost" onclick={stopNode} disabled={busy} style="flex:1">Stop / 停止</button>
    </div>
    {#if startErr}
      <p style="font-size:12px;margin-top:8px;color:#b91c1c">{startErr}</p>
    {/if}
  </div>

  <!-- Wallet API / 钱包网关 -->
  <div class="card" style="border:1px solid var(--c-border);border-radius:10px;padding:14px">
    <div style="display:flex;justify-content:space-between;align-items:center;margin-bottom:10px">
      <span style="font-weight:600">Wallet API / 钱包网关</span>
      <span class="badge" class:badge-green={wapiRunning} style="font-size:11px">
        {wapiRunning ? 'Running / 运行中' : 'Stopped / 已停止'}
      </span>
    </div>
    <div style="display:flex;gap:8px">
      <button class="btn btn-primary" onclick={startWapi} disabled={wapiBusy} style="flex:1">Start / 启动</button>
      <button class="btn btn-ghost" onclick={stopWapi} disabled={wapiBusy} style="flex:1">Stop / 停止</button>
    </div>
    {#if wapiErr}
      <p style="font-size:12px;margin-top:8px;color:#b91c1c">{wapiErr}</p>
    {/if}
  </div>

  <!-- Startup params / 启动参数 -->
  <div class="card" style="border:1px solid var(--c-border);border-radius:10px;padding:14px">
    <div style="display:flex;justify-content:space-between;align-items:center;cursor:pointer" onclick={() => paramsOpen = !paramsOpen}>
      <span style="font-weight:600">Startup params / 启动参数</span>
      <span class="dim" style="font-size:12px">{paramsOpen ? '▾ 收起' : '▸ 展开'}</span>
    </div>
    {#if paramsOpen}
      <div class="col" style="gap:8px;margin-top:10px">
        <label style="font-size:12px">
          <span class="dim" style="display:block">ini file / ini 文件</span>
          <div class="row" style="gap:6px">
            <input class="input mono" style="flex:1" bind:value={iniPath} placeholder="path/to/btcd-runtime.ini" />
            <button class="btn btn-ghost" onclick={viewIni} style="font-size:12px">View / 查看</button>
          </div>
        </label>
        {#if iniContent}
          <pre class="mono" style="max-height:160px;overflow:auto;font-size:11px;background:var(--c-border);border-radius:6px;padding:8px;white-space:pre-wrap">{iniContent}</pre>
        {/if}
        <div class="col" style="gap:6px">
          {#each Object.entries(params) as [k, v]}
            {#if k !== 'iniPath' && k !== 'rpcEndpoint' && k !== 'credFromIni'}
              <label style="font-size:12px">
                <span class="dim" style="display:block">{k}</span>
                <input class="input mono" style="width:100%" bind:value={params[k]} />
              </label>
            {/if}
          {/each}
        </div>
        <button class="btn btn-primary" onclick={saveParams} disabled={busy} style="font-size:12px">Save / 保存</button>
      </div>
    {/if}
    <p class="dim" style="font-size:11px;margin-top:8px">
      txindex/sugarindex/addcheckpoint changes require a full rebuild (double confirm).
      <br />txindex/sugarindex/addcheckpoint 变更需全量重建（双重确认）。
    </p>
  </div>

  <!-- Logs / 日志 -->
  <div class="card" style="border:1px solid var(--c-border);border-radius:10px;padding:14px">
    <div style="display:flex;justify-content:space-between;align-items:center;margin-bottom:10px">
      <span style="font-weight:600">Logs / 日志</span>
      <div style="display:flex;gap:6px;align-items:center">
        <select class="select" bind:value={logLevel} style="font-size:12px">
          <option value="trace">trace</option>
          <option value="debug">debug</option>
          <option value="info">info</option>
          <option value="warn">warn</option>
          <option value="error">error</option>
        </select>
        <button class="btn btn-ghost" onclick={applyLogLevel} style="font-size:12px">Apply / 应用</button>
        <button class="btn btn-ghost" onclick={toggleLogs} style="font-size:12px">{logsOpen ? 'Hide / 收起' : 'View / 查看'}</button>
      </div>
    </div>
    {#if logsOpen}
      <pre class="mono" style="max-height:220px;overflow:auto;font-size:11px;background:var(--c-border);border-radius:6px;padding:8px;white-space:pre-wrap">{logLines.join('\n') || 'No log output / 暂无日志'}</pre>
    {/if}
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
