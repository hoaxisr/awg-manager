<script lang="ts">
  import { api } from '$lib/api/client';
  import { notifications } from '$lib/stores/notifications';
  import { Button } from '$lib/components/ui';
  import { AlertTriangle, RotateCcw, RefreshCw, Download, Info, ChevronDown, ChevronUp, Sparkles, CheckCircle2 } from 'lucide-svelte';

  interface Props {
    onRestored?: () => void;
  }

  let { onRestored }: Props = $props();

  let reconciling = $state(false);
  let activeAction = $state<'rollback_to_lkg' | 'regenerate_from_desired' | null>(null);
  let downloading = $state(false);
  let showFaq = $state(false);

  async function handleReconcile(action: 'rollback_to_lkg' | 'regenerate_from_desired') {
    if (reconciling) return;
    reconciling = true;
    activeAction = action;
    try {
      await api.mihomoReconcile(action, true);
      const actionLabel = action === 'rollback_to_lkg' ? 'Откат к рабочей копии (LKG)' : 'Пересборка конфигурации';
      notifications.success(`${actionLabel} выполнен успешно`);
      onRestored?.();
    } catch (e) {
      notifications.error(e instanceof Error ? e.message : String(e));
    } finally {
      reconciling = false;
      activeAction = null;
    }
  }

  async function handleDownloadEvidence() {
    if (downloading) return;
    downloading = true;
    try {
      const blob = await api.mihomoRecoveryEvidence();
      const url = window.URL.createObjectURL(blob);
      const a = document.createElement('a');
      a.href = url;
      a.download = `mihomo-recovery-evidence-${new Date().toISOString().replace(/[:.]/g, '-')}.json`;
      document.body.appendChild(a);
      a.click();
      document.body.removeChild(a);
      window.URL.revokeObjectURL(url);
      notifications.success('Отчёт об аварии сохранён');
    } catch (e) {
      notifications.error(e instanceof Error ? e.message : String(e));
    } finally {
      downloading = false;
    }
  }
</script>

