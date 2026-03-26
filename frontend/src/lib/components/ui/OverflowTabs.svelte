<script lang="ts">
    interface Tab {
        id: string;
        label: string;
        badge?: number;
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
        const children = measureEl.children;
        if (children.length === 0) return;

        // Available width minus space for the "+N" chip (≈60px)
        const containerWidth = containerEl.offsetWidth;
        const chipWidth = 60;
        let usedWidth = 0;
        let fits = 0;

        for (let i = 0; i < children.length; i++) {
            const childWidth = (children[i] as HTMLElement).offsetWidth;
            const needsChip = i < children.length - 1;
            if (usedWidth + childWidth + (needsChip ? chipWidth : 0) <= containerWidth) {
                usedWidth += childWidth;
                fits++;
            } else {
                break;
            }
        }

        // At least 1 tab visible
        visibleCount = Math.max(1, fits);
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

    function handleWindowClick(e: MouseEvent) {
        if (dropdownOpen) {
            dropdownOpen = false;
        }
    }
</script>

<svelte:window onclick={handleWindowClick} />

<div class="overflow-tabs" bind:this={containerEl}>
    <!-- Hidden measurement row: renders all tabs offscreen to measure widths -->
    <div class="measure-row" bind:this={measureEl} aria-hidden="true">
        {#each tabs as tab (tab.id)}
            <button class="tab" tabindex="-1">
                {tab.label}
                {#if tab.badge !== undefined}
                    <span class="tab-badge">{tab.badge}</span>
                {/if}
            </button>
        {/each}
    </div>

    <!-- Visible tabs -->
    <div class="tab-row">
        {#each visibleTabs as tab (tab.id)}
            <button
                class="tab"
                class:active={tab.id === active}
                onclick={() => selectTab(tab.id)}
            >
                {tab.label}
                {#if tab.badge !== undefined}
                    <span class="tab-badge">{tab.badge}</span>
                {/if}
            </button>
        {/each}

        {#if overflowTabs.length > 0}
            <div class="more-wrap">
                <button
                    class="more-chip"
                    class:has-active={hasOverflowActive}
                    onclick={(e) => { e.stopPropagation(); dropdownOpen = !dropdownOpen; }}
                >
                    +{overflowTabs.length}
                    <svg viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2.5" stroke-linecap="round" stroke-linejoin="round">
                        <path d="M6 9l6 6 6-6"/>
                    </svg>
                </button>

                {#if dropdownOpen}
                    <!-- svelte-ignore a11y_no_static_element_interactions a11y_click_events_have_key_events -->
                    <div class="dropdown" onclick={(e) => e.stopPropagation()} onkeydown={(e) => e.stopPropagation()}>
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
    .overflow-tabs {
        position: relative;
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
        border-radius: 9999px;
        background: var(--bg-hover);
        color: var(--text-muted);
        font-size: 0.6875rem;
        font-weight: 600;
    }

    .tab.active .tab-badge {
        background: var(--accent);
        color: #fff;
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

    .more-chip svg {
        width: 14px;
        height: 14px;
        transition: transform 0.15s;
    }

    /* ─── Dropdown ─── */
    .dropdown {
        position: absolute;
        top: calc(100% + 4px);
        right: 0;
        background: var(--bg-card, var(--bg));
        border: 1px solid var(--border);
        border-radius: 8px;
        box-shadow: 0 4px 12px rgba(0, 0, 0, 0.12);
        min-width: 180px;
        z-index: 50;
        overflow: hidden;
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
