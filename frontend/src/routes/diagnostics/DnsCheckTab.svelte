<script lang="ts">
    import { api } from '$lib/api/client';
    import { notifications } from '$lib/stores/notifications';
    import type { DnsCheckResult } from '$lib/types';

    function defaultChecks(): DnsCheckResult[] {
        return [
            { id: 'tunnel_running', status: 'pending', title: 'Туннель работает', message: 'Проверка состояния VPN-интерфейса' },
            { id: 'dns_routes', status: 'pending', title: 'DNS-маршруты настроены', message: 'Есть ли активные правила маршрутизации по доменам' },
            { id: 'dns_probe', status: 'pending', title: 'Устройство использует DNS роутера', message: 'Ваши DNS-запросы проходят через роутер' },
            { id: 'client_policy', status: 'pending', title: 'Политика доступа', message: 'Устройство в стандартной политике маршрутизации' },
            { id: 'dns_encryption', status: 'pending', title: 'Шифрование DNS', message: 'DNS-over-TLS или DNS-over-HTTPS настроен на роутере' },
        ];
    }

    let pageState = $state<'idle' | 'running' | 'done'>('idle');
    let checks = $state<DnsCheckResult[]>(defaultChecks());
    let clientIP = $state('');
    let hostname = $state('');

    async function runCheck() {
        pageState = 'running';
        checks = defaultChecks();

        try {
            // Step 1: start — runs checks 1,2,4,5 server-side
            const start = await api.startDnsCheck();
            clientIP = start.clientIP;
            hostname = start.hostname;

            // Update server-side checks
            checks = checks.map(c => {
                const serverCheck = start.checks.find(sc => sc.id === c.id);
                return serverCheck || c;
            });

            // Step 2: DNS probe — client-side fetch to test hostname
            let dnsReached = false;
            if (start.token) {
                try {
                    const probeUrl = `http://awgm-dnscheck-${start.token}.test:${start.port}/api/dns-check/probe/${start.token}`;
                    await fetch(probeUrl, { signal: AbortSignal.timeout(3000) });
                    dnsReached = true;
                } catch {
                    dnsReached = false;
                }
            }

            // Step 3: complete — finalize check 3
            const result = await api.completeDnsCheck(start.token, dnsReached);
            checks = result.checks;
        } catch {
            notifications.error('Ошибка запуска диагностики');
        } finally {
            pageState = 'done';
        }
    }
</script>

