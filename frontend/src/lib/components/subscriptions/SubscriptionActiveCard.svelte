<script lang="ts">
    import { onMount, untrack } from 'svelte';
    import { goto } from '$app/navigation';
    import { browser } from '$app/environment';
    import { api } from '$lib/api/client';
    import { Button, Modal, TrafficChart, TrafficSparkline } from '$lib/components/ui';
    import { getTrafficRates, subscribeTraffic, loadHistory } from '$lib/stores/traffic';
    import {
        singboxDelayHistory,
        singboxTraffic,
        triggerDelayCheck,
    } from '$lib/stores/singbox';
    import { subscriptionsStore } from '$lib/stores/subscriptions';
    import { notifications } from '$lib/stores/notifications';
    import type { Subscription, SubscriptionMember } from '$lib/types';
    import { formatRelativeTime } from '$lib/utils/format';
    import SubscriptionMemberPicker from './SubscriptionMemberPicker.svelte';
    import type { SingboxLayoutMode } from '$lib/constants/singboxLayout';

    interface Props {
        subscription: Subscription;
        activeMember: SubscriptionMember;
        autoDelayCheckNonce?: number;
        autoDelayCheckDelayMs?: number;
        layout?: SingboxLayoutMode;
        ondetail?: (tag: string) => void;
    }
    let {
        subscription,
        activeMember,
        autoDelayCheckNonce = 0,
        autoDelayCheckDelayMs = 0,
        layout = 'grid',
        ondetail,
    }: Props = $props();

    let pickerOpen = $state(false);
    let checking = $state(false);
    let showEndpoint = $state(false);
    let confirmDeleteOpen = $state(false);
    let deleting = $state(false);

    // NDMS Proxy interface name (Proxy<N>) and matching kernel TUN
    // (t2s<N>) — same naming convention sing-box tunnels use, just
    // keyed off the subscription's allocated proxyIndex. Empty when
    // proxyIndex < 0 (defensive; live subscriptions on the active list
    // always have a valid index after the EnsureProxy step).
    const proxyIface = $derived(
        subscription.proxyIndex >= 0 ? `Proxy${subscription.proxyIndex}` : '',
    );
    const kernelIface = $derived(
        subscription.proxyIndex >= 0 ? `t2s${subscription.proxyIndex}` : '',
    );

    const DELAY_OK = 200;
    const DELAY_SLOW = 500;

    const history = $derived($singboxDelayHistory.get(activeMember.tag) ?? []);
    const latest = $derived(history.length > 0 ? history[history.length - 1] : -1);
    const hasConsecutiveTimeout = $derived(
        history.length >= 2 &&
            history[history.length - 1] <= 0 &&
            history[history.length - 2] <= 0,
    );
    const traffic = $derived($singboxTraffic.get(activeMember.tag));

    const trafficSparkData = $derived.by(() => {
        const n = Math.min(rxRates.length, txRates.length);
        if (n === 0) return [];
        const take = Math.min(36, n);
        const out: number[] = [];
        for (let i = n - take; i < n; i++) {
            out.push(Math.max(0, rxRates[i] ?? 0) + Math.max(0, txRates[i] ?? 0));
        }
        return out;
    });

    let rxRates = $state<number[]>([]);
    let txRates = $state<number[]>([]);
    let trafficMemberTag = $derived(activeMember.tag);

    $effect(() => {
        const tag = trafficMemberTag;
        const update = () => {
            const t = getTrafficRates(tag);
            rxRates = t.rx;
            txRates = t.tx;
        };
        update();
        return subscribeTraffic(update);
    });

    $effect(() => {
        const tag = trafficMemberTag;
        untrack(() => loadHistory(tag));
    });
    const CHART_KEY_PREFIX = 'sub_chart_expanded_';
    let chartStorageKey = $derived(`${CHART_KEY_PREFIX}${subscription.id}`);
    let chartExpanded = $state(true);
    onMount(() => {
        chartExpanded = localStorage.getItem(chartStorageKey) !== 'false';
    });
    function toggleCharts() {
        chartExpanded = !chartExpanded;
        if (browser) {
            localStorage.setItem(chartStorageKey, String(chartExpanded));
        }
    }
    const endpointText = $derived(`${activeMember.server}:${activeMember.port}`);
    /** List row: title above IP — prefer remark, else outbound tag. */
    const listActiveServerName = $derived(
        activeMember.label?.trim() || activeMember.tag?.trim() || '',
    );
    const activeEndpointTitle = $derived(
        listActiveServerName ? `${listActiveServerName} · ${endpointText}` : endpointText,
    );
    const isURLTest = $derived(subscription.mode === 'urltest');
    const lastFetchedHuman = $derived(
        subscription.lastFetched ? formatRelativeTime(subscription.lastFetched) : '—',
    );

    type State = 'ok' | 'slow' | 'fail' | 'unknown';
    const cardState: State = $derived.by(() => {
        if (latest < 0) return 'unknown';
        if (latest <= 0) return hasConsecutiveTimeout ? 'fail' : 'slow';
        if (latest < DELAY_OK) return 'ok';
        if (latest < DELAY_SLOW) return 'slow';
        return 'slow';
    });
    const latText = $derived.by(() => {
        if (cardState === 'unknown') return '—';
        if (cardState === 'fail') return 'timeout';
        if (latest <= 0) return 'проверка...';
        return `${latest}ms`;
    });
    const protocolLabel = $derived.by(() => {
        switch (activeMember.protocol) {
            case 'vless':         return 'VLESS';
            case 'trojan':        return 'Trojan';
            case 'shadowsocks':   return 'Shadowsocks';
            case 'hysteria2':     return 'Hysteria2';
            case 'naive':         return 'Naive';
            default:              return activeMember.protocol;
        }
    });

    async function triggerCheck(e?: MouseEvent | KeyboardEvent): Promise<void> {
        e?.stopPropagation();
        if (checking) return;
        checking = true;
        try {
            await triggerDelayCheck(activeMember.tag);
        } finally {
            checking = false;
        }
    }

    let lastAutoDelayCheckNonce = 0;
    $effect(() => {
        const nonce = autoDelayCheckNonce;
        const delay = autoDelayCheckDelayMs;
        const tag = activeMember.tag;

        if (nonce <= 0 || nonce === lastAutoDelayCheckNonce) return;
        lastAutoDelayCheckNonce = nonce;
        if (!tag || checking) return;

        const timer = setTimeout(() => {
            untrack(() => void triggerCheck());
        }, delay);
        return () => clearTimeout(timer);
    });

    async function pickMember(memberTag: string): Promise<void> {
        await api.setSubscriptionActiveMember(subscription.id, memberTag);
        await subscriptionsStore.refetch();
    }

    function openDetail(): void {
        goto(`/subscriptions/${subscription.id}`);
    }

    async function removeSubscription(): Promise<void> {
        if (deleting) return;
        deleting = true;
        try {
            await api.deleteSubscription(subscription.id);
            await subscriptionsStore.refetch();
            confirmDeleteOpen = false;
        } catch (e) {
            notifications.error(e instanceof Error ? e.message : 'Не удалось удалить подписку');
        } finally {
            deleting = false;
        }
    }

    function formatBytes(n: number): string {
        if (n < 1024) return `${n} B`;
        if (n < 1024 * 1024) return `${(n / 1024).toFixed(1)} KB`;
        if (n < 1024 * 1024 * 1024) return `${(n / (1024 * 1024)).toFixed(1)} MB`;
        return `${(n / (1024 * 1024 * 1024)).toFixed(1)} GB`;
    }


