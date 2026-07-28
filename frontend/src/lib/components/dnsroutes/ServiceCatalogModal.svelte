<script lang="ts">
    import { Modal, Button, Dropdown } from '$lib/components/ui';
    import ServiceIcon from './ServiceIcon.svelte';
    import { presetCatalog, presetCatalogLoaded, loadPresetCatalog } from '$lib/stores/presets';
    import { buildRoutingTunnelDropdownOptions } from '$lib/utils/routingTunnelOptions';
    import { pluralize, SERVICE_WORDS } from '$lib/utils/pluralize';
    import {
        addedCompositesByMember,
        applyPresetToggle,
        catalogCardMarker,
        catalogPresetCardNotice,
        dnsRouteCatalogPresetFilter,
        findCoveringPreset,
        isPresetFullyAdded,
        memberOfAddedCompositeTitle,
        normalizeCatalogSelection,
        presetAddedBadge,
        presetDnsLargeListRisk,
        presetSetReuseBadge,
    } from '$lib/utils/catalog-preset';
    import type { RoutingTunnel, CatalogPreset } from '$lib/types';
    import DownloadRouteNote from '$lib/components/downloads/DownloadRouteNote.svelte';
    import { Layers, Search } from 'lucide-svelte';

    type FooterMode = 'none' | 'tunnel';

    interface Props {
        open: boolean;
        title?: string;
        /** Preset filter; default: any DNS-capable preset (incl. composites via covers). */
        presetFilter?: (p: CatalogPreset, catalog: CatalogPreset[]) => boolean;
        /** Dim/disable tiles already present in the list (NDMS). */
        markExisting?: boolean;
        existingNames?: string[];
        /** sing-box: rule-set tags already in config. Powers the non-disabling
         *  «уже входит в добавленный композитный список» mark on member presets (#450)
         *  and, when not disabled via markExisting, the «набор уже есть» reuse badge. */
        existingRuleSetTags?: string[];
        /** sing-box: rule_set tag → number of referencing rules; differentiates
         *  «добавлено» vs «добавлено, без правил» on disabled tiles. */
        ruleSetUsage?: Map<string, number>;
        footer?: FooterMode;
        tunnels?: RoutingTunnel[];
        isOS5?: boolean;
        hydrarouteInstalled?: boolean;
        /** When false, only one preset can be selected (HR Neo). */
        multiple?: boolean;
        /** Pre-select when opening (e.g. sing-box router wizard). */
        initialSelectedIds?: string[];
        /** NDMS / HR Neo: red warning on presets with large DNS lists. */
        warnLargeDnsLists?: boolean;
        confirmLabel?: string;
        submitting?: boolean;
        onclose: () => void;
        /** Picker footer — return selected presets (HR Neo / inline pick). */
        onconfirm?: (presets: CatalogPreset[]) => void;
        /** Tunnel footer — batch create (NDMS). */
        oncreate?: (
            presets: CatalogPreset[],
            tunnelId: string,
            backend: 'ndms' | 'hydraroute',
        ) => void;
    }

    let {
        open = $bindable(false),
        title = 'Каталог сервисов',
        presetFilter = dnsRouteCatalogPresetFilter,
        markExisting = false,
        existingNames = [],
        existingRuleSetTags = [],
        ruleSetUsage = undefined,
        footer = 'none',
        tunnels = [],
        isOS5 = false,
        hydrarouteInstalled = false,
        multiple = true,
        initialSelectedIds = [],
        warnLargeDnsLists = true,
        confirmLabel,
        submitting = false,
        onclose,
        onconfirm,
        oncreate,
    }: Props = $props();

    let selected = $state<Set<string>>(new Set());
    let defaultTunnelId = $state('');
    let backend = $state<'ndms' | 'hydraroute'>('ndms');
    let wasOpen = $state(false);
    let query = $state('');
    let categoryFilter = $state<string>('all');

    const CATEGORY_LABELS: Record<string, string> = {
        social: 'Соцсети',
        media: 'Медиа',
        ai: 'AI',
        developer: 'Разработка',
        cloud: 'Облако',
        gaming: 'Игры',
        block: 'Блок',
    };
    const CATEGORY_ORDER = ['social', 'media', 'ai', 'developer', 'cloud', 'gaming', 'block'];

    let showBackendSelector = $derived(footer === 'tunnel' && isOS5 && hydrarouteInstalled);
    let noTunnels = $derived(tunnels.filter((t) => t.available).length === 0);
    let showTunnelFooter = $derived(footer === 'tunnel');

    const tunnelOpts = $derived(
        buildRoutingTunnelDropdownOptions(tunnels, { requireSelectable: true }),
    );
    let existingLower = $derived(existingNames.map((n) => n.toLowerCase()));

    const catalogPresets = $derived.by(() => {
        const all = $presetCatalog;
        return all.filter((p) => presetFilter(p, all));
    });

    const sortedPresets = $derived(
        [...catalogPresets].sort((a, b) => a.name.localeCompare(b.name, 'ru')),
    );

    // Memoized once per catalog/tags change (not per card): tag set + member → added composite.
    const existingTagsSet = $derived(new Set(existingRuleSetTags));
    const addedCompositeByMember = $derived(
        addedCompositesByMember(sortedPresets, existingTagsSet),
    );

    function matchesQuery(p: CatalogPreset, q: string): boolean {
        if (!q) return true;
        const hay = `${p.name} ${p.id} ${p.category}`.toLowerCase();
        return hay.includes(q.toLowerCase());
    }

    const queryTrimmed = $derived(query.trim());

    const queryFiltered = $derived(
        sortedPresets.filter((p) => matchesQuery(p, queryTrimmed)),
    );

    const catalogCategories = $derived.by(() => {
        const present = new Set(sortedPresets.map((p) => p.category));
        const ordered = CATEGORY_ORDER.filter((c) => present.has(c));
        for (const c of present) {
            if (!CATEGORY_ORDER.includes(c)) ordered.push(c);
        }
        return ordered;
    });

    const showCategoryChips = $derived(catalogCategories.length > 1);

    const categoryCounts = $derived.by(() => {
        const counts = new Map<string, number>();
        for (const p of queryFiltered) {
            counts.set(p.category, (counts.get(p.category) ?? 0) + 1);
        }
        return counts;
    });

    const filteredPresets = $derived(
        queryFiltered.filter(
            (p) => categoryFilter === 'all' || p.category === categoryFilter,
        ),
    );

    const selectedWithNotices = $derived(
        sortedPresets.filter(
            (p) =>
                selected.has(p.id) &&
                (p.notice || (warnLargeDnsLists && presetDnsLargeListRisk(p, sortedPresets))),
        ),
    );

    function noticeText(preset: CatalogPreset): string {
        return catalogPresetCardNotice(preset, warnLargeDnsLists, sortedPresets) ?? '';
    }

    function noticeIsLargeList(preset: CatalogPreset): boolean {
        return warnLargeDnsLists && presetDnsLargeListRisk(preset, sortedPresets);
    }

    const primaryLabel = $derived.by(() => {
        if (confirmLabel) return confirmLabel;
        if (showTunnelFooter) return `Создать (${selected.size})`;
        if (!multiple) return 'Выбрать';
        return `Выбрать (${selected.size})`;
    });

    $effect(() => {
        if (open && !wasOpen) {
            selected = normalizeCatalogSelection(new Set(initialSelectedIds), sortedPresets);
            defaultTunnelId = tunnels.find((t) => t.available)?.id ?? '';
            backend = isOS5 ? 'ndms' : hydrarouteInstalled ? 'hydraroute' : 'ndms';
            query = '';
            categoryFilter = 'all';
        }
        wasOpen = open;
    });

    $effect(() => {
        if (open) void loadPresetCatalog();
    });

    function categoryLabel(cat: string): string {
        return CATEGORY_LABELS[cat] ?? cat;
    }

    function isAdded(preset: CatalogPreset): boolean {
        return markExisting && existingLower.includes(preset.name.toLowerCase());
    }

    function toggle(presetId: string) {
        selected = applyPresetToggle(selected, presetId, sortedPresets, multiple);
    }

    function coveringPreset(presetId: string) {
        return findCoveringPreset(presetId, selected, sortedPresets);
    }

    function selectedPresets(): CatalogPreset[] {
        return sortedPresets.filter((p) => selected.has(p.id));
    }

    function handlePrimary() {
        const presets = selectedPresets();
        if (presets.length === 0) return;
        if (showTunnelFooter) {
            if (!defaultTunnelId || !oncreate) return;
            oncreate(presets, defaultTunnelId, backend);
            return;
        }
        onconfirm?.(presets);
    }

    let primaryDisabled = $derived(
        selected.size === 0 || submitting || (showTunnelFooter && noTunnels),
    );
