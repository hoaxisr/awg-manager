<script lang="ts">
    import { m } from '$lib/i18n';
    import { api } from '$lib/api/client';
    import type { AccessPolicy, PolicyDevice, PolicyGlobalInterface } from '$lib/types';
    import { ConfirmModal, StoreStatusBadge, Button } from '$lib/components/ui';
    import RoutingCreateButton from '$lib/components/routing/RoutingCreateButton.svelte';
    import { PolicyTable, PolicyCreateModal, PolicyEditView } from '$lib/components/accesspolicy';
    import { notifications } from '$lib/stores/notifications';
    import { accessPoliciesStore, policyDevicesStore, policyInterfacesStore, invalidateAllRouting } from '$lib/stores/routing';
    import { isHydraRouteAccessPolicy } from '$lib/utils/accessPolicy';

    interface Props {
        accessPolicies: AccessPolicy[];
        policyDevices: PolicyDevice[];
        policyInterfaces: PolicyGlobalInterface[];
        missing?: boolean;
        /** Deep-link из настроек sing-box: открыть редактор этой политики (#573). */
        openPolicy?: string | null;
    }

    let { accessPolicies, policyDevices, policyInterfaces, missing = false, openPolicy = null }: Props = $props();

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

    // Одноразовое открытие по deep-link: политики приезжают из стора, поэтому
    // ждём появления нужной. Отмечаем имя обработанным, иначе «Назад» из
    // редактора тут же открывал бы его снова.
    let appliedOpenPolicy = $state<string | null>(null);
    $effect(() => {
        if (!openPolicy || openPolicy === appliedOpenPolicy) return;
        const target = accessPolicies.find(p => p.name === openPolicy);
        if (!target) return;
        appliedOpenPolicy = openPolicy;
        editingPolicy = target.name;
        editingPolicyData = target;
    });

    async function createPolicy(description: string) {
        policyCreating = true;
        try {
            const created = await api.createAccessPolicy(description);
            policyCreateOpen = false;
            // Open newly created policy for editing
            editingPolicy = created.name;
            editingPolicyData = created;
            notifications.success(m.access_policy_created());
        } catch (e) {
            notifications.error(m.tunnels_error_with_message({ message: (e as Error).message }));
        } finally {
            policyCreating = false;
        }
    }

    async function deletePolicy(name: string) {
        try {
            await api.deleteAccessPolicy(name);
            policyDeleteName = null;
            notifications.success(m.access_policy_deleted());
        } catch (e) {
            notifications.error(m.tunnels_error_with_message({ message: (e as Error).message }));
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
                notifications.warning(m.access_policy_refresh_failed());
            } else {
                notifications.success(m.access_policy_refreshed());
            }
        } catch (e) {
            notifications.error(m.access_policy_refresh_error({ message: (e as Error).message }));
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
            if (fail > 0) notifications.warning(m.routing_bulk_deleted_policies_partial({ ok, total: ok + fail, fail }));
            else notifications.success(m.routing_bulk_deleted_policies({ count: ok }));
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
                {m.routing_policies_devices_summary({ policies: policyCount, devices: policyDeviceCount })}
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
                    {m.common_refresh()}
                </Button>
                {#if accessPolicies.length > 0}
                    <Button variant="ghost" size="sm" onclick={() => { policySelectionMode = true; policySelected = new Set(); }}>{m.common_select()}</Button>
                {/if}
                <RoutingCreateButton onclick={() => (policyCreateOpen = true)} />
            </div>
        {:else}
            <div class="bulk-bar">
                <div class="bulk-bar-nav">
                    <button class="bulk-btn bulk-btn-cancel" onclick={exitPolicySelection} disabled={policyBulkLoading}>✕ {m.common_cancel()}</button>
                    <span class="bulk-count">{m.routing_selected_count({ count: policySelected.size })}</span>
                    <button class="bulk-btn bulk-btn-select-all" onclick={policySelectAll} disabled={policyBulkLoading}>{m.routing_select_all()}</button>
                </div>
                <div class="bulk-bar-actions">
                    <button class="bulk-btn bulk-btn-delete" disabled={policySelected.size === 0 || policyBulkLoading} onclick={() => policyBulkDeleteConfirm = true}>{m.common_delete()}</button>
                </div>
            </div>
        {/if}
    </div>

    {#if accessPolicies.length === 0}
        {#if missing}
            <div class="warn-hint">
                {m.access_policy_no_data()}
            </div>
        {:else}
            <div class="empty-hint">
                {m.access_policy_empty()}
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
            title={m.access_policy_delete_title()}
            message={m.access_policy_delete_message({ name: pol?.description || policyDeleteName || '' })}
            secondary={m.access_policy_delete_secondary()}
            onConfirm={() => deletePolicy(policyDeleteName!)}
            onClose={() => policyDeleteName = null}
        />
    {/if}

    {#if policyBulkDeleteConfirm}
        <ConfirmModal
            open={true}
            title={m.routing_delete_title()}
            message={m.routing_policy_bulk_delete_message({ count: policySelected.size })}
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
