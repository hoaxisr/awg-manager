<!--
  Источник дизайна: singbox-router/project/screens/EmptyState.jsx (Hero section)
-->

<script lang="ts">
  import { Shield } from 'lucide-svelte';

  interface Props {
    isMihomo?: boolean;
  }
  let { isMihomo = false }: Props = $props();
</script>

<section class="hero">
  <div class="text">
    <div class="status-row">
      <span class="status-dot"></span>
      <span class="status-label">Не настроен</span>
    </div>
    <h2 class="heading">
      {isMihomo
        ? 'Направляйте трафик через прокси-группы и VPN-туннели по вашим правилам'
        : 'Направляйте трафик через VPN-туннели по вашим правилам'}
    </h2>
    <p class="body">
      {#if isMihomo}
        Mihomo — это маршрутизатор внутри роутера. Решает для каждого запроса:
        пустить напрямую, через группу прокси / туннель или заблокировать.
        Настройка занимает 3 шага.
      {:else}
        sing-box — это маршрутизатор внутри роутера. Решает для каждого запроса:
        пустить напрямую, через туннель или заблокировать.
        Настройка занимает 3 шага.
      {/if}
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
    font-weight: 700;
    text-transform: uppercase;
    letter-spacing: 0.08em;
    color: var(--text-muted);
  }
  .heading {
    margin: 0 0 10px;
    font-size: 22px;
    font-weight: 700;
    line-height: 1.25;
    color: var(--text-primary);
  }
  .body {
    margin: 0;
    font-size: 14px;
    color: var(--text-secondary);
    line-height: 1.5;
  }
  .icon-wrap {
    color: var(--accent);
    opacity: 0.85;
    display: flex;
    align-items: center;
    justify-content: center;
  }
</style>
