<script lang="ts">
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
    }

    interface Props {
        tabs: Tab[];
        active: string;
        onchange: (id: string) => void;
    }

    let { tabs, active, onchange }: Props = $props();

    let containerEl: HTMLDivElement | undefined = $state();
    let measureEl: HTMLDivElement | undefined = $state();
    let visibleCount = $state(Infinity);
    let dropdownOpen = $state(false);

    let visibleTabs = $derived(tabs.slice(0, visibleCount));
    let overflowTabs = $derived(tabs.slice(visibleCount));
    let hasOverflowActive = $derived(overflowTabs.some(t => t.id === active));

    function recalc() {
        if (!containerEl || !measureEl) return;
        const children = Array.from(measureEl.children) as HTMLElement[];
        if (children.length === 0) return;

        // Available width minus space for the "+N" chip (≈60px)
        const containerWidth = containerEl.offsetWidth;
        const chipWidth = 60;
        let usedWidth = 0;
        let tabsFit = 0;
        const totalTabs = tabs.length;

        // Children are a mix of button.tab (real tabs) and span.tab-separator
        // (visual dividers). We count only the buttons toward fits, but
        // include separator widths in the running total when we cross them.
        let pendingSeparator = 0;
        for (const child of children) {
            const w = child.offsetWidth;
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
        // Re-run when tabs change
        void tabs.length;
        recalc();
    });

    $effect(() => {
        if (!containerEl) return;
        const ro = new ResizeObserver(() => recalc());
        ro.observe(containerEl);
        return () => ro.disconnect();
    });

    function selectTab(id: string) {
        dropdownOpen = false;
        onchange(id);
    }

</script>

{#if dropdownOpen}
    <!-- svelte-ignore a11y_no_static_element_interactions a11y_click_events_have_key_events -->
    <div class="backdrop" onclick={() => dropdownOpen = false} onkeydown={() => {}}></div>
{/if}

<div class="overflow-tabs" bind:this={containerEl}>
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
                    <svg viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2.5" stroke-linecap="round" stroke-linejoin="round">
                        <path d="M6 9l6 6 6-6"/>
                    </svg>
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
    .backdrop {
        position: fixed;
        inset: 0;
        background: rgba(0, 0, 0, 0.3);
        z-index: 40;
    }

    .overflow-tabs {
        position: relative;
        z-index: 41;
        margin-bottom: 1rem;
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
        color: #fff;
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
        color: #fff;
    }
    .tab.active .tab-badge.success { background: var(--success); }
    .tab.active .tab-badge.warning { background: var(--warning); }

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

    .more-chip svg {
        width: 14px;
        height: 14px;
        transition: transform 0.15s;
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
        z-index: 50;
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
