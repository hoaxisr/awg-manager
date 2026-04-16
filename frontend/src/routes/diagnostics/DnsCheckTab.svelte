<script lang="ts">
    import { api } from '$lib/api/client';
    import { notifications } from '$lib/stores/notifications';
    import type { DnsCheckResult } from '$lib/types';

    function defaultChecks(): DnsCheckResult[] {
        return [
            { id: 'tunnel_running', status: 'pending', title: 'Туннель работает', message: 'Проверка состояния VPN-интерфейса' },
            { id: 'dns_routes', status: 'pending', title: 'DNS-маршруты настроены', message: 'Есть ли активные правила маршрутизации по доменам' },
            { id: 'dns_probe', status: 'pending', title: 'DNS-запрос к роутеру', message: 'DNS-запрос достигает роутера' },
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
            // Step 1: server-side checks (1,2,4,5)
            const start = await api.startDnsCheck();
            clientIP = start.clientIP;
            hostname = start.hostname;

            // Merge server results into checks array
            checks = checks.map(c => {
                const serverCheck = start.checks.find(sc => sc.id === c.id);
                return serverCheck || c;
            });

            // Step 2: DNS probe — client-side fetch to awgm-dnscheck.test
            // If client DNS goes through the router, ip host resolves this
            // to the router IP and our API responds. .test TLD is reserved
            // (RFC 6761) — public DNS always returns NXDOMAIN.
            let dnsReached = false;
            try {
                const port = window.location.port || '80';
                const probeUrl = `http://awgm-dnscheck.test:${port}/api/dns-check/probe`;
                const resp = await fetch(probeUrl, { signal: AbortSignal.timeout(3000) });
                dnsReached = resp.ok;
            } catch {
                dnsReached = false;
            }

            // Build check 3 result
            checks = checks.map(c => {
                if (c.id !== 'dns_probe') return c;
                return dnsReached
                    ? { ...c, status: 'ok' as const, message: 'DNS-запрос успешно достиг роутера' }
                    : { ...c, status: 'fail' as const, message: 'DNS-запрос не достиг роутера. Возможно клиент не настроен корректно' };
            });
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
        background: var(--bg-secondary, var(--bg-card));
        border: 1px solid var(--border);
        border-radius: var(--radius, 8px);
        overflow: hidden;
    }

    .dns-check-header {
        display: flex;
        align-items: flex-start;
        justify-content: space-between;
        gap: 1rem;
        padding: 1.25rem;
        border-bottom: 1px solid var(--border);
    }

    .dns-check-header-text {
        flex: 1;
        min-width: 0;
    }

    .dns-check-title {
        font-size: 0.9375rem;
        font-weight: 600;
        margin: 0 0 0.25rem 0;
        color: var(--text-primary);
    }

    .dns-check-desc {
        font-size: 0.75rem;
        color: var(--text-muted);
        margin: 0;
        line-height: 1.4;
    }

    .dns-check-btn {
        flex-shrink: 0;
        display: inline-flex;
        align-items: center;
        gap: 0.4rem;
        padding: 0.375rem 0.75rem;
        border-radius: 6px;
        border: 1px solid var(--accent);
        background: var(--accent);
        color: #fff;
        font-size: 0.8125rem;
        font-weight: 500;
        font-family: inherit;
        cursor: pointer;
        transition: filter 0.15s;
    }

    .dns-check-btn:hover:not(:disabled) {
        filter: brightness(1.1);
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
        padding: 0.625rem 1.25rem;
        background: var(--bg-primary);
        border-bottom: 1px solid var(--border);
        font-size: 0.75rem;
        color: var(--text-secondary);
    }

    .client-info svg {
        flex-shrink: 0;
        color: var(--text-muted);
    }

    .check-list {
        display: flex;
        flex-direction: column;
    }

    .check-item {
        display: flex;
        align-items: center;
        gap: 0.75rem;
        padding: 0.875rem 1.25rem;
        border-bottom: 1px solid var(--border);
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
    }

    .check-icon-ok {
        background: rgba(16, 185, 129, 0.15);
        color: var(--success);
    }

    .check-icon-fail {
        background: rgba(239, 68, 68, 0.15);
        color: var(--error);
    }

    .check-icon-warning {
        background: rgba(245, 158, 11, 0.15);
        color: var(--warning);
    }

    .check-icon-pending {
        background: var(--bg-primary);
        color: var(--text-muted);
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
        color: var(--text-primary);
    }

    .check-desc {
        font-size: 0.75rem;
        color: var(--text-muted);
        line-height: 1.4;
    }

    .check-detail {
        display: inline-block;
        margin-top: 0.25rem;
        font-family: monospace;
        font-size: 0.6875rem;
        background: var(--bg-primary);
        color: var(--text-secondary);
        padding: 0.125rem 0.5rem;
        border-radius: 4px;
    }

    .check-status {
        flex-shrink: 0;
        font-size: 0.6875rem;
        font-weight: 600;
        padding: 0.125rem 0.5rem;
        border-radius: 4px;
    }

    .status-ok {
        background: rgba(16, 185, 129, 0.15);
        color: var(--success);
    }

    .status-fail {
        background: rgba(239, 68, 68, 0.15);
        color: var(--error);
    }

    .status-warn {
        background: rgba(245, 158, 11, 0.15);
        color: var(--warning);
    }

    .summary-bar {
        display: flex;
        align-items: flex-start;
        gap: 0.75rem;
        padding: 0.75rem 1.25rem;
        background: var(--bg-primary);
        border-top: 1px solid var(--border);
        border-left: 3px solid transparent;
    }

    .summary-ok {
        border-left-color: var(--success);
    }

    .summary-error {
        border-left-color: var(--error);
    }

    .summary-warning {
        border-left-color: var(--warning);
    }

    .summary-icon {
        flex-shrink: 0;
        font-size: 1rem;
        line-height: 1.4;
    }

    .summary-text {
        font-size: 0.8125rem;
        color: var(--text-secondary);
        line-height: 1.5;
    }

    .spinner {
        width: 16px;
        height: 16px;
        border: 2px solid var(--border);
        border-top-color: var(--accent);
        border-radius: 50%;
        animation: spin 0.7s linear infinite;
    }

    @keyframes spin {
        to { transform: rotate(360deg); }
    }
</style>
