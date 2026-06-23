<script lang="ts">
    import { untrack } from 'svelte';
    import { goto } from '$app/navigation';
    import { page } from '$app/stores';
    import { ChevronDown } from 'lucide-svelte';

    interface Tab {
        id: string;
        label: string;
        badge?: number | string;
        // Optional visual tone for status-style string badges. Numbers keep
        // the neutral default. `success` = green, `warning` = amber,
        // `muted` = subdued grey (e.g. "stopped").
        badgeTone?: 'default' | 'success' | 'warning' | 'muted';
        // When true, renders a small vertical divider + extra spacing before
        // this tab. Used to visually group tabs into clusters (e.g. legacy
        // NDMS-stack tabs vs sing-box stack on /routing).
        separatorBefore?: boolean;
        // When true, the tab label is rendered extra-subdued (dormant) to signal
        // an inactive mutually-exclusive mode (e.g. the TProxy tab while FakeIP is
        // the active routing mode). Stays clickable; the `active` style overrides.
        muted?: boolean;
    }

    interface Props {
        tabs: Tab[];
        active: string;
        onchange: (id: string) => void;
        /**
         * When set, syncs the active tab with this URL query param. The
         * primitive reads it on mount / on history navigation, and writes
         * to it on user clicks via goto({ replaceState: true }). Other
         * search params are preserved.
         */
        urlParam?: string;
        /**
         * Tab id treated as the page default. When the active tab equals
         * this value, the URL param is removed (clean URL). Defaults to
         * tabs[0]?.id.
         */
        defaultTab?: string;
    }

    let { tabs, active, onchange, urlParam, defaultTab }: Props = $props();

    let containerEl: HTMLDivElement | undefined = $state();
    let measureEl: HTMLDivElement | undefined = $state();
    let visibleCount = $state(Infinity);
    let dropdownOpen = $state(false);
    let cachedContainerWidth = 0;

    // Gates the outbound URL writer until the inbound effect has had a chance
    // to apply (or rule out) the value from the URL. Conditional tabs whose
    // visibility depends on async stores ($systemInfo.isOS5,
    // $systemInfo.singbox.installed, hydrarouteInstalled) arrive AFTER mount,
    // so a deep-linked `?tab=policy` may not yet match any known tab on the
    // first inbound run. Without this flag, the outbound effect would
    // overwrite `?tab=policy` with the default tab's empty URL before the
    // conditional tab appears, losing the deep-link value.
    let urlConsumed = $state(false);

    function writeUrl(id: string) {
        if (!urlParam) return;
        const url = new URL($page.url);
        const fallback = defaultTab ?? tabs[0]?.id;
        if (id === fallback) {
            url.searchParams.delete(urlParam);
        } else {
            url.searchParams.set(urlParam, id);
        }
        const nextSearch = url.searchParams.toString();
        const currentSearch = $page.url.searchParams.toString();
        if (
            url.pathname === $page.url.pathname &&
            nextSearch === currentSearch &&
            url.hash === $page.url.hash
        ) return;
        const target = url.pathname + (nextSearch ? `?${nextSearch}` : '') + url.hash;
        void goto(target, { replaceState: true, keepFocus: true, noScroll: true });
    }

    // Inbound sync: URL → onchange. Triggers on mount, browser back/forward,
    // and any external goto() touching this query param. Silently ignores
    // values that don't match a known tab id. `active` is read via untrack
    // so this effect does NOT re-fire when the parent updates active —
    // otherwise on a click the inbound effect would race the outbound one
    // (declaration order: inbound first), see stale URL, and override the
    // user's selection by calling onchange with the previous tab.
    //
    // Sets urlConsumed in every terminal branch except "tab not yet known" —
    // that branch leaves urlConsumed=false so outbound holds off until
    // `tabs` updates (effect re-fires on the tabs.find dependency).
    $effect(() => {
        if (!urlParam) { urlConsumed = true; return; }
        const fromUrl = $page.url.searchParams.get(urlParam);
        if (fromUrl == null) { urlConsumed = true; return; }
        if (fromUrl === untrack(() => active)) { urlConsumed = true; return; }
        if (!tabs.find((t) => t.id === fromUrl)) return;
        urlConsumed = true;
        onchange(fromUrl);
    });

    // Outbound sync: active prop → URL. Catches programmatic changes
    // (e.g. parent bouncing off a hidden tab). No-op when URL already
    // matches via writeUrl's own guard. Holds off until inbound has
    // consumed the URL value to avoid clobbering deep links to async tabs.
    $effect(() => {
        if (!urlParam) return;
        if (!urlConsumed) return;
        writeUrl(active);
    });

    let visibleTabs = $derived(tabs.slice(0, visibleCount));
    let overflowTabs = $derived(tabs.slice(visibleCount));
    let hasOverflowActive = $derived(overflowTabs.some(t => t.id === active));

    // containerWidth is passed in from ResizeObserver entries (free — no forced
    // layout) and cached so the tabs-change effect can reuse the last known
    // width without reading offsetWidth again.
    function recalc(containerWidth = cachedContainerWidth) {
        if (!measureEl || containerWidth === 0) return;
        const children = Array.from(measureEl.children) as HTMLElement[];
        if (children.length === 0) return;

        // Available width minus space for the "+N" chip (≈60px)
        const chipWidth = 60;
        let usedWidth = 0;
        let tabsFit = 0;
        const totalTabs = tabs.length;

        // Children are a mix of button.tab (real tabs) and span.tab-separator
        // (visual dividers). We count only the buttons toward fits, but
        // include separator widths in the running total when we cross them.
        let pendingSeparator = 0;
        for (const child of children) {
            // getBoundingClientRect() returns fractional pixels and, inside a
            // ResizeObserver callback, does not force an additional layout.
            const w = child.getBoundingClientRect().width;
            if (child.tagName === 'SPAN') {
                // separator — accumulate; only "spent" once we accept the
                // following tab.
                pendingSeparator += w;
                continue;
            }
            const isLastTab = tabsFit === totalTabs - 1;
            const cost = w + pendingSeparator + (isLastTab ? 0 : chipWidth);
            if (usedWidth + cost <= containerWidth) {
                usedWidth += w + pendingSeparator;
                pendingSeparator = 0;
                tabsFit++;
            } else {
                break;
            }
        }

        // At least 1 tab visible
        visibleCount = Math.max(1, tabsFit);
    }

    $effect(() => {
        // Re-run when tabs change; reuse cached container width so we don't
        // force a redundant layout read here.
        void tabs.length;
        recalc();
    });

    $effect(() => {
        if (!containerEl) return;
        const ro = new ResizeObserver((entries) => {
            // contentRect.width comes free from the ResizeObserver — the
            // browser already computed layout, no forced reflow.
            cachedContainerWidth = entries[0].contentRect.width;
            recalc(cachedContainerWidth);
        });
        ro.observe(containerEl);
        return () => ro.disconnect();
    });

    function selectTab(id: string) {
        dropdownOpen = false;
        onchange(id);
    }

    // Close dropdown on outside click or ESC. Document-level listeners
    // (vs. a backdrop element) avoid dimming the page and keep the z-stack
    // flat — see app.css z-index scale.
    $effect(() => {
        if (!dropdownOpen) return;
        function handleOutside(e: MouseEvent) {
            if (!containerEl?.contains(e.target as Node)) {
                dropdownOpen = false;
            }
        }
        function handleKey(e: KeyboardEvent) {
            if (e.key === 'Escape') dropdownOpen = false;
        }
        document.addEventListener('mousedown', handleOutside);
        document.addEventListener('keydown', handleKey);
        return () => {
            document.removeEventListener('mousedown', handleOutside);
            document.removeEventListener('keydown', handleKey);
        };
    });