<div class="mihomo-recovery-banner" role="alert">
  <!-- Header -->
  <div class="banner-header">
    <div class="icon-wrap">
      <AlertTriangle size={22} class="alert-icon" />
    </div>
    <div class="banner-text">
      <h4>Требуется восстановление конфигурации Mihomo</h4>
      <p>
        Произошел сбой при сохранении настроек или после обновления пакета. Система перешла в безопасный режим (Recovery Mode), чтобы предотвратить потерю сетевого доступа. Выберите подходящий вариант восстановления:
      </p>
    </div>
  </div>

  <!-- Action Decision Cards -->
  <div class="decision-cards">
    <!-- Card 1: Regenerate from desired (Recommended) -->
    <div class="decision-card primary-card">
      <div class="card-head">
        <div class="card-title-row">
          <RefreshCw size={16} class="card-head-icon text-accent" />
          <span class="card-title">Пересобрать из настроек</span>
        </div>
        <span class="badge badge-accent">Рекомендуется</span>
      </div>
      <p class="card-desc">
        Заново скомпилирует конфигурацию из ваших текущих правил и пересоздаст сетевые интерфейсы роутера. <strong>Ваши добавленные сервисы и правила сохранятся.</strong>
      </p>
      <div class="card-meta">
        <span class="meta-label">Когда жать:</span> после обновления пакета, перезагрузки роутера или если сбой был временным.
      </div>
      <div class="card-action">
        <Button
          variant="secondary"
          size="sm"
          onclick={() => handleReconcile('regenerate_from_desired')}
          disabled={reconciling}
          fullWidth
        >
          <RefreshCw size={14} class={reconciling && activeAction === 'regenerate_from_desired' ? 'spin' : ''} />
          {reconciling && activeAction === 'regenerate_from_desired' ? 'Пересборка…' : 'Пересобрать конфигурацию'}
        </Button>
      </div>
    </div>

    <!-- Card 2: Rollback to LKG -->
    <div class="decision-card danger-card">
      <div class="card-head">
        <div class="card-title-row">
          <RotateCcw size={16} class="card-head-icon text-danger" />
          <span class="card-title">Откатить к рабочей копии</span>
        </div>
        <span class="badge badge-warning">При сбое настроек</span>
      </div>
      <p class="card-desc">
        Отменит последние изменения и вернет роутер к предыдущей гарантированно рабочей точке (Last Known Good), когда всё работало без сбоев.
      </p>
      <div class="card-meta">
        <span class="meta-label">Когда жать:</span> если вы вручную изменили правила или узел, после чего пропал интернет.
      </div>
      <div class="card-action">
        <Button
          variant="danger"
          size="sm"
          onclick={() => handleReconcile('rollback_to_lkg')}
          disabled={reconciling}
          fullWidth
        >
          <RotateCcw size={14} class={reconciling && activeAction === 'rollback_to_lkg' ? 'spin' : ''} />
          {reconciling && activeAction === 'rollback_to_lkg' ? 'Откат…' : 'Откатить к LKG'}
        </Button>
      </div>
    </div>
  </div>

  <!-- Explanatory Accordion -->
  <div class="faq-container">
    <button
      type="button"
      class="faq-toggle"
      onclick={() => (showFaq = !showFaq)}
      aria-expanded={showFaq}
    >
      <Info size={14} class="faq-icon" />
      <span>Что такое LKG и безопасный режим? Пояснение для пользователя</span>
      {#if showFaq}
        <ChevronUp size={14} class="faq-chevron" />
      {:else}
        <ChevronDown size={14} class="faq-chevron" />
      {/if}
    </button>

    {#if showFaq}
      <div class="faq-body">
        <div class="faq-item">
          <div class="faq-item-title">
            <CheckCircle2 size={14} class="text-accent inline-icon" />
            Что такое LKG (Last Known Good)?
          </div>
          <p>
            Это автоматическая <strong>«точка восстановления»</strong> маршрутизации. Каждый раз, когда Mihomo успешно применяет настройки и все порты работают штатно, система сохраняет проверенный снимок (правила, прокси и мосты). Если в будущем при редактировании возникнет ошибка, вы всегда сможете мгновенно вернуться к рабочему состоянию.
          </p>
        </div>
        <div class="faq-item">
          <div class="faq-item-title">
            <AlertTriangle size={14} class="text-danger inline-icon" />
            Почему добавление правил сейчас заблокировано?
          </div>
          <p>
            Чтобы роутер не ушел в циклический сбой, сторож транзакций блокирует изменение правил, пока текущий сбой не устранен. Как только вы нажмете <em>«Пересобрать конфигурацию»</em> или <em>«Откатить к LKG»</em>, блокировка немедленно снимется.
          </p>
        </div>
        <div class="faq-item">
          <div class="faq-item-title">
            <Sparkles size={14} class="text-accent inline-icon" />
            Что делать, если ошибка повторяется?
          </div>
          <p>
            Вы можете скачать технический отчет об аварии с помощью кнопки ниже или открыть <strong>ИИ-помощник</strong> в правом верхнем углу — он умеет автоматически диагностировать туннели и исправлять маршрутизацию роутера.
          </p>
        </div>
      </div>
    {/if}
  </div>

  <!-- Utility Footer -->
  <div class="banner-footer">
    <Button
      variant="ghost"
      size="sm"
      onclick={handleDownloadEvidence}
      disabled={downloading}
    >
      <Download size={14} class={downloading ? 'spin' : ''} />
      {downloading ? 'Сохранение…' : 'Скачать отчёт об аварии (Evidence)'}
    </Button>
  </div>
</div>

<style>
  .mihomo-recovery-banner {
    background: color-mix(in srgb, var(--danger, #ef4444) 7%, var(--bg-secondary, #1e293b));
    border: 1px solid color-mix(in srgb, var(--danger, #ef4444) 40%, var(--border, rgba(255, 255, 255, 0.1)));
    border-radius: var(--radius-lg, 12px);
    padding: 18px 22px;
    margin-bottom: 22px;
    display: flex;
    flex-direction: column;
    gap: 16px;
    box-shadow: 0 4px 20px rgba(0, 0, 0, 0.25), 0 0 15px rgba(239, 68, 68, 0.1);
  }

  .banner-header {
    display: flex;
    align-items: flex-start;
    gap: 14px;
  }

  .icon-wrap {
    flex-shrink: 0;
    margin-top: 2px;
    background: rgba(239, 68, 68, 0.15);
    border-radius: 8px;
    padding: 6px;
    display: flex;
    align-items: center;
    justify-content: center;
  }

  :global(.alert-icon) {
    color: var(--danger, #ef4444);
  }

  .banner-text h4 {
    margin: 0 0 6px 0;
    font-size: 16px;
    font-weight: 600;
    color: var(--text-primary, #f8fafc);
  }

  .banner-text p {
    margin: 0;
    font-size: 13.5px;
    line-height: 1.5;
    color: var(--text-secondary, #94a3b8);
  }

  /* Decision Cards */
  .decision-cards {
    display: grid;
    grid-template-columns: repeat(auto-fit, minmax(280px, 1fr));
    gap: 14px;
    margin-top: 2px;
  }

  .decision-card {
    background: var(--bg-tertiary, #0f172a);
    border: 1px solid var(--border, rgba(255, 255, 255, 0.08));
    border-radius: 10px;
    padding: 14px 16px;
    display: flex;
    flex-direction: column;
    gap: 10px;
    transition: border-color 0.2s, box-shadow 0.2s;
  }

  .decision-card:hover {
    border-color: rgba(255, 255, 255, 0.18);
    box-shadow: 0 4px 12px rgba(0, 0, 0, 0.2);
  }

  .primary-card {
    border-color: color-mix(in srgb, var(--accent, #38bdf8) 30%, transparent);
    background: color-mix(in srgb, var(--accent, #38bdf8) 4%, var(--bg-tertiary, #0f172a));
  }

  .danger-card {
    border-color: color-mix(in srgb, var(--danger, #ef4444) 30%, transparent);
    background: color-mix(in srgb, var(--danger, #ef4444) 4%, var(--bg-tertiary, #0f172a));
  }

  .card-head {
    display: flex;
    align-items: center;
    justify-content: space-between;
    gap: 8px;
  }

  .card-title-row {
    display: flex;
    align-items: center;
    gap: 8px;
  }

  :global(.card-head-icon) {
    flex-shrink: 0;
  }

  .card-title {
    font-size: 14px;
    font-weight: 600;
    color: var(--text-primary, #f8fafc);
  }

  .badge {
    font-size: 11px;
    font-weight: 500;
    padding: 2px 7px;
    border-radius: 9999px;
  }

  .badge-accent {
    background: color-mix(in srgb, var(--accent, #38bdf8) 18%, transparent);
    color: var(--accent, #38bdf8);
    border: 1px solid color-mix(in srgb, var(--accent, #38bdf8) 35%, transparent);
  }

  .badge-warning {
    background: color-mix(in srgb, var(--warning, #f59e0b) 18%, transparent);
    color: var(--warning, #f59e0b);
    border: 1px solid color-mix(in srgb, var(--warning, #f59e0b) 35%, transparent);
  }

  .card-desc {
    margin: 0;
    font-size: 12.5px;
    line-height: 1.45;
    color: var(--text-secondary, #94a3b8);
  }

  .card-desc strong {
    color: var(--text-primary, #f8fafc);
  }

  .card-meta {
    font-size: 11.5px;
    line-height: 1.4;
    color: var(--text-muted, #64748b);
    margin-top: auto;
    padding-top: 4px;
  }

  .meta-label {
    color: var(--text-secondary, #94a3b8);
    font-weight: 500;
  }

  .card-action {
    margin-top: 6px;
  }

  /* FAQ Accordion */
  .faq-container {
    border-top: 1px solid rgba(255, 255, 255, 0.08);
    padding-top: 10px;
  }

  .faq-toggle {
    display: flex;
    align-items: center;
    gap: 8px;
    width: 100%;
    background: transparent;
    border: none;
    padding: 6px 0;
    color: var(--text-secondary, #94a3b8);
    font-size: 12.5px;
    cursor: pointer;
    text-align: left;
    transition: color 0.15s;
  }

  .faq-toggle:hover {
    color: var(--text-primary, #f8fafc);
  }

  :global(.faq-icon) {
    color: var(--accent, #38bdf8);
    flex-shrink: 0;
  }

  :global(.faq-chevron) {
    margin-left: auto;
    flex-shrink: 0;
  }

  .faq-body {
    display: flex;
    flex-direction: column;
    gap: 12px;
    margin-top: 8px;
    padding: 12px 16px;
    background: rgba(0, 0, 0, 0.2);
    border-radius: 8px;
    border: 1px solid rgba(255, 255, 255, 0.05);
  }

  .faq-item-title {
    display: flex;
    align-items: center;
    gap: 6px;
    font-size: 12.5px;
    font-weight: 600;
    color: var(--text-primary, #f8fafc);
    margin-bottom: 3px;
  }

  :global(.inline-icon) {
    flex-shrink: 0;
  }

  .faq-item p {
    margin: 0;
    font-size: 12px;
    line-height: 1.45;
    color: var(--text-secondary, #94a3b8);
  }

  .faq-item p em,
  .faq-item p strong {
    color: var(--text-primary, #f8fafc);
  }

  /* Footer */
  .banner-footer {
    display: flex;
    align-items: center;
    justify-content: flex-end;
    padding-top: 4px;
  }

  :global(.spin) {
    animation: spin 1s linear infinite;
  }

  @keyframes spin {
    from { transform: rotate(0deg); }
    to { transform: rotate(360deg); }
  }
</style>
