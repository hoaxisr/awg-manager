<script lang="ts">
    import { onMount } from 'svelte';
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
    const SOFT_CARD_LATENCY_EVENT = 'awgm:soft-card-latency-refresh';

    interface Props {
        subscription: Subscription;
        activeMember: SubscriptionMember;
    }
    let { subscription, activeMember }: Props = $props();

    let pickerOpen = $state(false);
    let checking = $state(false);
    let speedtestOpen = $state(false);
    let showServer = $state(false);
    let lastSoftWarmupAt = 0;

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
    const traffic = $derived($singboxTraffic.get(activeMember.tag));

    type State = 'ok' | 'slow' | 'fail' | 'unknown';
    const cardState: State = $derived.by(() => {
        if (latest < 0) return 'unknown';
        if (latest <= 0) return 'fail';
        if (latest < DELAY_OK) return 'ok';
        if (latest < DELAY_SLOW) return 'slow';
        return 'fail';
    });
    const latText = $derived.by(() => {
        if (cardState === 'unknown') return '—';
        if (cardState === 'fail') return 'timeout';
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

    const memberSNI = $derived.by(() => {
        const m = activeMember as unknown as Record<string, unknown>;
        const candidates = [
            m.sni,
            m.serverName,
            m.server_name,
            m.tlsServerName,
            m.tls_server_name,
        ];
        for (const v of candidates) {
            if (typeof v === 'string' && v.trim() !== '') return v.trim();
        }
        return '';
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

    async function runSoftDelayWarmup(): Promise<void> {
        if (checking) return;
        const now = Date.now();
        if (now - lastSoftWarmupAt < 15_000) return;
        lastSoftWarmupAt = now;
        await triggerCheck();
    }

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

    onMount(() => {
        const onSoftRefresh = (event: Event) => {
            const detail = (event as CustomEvent<{ target?: string }>).detail;
            if (detail?.target !== 'singbox') return;
            void runSoftDelayWarmup();
        };
        window.addEventListener(SOFT_CARD_LATENCY_EVENT, onSoftRefresh as EventListener);
        return () =>
            window.removeEventListener(SOFT_CARD_LATENCY_EVENT, onSoftRefresh as EventListener);
    });
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

    <div class="server-row">
        <span class="label">Сервер</span>
        <div class="picker-anchor">
            <button
                class="server-btn"
                onclick={(e) => {
                    e.stopPropagation();
                    pickerOpen = !pickerOpen;
                }}
                aria-haspopup="listbox"
                aria-expanded={pickerOpen}
            >
                <span class="server-value-wrap">
                    {#if showServer}
                        <span class="server-text">{activeMember.server}</span>
                    {:else}
                        <span class="server-hidden">●●●●●●●●</span>
                    {/if}
                    <span
                        class="icon-btn"
                        role="button"
                        tabindex="0"
                        aria-label={showServer ? 'Скрыть сервер и SNI' : 'Показать сервер и SNI'}
                        onclick={(e) => {
                            e.stopPropagation();
                            showServer = !showServer;
                        }}
                        onkeydown={(e) => {
                            if (e.key === 'Enter' || e.key === ' ') {
                                e.preventDefault();
                                e.stopPropagation();
                                showServer = !showServer;
                            }
                        }}
                    >
                        <svg viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2">
                            <path d="M1 12s4-8 11-8 11 8 11 8-4 8-11 8-11-8-11-8z"/>
                            <circle cx="12" cy="12" r="3"/>
                        </svg>
                    </span>
                    <span class="port">:{activeMember.port}</span>
                </span>
                <span class="caret" aria-hidden="true">▾</span>
            </button>
            {#if pickerOpen}
                <SubscriptionMemberPicker
                    members={subscription.members ?? []}
                    activeMemberTag={subscription.activeMember}
                    onPick={pickMember}
                    onClose={() => (pickerOpen = false)}
                />
            {/if}
        </div>
    </div>

    {#if memberSNI}
        <div class="server-row">
            <span class="label">SNI</span>
            <span class="sni-value">
                {#if showServer}
                    {memberSNI}
                {:else}
                    <span class="server-hidden">●●●●●●●●</span>
                {/if}
            </span>
        </div>
    {/if}

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
    }
    .server-btn:hover { border-color: var(--color-accent); }
    .server-text {
        font-family: var(--font-mono, ui-monospace, monospace);
        font-size: 0.78rem;
        overflow: hidden;
        text-overflow: ellipsis;
        white-space: nowrap;
    }
    .server-value-wrap {
        display: inline-flex;
        align-items: center;
        gap: 0.45rem;
        min-width: 0;
    }
    .server-hidden {
        font-family: var(--font-mono, ui-monospace, monospace);
        letter-spacing: 1px;
        color: var(--color-text-primary);
    }
    .icon-btn {
        display: inline-flex;
        align-items: center;
        justify-content: center;
        width: 20px;
        height: 20px;
        border: none;
        background: transparent;
        color: var(--color-text-muted);
        cursor: pointer;
        padding: 0;
    }
    .icon-btn svg {
        width: 14px;
        height: 14px;
    }
    .icon-btn:hover { color: var(--color-accent); }
    .port {
        font-family: var(--font-mono, ui-monospace, monospace);
        color: var(--color-text-primary);
        white-space: nowrap;
    }
    .sni-value {
        font-family: var(--font-mono, ui-monospace, monospace);
        font-size: 0.78rem;
        color: var(--color-text-primary);
    }
    .caret { color: var(--color-text-muted); font-size: 0.7rem; }
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
</style>
