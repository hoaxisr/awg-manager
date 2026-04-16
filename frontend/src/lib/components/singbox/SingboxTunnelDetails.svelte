<script lang="ts">
  import type { SingboxTunnel } from '$lib/types';

  interface Props {
    tunnel: SingboxTunnel;
  }

  let { tunnel }: Props = $props();
  let showEndpoint = $state(false);
</script>

<div class="details">
  <div class="row">
    <div class="field">
      <span class="label">Сервер</span>
      <span class="value">{showEndpoint ? tunnel.server : '•••••••••'}</span>
      <button class="eye" onclick={() => (showEndpoint = !showEndpoint)}>
        {showEndpoint ? '🙈' : '👁'}
      </button>
    </div>
    <div class="field">
      <span class="label">Порт</span>
      <span class="value">{tunnel.port}</span>
    </div>
  </div>

  {#if tunnel.protocol === 'vless'}
    <div class="row">
      {#if tunnel.sni}
        <div class="field"><span class="label">SNI</span><span class="value">{tunnel.sni}</span></div>
      {/if}
      {#if tunnel.fingerprint}
        <div class="field"><span class="label">Fingerprint</span><span class="value">{tunnel.fingerprint}</span></div>
      {/if}
    </div>
  {:else if tunnel.protocol === 'naive'}
    <div class="row">
      <div class="field"><span class="label">Пользователь</span><span class="value">{tunnel.username || '—'}</span></div>
    </div>
  {/if}
</div>

<style>
  .details { display: flex; flex-direction: column; gap: 12px; }
  .row { display: flex; gap: 16px; }
  .field { display: flex; flex-direction: column; gap: 2px; min-width: 0; }
  .label { font-size: 11px; text-transform: uppercase; color: var(--text-muted); letter-spacing: 0.05em; }
  .value { font-size: 13px; font-family: var(--font-mono, monospace); color: var(--text-secondary); }
  .eye { background: none; border: none; cursor: pointer; font-size: 12px; margin-left: 4px; }
</style>
