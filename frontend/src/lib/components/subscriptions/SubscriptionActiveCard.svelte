<script lang="ts">
    import { untrack } from 'svelte';
    import { goto } from '$app/navigation';
    import { api } from '$lib/api/client';
    import { Button } from '$lib/components/ui';
    import {
        singboxDelayHistory,
        singboxTraffic,
        triggerDelayCheck,
    } from '$lib/stores/singbox';
    import { subscriptionsStore } from '$lib/stores/subscriptions';
    import type { Subscription, SubscriptionMember } from '$lib/types';
    import SubscriptionMemberPicker from './SubscriptionMemberPicker.svelte';
    import SingboxSpeedTestModal from '$lib/components/singbox/SingboxSpeedTestModal.svelte';

    interface Props {
        subscription: Subscription;
        activeMember: SubscriptionMember;
        autoDelayCheckNonce?: number;
        autoDelayCheckDelayMs?: number;
    }
    let {
        subscription,
        activeMember,
        autoDelayCheckNonce = 0,
        autoDelayCheckDelayMs = 0,
    }: Props = $props();

    let pickerOpen = $state(false);
    let checking = $state(false);
    let speedtestOpen = $state(false);
    let showEndpoint = $state(false);

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
    const endpointText = $derived(`${activeMember.server}:${activeMember.port}`);
    const isURLTest = $derived(subscription.mode === 'urltest');
    const lastFetchedHuman = $derived(
        subscription.lastFetched ? formatRelative(subscription.lastFetched) : '—',
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

    function formatBytes(n: number): string {
        if (n < 1024) return `${n} B`;
        if (n < 1024 * 1024) return `${(n / 1024).toFixed(1)} KB`;
        if (n < 1024 * 1024 * 1024) return `${(n / (1024 * 1024)).toFixed(1)} MB`;
        return `${(n / (1024 * 1024 * 1024)).toFixed(1)} GB`;
    }

    function formatRelative(iso: string): string {
        const d = new Date(iso);
        const diff = Date.now() - d.getTime();
        const hours = Math.floor(diff / 3_600_000);
        if (hours < 1) return 'только что';
        if (hours < 24) return `${hours}ч назад`;
        return `${Math.floor(hours / 24)}д назад`;
    }

</script>

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

    <div class="server-row">
        <span class="label">{isURLTest ? 'Авто' : 'Сервер'}</span>
        <div class="picker-anchor">
            <div class="server-control">
                <button
                    class="server-btn"
                    class:server-btn-readonly={isURLTest}
                    onclick={(e) => {
                        e.stopPropagation();
                        if (isURLTest) return;
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

    <div class="chart-block">
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
            onclick={triggerCheck}
            onkeydown={(e) => (e.key === 'Enter' || e.key === ' ') && triggerCheck(e)}
            role="button"
            tabindex="0"
            title="Клик — обновить delay"
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
    </div>

    <div class="chart-block">
        <div class="chart-head">
            <span>Трафик</span>
            <span class="stats">
                ↓ {formatBytes(traffic?.download ?? 0)} · ↑ {formatBytes(traffic?.upload ?? 0)}
            </span>
        </div>
    </div>

    <div class="actions">
        <Button
            variant="ghost"
            size="sm"
            disabled={!kernelIface}
            onclick={() => kernelIface && (speedtestOpen = true)}
        >
            Тест скорости
        </Button>
        <Button variant="ghost" size="sm" onclick={openDetail}>Открыть подписку</Button>
    </div>
</div>

<SingboxSpeedTestModal
    open={speedtestOpen}
    tag={subscription.selectorTag}
    kernelInterface={kernelIface}
    onclose={() => (speedtestOpen = false)}
/>

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
    .title { font-size: 1rem; font-weight: 600; margin: 0; flex: 1; }
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
    }
    .label { color: var(--color-text-muted); font-size: 0.7rem; text-transform: uppercase; letter-spacing: 0.5px; }
    .picker-anchor { position: relative; }
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
        text-overflow: ellipsis;
        white-space: nowrap;
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
    .divider { height: 1px; background: var(--color-border); margin: 0.4rem 0; }
    .chart-block { display: flex; flex-direction: column; gap: 0.3rem; }
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
        cursor: pointer;
    }
    .bar { flex: 1; background: var(--color-bg-tertiary); border-radius: 1px; }
    .spark.ok .bar    { background: #3fb950; }
    .spark.slow .bar  { background: #d29922; }
    .spark.fail .bar  { background: #f85149; }
    .bar.empty        { opacity: 0.3; }
    .actions { display: flex; gap: 0.4rem; justify-content: flex-end; margin-top: 0.4rem; }

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
</style>
