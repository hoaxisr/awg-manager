<script lang="ts">
    import { untrack } from 'svelte';
    import { Eye, EyeOff } from 'lucide-svelte';
    import { goto } from '$app/navigation';
    import { api } from '$lib/api/client';
    import { Badge, Button, Modal, TrafficChart, TrafficSparkline, TunnelListActions } from '$lib/components/ui';
    import {
        TunnelDelaySparkBars,
        TunnelListEndpointLine,
        TunnelListTrafficCell,
        TunnelMetaText,
        TunnelSingboxPingButton,
        TunnelTitleRow,
    } from '$lib/components/tunnels';
    import { singboxDelayFromHistory } from '$lib/utils/singboxDelay';
    import { singboxDelayStatusDot } from '$lib/utils/statusDot';
    import { getTrafficRates, subscribeTraffic, loadHistory } from '$lib/stores/traffic';
    import {
        singboxDelayHistory,
        singboxTraffic,
        triggerDelayCheck,
    } from '$lib/stores/singbox';
    import { subscriptionsStore } from '$lib/stores/subscriptions';
    import { notifications } from '$lib/stores/notifications';
    import type { Subscription, SubscriptionMember } from '$lib/types';
    import { formatBitRate, formatBytes, formatRelativeTime } from '$lib/utils/format';
    import { isCardNestedInteraction } from '$lib/utils/cardClick';
    import { showOutboundReferencedError } from '$lib/utils/outboundReferenced';
    import SubscriptionMemberPicker from './SubscriptionMemberPicker.svelte';
    import type { SingboxLayoutMode } from '$lib/constants/singboxLayout';
    import TunnelDiagnosticsModal from '$lib/components/testing/TunnelDiagnosticsModal.svelte';

    interface Props {
        subscription: Subscription;
        activeMember: SubscriptionMember;
        autoDelayCheckNonce?: number;
        autoDelayCheckDelayMs?: number;
        layout?: SingboxLayoutMode;
        renderMode?: import('$lib/constants/singboxLayout').TunnelRenderMode;
        ondetail?: (tag: string) => void;
    }
    let {
        subscription,
        activeMember,
        autoDelayCheckNonce = 0,
        autoDelayCheckDelayMs = 0,
        layout = 'compact',
        renderMode = 'compact',
        ondetail,
    }: Props = $props();

    let pickerOpen = $state(false);
    let checking = $state(false);
    let showEndpoint = $state(false);
    let confirmDeleteOpen = $state(false);
    let deleting = $state(false);
    let diagnosticsOpen = $state(false);

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
    const selectorTag = $derived(subscription.selectorTag ?? '');
    const diagnosticsUnavailableReason = $derived(
        !selectorTag || !kernelIface
            ? 'Для подписки не удалось определить интерфейс тестирования.'
            : undefined,
    );

    const history = $derived($singboxDelayHistory.get(activeMember.tag) ?? []);
    const delayPresentation = $derived(singboxDelayFromHistory(history));
    const traffic = $derived($singboxTraffic.get(activeMember.tag));

    const trafficSparkSeries = $derived.by(() => {
        const n = Math.min(rxRates.length, txRates.length);
        if (n === 0) return { rx: [] as number[], tx: [] as number[] };
        const take = Math.min(36, n);
        const start = n - take;
        return {
            rx: rxRates.slice(start, n),
            tx: txRates.slice(start, n),
        };
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
    const endpointText = $derived(`${activeMember.server}:${activeMember.port}`);
    const hiddenEndpointText = $derived(`••••••••:${activeMember.port}`);
    const inlineRxRate = $derived(rxRates.length > 0 ? rxRates[rxRates.length - 1] : 0);
    const inlineTxRate = $derived(txRates.length > 0 ? txRates[txRates.length - 1] : 0);
    /** List row: title above IP — prefer remark, else outbound tag. */
    const listActiveServerName = $derived(
        activeMember.label?.trim() || activeMember.tag?.trim() || '',
    );
    const activeEndpointTitle = $derived(
        listActiveServerName ? `${listActiveServerName} · ${endpointText}` : endpointText,
    );
    const isURLTest = $derived(subscription.mode === 'urltest');
    /** URL feed vs inline server list (wizard: «Подписка» / «Группа серверов»). */
    const isInlineGroup = $derived(subscription.isInline || !subscription.url?.trim());
    const sourceKindLabel = $derived(isInlineGroup ? 'группа' : 'подписка');
    const lastFetchedHuman = $derived(
        subscription.lastFetched ? formatRelativeTime(subscription.lastFetched) : '—',
    );

    const cardState = $derived(delayPresentation.state);
    const latText = $derived(delayPresentation.label);
    const statusDot = $derived.by(() => {
        if (subscription.lastError) {
            return { variant: 'error' as const, pulse: false, label: 'error' };
        }
        return singboxDelayStatusDot(cardState, true);
    });
    const protocolLabel = $derived.by(() => {
        switch (activeMember.protocol) {
            case 'vless':         return 'VLESS';
            case 'trojan':        return 'Trojan';
            case 'shadowsocks':   return 'Shadowsocks';
            case 'hysteria2':     return 'Hysteria2';
            case 'naive':         return 'Naive';
            case 'mieru':         return 'Mieru';
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

    function openDetail(e?: MouseEvent | KeyboardEvent): void {
        if (e && isCardNestedInteraction(e)) return;
        goto(`/subscriptions/${subscription.id}`);
    }

    function openSettings(): void {
        goto(`/subscriptions/${subscription.id}?tab=settings`);
    }

    async function removeSubscription(): Promise<void> {
        if (deleting) return;
        deleting = true;
        try {
            await api.deleteSubscription(subscription.id);
            await subscriptionsStore.refetch();
            confirmDeleteOpen = false;
        } catch (e) {
            const name = subscription.label || subscription.selectorTag || subscription.id;
            if (showOutboundReferencedError(e, name, 'Подписка')) {
                confirmDeleteOpen = false;
            } else {
                notifications.error(e instanceof Error ? e.message : 'Не удалось удалить подписку');
            }
        } finally {
            deleting = false;
        }
    }

</script>

{#if renderMode === 'table'}
    <tr
        class="sbx-sub-active-row"
        class:ok={cardState === 'ok'}
        class:slow={cardState === 'slow'}
        class:fail={cardState === 'fail'}
        class:unknown={cardState === 'unknown'}
        onclick={(e) => openDetail(e)}
        onkeydown={(e) => {
            if (e.key === 'Enter' || e.key === ' ') {
                e.preventDefault();
                openDetail(e);
            }
        }}
        role="button"
        tabindex="0"
    >
            <td class="tunnel-list-cell tunnel-list-cell--delay lc lc-delay" data-label="Delay">
                {#if subscription.lastError}
                    <span class="delay-inline-err mono" title={subscription.lastError}>
                        {subscription.lastError}
                    </span>
                {:else}
                    <TunnelSingboxPingButton
                        layout="list"
                        label={latText}
                        state={cardState}
                        {checking}
                        onclick={(e) => {
                            e.stopPropagation();
                            void triggerCheck(e);
                        }}
                    />
                {/if}
            </td>
            <td class="tunnel-list-cell tunnel-list-cell--name lc lc-name" data-label="Подписка">
                <div class="tunnel-list-name-stack">
                    <TunnelTitleRow
                        title={subscription.label}
                        dotVariant={statusDot.variant}
                        dotPulse={statusDot.pulse}
                        staticTitle
                    />
                    <TunnelMetaText>
                        <span>{subscription.memberTags.length} серверов</span>
                        <span class="meta-dot" aria-hidden="true">·</span>
                        <span>{lastFetchedHuman}</span>
                    </TunnelMetaText>
                    <TunnelMetaText mono>
                        {#if proxyIface}
                            <span>{proxyIface}</span>
                            {#if kernelIface}<span class="meta-dot" aria-hidden="true">·</span><span>{kernelIface}</span>{/if}
                            <span class="meta-dot" aria-hidden="true">·</span>
                        {/if}
                        <span>{isURLTest ? 'URLTest' : 'Selector'}</span>
                    </TunnelMetaText>
                </div>
            </td>
            <td class="tunnel-list-cell tunnel-list-cell--endpoint lc lc-endpoint" data-label="Активный сервер" title={activeEndpointTitle}>
                <div class="lc-endpoint-stack">
                    {#if listActiveServerName}
                        <span class="lc-endpoint-name" title={listActiveServerName}>{listActiveServerName}</span>
                    {/if}
                    <TunnelListEndpointLine
                        host={activeMember.server}
                        port={activeMember.port}
                        bind:show={showEndpoint}
                    />
                </div>
            </td>
            <td
                class="tunnel-list-cell tunnel-list-cell--traffic lc lc-traffic"
                data-label="Трафик"
            >
                {#if subscription.lastError}
                    <span class="delay-dash">—</span>
                {:else}
                    <TunnelListTrafficCell
                        rxRate={inlineRxRate}
                        txRate={inlineTxRate}
                        rxData={trafficSparkSeries.rx}
                        txData={trafficSparkSeries.tx}
                        onclick={() => ondetail?.(activeMember.tag)}
                        title="Открыть детальный график"
                    />
                {/if}
            </td>
            <td
                class="tunnel-list-cell tunnel-list-cell--ping lc"
                data-label="Ping"
            >
                {#if subscription.lastError}
                    <span class="delay-dash">—</span>
                {:else}
                    <TunnelDelaySparkBars
                        {history}
                        state={cardState}
                        layout="list"
                        onclick={() => void triggerCheck()}
                        title="Delay за последние проверки"
                    />
                {/if}
            </td>
            <td
                class="tunnel-list-cell tunnel-list-cell--actions lc lc-actions col-actions"
                data-label=""
            >
                <TunnelListActions
                    onEdit={openSettings}
                    editLabel="Изменить"
                    editTitle="Настройки подписки «{subscription.label}»"
                    onTest={() => (diagnosticsOpen = true)}
                    testTitle="Открыть диагностику подписки «{subscription.label}»"
                    onDelete={() => (confirmDeleteOpen = true)}
                    deleteTitle="Удалить подписку «{subscription.label}»"
                />
            </td>
    </tr>
{:else if layout === 'dense' || renderMode === 'list-card'}
<div
    class="card view-dense card-clickable"
    class:view-list={renderMode === 'list-card'}
    class:ok={cardState === 'ok'}
    class:slow={cardState === 'slow'}
    class:fail={cardState === 'fail'}
    class:unknown={cardState === 'unknown'}
    role="button"
    tabindex="0"
    onclick={(e) => openDetail(e)}
    onkeydown={(e) => {
        if (e.key === 'Enter' || e.key === ' ') {
            e.preventDefault();
            openDetail(e);
        }
    }}
>
    <div class="header header-dense">
        <div class="header-dense-body">
            <div class="title-row-dense">
                <TunnelTitleRow
                    title={subscription.label}
                    dotVariant={statusDot.variant}
                    dotPulse={statusDot.pulse}
                    dense
                    staticTitle
                >
                    {#snippet badges()}
                        <Badge variant="accent" size="sm">{sourceKindLabel}</Badge>
                    {/snippet}
                </TunnelTitleRow>
            </div>
            <div class="meta-tags-dense">
                <span class="iface-dense">
                    {#if proxyIface}
                        <span>{proxyIface}</span>
                        {#if kernelIface}<span class="meta-dot" aria-hidden="true">·</span><span>{kernelIface}</span>{/if}
                        <span class="meta-dot" aria-hidden="true">·</span><span>{subscription.inboundTag}</span>
                    {:else}
                        <span>{subscription.inboundTag}</span>
                    {/if}
                </span>
                <span class="badge proto">{protocolLabel}</span>
                {#if activeMember.transport && activeMember.transport !== 'tcp'}
                    <span class="badge transport">{activeMember.transport.toUpperCase()}</span>
                {/if}
                {#if activeMember.security === 'reality'}
                    <span class="badge reality">Reality</span>
                {:else if activeMember.security === 'tls'}
                    <span class="badge tls">TLS</span>
                {/if}
                <span class="badge mode">{isURLTest ? 'URLTest' : 'Selector'}</span>
            </div>
        </div>
        <div class="dense-toolbar">
            <div class="dense-toolbar-bottom">
                <TunnelSingboxPingButton
                    layout="dense"
                    label={latText}
                    state={cardState}
                    {checking}
                    onclick={triggerCheck}
                />
            </div>
        </div>
    </div>

    {#if renderMode !== 'list-card'}
    <div class="details">
    {#if subscription.lastError}
        <div class="sub-error mono">{subscription.lastError}</div>
    {/if}

    <div class="details-dense-cols">
        <div class="details-dense-col">
            <div class="kv-stacked-stat">
                <span class="kv-stacked-label">{isURLTest ? 'Авто' : 'Активный сервер'}</span>
                <span class="kv-endpoint">
                    <span
                        class="kv-stacked-value"
                        title={showEndpoint ? activeEndpointTitle : (listActiveServerName || hiddenEndpointText)}
                    >
                        {#if showEndpoint}
                            {endpointText}
                        {:else if listActiveServerName}
                            {listActiveServerName}
                        {:else}
                            {hiddenEndpointText}
                        {/if}
                    </span>
                    <button
                        type="button"
                        class="eye-btn"
                        onclick={(e) => {
                            e.stopPropagation();
                            showEndpoint = !showEndpoint;
                        }}
                        aria-label={showEndpoint ? 'Скрыть IP' : 'Показать IP'}
                    >
                        {#if showEndpoint}
                            <Eye size={12} aria-hidden="true" />
                        {:else}
                            <EyeOff size={12} aria-hidden="true" />
                        {/if}
                    </button>
                </span>
            </div>
        </div>
    </div>
    <div class="dense-meta-line mono">
        <span>{subscription.memberTags.length} серверов</span>
        <span>обновлено: {lastFetchedHuman}</span>
    </div>
    </div>
    {/if}

    {#if renderMode === 'list-card'}
    <div class="list-card-endpoint mono">
        <span class="list-card-endpoint-label">{isURLTest ? 'Авто' : 'Активен'}</span>
        <span
            class="list-card-endpoint-value"
            title={showEndpoint ? activeEndpointTitle : (listActiveServerName || subscription.activeMember || activeMember.tag)}
        >
            {#if showEndpoint}
                {endpointText}
            {:else if listActiveServerName}
                {listActiveServerName}
            {:else}
                {subscription.activeMember || activeMember.tag}
            {/if}
        </span>
    </div>
    {/if}

    <div class="actions">
        <TunnelListActions
            variant="labeled"
            onEdit={openSettings}
            editLabel="Изменить"
            editTitle="Настройки подписки «{subscription.label}»"
            onTest={() => (diagnosticsOpen = true)}
            testTitle="Тест подписки «{subscription.label}»"
            onDelete={() => (confirmDeleteOpen = true)}
            deleteTitle="Удалить подписку «{subscription.label}»"
        />
    </div>

    {#if renderMode !== 'list-card' && !subscription.lastError}
        <div class="charts-dense">
            <button
                type="button"
                class="traffic-inline"
                onclick={() => ondetail?.(activeMember.tag)}
                title="Открыть график трафика"
            >
                <TrafficSparkline
                    rxData={trafficSparkSeries.rx}
                    txData={trafficSparkSeries.tx}
                    responsive
                    height={20}
                />
                <div class="traffic-inline-rates">
                    <span class="traffic-inline-rate rx">↓ {formatBitRate(inlineRxRate)}</span>
                    <span class="traffic-inline-rate tx">↑ {formatBitRate(inlineTxRate)}</span>
                </div>
            </button>
            <div class="chart-inline delay-inline">
                <div class="chart-inline-head">
                    <span class="chart-inline-label">Delay (5 мин)</span>
                </div>
                <TunnelDelaySparkBars
                    {history}
                    state={cardState}
                    layout="dense"
                    onclick={() => void triggerCheck()}
                />
            </div>
        </div>
    {/if}
</div>
{:else}
<div
    class="card view-compact card-clickable"
    class:ok={cardState === 'ok'}
    class:slow={cardState === 'slow'}
    class:fail={cardState === 'fail'}
    class:unknown={cardState === 'unknown'}
    role="button"
    tabindex="0"
    onclick={(e) => openDetail(e)}
    onkeydown={(e) => {
        if (e.key === 'Enter' || e.key === ' ') {
            e.preventDefault();
            openDetail(e);
        }
    }}
>
    <div class="tunnel-card-intro">
    <div class="title-row">
        <TunnelTitleRow
            title={subscription.label}
            dotVariant={statusDot.variant}
            dotPulse={statusDot.pulse}
            staticTitle
        >
            {#snippet badges()}
                <Badge variant="accent" size="sm">{sourceKindLabel}</Badge>
            {/snippet}
        </TunnelTitleRow>
        <TunnelSingboxPingButton
            layout="compact"
            label={latText}
            state={cardState}
            {checking}
            onclick={triggerCheck}
        />
    </div>
    <div class="iface">
        {#if proxyIface}
            <span>{proxyIface}</span>
            {#if kernelIface}<span class="meta-dot" aria-hidden="true">·</span><span>{kernelIface}</span>{/if}
            <span class="meta-dot" aria-hidden="true">·</span><span>{subscription.inboundTag}</span>
        {:else}
            <span>{subscription.inboundTag}</span>
        {/if}
        <span class="meta-dot" aria-hidden="true">·</span><span>:{subscription.listenPort}</span>
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

    <div class="server-section">
    <div class="server-row">
        <span class="label">{isURLTest ? 'Авто' : 'Активный сервер'}</span>
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
                        class:mono={showEndpoint || !listActiveServerName}
                        title={showEndpoint ? activeEndpointTitle : (listActiveServerName || hiddenEndpointText)}
                    >
                        {#if showEndpoint}
                            {endpointText}
                        {:else if listActiveServerName}
                            {listActiveServerName}
                        {:else}
                            {hiddenEndpointText}
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
                        <Eye size={14} aria-hidden="true" />
                    {:else}
                        <EyeOff size={14} aria-hidden="true" />
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
    </div>

    <div class="actions actions--bar">
        <TunnelListActions
            variant="labeled"
            onEdit={openSettings}
            editLabel="Изменить"
            editTitle="Настройки подписки «{subscription.label}»"
            onTest={() => (diagnosticsOpen = true)}
            testTitle="Открыть диагностику подписки «{subscription.label}»"
            onDelete={() => (confirmDeleteOpen = true)}
            deleteTitle="Удалить подписку «{subscription.label}»"
        />
    </div>

    {#if !subscription.lastError}
        <div class="chart-section">
            <div class="chart-body">
                <div class="chart-head">
                    <span>Delay (5 мин)</span>
                </div>
                <TunnelDelaySparkBars
                    {history}
                    state={cardState}
                    layout="compact"
                    onclick={() => void triggerCheck()}
                />
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
    {/if}
</div>
{/if}

<TunnelDiagnosticsModal
    open={diagnosticsOpen}
    kind="subscription"
    targetId={selectorTag}
    displayName={subscription.label || selectorTag || subscription.id}
    subjectLabel="подписку"
    iface={kernelIface}
    loading={false}
    unavailableReason={diagnosticsUnavailableReason}
    onclose={() => (diagnosticsOpen = false)}
/>

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
        gap: 10px;
        padding: 12px 14px;
        border: 1px solid var(--color-border);
        border-radius: var(--radius);
        background: var(--color-bg-secondary);
        color: var(--color-text-primary);
        transition: border-color var(--t-fast) ease;
    }
    .card.ok { border-color: var(--color-success-border); }
    .card.slow { border-color: var(--color-warning-border); }
    .card.fail { border-color: var(--color-error-border); }
    .card.unknown { border-color: var(--color-border); }

    .card.card-clickable {
        cursor: pointer;
    }

    @media (hover: hover) and (pointer: fine) {
        .card.card-clickable:hover {
            border-color: var(--color-accent-border);
        }
    }

    .card.card-clickable:focus-visible {
        outline: 2px solid var(--color-accent);
        outline-offset: 2px;
    }

    .card.view-dense {
        gap: 8px;
        padding: 10px 12px;
    }

    .card.view-dense .header.header-dense {
        display: grid;
        grid-template-columns: minmax(0, 1fr) auto;
        align-items: flex-start;
        gap: 6px;
    }

    .header-dense-body {
        display: flex;
        flex-direction: column;
        gap: 1px;
        min-width: 0;
    }

    .title-row-dense {
        display: flex;
        align-items: center;
        gap: 5px;
        min-width: 0;
        overflow: hidden;
    }

    .title-row-dense :global(.badge) {
        flex-shrink: 0;
        font-size: 9px;
        padding: 1px 5px;
    }

    .meta-tags-dense {
        display: flex;
        flex-wrap: wrap;
        align-items: center;
        margin-top: 3px;
        gap: 3px;
        min-width: 0;
    }

    .iface-dense {
        display: inline-flex;
        flex-wrap: wrap;
        align-items: center;
        gap: 0;
        font-size: 9px;
        font-family: var(--font-mono, monospace);
        color: var(--color-text-muted);
        min-width: 0;
    }

    .meta-dot {
        margin: 0 0.35em;
        opacity: 0.75;
    }

    .card.view-dense .meta-tags-dense .badge {
        font-size: 9px;
        padding: 1px 5px;
        line-height: 1.3;
    }

    .card.view-dense .badge.mode {
        background: rgba(100, 100, 100, 0.3);
        color: var(--color-text-muted);
    }

    .dense-toolbar {
        display: flex;
        flex-direction: column;
        align-items: flex-end;
        flex-shrink: 0;
    }

    .dense-toolbar-bottom { display: flex; align-items: center; }

    .details-dense-cols {
        display: grid;
        grid-template-columns: minmax(0, 1fr);
        gap: 10px 10px;
        align-items: start;
    }

    .details-dense-col {
        display: flex;
        flex-direction: column;
        gap: 6px;
        min-width: 0;
    }

    .dense-meta-line {
        display: flex;
        flex-wrap: wrap;
        align-items: center;
        justify-content: flex-start;
        gap: 0.35rem 0.75rem;
        min-width: 0;
        font-size: 9px;
        line-height: 1.25;
        color: var(--color-text-muted);
    }

    .dense-meta-line span {
        white-space: nowrap;
    }

    .list-card-endpoint {
        display: flex;
        align-items: baseline;
        gap: 0.35rem;
        min-width: 0;
        font-size: 10px;
        line-height: 1.3;
        color: var(--color-text-muted);
    }

    .list-card-endpoint-label {
        flex: 0 0 auto;
        text-transform: uppercase;
        letter-spacing: 0.04em;
        font-size: 9px;
    }

    .list-card-endpoint-value {
        flex: 1 1 auto;
        min-width: 0;
        overflow: hidden;
        text-overflow: ellipsis;
        white-space: nowrap;
        color: var(--color-text-secondary);
    }

    .kv-stacked-stat {
        display: flex;
        flex-direction: column;
        gap: 1px;
        min-width: 0;
    }

    .card.view-dense .details-dense-cols .kv-stacked-stat {
        flex-direction: row;
        align-items: center;
        gap: 0.35rem;
    }

    .card.view-dense .details-dense-cols .kv-stacked-label {
        flex: 0 0 auto;
    }

    .card.view-dense .details-dense-cols .kv-endpoint {
        flex: 1 1 auto;
        min-width: 0;
    }

    .card.view-dense .details-dense-cols .kv-stacked-value {
        min-width: 0;
    }

    .card.view-dense .kv-endpoint {
        display: flex;
        align-items: center;
        gap: 2px;
        min-width: 0;
    }

    .kv-stacked-label {
        font-size: 9px;
        text-transform: uppercase;
        letter-spacing: 0.04em;
        color: var(--color-text-muted);
    }

    .kv-stacked-value {
        font-size: 10px;
        font-family: var(--font-mono, monospace);
        color: var(--color-text-secondary);
        white-space: nowrap;
        overflow: hidden;
        text-overflow: ellipsis;
    }

    .card.view-dense .details {
        display: flex;
        flex-direction: column;
        gap: 6px;
        padding: 4px 0 8px;
        border-top: 1px solid var(--color-border);
        border-bottom: 1px solid var(--color-border);
    }

    .charts-dense {
        display: flex;
        flex-direction: row;
        align-items: stretch;
        gap: 4px;
        width: 100%;
        min-width: 0;
    }

    .charts-dense > .delay-inline,
    .charts-dense > .traffic-inline {
        flex: 1 1 0;
        min-width: 0;
        width: auto;
    }

    .chart-inline {
        display: flex;
        flex-direction: column;
        gap: 3px;
        min-width: 0;
        padding: 5px 6px;
        border: 1px solid var(--color-border);
        border-radius: var(--radius-sm);
        background: var(--color-bg-secondary);
        font: inherit;
        color: inherit;
        text-align: left;
    }

    .chart-inline.delay-inline {
        gap: 0;
        padding: 0;
        overflow: hidden;
    }

    .chart-inline.delay-inline .chart-inline-head {
        padding: 5px 6px 3px;
    }

    .charts-dense .traffic-inline {
        display: flex;
        align-items: center;
        gap: 0.3rem;
        padding: 5px 4px 5px 5px;
        border: 1px solid var(--color-border);
        border-radius: var(--radius-sm);
        background: var(--color-bg-secondary);
        cursor: pointer;
        font: inherit;
        color: inherit;
        text-align: left;
    }

    .charts-dense .traffic-inline :global(svg.responsive) {
        flex: 1 1 auto;
        width: 100%;
        min-width: 0;
    }

    .traffic-inline:hover {
        background: var(--color-bg-hover);
    }

    .traffic-inline:focus-visible {
        outline: 2px solid var(--color-accent);
        outline-offset: 2px;
    }

    .charts-dense .traffic-inline-rates {
        display: flex;
        flex-direction: column;
        gap: 0.06rem;
        min-width: 0;
        flex: 0 0 auto;
        font-size: 9px;
        line-height: 1.1;
        font-family: var(--font-mono, monospace);
    }

    .charts-dense .traffic-inline-rate {
        max-width: 100%;
        overflow: hidden;
        text-overflow: ellipsis;
        white-space: nowrap;
    }

    .charts-dense .traffic-inline-rate.rx { color: var(--color-accent); }
    .charts-dense .traffic-inline-rate.tx { color: var(--color-success); }

    .chart-inline-head {
        display: flex;
        justify-content: space-between;
        align-items: baseline;
        gap: 6px;
        font-size: 9px;
        line-height: 1.2;
    }

    .chart-inline-label {
        color: var(--color-text-muted);
        text-transform: uppercase;
        letter-spacing: 0.04em;
        font-weight: 500;
    }

    .charts-dense .chart-inline-head {
        gap: 4px;
    }

    .title-row {
        display: flex;
        align-items: center;
        gap: 0.5rem;
        min-width: 0;
    }
    .title-row :global(.tunnel-title-row) {
        flex: 1;
        min-width: 0;
    }
    .title-row :global(.ping-btn) {
        flex-shrink: 0;
        margin-left: auto;
    }
    .title-row :global(.badge) {
        flex-shrink: 0;
    }
    .iface {
        display: flex;
        flex-wrap: wrap;
        align-items: center;
        font-size: var(--sbx-card-meta);
        color: var(--color-text-muted);
        font-family: var(--font-mono, ui-monospace, monospace);
    }
    .badges { display: flex; gap: 0.4rem; flex-wrap: wrap; }
    .badge {
        font-size: var(--sbx-card-badge);
        padding: 2px 8px;
        border-radius: 10px;
        font-weight: 500;
    }
    .badge.proto    { background: rgba(88,166,255,0.15); color: var(--color-accent); }
    .badge.transport{ background: var(--color-bg-tertiary); color: var(--color-text-muted); }
    .badge.tls      { background: rgba(63,185,80,0.15); color: #3fb950; }
    .badge.reality  { background: rgba(210,153,34,0.15); color: #d29922; }
    .server-section {
        padding-top: 8px;
        border-top: 1px dashed var(--color-border);
    }
    .server-row {
        display: grid;
        grid-template-columns: max-content minmax(0, 1fr);
        gap: 0.45rem;
        align-items: center;
        margin: 0;
    }
    .label {
        color: var(--color-text-muted);
        font-size: var(--sbx-card-label);
        text-transform: uppercase;
        letter-spacing: 0.04em;
    }
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
        min-width: 0;
        padding: 0.4rem 0.55rem;
        background: var(--color-bg-primary);
        border: 1px solid var(--color-border);
        border-radius: 4px;
        font: inherit;
        font-size: var(--sbx-card-value);
        color: var(--color-text-primary);
        cursor: pointer;
        min-width: 0;
    }
    .server-btn:hover { border-color: var(--color-accent); }
    .server-btn-readonly { cursor: default; }
    .server-btn-readonly:hover { border-color: var(--color-border); }
    .server-text {
        font-size: var(--sbx-card-value);
        overflow: hidden;
        display: block;
        text-overflow: ellipsis;
        white-space: nowrap;
        word-break: normal;
        overflow-wrap: normal;
        min-width: 0;
    }
    .server-text.mono {
        font-family: var(--font-mono, ui-monospace, monospace);
        font-size: var(--sbx-card-value);
    }
    .caret { color: var(--color-text-muted); font-size: var(--sbx-card-note); }
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

    .chart-head {
        display: flex;
        justify-content: space-between;
        font-size: var(--sbx-card-label);
        color: var(--color-text-muted);
        text-transform: uppercase;
        letter-spacing: 0.04em;
    }
    .chart-head .stats {
        font-size: var(--sbx-card-value);
    }
    .chart-head.traffic-head .stats {
        font-size: 0.6875rem;
    }
    .stats { font-family: var(--font-mono, ui-monospace, monospace); }
    .chart-section {
        margin: 0 -14px -12px;
        border-radius: 0 0 var(--radius) var(--radius);
        background: var(--color-bg-secondary);
        overflow: hidden;
    }
    .chart-body {
        padding: 8px 12px 10px;
    }
    .traffic-head { margin-top: 8px; }

    .sub-meta {
        font-size: var(--sbx-card-meta);
        color: var(--color-text-muted);
        line-height: 1.35;
        display: flex;
        flex-direction: column;
        gap: 0.25rem;
    }
    .sub-error {
        font-size: var(--sbx-card-meta);
        color: #f85149;
    }
    .mono {
        font-family: var(--font-mono, ui-monospace, monospace);
    }

    /* Dense delay chartlet — same inset as traffic sparkline box */
    .card.view-dense .chart-inline.delay-inline {
        gap: 3px;
        padding: 5px 4px 5px 5px;
        overflow: hidden;
    }

    .card.view-dense .chart-inline.delay-inline .chart-inline-head {
        padding: 0;
    }

    .chart-section {
        margin: 0 -14px -12px;
        border-radius: 0 0 var(--radius) var(--radius);
        background: var(--color-bg-secondary);
        overflow: hidden;
    }

    .chart-body {
        padding: 8px 12px 8px;
    }

    .chart-body :global(.tunnel-delay-spark--compact) {
        height: 36px;
    }

    .sbx-sub-active-row {
        cursor: pointer;
    }
    .sbx-sub-active-row:focus-visible {
        outline: 2px solid var(--color-accent);
        outline-offset: -2px;
    }
    .lc {
        display: flex;
        align-items: center;
        min-width: 0;
        font-size: var(--sbx-card-value);
        color: var(--color-text-secondary);
        vertical-align: middle;
    }
    .lc-delay {
        gap: 0.35rem;
        min-width: 0;
    }
    .delay-inline-err {
        font-size: var(--sbx-card-badge);
        line-height: 1.25;
        color: #f85149;
        overflow: hidden;
        text-overflow: ellipsis;
        white-space: nowrap;
        min-width: 0;
        flex: 1;
    }
    .delay-dash {
        font-size: var(--sbx-card-value);
        color: var(--color-text-muted);
    }
    .lc-name {
        flex-direction: column;
        align-items: flex-start !important;
        gap: 0.15rem;
    }
    .lc-endpoint {
        display: flex;
        align-items: center;
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
        font-size: var(--sbx-card-value);
        font-weight: 500;
        color: var(--color-text-primary);
        line-height: 1.2;
        overflow: hidden;
        text-overflow: ellipsis;
        white-space: nowrap;
    }
    .lc-actions {
        flex-wrap: nowrap;
        gap: 0.375rem;
        justify-content: center;
        align-items: center;
        white-space: nowrap;
    }
</style>