</script>

<div class="overflow-tabs" class:has-dropdown={dropdownOpen} bind:this={containerEl}>
    <!-- Hidden measurement row: renders all tabs offscreen to measure widths -->
    <div class="measure-row" bind:this={measureEl} aria-hidden="true">
        {#each tabs as tab, i (tab.id)}
            {#if tab.separatorBefore && i > 0}
                <span class="tab-separator"></span>
            {/if}
            <button class="tab" tabindex="-1">
                {tab.label}
                {#if tab.badge !== undefined}
                    <span class="tab-badge" class:success={tab.badgeTone === 'success'} class:warning={tab.badgeTone === 'warning'} class:muted={tab.badgeTone === 'muted'}>{tab.badge}</span>
                {/if}
            </button>
        {/each}
    </div>

    <!-- Visible tabs -->
    <div class="tab-row">
        {#each visibleTabs as tab, i (tab.id)}
            {#if tab.separatorBefore && i > 0}
                <span class="tab-separator" aria-hidden="true"></span>
            {/if}
            <button
                class="tab"
                class:active={tab.id === active}
                class:muted={tab.muted}
                onclick={() => selectTab(tab.id)}
            >
                {tab.label}
                {#if tab.badge !== undefined}
                    <span class="tab-badge" class:success={tab.badgeTone === 'success'} class:warning={tab.badgeTone === 'warning'} class:muted={tab.badgeTone === 'muted'}>{tab.badge}</span>
                {/if}
            </button>
        {/each}

        {#if overflowTabs.length > 0}
            <div class="more-wrap">
                <button
                    class="more-chip"
                    class:has-active={hasOverflowActive}
                    onclick={() => dropdownOpen = !dropdownOpen}
                >
                    +{overflowTabs.length}
                    <ChevronDown size={14} strokeWidth={2.5} style="transition: transform 0.15s;" />
                </button>

                {#if dropdownOpen}
                    <!-- svelte-ignore a11y_no_static_element_interactions a11y_click_events_have_key_events -->
                    <div class="dropdown">
                        {#each overflowTabs as tab (tab.id)}
                            <button
                                class="dropdown-item"
                                class:active={tab.id === active}
                                onclick={() => selectTab(tab.id)}
                            >
                                {tab.label}
                                {#if tab.badge !== undefined}
                                    <span class="tab-badge">{tab.badge}</span>
                                {/if}
                            </button>
                        {/each}
                    </div>
                {/if}
            </div>
        {/if}
    </div>
</div>

<style>
    /* Resting z-index 41 is intentionally raw, not a token. It creates a
       stacking context so sibling Tabs (on /routing — page-level + inner)
       paint in a predictable order, while staying BELOW
       --z-sticky-secondary (50) so sub-stickys like TunnelEditHeader paint
       over idle tab chips. When the dropdown opens, .has-dropdown bumps to
       --z-page-overlay so the dropdown lifts above sub-stickys. */
    .overflow-tabs {
        position: relative;
        z-index: 41;
        margin-bottom: 1rem;
    }

    .overflow-tabs.has-dropdown {
        z-index: var(--z-page-overlay);
    }

    .measure-row {
        display: flex;
        visibility: hidden;
        position: absolute;
        top: 0;
        left: 0;
        pointer-events: none;
        height: 0;
        overflow: hidden;
    }

    .tab-separator {
        align-self: center;
        width: 1px;
        height: 18px;
        margin: 0 0.75rem;
        background: var(--border);
        flex: 0 0 auto;
    }

    .tab-row {
        display: flex;
        align-items: stretch;
        border-bottom: 1px solid var(--border);
    }

    .tab {
        display: flex;
        align-items: center;
        gap: 0.375rem;
        padding: 0.625rem 1rem;
        background: none;
        border: none;
        border-bottom: 2px solid transparent;
        color: var(--text-muted);
        font-size: 0.875rem;
        font-weight: 500;
        cursor: pointer;
        white-space: nowrap;
        transition: color 0.15s, border-color 0.15s;
    }

    .tab:hover {
        color: var(--text-primary);
    }

    .tab.active {
        color: var(--text-primary);
        border-bottom-color: var(--accent);
    }

    /* Dormant mutually-exclusive mode (e.g. TProxy while FakeIP is active).
       Extra-subdued vs a normal inactive tab; opening it (active) restores full. */
    .tab.muted:not(.active) {
        opacity: 0.45;
    }

    .tab-badge {
        display: inline-flex;
        align-items: center;
        justify-content: center;
        min-width: 1.25rem;
        height: 1.25rem;
        padding: 0 0.375rem;
        border-radius: var(--radius-pill);
        background: var(--bg-hover);
        color: var(--text-muted);
        font-size: 0.6875rem;
        font-weight: 600;
    }

    .tab.active .tab-badge {
        background: var(--accent);
        color: var(--color-accent-contrast, #fff);
    }

    .tab-badge.success {
        background: rgba(158, 206, 106, 0.18);
        color: var(--success);
    }
    .tab-badge.warning {
        background: rgba(224, 175, 104, 0.18);
        color: var(--warning);
    }
    .tab-badge.muted {
        background: var(--bg-hover);
        color: var(--text-muted);
        opacity: 0.75;
    }
    /* Active-tab overrides keep contrast on the selected tab. */
    .tab.active .tab-badge.success,
    .tab.active .tab-badge.warning {
        color: var(--color-success-contrast, #fff);
    }
    .tab.active .tab-badge.success { background: var(--success); }
    .tab.active .tab-badge.warning {
        background: var(--warning);
        color: var(--color-warning-contrast, #fff);
    }

    /* ─── More chip ─── */
    .more-wrap {
        position: relative;
        display: flex;
        align-items: stretch;
        margin-left: auto;
    }

    .more-chip {
        display: flex;
        align-items: center;
        gap: 0.25rem;
        padding: 0.5rem 0.75rem;
        background: none;
        border: none;
        border-bottom: 2px solid transparent;
        color: var(--accent);
        font-size: 0.8rem;
        font-weight: 600;
        cursor: pointer;
        white-space: nowrap;
        transition: color 0.15s, border-color 0.15s;
    }

    .more-chip:hover {
        color: var(--accent-hover, var(--accent));
    }

    .more-chip.has-active {
        border-bottom-color: var(--accent);
    }

    /* ─── Dropdown ─── */
    .dropdown {
        position: absolute;
        top: calc(100% + 6px);
        right: 0;
        background: var(--bg-tertiary);
        border: 1px solid var(--border-bright, var(--border));
        border-radius: var(--radius);
        box-shadow: 0 8px 32px rgba(0, 0, 0, 0.5);
        min-width: 180px;
        z-index: var(--z-page-overlay);
        overflow: hidden;
    }

    @media (max-width: 768px) {
        .dropdown {
            max-height: calc(100vh - 200px);
            overflow-y: auto;
        }
    }

    .dropdown-item {
        display: flex;
        align-items: center;
        gap: 0.5rem;
        padding: 0.625rem 0.875rem;
        width: 100%;
        background: none;
        border: none;
        color: var(--text-secondary, var(--text-primary));
        font-size: 0.8125rem;
        cursor: pointer;
        text-align: left;
        transition: background 0.1s;
    }

    .dropdown-item:hover {
        background: var(--bg-hover);
    }

    .dropdown-item.active {
        color: var(--accent);
        font-weight: 600;
    }
</style>
