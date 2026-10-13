<script lang="ts">
  import { SideDrawer, Button } from '$lib/components/ui';
  import { api } from '$lib/api/client';
  import { copyToClipboard } from '$lib/utils/clipboard';
  import { notifications } from '$lib/stores/notifications';

  interface Props {
    open: boolean;
    onClose: () => void;
  }

  let { open, onClose }: Props = $props();

  let yaml = $state<string>('');
  let loading = $state(false);
  let error = $state<string | null>(null);
  let copied = $state(false);
  let lastLoadedFor = $state(false);

  async function load() {
    loading = true;
    error = null;
    try {
      const res = await api.mihomoConfig();
      yaml = res.yaml;
    } catch (e) {
      error = e instanceof Error ? e.message : String(e);
      yaml = '';
    } finally {
      loading = false;
    }
  }

  async function onCopy() {
    if (!yaml) return;
    const ok = await copyToClipboard(yaml);
    if (ok) {
      copied = true;
      notifications.success('YAML скопирован в буфер обмена');
      setTimeout(() => {
        copied = false;
      }, 1500);
    } else {
      notifications.error('Не удалось скопировать');
    }
  }

  $effect(() => {
    if (open && !lastLoadedFor) {
      lastLoadedFor = true;
      load();
    } else if (!open && lastLoadedFor) {
      lastLoadedFor = false;
    }
  });
</script>

<SideDrawer {open} {onClose} title="Конфигурация Mihomo (YAML)" width={720}>
  <div class="content">
    <div class="toolbar">
      <span class="file-path">/opt/etc/awg-manager/mihomo/config.yaml</span>
      <div style="flex:1"></div>
      <Button variant="secondary" size="sm" onclick={load} disabled={loading}>
        {loading ? 'Загрузка…' : 'Обновить'}
      </Button>
      <Button
        variant="secondary"
        size="sm"
        onclick={onCopy}
        disabled={loading || !yaml}
      >
        {copied ? 'Скопировано' : 'Копировать'}
      </Button>
    </div>

    <div class="state">
      {#if loading && !yaml}
        <div class="placeholder">Загрузка конфига…</div>
      {:else if error}
        <div class="error">
          <div class="error-title">Не удалось загрузить конфиг</div>
          <div class="error-message">{error}</div>
        </div>
      {:else}
        <pre class="yaml"><code>{yaml}</code></pre>
      {/if}
    </div>
  </div>
</SideDrawer>

<style>
  .content {
    display: flex;
    flex-direction: column;
    gap: 0.75rem;
    height: 100%;
    min-height: 0;
  }

  .toolbar {
    display: flex;
    align-items: center;
    gap: 0.5rem;
    flex-wrap: wrap;
  }

  .file-path {
    font-size: 11px;
    font-family: var(--font-mono, monospace);
    color: var(--color-text-secondary, #9ba1a6);
  }

  .state {
    flex: 1;
    min-height: 0;
    display: flex;
    flex-direction: column;
  }

  .placeholder {
    padding: 1.5rem;
    text-align: center;
    color: var(--color-text-secondary);
    font-size: 13px;
    border: 1px dashed var(--color-border);
    border-radius: var(--radius-sm);
  }

  .error {
    padding: 0.875rem 1rem;
    border: 1px solid var(--color-error);
    background: color-mix(in srgb, var(--color-error) 12%, transparent);
    border-radius: var(--radius-sm);
    color: var(--color-text-primary);
  }

  .error-title {
    font-weight: 600;
    font-size: 13px;
    margin-bottom: 0.25rem;
  }

  .error-message {
    font-size: 12px;
    color: var(--color-text-secondary);
    word-break: break-word;
  }

  .yaml {
    flex: 1;
    margin: 0;
    padding: 0.875rem 1rem;
    background: var(--color-bg-primary, var(--bg-primary, #1e1e24));
    border: 1px solid var(--color-border, var(--border, #2e2e38));
    border-radius: var(--radius-sm);
    font-family: ui-monospace, SFMono-Regular, Menlo, Monaco, Consolas, 'Liberation Mono', monospace;
    font-size: 12px;
    line-height: 1.5;
    color: var(--color-text-primary, var(--text-primary, #ffffff));
    overflow: auto;
    white-space: pre;
    tab-size: 2;
  }
</style>
