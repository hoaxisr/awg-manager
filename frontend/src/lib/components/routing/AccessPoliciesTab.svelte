<script lang="ts">
    import { api } from '$lib/api/client';
    import type { AccessPolicy, PolicyDevice, PolicyGlobalInterface } from '$lib/types';
    import { ConfirmModal, StoreStatusBadge, Button } from '$lib/components/ui';
    import RoutingCreateButton from '$lib/components/routing/RoutingCreateButton.svelte';
    import { PolicyTable, PolicyCreateModal, PolicyEditView } from '$lib/components/accesspolicy';
    import { notifications } from '$lib/stores/notifications';
    import { accessPoliciesStore, policyDevicesStore, policyInterfacesStore, invalidateAllRouting } from '$lib/stores/routing';
    import { isHydraRouteAccessPolicy } from '$lib/utils/accessPolicy';
    import { ERROR_WORDS, pluralForm, pluralize, DEVICE_WORDS, POLICY_WORDS } from '$lib/utils/pluralize';

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
	let policyRefreshing = $state(false);

    let policyCount = $derived(accessPolicies.length);
    let policyDeviceCount = $derived(accessPolicies.reduce((n, p) => n + p.deviceCount, 0));

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

    async function refreshPolicyData() {
        invalidateAllRouting();
    }

    async function refreshPolicies() {
        if (policyRefreshing) return;
        policyRefreshing = true;
        try {
            const res = await api.refreshRouting();
            invalidateAllRouting();
            if (res.missing?.includes('accessPolicies')) {
                notifications.warning('Не удалось обновить политики доступа');
            } else {
                notifications.success('Политики обновлены');
            }
        } catch (e) {
            notifications.error(`Ошибка обновления политик: ${(e as Error).message}`);
        } finally {
            policyRefreshing = false;
        }
    }

    function handleDeviceAssigned(_mac: string, _policyName: string) {
        // SSE will push updated policyDevices and accessPolicies
    }

    function handleDeviceUnassigned(_mac: string, _fromPolicy: string) {
        // SSE will push updated policyDevices and accessPolicies
    }

    function togglePolicySelect(name: string) {
        const pol = accessPolicies.find((p) => p.name === name);
        if (pol && isHydraRouteAccessPolicy(pol)) return;
        const next = new Set(policySelected);
        if (next.has(name)) next.delete(name);
        else next.add(name);
        policySelected = next;
    }

    function policySelectAll() {
        policySelected = new Set(
            accessPolicies.filter((p) => !isHydraRouteAccessPolicy(p)).map((p) => p.name),
        );
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
            if (fail > 0) notifications.warning(`Удалено ${ok} из ${ok + fail} ${pluralForm(ok + fail, POLICY_WORDS)} (${pluralize(fail, ERROR_WORDS)})`);
            else notifications.success(`Удалено ${pluralize(ok, POLICY_WORDS)}`);
        } finally {
            policyBulkLoading = false;
            policyBulkDeleteConfirm = false;
        }
    }
</script>

{#if editingPolicyData}
    <div class="policy-tab policy-tab--edit">
        <PolicyEditView
            policy={editingPolicyData}
            devices={policyDevices}
            globalInterfaces={policyInterfaces}
            onback={() => { editingPolicy = null; editingPolicyData = null; }}
            onupdate={refreshPolicyData}
            ondeviceassigned={handleDeviceAssigned}
            ondeviceunassigned={handleDeviceUnassigned}
        />
    </div>
{:else}
    <div class="policy-tab policy-tab--list">
    <div class="section-header">
        {#if !policySelectionMode}
            <span class="section-summary">
                {pluralize(policyCount, POLICY_WORDS)}, {pluralize(policyDeviceCount, DEVICE_WORDS)}
            </span>
            <div class="section-buttons">
                <StoreStatusBadge store={accessPoliciesStore} />
                <StoreStatusBadge store={policyDevicesStore} />
                <StoreStatusBadge store={policyInterfacesStore} />
                <Button
                    variant="ghost"
                    size="sm"
                    onclick={refreshPolicies}
                    disabled={policyRefreshing}
                    loading={policyRefreshing}
                >
                    Обновить
                </Button>
                {#if accessPolicies.length > 0}
                    <Button variant="ghost" size="sm" onclick={() => { policySelectionMode = true; policySelected = new Set(); }}>Выбрать</Button>
                {/if}
                <RoutingCreateButton onclick={() => (policyCreateOpen = true)} />
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
        <div class="policy-list-scroll">
            <PolicyTable
                policies={accessPolicies}
                onedit={(name) => { editingPolicy = name; editingPolicyData = accessPolicies.find(p => p.name === name) ?? null; }}
                ondelete={(name) => policyDeleteName = name}
                selectable={policySelectionMode}
                selectedNames={policySelected}
                onselect={togglePolicySelect}
            />
        </div>
    {/if}

    <PolicyCreateModal
        bind:open={policyCreateOpen}
        saving={policyCreating}
        oncreate={createPolicy}
        onclose={() => policyCreateOpen = false}
    />

    {#if policyDeleteName}
        {@const pol = accessPolicies.find(p => p.name === policyDeleteName)}
        <ConfirmModal
            open={true}
            title="Удаление политики"
            message={`Удалить политику «${pol?.description || policyDeleteName}»?`}
            secondary="Все устройства будут отвязаны от этой политики."
            onConfirm={() => deletePolicy(policyDeleteName!)}
            onClose={() => policyDeleteName = null}
        />
    {/if}

    {#if policyBulkDeleteConfirm}
        <ConfirmModal
            open={true}
            title="Удаление"
            message={`Удалить ${pluralize(policySelected.size, POLICY_WORDS)}? Все устройства будут отвязаны.`}
            onConfirm={bulkPolicyDelete}
            onClose={() => policyBulkDeleteConfirm = false}
        />
    {/if}
    </div>
{/if}

<style>
    /* Высота под viewport: скролл внутри панелей, не у всей страницы */
    .policy-tab {
        display: flex;
        flex-direction: column;
        min-height: 0;
        overflow: hidden;
    }

    .policy-tab--edit,
    .policy-tab--list {
        height: calc(100dvh - 12.5rem);
        min-height: 280px;
        max-height: calc(100dvh - 12.5rem);
    }

    .policy-list-scroll {
        flex: 1;
        min-height: 0;
        overflow-y: auto;
        padding-right: 2px;
    }

    @media (max-width: 768px) {
        .policy-tab--edit,
        .policy-tab--list {
            height: auto;
            max-height: none;
            overflow: visible;
        }

        .policy-list-scroll {
            flex: none;
            overflow-y: visible;
        }
    }


</style>
