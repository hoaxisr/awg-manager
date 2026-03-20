<script lang="ts">
    import { Modal } from '$lib/components/ui';
    import { parseImportFile, type PortableDnsRoute } from '$lib/utils/dns-export';

    interface Props {
        open: boolean;
        existingNames: string[];
        onclose: () => void;
        onimport: (routes: PortableDnsRoute[]) => void;
    }

    let {
        open = $bindable(false),
        existingNames,
        onclose,
        onimport,
    }: Props = $props();

    let parsed = $state<PortableDnsRoute[] | null>(null);
    let selectedFlags = $state<boolean[]>([]);
    let parseError = $state('');
    let importing = $state(false);
    let wasOpen = $state(false);

    // Reset on open
    $effect(() => {
        if (open && !wasOpen) {
            parsed = null;
            selectedFlags = [];
            parseError = '';
            importing = false;
        }
        wasOpen = open;
    });

    let selectedCount = $derived(selectedFlags.filter(Boolean).length);
    let existingLower = $derived(existingNames.map(n => n.toLowerCase()));

    function isDuplicate(name: string): boolean {
        return existingLower.includes(name.toLowerCase());
    }

    async function handleFile(e: Event) {
        const input = e.target as HTMLInputElement;
        const file = input.files?.[0];
        if (!file) return;
        try {
            const text = await file.text();
            const routes = parseImportFile(text);
            if (routes.length === 0) {
                parseError = 'Не найдено валидных правил в файле';
                return;
            }
            parsed = routes;
            // Auto-select non-duplicates
            selectedFlags = routes.map(r => !isDuplicate(r.name));
        } catch (e) {
            parseError = e instanceof Error ? e.message : 'Ошибка чтения файла';
        }
    }

    function handleImport() {
        if (!parsed) return;
        const selected = parsed.filter((_, i) => selectedFlags[i]);
        importing = true;
        onimport(selected);
    }
</script>

<Modal {open} title="Импорт правил" size="md" {onclose}>
    {#if !parsed}
        <!-- File picker -->
        <div class="import-upload">
            <label class="import-label">
                Выберите .json файл с правилами
                <input type="file" accept=".json" onchange={handleFile} class="import-input" />
            </label>
            {#if parseError}
                <p class="import-error">{parseError}</p>
            {/if}
        </div>
    {:else}
        <!-- Preview list -->
        <p class="import-hint">Найдено {parsed.length} правил:</p>
        <div class="import-list">
            {#each parsed as route, i}
                <label class="import-item" class:duplicate={isDuplicate(route.name)}>
                    <input type="checkbox" bind:checked={selectedFlags[i]} disabled={importing} />
                    <span class="import-name">{route.name}</span>
                    <span class="import-meta">
                        {route.manualDomains?.length ?? 0} доменов
                        {#if route.subscriptions?.length}
                            , {route.subscriptions.length} листов
                        {/if}
                    </span>
                    {#if isDuplicate(route.name)}
                        <span class="import-dup">(дубликат)</span>
                    {/if}
                </label>
            {/each}
        </div>
    {/if}

    {#snippet actions()}
        <button class="btn btn-ghost" onclick={onclose} disabled={importing}>Отмена</button>
        {#if parsed}
            <button class="btn btn-primary" onclick={handleImport} disabled={importing || selectedCount === 0}>
                {importing ? 'Импорт...' : `Импортировать (${selectedCount})`}
            </button>
        {/if}
    {/snippet}
</Modal>

<style>
    .import-upload {
        display: flex;
        flex-direction: column;
        align-items: center;
        gap: 1rem;
        padding: 2rem;
    }

    .import-label {
        display: flex;
        flex-direction: column;
        align-items: center;
        gap: 0.5rem;
        cursor: pointer;
        color: var(--text-secondary);
        font-size: 0.875rem;
    }

    .import-input {
        font-size: 0.8125rem;
    }

    .import-error {
        color: var(--error);
        font-size: 0.8125rem;
    }

    .import-hint {
        color: var(--text-secondary);
        font-size: 0.875rem;
        margin-bottom: 0.75rem;
    }

    .import-list {
        display: flex;
        flex-direction: column;
        gap: 0.5rem;
        max-height: 400px;
        overflow-y: auto;
    }

    .import-item {
        display: flex;
        align-items: center;
        gap: 0.5rem;
        padding: 0.5rem 0.75rem;
        background: var(--bg-primary);
        border: 1px solid var(--border);
        border-radius: 6px;
        cursor: pointer;
        font-size: 0.8125rem;
    }

    .import-item.duplicate {
        opacity: 0.5;
    }

    .import-name {
        font-weight: 500;
        color: var(--text-primary);
    }

    .import-meta {
        color: var(--text-muted);
        font-size: 0.75rem;
    }

    .import-dup {
        color: var(--warning);
        font-size: 0.6875rem;
        font-style: italic;
    }
</style>
