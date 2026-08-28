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

  async function stopNode(force = false) {
    busy = true
    try {
      await fetch(force ? '/api/node-stop-force' : '/api/node-stop')
      setTimeout(poll, 3000)
    } catch { /* ignore */ }
    busy = false
  }

  // Startup params / 启动参数
  let params: Record<string, string> = {}
  let paramsOpen = false    // collapsed by default / 默认折叠
  // Level-C params require a full rebuild: double confirm (module scope so
  // the template can show the hint per field).
  // 级别 C 参数变更需全量重建:双重确认(模块级作用域,供模板按字段显示提示)。
  const levelC = ['txindex', 'sugarindex', 'addcheckpoint']
  let dbParamsOpen = false  // DB params collapsed by default / 数据库参数默认折叠
  let iniPath = ''          // current ini file path / 当前 ini 文件路径
  let iniContent = ''       // raw ini text (shown on demand) / ini 原始文本（按需显示）
  async function loadParams() {
    try {
      const res = await fetch('/api/node-config')
      if (res.ok) {
        const d = await res.json()
        params = d
        iniPath = d.iniPath || ''
        logRedirect = d.logredirect === '1'
      }
    } catch { /* ignore */ }
  }
  // Redirect node stdout to node.stdout.log (ini logredirect=1).  Off by
  // default: btcd already writes its own rotated btcd.log, so capturing
  // stdout is opt-in.  / 是否把节点 stdout 重定向到 node.stdout.log
  // (ini logredirect=1)。默认关闭:btcd 自身已写轮转 btcd.log,捕获 stdout
  // 为可选行为。
  let logRedirect = false
  async function saveLogRedirect() {
    try {
      await fetch('/api/node-config', {
        method: 'POST',
        headers: { 'content-type': 'application/json' },
        body: JSON.stringify({ logredirect: logRedirect ? '1' : '0' }),
      })
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
  // btclog reports levels as abbreviations (TRC/DBG/INF/WRN/ERR/CRT) even in
  // the lowercase ini value; map every variant to the <select> option names so
  // the current level shows while the node is running.
  // btclog 返回缩写级别(TRC/DBG/INF/WRN/ERR/CRT),统一映射为下拉框的小写全称,
  // 保证节点运行中也能显示当前级别。
  const LEVEL_ALIASES: Record<string, string> = {
    trc: 'trace', trace: 'trace',
    dbg: 'debug', debug: 'debug',
    inf: 'info', info: 'info',
    wrn: 'warn', warn: 'warn',
    err: 'error', error: 'error',
    crt: 'critical', critical: 'critical',
    off: 'off',
  }
  function normaliseLevel(l: string): string {
    // Preserve unmapped values verbatim (lowercased) instead of defaulting to
    // "info": btcd supports "off", and coercing it to "info" would mislabel
    // the current level and silently flip the node level if Apply is clicked.
    // 未映射的级别(如 off)原样保留(仅小写化),不要默认成 info,否则会错误
    // 显示级别,且点击 Apply 会把节点级别从 off 悄悄改成 info。
    return LEVEL_ALIASES[(l || '').trim().toLowerCase()] || (l || '').trim().toLowerCase()
  }
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
      if (d.ok) logLevel = normaliseLevel(d.level || 'info')
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
  // Logs modal: View opens a dialog with a clear-logs button / 日志弹窗:
  // View 打开对话框,包含清空日志按钮。
  let logsModalOpen = false
  function openLogs() {
    logsModalOpen = true
    loadLogs()
  }
  async function clearLogs() {
    // Truncate the log file on the backend too, otherwise reopening the
    // viewer reloads the old lines from disk. / 同时清空后端日志文件,
    // 否则重新打开查看器时会从磁盘重新加载旧日志。
    try {
      await fetch('/api/logs', { method: 'POST' })
    } catch { /* ignore */ }
    logLines = []
  }
  function closeLogs() {
    logsModalOpen = false
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
      <button class="btn btn-ghost" onclick={() => stopNode(false)} disabled={busy} style="flex:1"
        title="Graceful stop via RPC: flushes the DB, no UTXO rebuild on next start / 通过 RPC 优雅停止:会 flush 数据库,下次启动无需重建 UTXO">Graceful stop / 优雅结束节点</button>
      <button class="btn btn-danger force-btn" onclick={() => stopNode(true)} disabled={busy}
        style="width:40px;flex:none;display:inline-flex;align-items:center;justify-content:center;padding:6px 0"
        title="Force kill: does NOT flush the DB, next start rebuilds UTXO (emergency only) / 强制结束:不 flush 数据库,下次启动重建 UTXO(仅应急)"
        aria-label="Force stop / 强制结束">
        <svg viewBox="0 0 16 16" width="14" height="14" fill="none" stroke="currentColor" stroke-width="2" stroke-linecap="round" aria-hidden="true">
          <line x1="8" y1="1.5" x2="8" y2="7" />
          <path d="M4.2 3.8a6 6 0 1 0 7.6 0" />
        </svg>
      </button>
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
                {#if levelC.includes(k)}
                  <span class="dim" style="display:block;font-size:11px;color:#d97706;margin-top:2px">
                    Requires a full rebuild (double confirm). / 变更需全量重建（双重确认）。
                  </span>
                {/if}
              </label>
            {/if}
          {/each}
        </div>
        <button class="btn btn-primary" onclick={saveParams} disabled={busy} style="font-size:12px">Save / 保存</button>
      </div>
    {/if}
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
          <option value="critical">critical</option>
          <option value="off">off</option>
        </select>
        <button class="btn btn-ghost" onclick={applyLogLevel} style="font-size:12px">Apply / 应用</button>
        <button class="btn btn-ghost" onclick={openLogs} style="font-size:12px">View / 查看</button>
      </div>
    </div>
    <label style="display:flex;align-items:center;gap:6px;font-size:12px;cursor:pointer">
      <input type="checkbox" bind:checked={logRedirect} onchange={saveLogRedirect} />
      <span>Redirect node stdout to node.stdout.log / 重定向 stdout 到 node.stdout.log</span>
    </label>
  </div>

  {#if logsModalOpen}
    <div
      style="position:fixed;inset:0;background:rgba(0,0,0,.5);display:flex;align-items:center;justify-content:center;z-index:100"
      onclick={closeLogs}
    >
      <div
        style="background:var(--ink);border:1px solid var(--line);border-radius:10px;padding:14px;max-width:680px;width:90%;max-height:72vh;display:flex;flex-direction:column"
        onclick={(e) => e.stopPropagation()}
      >
        <div style="display:flex;justify-content:space-between;align-items:center;margin-bottom:8px">
          <span style="font-weight:600">Logs / 日志</span>
          <button class="btn btn-ghost" onclick={closeLogs} style="font-size:12px">Close / 关闭</button>
        </div>
        <pre class="mono" style="flex:1;min-height:0;overflow:auto;font-size:11px;background:var(--c-border);border-radius:6px;padding:8px;white-space:pre-wrap">{logLines.join('\n') || 'No log output / 暂无日志'}</pre>
        <div style="display:flex;justify-content:flex-end;gap:6px;margin-top:8px">
          <button class="btn btn-danger" onclick={clearLogs} style="font-size:12px">Clear logs / 清空日志</button>
        </div>
      </div>
    </div>
  {/if}

  <!-- DB params / 数据库参数 -->
  <div class="card" style="border:1px solid var(--c-border);border-radius:10px;padding:14px">
    <div style="display:flex;justify-content:space-between;align-items:center;cursor:pointer" onclick={() => dbParamsOpen = !dbParamsOpen}>
      <span style="font-weight:600">DB params / 数据库参数</span>
      <span class="dim" style="font-size:12px">{dbParamsOpen ? '▾ 收起' : '▸ 展开'}</span>
    </div>
    {#if dbParamsOpen}
      <div class="col" style="gap:6px;margin-top:10px">
        {#each Object.entries(dbParams) as [k, v]}
          <div class="row between" style="font-size:12px">
            <span class="dim">{k}</span>
            <span class="mono">{v}</span>
          </div>
        {/each}
      </div>
    {/if}
  </div>
</div>
