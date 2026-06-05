<script lang="ts" module>
    export interface ChipOption {
        value: string;
        label?: string;
        // usedCount: how many *other* records already reference this value.
        // 0 / undefined = unused; > 0 = used elsewhere. The dropdown groups
        // unused entries on top and pushes used ones below a divider so the
        // picker stays scannable when most options are already in use.
        usedCount?: number;
    }
</script>

<script lang="ts">
    import { tick } from 'svelte';

    interface Props {
        values: string[];
        options: ChipOption[];
        onchange: (next: string[]) => void;
        placeholder?: string;
        allowOrphans?: boolean;
        disabled?: boolean;
    }

    let {
        values,
        options,
        onchange,
        placeholder = 'Не выбрано',
        allowOrphans = false,
        disabled = false,
    }: Props = $props();

    let open = $state(false);
    let triggerEl = $state<HTMLButtonElement | null>(null);
    let containerEl = $state<HTMLDivElement | null>(null);
    let panelEl = $state<HTMLDivElement | null>(null);
    let searchInputEl = $state<HTMLInputElement | null>(null);
    let panelTop = $state(0);
    let panelBottom = $state(0);
    let flipUp = $state(false);
    let panelRight = $state(0);
    let panelWidth = $state(0);
    let panelMaxHeight = $state(0);
    let searchQuery = $state('');

    const selectedSet = $derived(new Set(values));
    const orphanValues = $derived(
        allowOrphans ? values.filter((v) => !options.find((o) => o.value === v)) : [],
    );
    const knownChips = $derived(
        values
            .map((v) => options.find((o) => o.value === v))
            .filter((o): o is ChipOption => o !== undefined),
    );
    const dropdownItems = $derived(options.filter((o) => !selectedSet.has(o.value)));
    const allSelected = $derived(dropdownItems.length === 0);

    const filteredItems = $derived.by(() => {
        const q = searchQuery.toLowerCase().trim();
        if (!q) return dropdownItems;
        return dropdownItems.filter((o) => {
            const text = (o.label ?? o.value).toLowerCase();
            return text.includes(q) || o.value.toLowerCase().includes(q);
        });
    });
    // Group split: unused options first (so users find fresh sets without
    // scrolling past 50 already-used ones), divider, then used options sorted
    // by usage descending. Sort key is stable on equal usedCount because
    // Array.prototype.sort in V8/JSC is stable for ES2019+.
    const unusedItems = $derived(
        filteredItems.filter((o) => !(o.usedCount && o.usedCount > 0)),
    );
    const usedItems = $derived(
        filteredItems
            .filter((o): o is ChipOption & { usedCount: number } =>
                Boolean(o.usedCount && o.usedCount > 0),
            )
            .sort((a, b) => b.usedCount - a.usedCount),
    );

    function portal(node: HTMLElement, target: HTMLElement = document.body) {
        target.appendChild(node);
        return {
            destroy() {
                if (node.parentNode === target) {
                    target.removeChild(node);
                }
            },
        };
    }

    // recomputePlacement positions the panel below the trigger by default,
    // but flips up when there's more room above. Crucially, panelMaxHeight is
    // derived from the *available* viewport space rather than a fixed `60vh`
    // — without this, opening the picker low on the page would push the
    // panel below the viewport edge, hiding the bottom items even though the
    // internal scroll appeared to be at its limit.
    function recomputePlacement() {
        const el = containerEl ?? triggerEl;
        if (!el) return;
        const r = el.getBoundingClientRect();
        const margin = 16;
        const spaceBelow = window.innerHeight - r.bottom - margin;
        const spaceAbove = r.top - margin;
        const wantedHeight = 400;
        // Flip up only when below is genuinely too cramped AND above offers
        // meaningfully more room. Avoids flicker when both sides are ~equal.
        // When flipping up, anchor by `bottom` (just above the trigger) so the
        // panel grows upward to its *content* height — anchoring by `top` at
        // (trigger - panelMaxHeight) detaches a short panel to the viewport
        // top, leaving a large gap below it.
        if (spaceBelow < wantedHeight && spaceAbove > spaceBelow) {
            flipUp = true;
            panelMaxHeight = Math.max(180, spaceAbove);
            panelBottom = window.innerHeight - r.top + 4;
        } else {
            flipUp = false;
            panelMaxHeight = Math.max(180, spaceBelow);
            panelTop = r.bottom + 4;
        }
        panelWidth = r.width;
        // Right-align the panel to the trigger's right edge so it grows
        // leftward and its right edge stays flush with the chip container
        // (never spilling past the parent card). `right` is the distance from
        // the viewport's right edge; max-width (CSS) caps leftward growth so a
        // very wide panel can't run off the left side.
        panelRight = Math.max(margin, window.innerWidth - r.right);
    }

    async function toggleOpen() {
        if (disabled || allSelected) return;
        open = !open;
        if (open) {
            searchQuery = '';
            await tick();
            recomputePlacement();
            searchInputEl?.focus();
        }
    }

    function addValue(v: string) {
        if (selectedSet.has(v)) return;
        onchange([...values, v]);
    }

    function removeValue(v: string) {
        onchange(values.filter((x) => x !== v));
    }

    function handleOutsideClick(e: MouseEvent) {
        if (!open) return;
        const target = e.target as Node | null;
        if (panelEl?.contains(target as Node)) return;
        if (triggerEl?.contains(target as Node)) return;
        open = false;
    }

    function handleScroll() {
        if (open) recomputePlacement();
    }

    function handleKeydown(e: KeyboardEvent) {
        if (e.key !== 'Escape' || !open) return;
        // Two-stage Esc: first clear the search if any, then close. Matches
        // common search-dropdown UX so users don't lose their entire query
        // state when they only meant to dismiss the filter.
        if (searchQuery) {
            searchQuery = '';
            searchInputEl?.focus();
        } else {
            open = false;
            triggerEl?.focus();
        }
    }

    $effect(() => {
        if (!open) return;
        document.addEventListener('mousedown', handleOutsideClick);
        document.addEventListener('keydown', handleKeydown);
        window.addEventListener('scroll', handleScroll, true);
        window.addEventListener('resize', handleScroll);
        return () => {
            document.removeEventListener('mousedown', handleOutsideClick);
            document.removeEventListener('keydown', handleKeydown);
            window.removeEventListener('scroll', handleScroll, true);
            window.removeEventListener('resize', handleScroll);
        };
    });
