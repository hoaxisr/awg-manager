<!--
  Источник дизайна: singbox-router/project/screens/EmptyState.jsx (Hero section)
-->

<script lang="ts">
  import { m } from '$lib/i18n';
  import { Shield } from 'lucide-svelte';
</script>

<section class="hero">
  <div class="text">
    <div class="status-row">
      <span class="status-dot"></span>
      <span class="status-label">{m.sb_router_empty_hero_status()}</span>
    </div>
    <h2 class="heading">{m.sb_router_empty_hero_heading()}</h2>
    <p class="body">
      {m.sb_router_empty_hero_body()}
    </p>
  </div>
  <div class="icon-wrap">
    <Shield size={64} />
  </div>
</section>

<style>
  .hero {
    margin-bottom: 24px;
    padding: 24px 28px;
    background: linear-gradient(180deg, color-mix(in srgb, var(--accent) 8%, var(--bg-secondary)) 0%, var(--bg-secondary) 100%);
    border: 1px solid var(--border);
    border-radius: var(--radius);
    display: grid;
    grid-template-columns: 1fr auto;
    gap: 20px;
    align-items: center;
  }
  @media (max-width: 768px) {
    .hero {
      grid-template-columns: 1fr;
    }
    .icon-wrap {
      justify-self: center;
    }
  }
  .status-row {
    display: inline-flex;
    align-items: center;
    gap: 8px;
    margin-bottom: 8px;
  }
  .status-dot {
    width: 8px;
    height: 8px;
    border-radius: 50%;
    background: var(--text-muted);
  }
  .status-label {
    font-size: 11px;
    font-weight: 600;
    text-transform: uppercase;
    letter-spacing: 0.05em;
    color: var(--text-muted);
  }
  .heading {
    margin: 0 0 8px;
    font-size: 22px;
    font-weight: 600;
    color: var(--text-primary);
  }
  .body {
    margin: 0;
    font-size: 14px;
    color: var(--text-secondary);
    max-width: 540px;
    line-height: 1.55;
  }
  .icon-wrap {
    width: 140px;
    height: 140px;
    border-radius: 16px;
    background: var(--bg-primary);
    border: 1px solid var(--border);
    display: flex;
    align-items: center;
    justify-content: center;
    color: var(--accent);
  }
</style>