</script>

{#if layout === 'list'}
    <div
        class="sub-active-list-group"
        class:ok={cardState === 'ok'}
        class:slow={cardState === 'slow'}
        class:fail={cardState === 'fail'}
        class:unknown={cardState === 'unknown'}
    >
        <div
            class="sbx-sub-active-row"
            onclick={openDetail}
            onkeydown={(e) => {
                if (e.key === 'Enter' || e.key === ' ') {
                    e.preventDefault();
                    openDetail();
                }
            }}
            role="button"
            tabindex="0"
        >
            <div class="lc lc-delay" data-label="Delay">
                {#if subscription.lastError}
                    <span class="dot fail" aria-hidden="true"></span>
                    <span class="delay-inline-err mono" title={subscription.lastError}>
                        {subscription.lastError}
                    </span>
                {:else}
                    <span class="dot {cardState}" aria-hidden="true"></span>
                    <button
                        type="button"
                        class="lat-btn {cardState}"
                        class:checking
                        onclick={(e) => {
                            e.stopPropagation();
                            void triggerCheck(e);
                        }}
                        title="Обновить delay"
                        disabled={checking}
                    >
                        {checking ? '...' : latText}
                    </button>
                {/if}
            </div>
            <div class="lc lc-name" data-label="Подписка">
                <div class="t1">{subscription.label}</div>
                <div class="t2 mono">{proxyIface}{#if kernelIface} · {kernelIface}{/if}</div>
            </div>
            <div class="lc lc-mode" data-label="Режим">
                {isURLTest ? 'URLTest' : 'Selector'}
            </div>
            <div class="lc lc-endpoint" data-label="Активный сервер" title={activeEndpointTitle}>
                <div class="lc-endpoint-stack">
                    {#if listActiveServerName}
                        <span class="lc-endpoint-name" title={listActiveServerName}>{listActiveServerName}</span>
                    {/if}
                    <span class="lc-endpoint-host mono">
                        {#if showEndpoint}{endpointText}{:else}••••••••{/if}
                    </span>
                </div>
                <button
                    type="button"
                    class="eye-mini"
                    onclick={(e) => {
                        e.stopPropagation();
                        showEndpoint = !showEndpoint;
                    }}
                    aria-label={showEndpoint ? 'Скрыть' : 'Показать'}
                >
                    {#if showEndpoint}
                        <svg width="12" height="12" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2"><path d="M1 12s4-8 11-8 11 8 11 8-4 8-11 8-11-8-11-8z"/><circle cx="12" cy="12" r="3"/></svg>
                    {:else}
                        <svg width="12" height="12" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2"><path d="M17.94 17.94A10.07 10.07 0 0 1 12 20c-7 0-11-8-11-8a18.45 18.45 0 0 1 5.06-5.94M9.9 4.24A9.12 9.12 0 0 1 12 4c7 0 11 8 11 8a18.5 18.5 0 0 1-2.16 3.19m-6.72-1.07a3 3 0 1 1-4.24-4.24"/><line x1="1" y1="1" x2="23" y2="23"/></svg>
                    {/if}
                </button>
            </div>
            <div class="lc lc-members" data-label="Серверов">
                {subscription.memberTags.length}
            </div>
            <div class="lc lc-updated mono" data-label="Обновлено">
                {lastFetchedHuman}
            </div>
            <div class="lc lc-traffic" data-label="Трафик">
                {#if subscription.lastError}
                    <span class="delay-dash">—</span>
                {:else}
                    <div class="traffic-row-list">
                        <div
                            role="button"
                            tabindex="0"
                            class="traffic-mini-click"
                            onclick={(e) => {
                                e.stopPropagation();
                                ondetail?.(activeMember.tag);
                            }}
                            onkeydown={(e) => {
                                if (e.key === 'Enter' || e.key === ' ') {
                                    e.preventDefault();
                                    e.stopPropagation();
                                    ondetail?.(activeMember.tag);
                                }
                            }}
                            title="Открыть детальный график"
                        >
                            <TrafficSparkline
                                data={trafficSparkData}
                                width={84}
                                height={22}
                                color="var(--color-accent)"
                            />
                        </div>
                        <div class="traffic-mini-col mono">
                            <span>↓ {formatBytes(traffic?.download ?? 0)}</span>
                            <span>↑ {formatBytes(traffic?.upload ?? 0)}</span>
                        </div>
                    </div>
                {/if}
            </div>
            <div class="lc lc-ping-mini" data-label="Ping">
                {#if subscription.lastError}
                    <span class="delay-dash">—</span>
                {:else}
                    <div
                        class="spark-mini {cardState}"
                        title="Delay за последние проверки"
                    >
                        {#if history.length === 0}
                            {#each Array(10) as _, i (i)}
                                <div class="bar empty"></div>
                            {/each}
                        {:else}
                            {@const max = Math.max(...history.map((v) => (v <= 0 ? 100 : v)), 100)}
                            {#each history.slice(-14) as d, i (i)}
                                <div class="bar" style="height: {Math.max((d <= 0 ? max : d) / max, 0.08) * 100}%;"></div>
                            {/each}
                        {/if}
                    </div>
                {/if}
            </div>
            <div class="lc lc-actions" data-label="">
                <button
                    class="action-btn"
                    onclick={(e) => {
                        e.stopPropagation();
                        openDetail();
                    }}
                >
                    <svg width="14" height="14" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2">
                        <path d="M11 4H4a2 2 0 0 0-2 2v14a2 2 0 0 0 2 2h14a2 2 0 0 0 2-2v-7"/>
                        <path d="M18.5 2.5a2.121 2.121 0 0 1 3 3L12 15l-4 1 1-4 9.5-9.5z"/>
                    </svg>
                    Открыть
                </button>
                <button
                    class="action-btn"
                    disabled={!kernelIface}
                    onclick={(e) => {
                        e.stopPropagation();
                        if (kernelIface) goto(`/subscriptions/${subscription.id}/test`);
                    }}
                >
                    <svg width="14" height="14" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2">
                        <path d="M22 11.08V12a10 10 0 1 1-5.93-9.14"/>
                        <polyline points="22,4 12,14.01 9,11.01"/>
                    </svg>
                    Тест
                </button>
                <button
                    class="action-btn action-danger"
                    onclick={(e) => {
                        e.stopPropagation();
                        confirmDeleteOpen = true;
                    }}
                >
                    <svg width="14" height="14" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2">
                        <polyline points="3,6 5,6 21,6"/>
                        <path d="M19 6v14a2 2 0 0 1-2 2H7a2 2 0 0 1-2-2V6m3 0V4a2 2 0 0 1 2-2h4a2 2 0 0 1 2 2v2"/>
                    </svg>
                    Удалить
                </button>
            </div>
        </div>
    </div>
{:else}
<div
    class="card"
    class:ok={cardState === 'ok'}
    class:slow={cardState === 'slow'}
    class:fail={cardState === 'fail'}
    class:unknown={cardState === 'unknown'}
>
    <div class="led-wrap">
        <span class="dot {cardState}" aria-hidden="true"></span>
        <button
            class="lat-btn {cardState}"
            class:checking
            onclick={triggerCheck}
            title="Обновить delay"
            disabled={checking}
        >
            {checking ? '...' : latText}
        </button>
    </div>

    <div class="title-row">
        <h3 class="title">{subscription.label}</h3>
        <span class="kind-badge">подписка</span>
    </div>
    <div class="iface">
        {#if proxyIface}
            {proxyIface}
            {#if kernelIface}<span class="kernel">· {kernelIface}</span>{/if}
            <span class="kernel">· {subscription.inboundTag}</span>
        {:else}
            {subscription.inboundTag}
        {/if}
        <span class="kernel">· :{subscription.listenPort}</span>
    </div>

    <div class="badges">
        <span class="badge proto">{protocolLabel}</span>
        {#if activeMember.transport && activeMember.transport !== 'tcp'}
            <span class="badge transport">{activeMember.transport.toUpperCase()}</span>
        {/if}
        {#if activeMember.security === 'reality'}
            <span class="badge reality">Reality</span>
        {:else if activeMember.security === 'tls'}
            <span class="badge tls">TLS</span>
        {/if}
    </div>

    <div class="sub-meta">
        <div>
            <span>{subscription.memberTags.length} серверов</span>
            {#if subscription.activeMember}
                <span>· активен <span class="mono">{subscription.activeMember}</span></span>
            {/if}
        </div>
        <div>
            <span>обновлено {lastFetchedHuman}</span>
            {#if subscription.refreshHours > 0}
                <span>· auto {subscription.refreshHours}ч</span>
            {/if}
        </div>
    </div>

    {#if subscription.lastError}
        <div class="sub-error mono">{subscription.lastError}</div>
    {/if}

    <div class="divider"></div>

    <div class="server-row">
        <span class="label">{isURLTest ? 'Авто' : 'Сервер'}</span>
        <div class="picker-anchor">
            <div class="server-control">
                <button
                    class="server-btn"
                    class:server-btn-readonly={isURLTest}
                    onclick={(e) => {
                        e.stopPropagation();
                        if (isURLTest) {
                            notifications.info(
                                'Включён автовыбор (URLTest). Чтобы выбирать сервер вручную, откройте подписку → вкладка «Настройки» → режим «Вручную».',
                                { duration: 9000 },
                            );
                            return;
                        }
                        pickerOpen = !pickerOpen;
                    }}
                    aria-haspopup={isURLTest ? undefined : 'listbox'}
                    aria-expanded={isURLTest ? undefined : pickerOpen}
                    title={isURLTest ? 'Sing-box выбирает самый быстрый сервер автоматически' : ''}
                >
                    <span
                        class="server-text"
                        class:mono={showEndpoint || !activeMember.label}
                        title={activeMember.label ? `${activeMember.label} · ${endpointText}` : endpointText}
                    >
                        {#if showEndpoint}
                            {endpointText}
                        {:else if activeMember.label}
                            {activeMember.label}
                        {:else}
                            {endpointText}
                        {/if}
                    </span>
                    {#if !isURLTest}
                        <span class="caret" aria-hidden="true">▾</span>
                    {/if}
                </button>
                <button
                    type="button"
                    class="eye-btn"
                    onclick={(e) => {
                        e.stopPropagation();
                        showEndpoint = !showEndpoint;
                    }}
                    title={showEndpoint ? 'Скрыть IP' : 'Показать IP'}
                    aria-label={showEndpoint ? 'Скрыть IP сервера' : 'Показать IP сервера'}
                >
                    {#if showEndpoint}
                        <svg width="14" height="14" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2" stroke-linecap="round" stroke-linejoin="round"><path d="M1 12s4-8 11-8 11 8 11 8-4 8-11 8-11-8-11-8z"/><circle cx="12" cy="12" r="3"/></svg>
                    {:else}
                        <svg width="14" height="14" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2" stroke-linecap="round" stroke-linejoin="round"><path d="M17.94 17.94A10.07 10.07 0 0 1 12 20c-7 0-11-8-11-8a18.45 18.45 0 0 1 5.06-5.94M9.9 4.24A9.12 9.12 0 0 1 12 4c7 0 11 8 11 8a18.5 18.5 0 0 1-2.16 3.19m-6.72-1.07a3 3 0 1 1-4.24-4.24"/><line x1="1" y1="1" x2="23" y2="23"/></svg>
                    {/if}
                </button>
            </div>
            {#if pickerOpen && !isURLTest}
                <SubscriptionMemberPicker
                    members={subscription.members ?? []}
                    activeMemberTag={subscription.activeMember}
                    onPick={pickMember}
                    onClose={() => (pickerOpen = false)}
                />
            {/if}
        </div>
    </div>
    <div class="divider"></div>

    <div class="actions">
        <button class="action-btn" onclick={openDetail}>
            <svg width="14" height="14" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2">
                <path d="M11 4H4a2 2 0 0 0-2 2v14a2 2 0 0 0 2 2h14a2 2 0 0 0 2-2v-7"/>
                <path d="M18.5 2.5a2.121 2.121 0 0 1 3 3L12 15l-4 1 1-4 9.5-9.5z"/>
            </svg>
            Открыть подписку
        </button>
        <button
            class="action-btn"
            disabled={!kernelIface}
            onclick={() => kernelIface && goto(`/subscriptions/${subscription.id}/test`)}
        >
            <svg width="14" height="14" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2">
                <path d="M22 11.08V12a10 10 0 1 1-5.93-9.14"/>
                <polyline points="22,4 12,14.01 9,11.01"/>
            </svg>
            Тест
        </button>
        <button class="action-btn action-danger" onclick={() => (confirmDeleteOpen = true)}>
            <svg width="14" height="14" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2">
                <polyline points="3,6 5,6 21,6"/>
                <path d="M19 6v14a2 2 0 0 1-2 2H7a2 2 0 0 1-2-2V6m3 0V4a2 2 0 0 1 2-2h4a2 2 0 0 1 2 2v2"/>
            </svg>
            Удалить
        </button>
    </div>

    <div class="chart-section">
        <button type="button" class="chart-header" onclick={toggleCharts}>
            <span class="chart-label">Графики</span>
            <span class="chart-chevron" class:expanded={chartExpanded}>▾</span>
        </button>
        <div class="chart-body" class:expanded={chartExpanded}>
            <div class="chart-head">
                <span>Delay (5 мин)</span>
                <span class="stats">
                    {#if cardState === 'unknown'}ещё не тестировали
                    {:else if cardState === 'fail'}<span class="err">не отвечает</span>
                    {:else}{latText}{/if}
                </span>
            </div>
            <div
                class="spark {cardState}"
                title="Delay за последние проверки"
            >
                {#if history.length === 0}
                    {#each Array(6) as _, i (i)}<div class="bar empty"></div>{/each}
                {:else}
                    {@const max = Math.max(...history.map((v) => (v <= 0 ? 100 : v)), 100)}
                    {#each history as d, i (i)}
                        <div class="bar" style="height: {Math.max((d <= 0 ? max : d) / max, 0.1) * 100}%;"></div>
                    {/each}
                {/if}
            </div>
            <div class="chart-head traffic-head">
                <span>Трафик</span>
                <span class="stats">
                    ↓ {formatBytes(traffic?.download ?? 0)} · ↑ {formatBytes(traffic?.upload ?? 0)}
                </span>
            </div>
            <TrafficChart
                {rxRates}
                {txRates}
                rxTotal={traffic?.download ?? 0}
                txTotal={traffic?.upload ?? 0}
                height={56}
                onclick={() => ondetail?.(activeMember.tag)}
            />
        </div>
    </div>
</div>
{/if}

<Modal
    open={confirmDeleteOpen}
    title="Удалить подписку?"
    size="md"
    onclose={() => {
        if (deleting) return;
        confirmDeleteOpen = false;
    }}
>
    <p>
        Подписка <strong>{subscription.label || subscription.url}</strong> будет
        удалена вместе с её sing-box outbound'ами и NDMS Proxy
        <code class="mono">Proxy{subscription.proxyIndex}</code>.
    </p>
    {#snippet actions()}
        <Button variant="ghost" disabled={deleting} onclick={() => (confirmDeleteOpen = false)}>
            Отмена
        </Button>
        <Button variant="danger" disabled={deleting} loading={deleting} onclick={removeSubscription}>
            {deleting ? 'Удаляем...' : 'Удалить'}
        </Button>
    {/snippet}
</Modal>

<style>
    .card {
        position: relative;
        display: flex;
        flex-direction: column;
        padding: 16px;
        border: 1px solid var(--color-border);
        border-radius: 10px;
        background: var(--color-bg-secondary);
        color: var(--color-text-primary);
        gap: 0.5rem;
    }
    .led-wrap {
        position: absolute;
        top: 12px; right: 12px;
        display: flex;
        align-items: center;
        gap: 0.4rem;
    }
    .dot {
        width: 10px; height: 10px;
        border-radius: 999px;
        background: var(--color-bg-tertiary);
    }
    .dot.ok      { background: #3fb950; box-shadow: 0 0 0 3px rgba(63,185,80,0.22); }
    .dot.slow    { background: #d29922; box-shadow: 0 0 0 3px rgba(210,153,34,0.22); }
    .dot.fail    { background: #f85149; box-shadow: 0 0 0 3px rgba(248,81,73,0.22); }
    .lat-btn {
        padding: 0.15rem 0.5rem;
        border-radius: 4px;
        background: var(--color-bg-tertiary);
        color: var(--color-text-muted);
        border: 1px solid var(--color-border);
        font: inherit;
        font-size: 0.7rem;
        font-family: var(--font-mono, ui-monospace, monospace);
        cursor: pointer;
    }
    .lat-btn:disabled { opacity: 0.5; }
    .lat-btn.ok   { color: #3fb950; }
    .lat-btn.slow { color: #d29922; }
    .lat-btn.fail { color: #f85149; }
    .title-row {
        display: flex;
        align-items: center;
        gap: 0.5rem;
        margin-right: 90px; /* room for led-wrap */
    }
    .title {
        font-size: 1rem;
        font-weight: 600;
        margin: 0;
        flex: 0 1 auto;
    }
    .kind-badge {
        font-size: 0.65rem;
        padding: 0.1rem 0.45rem;
        border-radius: 999px;
        background: rgba(88, 166, 255, 0.15);
        color: var(--color-accent);
        font-weight: 600;
    }
    .iface {
        font-size: 0.75rem;
        color: var(--color-text-muted);
        font-family: var(--font-mono, ui-monospace, monospace);
    }
    .iface .kernel {
        color: var(--color-text-muted);
        opacity: 0.7;
        margin-left: 0.2rem;
    }
    .badges { display: flex; gap: 0.4rem; flex-wrap: wrap; }
    .badge {
        font-size: 0.68rem;
        padding: 0.15rem 0.5rem;
        border-radius: 4px;
        font-weight: 600;
    }
    .badge.proto    { background: rgba(88,166,255,0.15); color: var(--color-accent); }
    .badge.transport{ background: var(--color-bg-tertiary); color: var(--color-text-muted); }
    .badge.tls      { background: rgba(63,185,80,0.15); color: #3fb950; }
    .badge.reality  { background: rgba(210,153,34,0.15); color: #d29922; }
    .server-row {
        display: grid;
        grid-template-columns: 80px 1fr;
        gap: 0.5rem;
        align-items: center;
        font-size: 0.82rem;
        margin: 0.2rem 0;
    }
    .label { color: var(--color-text-muted); font-size: 0.7rem; text-transform: uppercase; letter-spacing: 0.5px; }
    .picker-anchor { position: relative; min-width: 0; }
    .server-control {
        display: flex;
        align-items: center;
        gap: 0.25rem;
        min-width: 0;
    }
    .server-btn {
        display: flex;
        align-items: center;
        justify-content: space-between;
        gap: 0.5rem;
        width: 100%;
        padding: 0.4rem 0.55rem;
        background: var(--color-bg-primary);
        border: 1px solid var(--color-border);
        border-radius: 4px;
        font: inherit;
        font-size: 0.82rem;
        color: var(--color-text-primary);
        cursor: pointer;
        min-width: 0;
    }
    .server-btn:hover { border-color: var(--color-accent); }
    .server-btn-readonly { cursor: default; }
    .server-btn-readonly:hover { border-color: var(--color-border); }
    .server-text {
        font-size: 0.82rem;
        overflow: hidden;
        display: -webkit-box;
        -webkit-box-orient: vertical;
        -webkit-line-clamp: 2;
        line-clamp: 2;
        white-space: normal;
        word-break: break-word;
        overflow-wrap: anywhere;
        min-width: 0;
    }
    .server-text.mono {
        font-family: var(--font-mono, ui-monospace, monospace);
        font-size: 0.78rem;
    }
    .caret { color: var(--color-text-muted); font-size: 0.7rem; }
    .eye-btn {
        display: inline-flex;
        align-items: center;
        justify-content: center;
        flex: 0 0 auto;
        padding: 0.35rem;
        border: none;
        background: none;
        color: var(--color-text-muted);
        cursor: pointer;
        transition: color var(--t-fast) ease;
    }
    .eye-btn:hover { color: var(--color-text-secondary); }
    .divider { height: 1px; background: var(--color-border); margin: 0.2rem 0; }
    .chart-block { display: flex; flex-direction: column; gap: 0.3rem; }
    .traffic-block { margin-bottom: 0.25rem; }
    .chart-head {
        display: flex;
        justify-content: space-between;
        font-size: 0.7rem;
        color: var(--color-text-muted);
        text-transform: uppercase;
        letter-spacing: 0.5px;
    }
    .stats { font-family: var(--font-mono, ui-monospace, monospace); }
    .err { color: #f85149; text-transform: none; }
    .spark {
        display: flex;
        gap: 1px;
        align-items: flex-end;
        height: 28px;
    }
    .bar { flex: 1; background: var(--color-bg-tertiary); border-radius: 1px; }
    .spark.ok .bar    { background: #3fb950; }
    .spark.slow .bar  { background: #d29922; }
    .spark.fail .bar  { background: #f85149; }
    .bar.empty        { opacity: 0.3; }
    .actions {
        display: flex;
        gap: 0.4rem;
        justify-content: flex-end;
        align-items: center;
        margin-top: 0;
        padding: 10px 0;
        border-bottom: 1px solid var(--color-border);
    }
    .action-btn {
        display: inline-flex;
        align-items: center;
        gap: 4px;
        padding: 5px 9px;
        font-size: 11px;
        font-weight: 500;
        border: none;
        background: transparent;
        color: var(--color-text-secondary);
        cursor: pointer;
        border-radius: var(--radius-sm);
        text-decoration: none;
        font-family: inherit;
        transition: background var(--t-fast) ease, color var(--t-fast) ease;
    }
    .action-btn:hover:not(:disabled) {
        background: var(--color-bg-hover);
        color: var(--color-text-primary);
    }
    .action-btn:disabled {
        opacity: 0.5;
        cursor: not-allowed;
    }
    .action-danger:hover:not(:disabled) {
        color: var(--color-error);
        background: var(--color-error-tint);
    }
    .chart-section {
        margin: 0 -16px -16px;
        border-radius: 0 0 10px 10px;
        background: var(--color-bg-secondary);
        overflow: hidden;
    }
    .chart-header {
        display: flex;
        justify-content: space-between;
        align-items: center;
        width: 100%;
        padding: 6px 12px;
        border: none;
        background: none;
        cursor: pointer;
        user-select: none;
        font: inherit;
        transition: background var(--t-fast) ease;
    }
    .chart-header:hover {
        background: var(--color-bg-tertiary);
    }
    .chart-label {
        font-size: 11px;
        font-weight: 500;
        color: var(--color-text-muted);
        text-transform: uppercase;
        letter-spacing: 0.03em;
    }
    .chart-chevron {
        font-size: 14px;
        color: var(--color-text-muted);
        transition: transform var(--t-fast) ease;
        transform: rotate(-90deg);
    }
    .chart-chevron.expanded {
        transform: rotate(0deg);
    }
    .chart-body {
        max-height: 0;
        overflow: hidden;
        transition: max-height var(--t-med) ease;
        padding: 0 12px;
    }
    .chart-body.expanded {
        max-height: 300px;
        padding: 0 12px 8px;
    }
    .traffic-head { margin-top: 8px; }

    .sub-meta {
        font-size: 0.78rem;
        color: var(--color-text-muted);
        line-height: 1.35;
        display: flex;
        flex-direction: column;
        gap: 0.25rem;
    }
    .sub-error {
        font-size: 0.75rem;
        color: #f85149;
    }
    .mono {
        font-family: var(--font-mono, ui-monospace, monospace);
    }

    .sub-active-list-group {
        border-bottom: 1px solid var(--color-border);
    }
    .sub-active-list-group:last-child {
        border-bottom: none;
    }
    .sbx-sub-active-row {
        display: grid;
        grid-template-columns:
            minmax(92px, 1fr)
            minmax(132px, 1.1fr)
            minmax(72px, 0.9fr)
            minmax(112px, 1fr)
            minmax(52px, 0.75fr)
            minmax(88px, 0.95fr)
            minmax(148px, 1.1fr)
            minmax(120px, 0.95fr)
            minmax(220px, 1.15fr);
        gap: 0.75rem 1rem;
        align-items: center;
        padding: 0.75rem 1rem;
        cursor: pointer;
        min-width: 1040px;
    }
    .sbx-sub-active-row:focus-visible {
        outline: 2px solid var(--color-accent);
        outline-offset: -2px;
    }
    .lc {
        display: flex;
        align-items: center;
        min-width: 0;
        font-size: 0.8125rem;
        color: var(--color-text-secondary);
    }
    .lc-delay {
        gap: 0.35rem;
        min-width: 0;
    }
    .delay-inline-err {
        font-size: 0.68rem;
        line-height: 1.25;
        color: #f85149;
        overflow: hidden;
        text-overflow: ellipsis;
        white-space: nowrap;
        min-width: 0;
        flex: 1;
    }
    .delay-dash {
        font-size: 0.8125rem;
        color: var(--color-text-muted);
    }
    .lc-ping-mini {
        justify-content: flex-start;
    }
    .spark-mini {
        width: 100%;
        height: 22px;
        max-width: 100%;
        display: flex;
        align-items: flex-end;
        gap: 1px;
        padding: 1px 0;
    }
    .spark-mini .bar {
        flex: 1;
        min-width: 0;
        min-height: 2px;
        border-radius: 1px;
        background: var(--color-bg-tertiary);
    }
    .spark-mini.ok .bar {
        background: #3fb950;
    }
    .spark-mini.slow .bar {
        background: #d29922;
    }
    .spark-mini.fail .bar {
        background: #f85149;
    }
    .spark-mini.unknown .bar,
    .spark-mini .bar.empty {
        opacity: 0.35;
        height: 30% !important;
    }
    .traffic-row-list {
        display: flex;
        align-items: center;
        gap: 0.5rem;
        min-width: 0;
        width: 100%;
    }
    .traffic-mini-col {
        display: flex;
        flex-direction: column;
        gap: 0.1rem;
        font-size: 0.7rem;
        line-height: 1.15;
        color: var(--color-text-muted);
        flex-shrink: 0;
    }
    .traffic-mini-click {
        display: inline-flex;
        border-radius: 4px;
        cursor: pointer;
        transition: background var(--t-fast) ease;
    }
    .traffic-mini-click:hover {
        background: rgba(96, 165, 250, 0.06);
    }
    .traffic-mini-click:focus-visible {
        outline: 1px solid var(--color-accent, #58a6ff);
        outline-offset: 1px;
    }
    .lc-name {
        flex-direction: column;
        align-items: flex-start !important;
        gap: 0.15rem;
    }
    .lc-name .t1 {
        font-weight: 600;
        font-size: 0.9375rem;
        color: var(--color-text-primary);
    }
    .lc-name .t2 {
        font-size: 0.72rem;
        color: var(--color-text-muted);
    }
    .lc-endpoint {
        display: flex;
        align-items: flex-start;
        gap: 0.25rem;
        min-width: 0;
        overflow: hidden;
    }
    .lc-endpoint-stack {
        display: flex;
        flex-direction: column;
        align-items: flex-start;
        gap: 0.12rem;
        min-width: 0;
        flex: 1;
    }
    .lc-endpoint-name {
        width: 100%;
        font-size: 0.78rem;
        font-weight: 500;
        color: var(--color-text-primary);
        line-height: 1.2;
        overflow: hidden;
        text-overflow: ellipsis;
        white-space: nowrap;
    }
    .lc-endpoint-host {
        width: 100%;
        font-size: 0.72rem;
        line-height: 1.2;
        color: var(--color-text-muted);
        overflow: hidden;
        text-overflow: ellipsis;
        white-space: nowrap;
    }
    .eye-mini {
        display: inline-flex;
        padding: 0.15rem;
        border: none;
        background: none;
        color: var(--color-text-muted);
        cursor: pointer;
        flex-shrink: 0;
        align-self: center;
    }
    .lc-actions {
        flex-wrap: nowrap;
        gap: 0.5rem;
        justify-content: flex-end;
        align-items: center;
        white-space: nowrap;
    }
</style>