</script>

<div class="picker">
    <div class="chips" bind:this={containerEl}>
        {#if values.length === 0}
            <span class="placeholder">{placeholder}</span>
        {/if}
        {#each knownChips as opt (opt.value)}
            <span class="chip">
                <span class="chip-label">{opt.label ?? opt.value}</span>
                <button
                    type="button"
                    class="chip-remove"
                    aria-label="Удалить"
                    onclick={() => removeValue(opt.value)}
                    {disabled}
                >×</button>
            </span>
        {/each}
        {#each orphanValues as v (v)}
            <span class="chip chip-orphan" title="Набор не найден в текущем конфиге">
                <span class="chip-label">{v}</span>
                <span class="chip-orphan-badge">сирота / orphaned</span>
                <button
                    type="button"
                    class="chip-remove"
                    aria-label="Удалить"
                    onclick={() => removeValue(v)}
                    {disabled}
                >×</button>
            </span>
        {/each}
        <button
            type="button"
            class="trigger"
            bind:this={triggerEl}
            onclick={toggleOpen}
            disabled={disabled || allSelected}
            aria-haspopup="listbox"
            aria-expanded={open}
        >+</button>
    </div>
</div>

{#if open}
    <div
        use:portal
        class="panel"
        bind:this={panelEl}
        style="{flipUp ? `bottom: ${panelBottom}px` : `top: ${panelTop}px`}; right: {panelRight}px; min-width: {panelWidth}px; max-height: {panelMaxHeight}px;"
        role="listbox"
    >
        <div class="search-row">
            <input
                type="text"
                class="search-input"
                bind:this={searchInputEl}
                bind:value={searchQuery}
                placeholder="Поиск…"
                aria-label="Поиск по списку"
                autocomplete="off"
                spellcheck="false"
                inputmode="search"
            />
        </div>
        <div class="list">
            {#each unusedItems as opt (opt.value)}
                <button
                    type="button"
                    class="panel-item"
                    onclick={() => addValue(opt.value)}
                    role="option"
                    aria-selected="false"
                >
                    <span class="opt-label">{opt.label ?? opt.value}</span>
                </button>
            {/each}

            {#if usedItems.length > 0 && unusedItems.length > 0}
                <div class="group-divider">Используются в других правилах</div>
            {/if}

            {#each usedItems as opt (opt.value)}
                <button
                    type="button"
                    class="panel-item panel-item-used"
                    onclick={() => addValue(opt.value)}
                    role="option"
                    aria-selected="false"
                    title="Уже используется в {opt.usedCount} прав{opt.usedCount === 1 ? 'иле' : 'илах'}"
                >
                    <span class="opt-label">{opt.label ?? opt.value}</span>
                    <span class="usage-badge">×{opt.usedCount}</span>
                </button>
            {/each}

            {#if filteredItems.length === 0}
                <div class="empty">Ничего не найдено</div>
            {/if}
        </div>
    </div>
{/if}

<style>
    .picker {
        display: block;
    }
    .chips {
        display: flex;
        flex-wrap: wrap;
        gap: 0.3rem;
        align-items: center;
        padding: 0.35rem 0.45rem;
        background: var(--bg);
        border: 1px solid var(--border);
        border-radius: 4px;
        min-height: 2rem;
    }
    .placeholder {
        color: var(--muted-text);
        font-size: 0.85rem;
        padding: 0 0.25rem;
    }
    .chip {
        display: inline-flex;
        align-items: center;
        gap: 0.25rem;
        padding: 0.15rem 0.45rem;
        background: var(--bg-tertiary, var(--surface-bg));
        border: 1px solid var(--border);
        border-radius: 999px;
        font-family: ui-monospace, monospace;
        font-size: 0.78rem;
        color: var(--text);
    }
    .chip-orphan {
        border-color: var(--warning, #e0af68);
        background: rgba(224, 175, 104, 0.12);
    }
    .chip-orphan-badge {
        font-size: 0.65rem;
        font-weight: 600;
        color: var(--warning, #e0af68);
        text-transform: uppercase;
    }
    .chip-remove {
        background: none;
        border: none;
        color: var(--muted-text);
        cursor: pointer;
        font-size: 1rem;
        line-height: 1;
        padding: 0 0.15rem;
    }
    .chip-remove:hover {
        color: var(--text);
    }
    .chip-remove:disabled {
        cursor: not-allowed;
        opacity: 0.5;
    }
    .trigger {
        background: none;
        border: 1px dashed var(--border);
        border-radius: 999px;
        color: var(--muted-text);
        cursor: pointer;
        font-size: 0.95rem;
        line-height: 1;
        padding: 0.15rem 0.55rem;
    }
    .trigger:hover:not(:disabled) {
        color: var(--text);
        border-color: var(--accent, #3b82f6);
    }
    .trigger:disabled {
        cursor: not-allowed;
        opacity: 0.4;
    }
    .panel {
        position: fixed;
        max-width: calc(100vw - 32px);
        z-index: var(--z-floating);
        background: var(--bg-tertiary, var(--surface-bg));
        border: 1px solid var(--border-bright, var(--border));
        border-radius: 4px;
        box-shadow: 0 8px 32px rgba(0, 0, 0, 0.5);
        display: flex;
        flex-direction: column;
        /* min-height: 0 on flex children below lets the inner list shrink
           and scroll instead of overflowing the panel's clipped area. */
        overflow: hidden;
    }
    .search-row {
        flex: 0 0 auto;
        padding: 0.4rem 0.5rem;
        border-bottom: 1px solid var(--border);
        background: var(--bg-tertiary, var(--surface-bg));
    }
    .search-input {
        width: 100%;
        background: var(--bg);
        border: 1px solid var(--border);
        border-radius: 4px;
        color: var(--text);
        font-family: ui-monospace, monospace;
        font-size: 0.82rem;
        padding: 0.35rem 0.5rem;
        outline: none;
    }
    .search-input:focus {
        border-color: var(--accent, #3b82f6);
    }
    .list {
        flex: 1 1 auto;
        min-height: 0;
        overflow-y: auto;
        padding: 0.25rem 0;
    }
    .panel-item {
        display: flex;
        align-items: center;
        justify-content: space-between;
        gap: 0.5rem;
        width: 100%;
        text-align: left;
        background: none;
        border: none;
        padding: 0.45rem 0.7rem;
        font-family: ui-monospace, monospace;
        font-size: 0.82rem;
        color: var(--text);
        cursor: pointer;
    }
    .panel-item:hover {
        background: var(--bg-hover);
    }
    .opt-label {
        flex: 1 1 auto;
        overflow: hidden;
        text-overflow: ellipsis;
        white-space: nowrap;
    }
    .panel-item-used .opt-label {
        color: var(--muted-text);
    }
    .usage-badge {
        flex: 0 0 auto;
        font-size: 0.7rem;
        font-weight: 600;
        color: var(--muted-text);
        background: var(--bg);
        border: 1px solid var(--border);
        border-radius: 999px;
        padding: 0.05rem 0.4rem;
    }
    .group-divider {
        padding: 0.4rem 0.7rem 0.2rem;
        margin-top: 0.25rem;
        border-top: 1px solid var(--border);
        font-size: 0.7rem;
        font-weight: 600;
        color: var(--muted-text);
        text-transform: uppercase;
        letter-spacing: 0.04em;
        background: var(--bg);
    }
    .empty {
        padding: 0.6rem 0.7rem;
        font-family: ui-monospace, monospace;
        font-size: 0.8rem;
        color: var(--muted-text);
        text-align: center;
    }
</style>
