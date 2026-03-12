<script lang="ts">
    import type { Policy, TunnelListItem, HotspotClient } from '$lib/types';
    import { Modal } from '$lib/components/ui';

    interface Props {
        open: boolean;
        tunnels: TunnelListItem[];
        hotspotClients: HotspotClient[];
        oncreate: (policy: Partial<Policy>) => void;
        onclose: () => void;
        saving: boolean;
    }

    let { open, tunnels, hotspotClients, oncreate, onclose, saving }: Props = $props();

    let clientIP = $state('');
    let clientHostname = $state('');
    let tunnelID = $state('');
    let name = $state('');
    let manualIP = $state(false);

    let canCreate = $derived(clientIP !== '' && clientIP !== '0.0.0.0' && tunnelID !== '');

    $effect(() => {
        if (open) {
            clientIP = '';
            clientHostname = '';
            tunnelID = '';
            name = '';
            manualIP = false;
        }
    });

    function handleClientSelect(e: Event) {
        const value = (e.target as HTMLSelectElement).value;
        if (value === '__manual__') {
            manualIP = true;
            clientIP = '';
            clientHostname = '';
        } else if (value === '') {
            manualIP = false;
            clientIP = '';
            clientHostname = '';
        } else {
            manualIP = false;
            const client = hotspotClients.find(c => c.ip === value);
            if (client) {
                clientIP = client.ip;
                clientHostname = client.hostname;
                if (!name) {
                    name = client.hostname;
                }
            }
        }
    }

    function handleCreate() {
        oncreate({
            clientIP,
            clientHostname,
            tunnelID,
            fallback: 'bypass',
            name: name || clientHostname || clientIP,
            enabled: true,
        });
    }
</script>

<Modal {open} title="Новая политика доступа" onclose={onclose}>
    <div class="form-group">
        <!-- svelte-ignore a11y_label_has_associated_control -->
        <label class="form-label">Клиент</label>
        {#if !manualIP}
            <select class="form-select" onchange={handleClientSelect} value={clientIP || ''}>
                <option value="">Выберите клиента</option>
                {#each hotspotClients as client}
                    <option value={client.ip}>
                        {client.hostname || client.mac} ({client.ip})
                    </option>
                {/each}
                <option value="__manual__">Ввести IP вручную</option>
            </select>
        {:else}
            <input
                class="form-input"
                type="text"
                placeholder="192.168.1.100"
                bind:value={clientIP}
            />
            <button class="btn-link" onclick={() => { manualIP = false; clientIP = ''; clientHostname = ''; }}>
                Выбрать из списка
            </button>
        {/if}
    </div>

    <div class="form-group">
        <label class="form-label" for="policy-tunnel">Туннель</label>
        <select class="form-select" id="policy-tunnel" bind:value={tunnelID}>
            <option value="">Выберите туннель</option>
            {#each tunnels as tunnel}
                <option value={tunnel.id}>{tunnel.name}</option>
            {/each}
        </select>
    </div>

    <div class="form-group">
        <label class="form-label" for="policy-name">Название</label>
        <input
            class="form-input"
            id="policy-name"
            type="text"
            placeholder={clientHostname || clientIP || 'Имя политики'}
            bind:value={name}
        />
    </div>

    {#snippet actions()}
        <button class="btn btn-secondary" onclick={onclose}>Отмена</button>
        <button class="btn btn-accent" onclick={handleCreate} disabled={!canCreate || saving}>
            {saving ? 'Сохранение...' : 'Создать'}
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

    .form-select,
    .form-input {
        width: 100%;
        padding: 0.375rem 0.625rem;
        border: 1px solid var(--border);
        border-radius: 6px;
        background: var(--bg-primary);
        color: var(--text-primary);
        font-size: 0.8125rem;
        box-sizing: border-box;
    }

    .form-select:focus,
    .form-input:focus {
        outline: none;
        border-color: var(--accent);
    }

    .btn-link {
        background: none;
        border: none;
        color: var(--accent);
        cursor: pointer;
        font-size: 0.75rem;
        padding: 0.25rem 0;
    }

    .btn-link:hover {
        text-decoration: underline;
    }
</style>
