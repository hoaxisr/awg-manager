<script lang="ts">
    import { api } from '$lib/api/client';
    import type { AccessPolicy, PolicyDevice, PolicyGlobalInterface } from '$lib/types';
    import { Modal } from '$lib/components/ui';
    import { PolicyTable, PolicyCreateModal, PolicyEditView } from '$lib/components/accesspolicy';
    import { notifications } from '$lib/stores/notifications';

    interface Props {
        accessPolicies: AccessPolicy[];
        policyDevices: PolicyDevice[];
        policyInterfaces: PolicyGlobalInterface[];
        missing?: boolean;
    }

    let { accessPolicies, policyDevices, policyInterfaces, missing = false }: Props = $props();

    let policyCreateOpen = $state(false);
    let policyCreating = $state(false);
    let policyDeleteName = $state<string | null>(null);
    let editingPolicy = $state<string | null>(null);
    let editingPolicyData = $state<AccessPolicy | null>(null);
    let policySelectionMode = $state(false);
    let policySelected = $state<Set<string>>(new Set());
    let policyBulkLoading = $state(false);
    let policyBulkDeleteConfirm = $state(false);

    let policyCount = $derived(accessPolicies.length);

    // Keep editingPolicyData in sync with store-driven accessPolicies
    $effect(() => {
        if (editingPolicy) {
            editingPolicyData = accessPolicies.find(p => p.name === editingPolicy) ?? null;
        }
    });

    async function createPolicy(description: string) {
        policyCreating = true;
        try {
            const created = await api.createAccessPolicy(description);
            policyCreateOpen = false;
            // Open newly created policy for editing
            editingPolicy = created.name;
            editingPolicyData = created;
            notifications.success('Политика создана');
        } catch (e) {
            notifications.error(`Ошибка: ${(e as Error).message}`);
        } finally {
            policyCreating = false;
        }
    }

    async function deletePolicy(name: string) {
        try {
            await api.deleteAccessPolicy(name);
            policyDeleteName = null;
            notifications.success('Политика удалена');
        } catch (e) {
            notifications.error(`Ошибка: ${(e as Error).message}`);
        }
    }

    // No-op: SSE updates the store; PolicyEditView expects an async callback
    async function refreshPolicyData() {}

    function handleDeviceAssigned(_mac: string, _policyName: string) {
        // SSE will push updated policyDevices and accessPolicies
    }

    function handleDeviceUnassigned(_mac: string, _fromPolicy: string) {
        // SSE will push updated policyDevices and accessPolicies
    }

    function togglePolicySelect(name: string) {
        const next = new Set(policySelected);
        if (next.has(name)) next.delete(name);
        else next.add(name);
        policySelected = next;
    }

    function policySelectAll() {
        policySelected = new Set(accessPolicies.map(p => p.name));
    }

    function exitPolicySelection() {
        policySelectionMode = false;
        policySelected = new Set();
    }

    async function bulkPolicyDelete() {
        policyBulkLoading = true;
        try {
            let ok = 0, fail = 0;
            for (const name of policySelected) {
                try { await api.deleteAccessPolicy(name); ok++; } catch { fail++; }
            }
            exitPolicySelection();
            if (fail > 0) notifications.warning(`Удалено ${ok} из ${ok + fail} политик (${fail} ошибок)`);
            else notifications.success(`Удалено ${ok} политик`);
        } finally {
            policyBulkLoading = false;
            policyBulkDeleteConfirm = false;
        }
    }
</script>

{#if editingPolicyData}
        <PolicyEditView
            policy={editingPolicyData}
            devices={policyDevices}
            globalInterfaces={policyInterfaces}
            onback={() => { editingPolicy = null; editingPolicyData = null; }}
            onupdate={refreshPolicyData}
            ondeviceassigned={handleDeviceAssigned}
            ondeviceunassigned={handleDeviceUnassigned}
        />
{:else}
    <div class="section-header">
        {#if !policySelectionMode}
            <span class="section-summary">{policyCount} политик</span>
            <div class="section-buttons">
                {#if accessPolicies.length > 0}
                    <button class="btn btn-sm btn-ghost" onclick={() => { policySelectionMode = true; policySelected = new Set(); }}>Выбрать</button>
                {/if}
                <button class="btn btn-sm btn-primary" onclick={() => policyCreateOpen = true}>+ Создать</button>
            </div>
        {:else}
            <div class="bulk-bar">
                <div class="bulk-bar-nav">
                    <button class="bulk-btn bulk-btn-cancel" onclick={exitPolicySelection} disabled={policyBulkLoading}>✕ Отмена</button>
                    <span class="bulk-count">{policySelected.size} выбрано</span>
                    <button class="bulk-btn bulk-btn-select-all" onclick={policySelectAll} disabled={policyBulkLoading}>Выбрать все</button>
                </div>
                <div class="bulk-bar-actions">
                    <button class="bulk-btn bulk-btn-delete" disabled={policySelected.size === 0 || policyBulkLoading} onclick={() => policyBulkDeleteConfirm = true}>Удалить</button>
                </div>
            </div>
        {/if}
    </div>

    {#if accessPolicies.length === 0}
        {#if missing}
            <div class="warn-hint">
                Данные политик не получены от маршрутизатора. Нажмите «Загрузить недостающее» в заголовке страницы, чтобы повторить запрос.
            </div>
        {:else}
            <div class="empty-hint">
                Нет политик доступа. Создайте политику, чтобы направить трафик устройств через выбранные интерфейсы.
            </div>
        {/if}
    {:else}
        <PolicyTable
            policies={accessPolicies}
            onedit={(name) => { editingPolicy = name; editingPolicyData = accessPolicies.find(p => p.name === name) ?? null; }}
            ondelete={(name) => policyDeleteName = name}
            selectable={policySelectionMode}
            selectedNames={policySelected}
            onselect={togglePolicySelect}
        />
    {/if}

    <PolicyCreateModal
        bind:open={policyCreateOpen}
        saving={policyCreating}
        oncreate={createPolicy}
        onclose={() => policyCreateOpen = false}
    />

    {#if policyDeleteName}
        {@const pol = accessPolicies.find(p => p.name === policyDeleteName)}
        <Modal open={true} title="Удаление политики" size="sm" onclose={() => policyDeleteName = null}>
            <p class="confirm-text">Удалить политику «{pol?.description || policyDeleteName}»?</p>
            <p class="delete-hint">Все устройства будут отвязаны от этой политики.</p>
            {#snippet actions()}
                <button class="btn btn-ghost" onclick={() => policyDeleteName = null}>Отмена</button>
                <button class="btn btn-danger" onclick={() => deletePolicy(policyDeleteName!)}>Удалить</button>
            {/snippet}
        </Modal>
    {/if}

    {#if policyBulkDeleteConfirm}
        <Modal open={true} title="Удаление" size="sm" onclose={() => policyBulkDeleteConfirm = false}>
            <p class="confirm-text">Удалить {policySelected.size} политик? Все устройства будут отвязаны.</p>
            {#snippet actions()}
                <button class="btn btn-ghost" onclick={() => policyBulkDeleteConfirm = false}>Отмена</button>
                <button class="btn btn-danger" onclick={bulkPolicyDelete}>Удалить</button>
            {/snippet}
        </Modal>
    {/if}
{/if}
