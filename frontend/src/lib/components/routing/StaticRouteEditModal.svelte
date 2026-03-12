<script lang="ts">
    import type { StaticRouteList, TunnelListItem } from '$lib/types';
    import { Modal } from '$lib/components/ui';

    interface Props {
        open: boolean;
        route: StaticRouteList | null;
        tunnels: TunnelListItem[];
        saving: boolean;
        onsave: (data: Partial<StaticRouteList>) => void;
        onimport: (tunnelID: string, name: string, content: string) => void;
        onclose: () => void;
    }

    let { open, route, tunnels, saving, onsave, onimport, onclose }: Props = $props();

    let name = $state('');
    let tunnelID = $state('');
    let subnetsText = $state('');

    $effect(() => {
        if (open) {
            if (route) {
                name = route.name;
                tunnelID = route.tunnelID;
                subnetsText = route.subnets.join('\n');
            } else {
                name = '';
                tunnelID = tunnels.length > 0 ? tunnels[0].id : '';
                subnetsText = '';
            }
        }
    });

    let isEdit = $derived(route !== null);
    let title = $derived(isEdit ? `Редактирование: ${route?.name ?? ''}` : 'Новый список маршрутов');

    let canSave = $derived(
        name.trim() !== '' &&
        tunnelID !== '' &&
        subnetsText.trim() !== ''
    );

    function handleSave() {
        const subnets = subnetsText
            .split('\n')
            .map(s => s.trim())
            .filter(s => s !== '');
        onsave({
            ...(route ? { id: route.id } : {}),
            name: name.trim(),
            tunnelID,
            subnets,
            enabled: route?.enabled ?? true,
        });
    }

    function handleImportClick() {
        const input = document.createElement('input');
        input.type = 'file';
        input.accept = '.bat,.txt';
        input.onchange = async () => {
            const file = input.files?.[0];
            if (!file) return;
            const content = await file.text();
            const importName = file.name.replace(/\.(bat|txt)$/i, '');
            const importTunnelID = tunnelID || (tunnels.length > 0 ? tunnels[0].id : '');
            if (importTunnelID) {
                onimport(importTunnelID, importName, content);
            }
        };
        input.click();
    }
</script>

<Modal {open} {title} size="md" onclose={onclose}>
    <div class="form-group">
        <!-- svelte-ignore a11y_label_has_associated_control -->
        <label class="form-label">Название</label>
        <input
            class="form-input"
            type="text"
            placeholder="Рабочие сервисы"
            value={name}
            oninput={(e) => { name = (e.target as HTMLInputElement).value; }}
        />
    </div>

    <div class="form-group">
        <label class="form-label" for="sr-tunnel">Туннель</label>
        <select
            class="form-select"
            id="sr-tunnel"
            value={tunnelID}
            onchange={(e) => { tunnelID = (e.target as HTMLSelectElement).value; }}
        >
            <option value="">Выберите туннель</option>
            {#each tunnels as tunnel}
                <option value={tunnel.id}>{tunnel.name}</option>
            {/each}
        </select>
    </div>

    <div class="form-group">
        <!-- svelte-ignore a11y_label_has_associated_control -->
        <label class="form-label">Подсети (по одной на строку)</label>
        <textarea
            class="form-textarea"
            rows="8"
            placeholder="10.0.0.0/8&#10;192.168.1.0/24&#10;172.16.0.0/12"
            value={subnetsText}
            oninput={(e) => { subnetsText = (e.target as HTMLTextAreaElement).value; }}
        ></textarea>
    </div>

    {#snippet actions()}
        <button class="btn btn-secondary btn-sm" onclick={handleImportClick} disabled={saving}>
            Импорт .bat
        </button>
        <div class="spacer"></div>
        <button class="btn btn-secondary" onclick={onclose}>Отмена</button>
        <button class="btn btn-accent" onclick={handleSave} disabled={!canSave || saving}>
            {saving ? 'Сохранение...' : 'Сохранить'}
        </button>
    {/snippet}
</Modal>

<style>
    .form-group {
        margin-bottom: 1rem;
    }

    .form-label {
        display: block;
        font-size: 0.8125rem;
        font-weight: 500;
        color: var(--text-primary);
        margin-bottom: 0.375rem;
    }

    .form-input,
    .form-select,
    .form-textarea {
        width: 100%;
        padding: 0.375rem 0.625rem;
        border: 1px solid var(--border);
        border-radius: 6px;
        background: var(--bg-primary);
        color: var(--text-primary);
        font-size: 0.8125rem;
        box-sizing: border-box;
    }

    .form-textarea {
        font-family: ui-monospace, SFMono-Regular, 'SF Mono', Menlo, monospace;
        resize: vertical;
        line-height: 1.5;
    }

    .form-input:focus,
    .form-select:focus,
    .form-textarea:focus {
        outline: none;
        border-color: var(--accent);
    }

    .spacer {
        flex: 1;
    }
</style>