</script>

<Modal {open} {title} size="wide" bodyLayout="fill" {onclose}>
    <div class="catalog-root">
        {#if $presetCatalogLoaded && catalogPresets.length > 0}
            <div class="search-row">
                <div class="search">
                    <Search size={14} color="var(--text-muted)" />
                    <input
                        type="search"
                        placeholder="netflix, telegram, ai..."
                        bind:value={query}
                    />
                    <span class="search-count">{pluralize(filteredPresets.length, SERVICE_WORDS)}</span>
                </div>
                <div class="chips-slot" class:has-chips={showCategoryChips}>
                    {#if showCategoryChips}
                        <div class="chips">
                            <button
                                type="button"
                                class="chip"
                                class:active={categoryFilter === 'all'}
                                aria-pressed={categoryFilter === 'all'}
                                onclick={() => (categoryFilter = 'all')}
                            >
                                <span class="chip-label">Все</span>
                                <span class="chip-count">{queryFiltered.length}</span>
                            </button>
                            {#each catalogCategories as cat (cat)}
                                <button
                                    type="button"
                                    class="chip"
                                    class:active={categoryFilter === cat}
                                    aria-pressed={categoryFilter === cat}
                                    onclick={() => (categoryFilter = cat)}
                                >
                                    <span class="chip-label">{categoryLabel(cat)}</span>
                                    <span class="chip-count">{categoryCounts.get(cat) ?? 0}</span>
                                </button>
                            {/each}
                        </div>
                    {/if}
                </div>
            </div>
        {/if}

        <div class="catalog-scroll">
            {#if !$presetCatalogLoaded}
                <p class="catalog-loading">Загрузка каталога…</p>
            {:else if catalogPresets.length === 0}
                <p class="catalog-loading">Каталог пуст</p>
            {:else if filteredPresets.length === 0}
                <p class="catalog-loading">По запросу ничего не нашлось.</p>
            {:else}
                <div class="preset-grid">
                    {#each filteredPresets as preset (preset.id)}
                        {@const added = isAdded(preset)}
                        {@const coveredBy = coveringPreset(preset.id)}
                        {@const isSelected = selected.has(preset.id)}
                        {@const largeDnsWarn = noticeIsLargeList(preset)}
                        {@const cardNotice = noticeText(preset)}
                        {@const memberComposite = addedCompositeByMember.get(preset.id)}
                        {@const marker = catalogCardMarker({
                            added,
                            coveredBySelection: !!coveredBy,
                            ownSetAdded: isPresetFullyAdded(preset, existingTagsSet),
                            memberOfAddedComposite: !!memberComposite,
                        })}
                        {@const addedBadge =
                            marker === 'added' ? presetAddedBadge(preset, ruleSetUsage) : undefined}
                        {@const reuseBadge =
                            marker === 'own-set-added' ? presetSetReuseBadge() : undefined}
                        {@const memberTitle =
                            marker === 'member-of-added' && memberComposite
                                ? memberOfAddedCompositeTitle(memberComposite.name)
                                : undefined}
                        {@const stateTooltip =
                            addedBadge?.tooltip ?? reuseBadge?.tooltip ?? memberTitle}
                        <button
                            type="button"
                            class="preset-card"
                            class:selected={isSelected}
                            class:added
                            class:covered={!!coveredBy}
                            title={coveredBy
                                ? `Уже включено в «${coveredBy.name}»`
                                : [cardNotice, stateTooltip].filter(Boolean).join('\n\n') ||
                                  undefined}
                            onclick={() => {
                                if (!added && !coveredBy) toggle(preset.id);
                            }}
                            disabled={added || !!coveredBy || submitting}
                        >
                            {#if isSelected}
                                <span class="preset-check">&#10003;</span>
                            {:else if addedBadge}
                                <span class="preset-badge" title={addedBadge.tooltip}
                                    >{addedBadge.text}</span>
                            {:else if coveredBy}
                                <span class="preset-badge">в {coveredBy.name}</span>
                            {:else if reuseBadge}
                                <span class="preset-badge preset-badge-help" title={reuseBadge.tooltip}
                                    >{reuseBadge.text}</span>
                            {:else if memberTitle && memberComposite}
                                <span class="preset-badge preset-badge-help" title={memberTitle}
                                    >в {memberComposite.name}</span>
                            {/if}
                            <span class="corner-left">
                                {#if largeDnsWarn}
                                    <span
                                        class="preset-notice-mark preset-notice-mark-danger"
                                        aria-label="большой DNS-список"
                                    >⚠</span>
                                {:else if preset.notice}
                                    <span class="preset-notice-mark" aria-label="примечание">⚠</span>
                                {/if}
                                {#if memberTitle}
                                    <span
                                        class="preset-member-mark"
                                        role="img"
                                        aria-label="уже входит в добавленный композитный список"
                                        title={memberTitle}
                                    >
                                        <Layers size={12} aria-hidden="true" />
                                    </span>
                                {/if}
                            </span>
                            <ServiceIcon name={preset.name} iconSlug={preset.iconSlug} size={40} />
                            <span class="preset-name">{preset.name}</span>
                        </button>
                    {/each}
                </div>
            {/if}
        </div>

        {#if showTunnelFooter || selectedWithNotices.length > 0}
            <div class="catalog-pin">
                {#if selectedWithNotices.length > 0}
                    <div class="notices-panel">
                        {#each selectedWithNotices as p (p.id)}
                            {@const largeDns = noticeIsLargeList(p)}
                            <div class="notice-entry" class:notice-entry-danger={largeDns}>
                                <span class="notice-icon" class:notice-icon-danger={largeDns}>⚠</span>
                                <div class="notice-body">
                                    <strong class="notice-title">{p.name}</strong>
                                    <span class="notice-text notice-text-multiline">{noticeText(p)}</span>
                                </div>
                            </div>
                        {/each}
                    </div>
                {/if}

                {#if showTunnelFooter}
                    <div class="tunnel-row">
                        {#if showBackendSelector}
                            <span class="tunnel-label">Движок</span>
                            <div class="tunnel-control tunnel-control-engine">
                                <Dropdown
                                    bind:value={backend}
                                    options={[
                                        { value: 'ndms' as const, label: 'NDMS' },
                                        { value: 'hydraroute' as const, label: 'HydraRoute Neo' },
                                    ]}
                                    disabled={submitting}
                                    fullWidth
                                />
                            </div>
                        {/if}
                        <span class="tunnel-label">Туннель</span>
                        <div class="tunnel-control tunnel-control-main">
                            <Dropdown
                                bind:value={defaultTunnelId}
                                options={tunnelOpts}
                                disabled={submitting}
                                fullWidth
                            />
                        </div>
                    </div>
                    <DownloadRouteNote
                        text="Если сервис использует URL-лист, он будет получен через"
                    />
                    {#if noTunnels}
                        <p class="no-tunnels">Создайте хотя бы один туннель</p>
                    {/if}
                {/if}
            </div>
        {/if}
    </div>

    {#snippet actions()}
        <Button variant="ghost" onclick={onclose} disabled={submitting}>Отмена</Button>
        <Button
            variant="primary"
            onclick={handlePrimary}
            disabled={primaryDisabled}
            loading={submitting}
        >
            {primaryLabel}
        </Button>
    {/snippet}
</Modal>

<style>
    .catalog-root {
        display: flex;
        flex-direction: column;
        flex: 1;
        min-height: min(560px, calc(100dvh - 12rem));
        max-height: min(72vh, calc(100dvh - 11rem));
    }

    .search-row {
        flex: 0 0 auto;
        padding: 0.75rem 1rem;
        border-bottom: 1px solid var(--border);
        display: flex;
        flex-direction: column;
        gap: 0.625rem;
    }

    .chips-slot {
        min-height: 0;
        flex-shrink: 0;
    }

    .chips-slot.has-chips {
        min-height: 1.875rem;
    }

    .search {
        display: flex;
        align-items: center;
        gap: 8px;
        padding: 8px 12px;
        border-radius: var(--radius-sm);
        background: var(--color-bg-primary);
        border: 1px solid var(--color-border);
    }

    .search input {
        flex: 1;
        min-width: 0;
        background: transparent;
        border: 0;
        outline: none;
        color: var(--color-text-primary);
        font-size: 13px;
        font-family: inherit;
    }

    .search-count {
        font-size: 11px;
        color: var(--color-text-muted);
        font-family: var(--font-mono);
        white-space: nowrap;
    }

    .chips {
        display: flex;
        flex-wrap: wrap;
        gap: 6px;
    }

    .chip {
        display: inline-flex;
        align-items: center;
        gap: 6px;
        padding: 4px 10px;
        border-radius: 999px;
        background: transparent;
        border: 1px solid var(--color-border);
        color: var(--color-text-secondary);
        font-size: 11.5px;
        font-weight: 500;
        cursor: pointer;
        font-family: inherit;
    }

    .chip.active {
        background: var(--accent-soft);
        border-color: var(--accent-line, var(--color-accent));
        color: var(--color-accent);
        font-weight: 600;
    }

    .chip-count {
        color: var(--color-text-muted);
        font-family: var(--font-mono);
        font-size: 10px;
    }

    .chip.active .chip-count {
        color: var(--color-accent);
    }

    .catalog-scroll {
        flex: 1 1 auto;
        min-height: 22rem;
        overflow-y: auto;
        overflow-x: hidden;
        padding: 0.75rem 1rem;
    }

    .catalog-pin {
        flex: 0 0 auto;
        padding: 0.5rem 1rem 0.75rem;
        border-top: 1px solid var(--border);
        background: var(--bg-secondary);
        display: flex;
        flex-direction: column;
        gap: 0.5rem;
    }

    .preset-grid {
        display: grid;
        grid-template-columns: repeat(auto-fill, minmax(132px, 1fr));
        gap: 10px;
        align-items: stretch;
    }

    .preset-card {
        display: flex;
        flex-direction: column;
        align-items: center;
        justify-content: flex-start;
        gap: 0.375rem;
        height: 100%;
        min-height: 96px;
        padding: 0.875rem 0.5rem;
        background: var(--color-bg-primary);
        border: 2px solid var(--color-border);
        border-radius: 10px;
        cursor: pointer;
        transition: border-color 0.15s;
        position: relative;
    }

    .preset-card:hover:not(.added) {
        border-color: var(--color-text-muted);
    }

    .preset-card.selected {
        border-color: var(--color-accent);
    }

    .preset-card.added {
        opacity: 0.4;
        cursor: not-allowed;
    }

    .preset-card.covered {
        opacity: 0.45;
        cursor: not-allowed;
    }

    .catalog-loading {
        color: var(--color-text-muted);
        font-size: 0.8125rem;
        text-align: center;
        padding: 1.5rem 0;
    }

    .preset-check {
        position: absolute;
        top: 6px;
        right: 6px;
        width: 18px;
        height: 18px;
        border-radius: 4px;
        background: var(--color-accent);
        color: var(--color-accent-contrast, #fff);
        font-size: 11px;
        display: flex;
        align-items: center;
        justify-content: center;
    }

    .preset-badge {
        position: absolute;
        top: 6px;
        right: 6px;
        max-width: calc(100% - 44px);
        overflow: hidden;
        text-overflow: ellipsis;
        white-space: nowrap;
        font-size: 0.5625rem;
        color: var(--color-text-muted);
    }

    .preset-badge-help {
        cursor: help;
    }

    /* Top-left marks stack horizontally when notice ⚠ and member mark coexist. */
    .corner-left {
        position: absolute;
        top: 6px;
        left: 6px;
        display: flex;
        align-items: center;
        gap: 4px;
    }

    .preset-notice-mark {
        font-size: 0.875rem;
        color: var(--warning, #f59e0b);
        cursor: help;
        line-height: 1;
    }

    .preset-notice-mark-danger {
        color: var(--color-error, #ef4444);
    }

    .preset-member-mark {
        display: inline-flex;
        align-items: center;
        color: var(--color-accent);
        cursor: help;
        line-height: 1;
    }

    .preset-name {
        flex: 1;
        display: flex;
        align-items: center;
        justify-content: center;
        width: 100%;
        font-size: 0.6875rem;
        font-weight: 500;
        color: var(--color-text-primary);
        text-align: center;
        line-height: 1.25;
        word-break: break-word;
    }

    .tunnel-row {
        display: flex;
        align-items: center;
        flex-wrap: wrap;
        gap: 0.375rem 0.625rem;
    }

    .tunnel-label {
        color: var(--color-text-muted);
        font-size: 0.75rem;
        white-space: nowrap;
        flex-shrink: 0;
    }

    .tunnel-control {
        min-width: 0;
    }

    .tunnel-control-engine {
        width: min(180px, 100%);
    }

    .tunnel-control-main {
        flex: 1 1 12rem;
        min-width: 10rem;
    }

    .no-tunnels {
        color: var(--color-error);
        font-size: 0.8125rem;
        margin: 0;
    }

    .notices-panel {
        display: flex;
        flex-direction: column;
        gap: 0.5rem;
        padding: 0.625rem 0.75rem;
        background: rgba(245, 158, 11, 0.08);
        border: 1px solid rgba(245, 158, 11, 0.25);
        border-radius: 6px;
    }

    .notice-entry {
        display: flex;
        align-items: flex-start;
        gap: 0.5rem;
    }

    .notice-icon {
        color: var(--warning, #f59e0b);
        font-size: 0.875rem;
        line-height: 1.4;
        flex-shrink: 0;
    }

    .notice-body {
        display: flex;
        flex-direction: column;
        gap: 0.125rem;
        font-size: 0.75rem;
        line-height: 1.4;
        color: var(--color-text-secondary);
    }

    .notice-title {
        color: var(--color-text-primary);
        font-weight: 500;
        font-size: 0.75rem;
    }

    .notice-text {
        color: var(--color-text-secondary);
    }

    .notice-text-multiline {
        white-space: pre-line;
    }

    .notice-entry-danger .notice-title,
    .notice-entry-danger .notice-text {
        color: var(--color-error, #ef4444);
    }

    .notice-icon-danger {
        color: var(--color-error, #ef4444);
    }

    .notices-panel:has(.notice-entry-danger) {
        background: rgba(239, 68, 68, 0.08);
        border-color: rgba(239, 68, 68, 0.28);
    }

    @media (max-width: 640px) {
        .search-row {
            padding: 0.625rem 0.75rem;
        }

        .catalog-scroll {
            padding: 0.625rem 0.75rem;
        }

        .catalog-pin {
            padding: 0.625rem 0.75rem 0.75rem;
        }

        .chips {
            flex-wrap: nowrap;
            overflow-x: auto;
            padding-bottom: 4px;
        }

        .preset-grid {
            grid-template-columns: repeat(auto-fill, minmax(108px, 1fr));
        }
    }
</style>
