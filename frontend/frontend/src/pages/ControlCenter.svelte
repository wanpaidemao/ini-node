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

  async function poll() {
    const p = await Services.getIndexProgress()
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

  onMount(() => {
    poll()
    const id = setInterval(poll, 5000)
    return () => clearInterval(id)
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

  <!-- Start/Stop (placeholder) / 启停(占位) -->
  <div class="card" style="border:1px solid var(--c-border);border-radius:10px;padding:14px">
    <div style="display:flex;gap:8px">
      <button class="btn btn-primary" disabled style="flex:1">Start / 启动</button>
      <button class="btn btn-ghost" disabled style="flex:1">Stop / 停止</button>
    </div>
    <p class="dim" style="font-size:11px;margin-top:8px">
      Start/stop wiring lands with BackendHost (P1). / 启停接入待 BackendHost(P1) 实现。
    </p>
  </div>
</div>
