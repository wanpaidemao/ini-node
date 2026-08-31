<script lang="ts">
  import { t } from "../lib/i18n";
  import { navigate } from "../lib/store.svelte";
  import { explorerSettings, setExplorerSettings } from "../lib/explorer-settings.svelte";

  let recent = $state(explorerSettings.recentBlocks);
  let saved = $state(false);

  function save() {
    setExplorerSettings({ recentBlocks: recent });
    recent = explorerSettings.recentBlocks;
    saved = true;
    setTimeout(() => (saved = false), 1500);
  }
</script>

<section class="expset">
  <div class="head">
    <div>
      <p class="eyebrow">chain · explorer</p>
      <h1 class="h-page">{t("exp.set.title")}</h1>
    </div>
  </div>

  <div class="card">
    <label class="field">
      <span class="field-label">{t("exp.set.recent_blocks")}</span>
      <input type="number" min="1" max="100" step="1" bind:value={recent} />
    </label>

    <div class="actions">
      <button class="btn btn-primary" onclick={save}>{saved ? "✓" : t("g.save")}</button>
      <button class="btn btn-ghost" onclick={() => navigate("explorer")}>{t("exp.back_chain")}</button>
    </div>
  </div>
</section>

<style>
  .expset {
    display: flex;
    flex-direction: column;
    gap: 16px;
    max-width: 560px;
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
    .expset {
      animation: none;
    }
  }
  .h-page {
    font-family: var(--font-display);
    font-size: 24px;
    margin: 2px 0 0;
  }
  .card {
    display: flex;
    flex-direction: column;
    gap: 14px;
    padding: 16px;
  }
  .field {
    display: flex;
    flex-direction: column;
    gap: 6px;
  }
  .field-label {
    font-size: 12px;
    color: var(--ink-dim);
  }
  .field input {
    max-width: 160px;
  }
  .actions {
    display: flex;
    gap: 8px;
  }
</style>