<div class="dns-check-card">
    <div class="dns-check-header">
        <div class="dns-check-header-text">
            <h2 class="dns-check-title">Проверка DNS-маршрутизации</h2>
            <p class="dns-check-desc">Диагностика определяет, правильно ли настроена маршрутизация DNS-запросов через VPN-туннель для вашего устройства.</p>
        </div>
        <button
            class="dns-check-btn"
            class:dns-check-btn-running={pageState === 'running'}
            onclick={runCheck}
            disabled={pageState === 'running'}
        >
            {#if pageState === 'running'}
                <span class="btn-spinner"></span>
                Проверка...
            {:else if pageState === 'done'}
                Проверить снова
            {:else}
                Проверить
            {/if}
        </button>
    </div>

    {#if pageState !== 'idle'}
        <div class="client-info">
            <svg xmlns="http://www.w3.org/2000/svg" width="16" height="16" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2" stroke-linecap="round" stroke-linejoin="round">
                <rect x="2" y="3" width="20" height="14" rx="2" ry="2"></rect>
                <line x1="8" y1="21" x2="16" y2="21"></line>
                <line x1="12" y1="17" x2="12" y2="21"></line>
            </svg>
            <span>Ваше устройство: <strong>{clientIP || '...'}</strong>{hostname ? ` (${hostname})` : ''}</span>
        </div>
    {/if}

    <div class="check-list">
        {#each checks as check}
            {@const firstPendingIndex = checks.findIndex(c => c.status === 'pending')}
            {@const isActiveSpinner = pageState === 'running' && checks.indexOf(check) === firstPendingIndex}
            <div class="check-item">
                <div class="check-icon check-icon-{check.status}">
                    {#if check.status === 'ok'}
                        <svg xmlns="http://www.w3.org/2000/svg" width="14" height="14" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="3" stroke-linecap="round" stroke-linejoin="round">
                            <polyline points="20 6 9 17 4 12"></polyline>
                        </svg>
                    {:else if check.status === 'fail'}
                        <svg xmlns="http://www.w3.org/2000/svg" width="14" height="14" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="3" stroke-linecap="round" stroke-linejoin="round">
                            <line x1="18" y1="6" x2="6" y2="18"></line>
                            <line x1="6" y1="6" x2="18" y2="18"></line>
                        </svg>
                    {:else if check.status === 'warning'}
                        <svg xmlns="http://www.w3.org/2000/svg" width="14" height="14" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="3" stroke-linecap="round" stroke-linejoin="round">
                            <line x1="12" y1="9" x2="12" y2="13"></line>
                            <line x1="12" y1="17" x2="12.01" y2="17"></line>
                        </svg>
                    {:else if isActiveSpinner}
                        <div class="spinner"></div>
                    {:else}
                        <svg xmlns="http://www.w3.org/2000/svg" width="14" height="14" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2">
                            <circle cx="12" cy="12" r="10"></circle>
                        </svg>
                    {/if}
                </div>
                <div class="check-info">
                    <div class="check-title">{check.title}</div>
                    <div class="check-desc">{check.message}</div>
                    {#if check.detail}
                        <span class="check-detail">{check.detail}</span>
                    {/if}
                </div>
                {#if check.status === 'ok'}
                    <span class="check-status status-ok">OK</span>
                {:else if check.status === 'fail'}
                    <span class="check-status status-fail">Ошибка</span>
                {:else if check.status === 'warning'}
                    <span class="check-status status-warn">Предупреждение</span>
                {/if}
            </div>
        {/each}
    </div>

    {#if pageState === 'done'}
        {@const hasError = checks.some(c => c.status === 'fail')}
        {@const hasWarning = checks.some(c => c.status === 'warning')}
        <div class="summary-bar" class:summary-error={hasError} class:summary-warning={!hasError && hasWarning} class:summary-ok={!hasError && !hasWarning}>
            <span class="summary-icon">{hasError ? '❌' : hasWarning ? '⚠️' : '✅'}</span>
            <span class="summary-text">
                {#if hasError}
                    <strong>Обнаружены проблемы.</strong> DNS-маршрутизация может не работать для вашего устройства.
                {:else if hasWarning}
                    <strong>Маршрутизация работает,</strong> но есть предупреждения.
                {:else}
                    <strong>Всё в порядке.</strong> DNS-маршрутизация настроена корректно для вашего устройства.
                {/if}
            </span>
        </div>
    {/if}
</div>

<style>
    .dns-check-card {
        background: var(--color-surface-100);
        border: 1px solid var(--color-surface-300);
        border-radius: 0.75rem;
        overflow: hidden;
    }

    .dns-check-header {
        display: flex;
        align-items: flex-start;
        justify-content: space-between;
        gap: 1rem;
        padding: 1.25rem 1.5rem;
        border-bottom: 1px solid var(--color-surface-300);
    }

    .dns-check-header-text {
        flex: 1;
        min-width: 0;
    }

    .dns-check-title {
        font-size: 1rem;
        font-weight: 600;
        margin: 0 0 0.25rem 0;
        color: var(--color-surface-900);
    }

    .dns-check-desc {
        font-size: 0.8125rem;
        color: var(--color-surface-600);
        margin: 0;
        line-height: 1.4;
    }

    .dns-check-btn {
        flex-shrink: 0;
        display: inline-flex;
        align-items: center;
        gap: 0.4rem;
        padding: 0.45rem 1rem;
        border-radius: 0.5rem;
        border: none;
        background: var(--color-primary-500);
        color: white;
        font-size: 0.875rem;
        font-weight: 500;
        cursor: pointer;
        transition: background 0.15s;
    }

    .dns-check-btn:hover:not(:disabled) {
        background: var(--color-primary-600);
    }

    .dns-check-btn:disabled {
        opacity: 0.7;
        cursor: not-allowed;
    }

    .btn-spinner {
        display: inline-block;
        width: 14px;
        height: 14px;
        border: 2px solid rgba(255, 255, 255, 0.4);
        border-top-color: white;
        border-radius: 50%;
        animation: spin 0.7s linear infinite;
    }

    .client-info {
        display: flex;
        align-items: center;
        gap: 0.5rem;
        padding: 0.6rem 1.5rem;
        background: var(--color-surface-50, var(--color-surface-100));
        border-bottom: 1px solid var(--color-surface-200);
        font-size: 0.8125rem;
        color: var(--color-surface-600);
    }

    .client-info svg {
        flex-shrink: 0;
        color: var(--color-surface-500);
    }

    .check-list {
        display: flex;
        flex-direction: column;
    }

    .check-item {
        display: flex;
        align-items: center;
        gap: 0.875rem;
        padding: 0.875rem 1.5rem;
        border-bottom: 1px solid var(--color-surface-200);
    }

    .check-item:last-child {
        border-bottom: none;
    }

    .check-icon {
        flex-shrink: 0;
        width: 28px;
        height: 28px;
        border-radius: 50%;
        display: flex;
        align-items: center;
        justify-content: center;
        font-size: 0.875rem;
        font-weight: 700;
    }

    .check-icon-ok {
        background: rgba(34, 197, 94, 0.15);
        color: rgb(21, 128, 61);
    }

    .check-icon-fail {
        background: rgba(239, 68, 68, 0.15);
        color: rgb(185, 28, 28);
    }

    .check-icon-warning {
        background: rgba(234, 179, 8, 0.15);
        color: rgb(161, 98, 7);
    }

    .check-icon-pending {
        background: var(--color-surface-200);
        color: var(--color-surface-500);
    }

    .check-info {
        flex: 1;
        min-width: 0;
        display: flex;
        flex-direction: column;
        gap: 0.125rem;
    }

    .check-title {
        font-size: 0.8125rem;
        font-weight: 500;
        color: var(--color-surface-900);
    }

    .check-desc {
        font-size: 0.75rem;
        color: var(--color-surface-500);
    }

    .check-detail {
        display: inline-block;
        margin-top: 0.25rem;
        font-family: monospace;
        font-size: 0.75rem;
        background: var(--color-surface-200);
        color: var(--color-surface-700);
        padding: 0.1rem 0.4rem;
        border-radius: 0.25rem;
    }

    .check-status {
        flex-shrink: 0;
        font-size: 0.75rem;
        font-weight: 600;
        padding: 0.2rem 0.55rem;
        border-radius: 0.375rem;
    }

    .status-ok {
        background: rgba(34, 197, 94, 0.12);
        color: rgb(21, 128, 61);
    }

    .status-fail {
        background: rgba(239, 68, 68, 0.12);
        color: rgb(185, 28, 28);
    }

    .status-warn {
        background: rgba(234, 179, 8, 0.12);
        color: rgb(161, 98, 7);
    }

    .summary-bar {
        display: flex;
        align-items: flex-start;
        gap: 0.75rem;
        padding: 0.875rem 1.5rem;
        background: var(--color-surface-50, var(--color-surface-100));
        border-top: 1px solid var(--color-surface-200);
        border-left: 4px solid transparent;
    }

    .summary-ok {
        border-left-color: rgb(34, 197, 94);
    }

    .summary-error {
        border-left-color: rgb(239, 68, 68);
    }

    .summary-warning {
        border-left-color: rgb(234, 179, 8);
    }

    .summary-icon {
        flex-shrink: 0;
        font-size: 1rem;
        line-height: 1.4;
    }

    .summary-text {
        font-size: 0.8125rem;
        color: var(--color-surface-700);
        line-height: 1.5;
    }

    .spinner {
        width: 16px;
        height: 16px;
        border: 2px solid var(--color-surface-400);
        border-top-color: var(--color-primary-500);
        border-radius: 50%;
        animation: spin 0.7s linear infinite;
    }

    @keyframes spin {
        to { transform: rotate(360deg); }
    }
</style>
