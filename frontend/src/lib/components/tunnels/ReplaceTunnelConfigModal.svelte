<script lang="ts">
    import { TriangleAlert } from 'lucide-svelte';
    import { Modal, Button } from '$lib/components/ui';
    import TunnelConfigImportPanel from './TunnelConfigImportPanel.svelte';
    import { api } from '$lib/api/client';
    import { notifications } from '$lib/stores/notifications';
    import { isVpnLink } from '$lib/utils/vpnlink';

    interface Props {
        open: boolean;
        tunnelId: string;
        tunnelName: string;
        tunnelState: string;
        backendLabel: string;
        ndmsName: string;
        onclose: () => void;
        onreplaced?: () => void;
    }

    let {
        open = $bindable(false),
        tunnelId,
        tunnelName,
        tunnelState,
        backendLabel,
        ndmsName,
        onclose,
        onreplaced
    }: Props = $props();

    let loading = $state(false);
    let importContent = $state('');
    let newName = $state('');
    let activeTab = $state<'file' | 'paste' | 'vpn'>('file');
    let vpnPasteInput = $state('');
    let linkPreview = $state('');
    let wasOpen = $state(false);

    // Reset state when modal opens (only once per open cycle so polling-tick
    // re-runs don't wipe user edits).
    $effect(() => {
        if (!open) {
            wasOpen = false;
            return;
        }
        if (wasOpen) return;
        wasOpen = true;
        importContent = '';
        newName = tunnelName;
        activeTab = 'file';
        vpnPasteInput = '';
        linkPreview = '';
        loading = false;
    });

    function handlePremiumCountryConfig(_config: string, meta: { suggestedName?: string }) {
        if (meta.suggestedName && newName === tunnelName) {
            newName = meta.suggestedName;
        }
    }

    async function handleReplace() {
        let content = importContent.trim();
        if (!content) return;

        // Auto-detect vpn:// in paste tab (vpn tab already decodes via VpnLinkPasteImport)
        if (activeTab === 'paste' && isVpnLink(content)) {
            notifications.error('Для vpn:// используйте вкладку «Ссылка»');
            return;
        }

        loading = true;
        try {
            const result = await api.replaceConfig(tunnelId, content, newName !== tunnelName ? newName : undefined);
            if (result.warnings?.length) {
                result.warnings.forEach((w: string) => notifications.warning(w));
            }
            notifications.success('Конфигурация заменена');
            onclose();
            onreplaced?.();
        } catch (e) {
            notifications.error(e instanceof Error ? e.message : 'Ошибка замены конфигурации');
        } finally {
            loading = false;
        }
    }
</script>

<Modal {open} title="Замена конфигурации" size="lg" {onclose}>
    <div class="replace-info">
        <span class="replace-tunnel-label">{ndmsName}</span>
        <span class="replace-dot">&middot;</span>
        <span>{backendLabel}</span>
        <span class="replace-dot">&middot;</span>
        <span class="replace-state" class:state-running={tunnelState === 'running'}>
            {tunnelState === 'running' ? 'Работает' : 'Остановлен'}
        </span>
    </div>

    {#if tunnelState === 'running'}
        <div class="replace-warning">
            <TriangleAlert size={16} aria-hidden="true" style="flex-shrink: 0; margin-top: 1px;" />
            Туннель будет остановлен, переконфигурирован и запущен автоматически. Все правила маршрутизации сохранятся.
        </div>
    {/if}

    <TunnelConfigImportPanel
        variant="modal"
        bind:importContent
        bind:activeTab
        bind:vpnPasteInput
        bind:linkPreview
        loadStoredKeyOnMount={true}
        oncountryconfig={handlePremiumCountryConfig}
    />

    <div class="name-field">
        <label class="field-label" for="replace-name">Имя туннеля</label>
        <input type="text" id="replace-name" class="name-input" bind:value={newName} placeholder={tunnelName}>
        <div class="field-hint">Оставьте без изменений чтобы сохранить текущее имя</div>
    </div>

    {#snippet actions()}
        <Button variant="secondary" onclick={onclose} disabled={loading}>Отмена</Button>
        <Button variant="primary" onclick={handleReplace} disabled={!importContent.trim()} loading={loading}>
            Заменить
        </Button>
    {/snippet}
</Modal>

<style>
    .replace-info {
        display: flex;
        align-items: center;
        gap: 6px;
        font-size: 0.75rem;
        color: var(--text-muted);
        margin-bottom: 12px;
    }

    .replace-tunnel-label {
        font-weight: 600;
        color: var(--text-secondary);
    }

    .replace-dot {
        color: var(--text-muted);
    }

    .state-running {
        color: var(--success);
    }

    .replace-warning {
        display: flex;
        align-items: flex-start;
        gap: 8px;
        padding: 10px 14px;
        background: rgba(224, 175, 104, 0.08);
        border: 1px solid rgba(224, 175, 104, 0.25);
        border-radius: 6px;
        font-size: 0.75rem;
        color: var(--warning, #e0af68);
        margin-bottom: 12px;
    }

    .name-field {
        margin-top: 16px;
    }

    .field-label {
        display: block;
        font-size: 0.75rem;
        font-weight: 500;
        color: var(--text-secondary);
        margin-bottom: 4px;
    }

    .name-input {
        width: 100%;
        padding: 8px 12px;
        font-size: 0.875rem;
        background: var(--bg-primary);
        border: 1px solid var(--border);
        border-radius: 6px;
        color: var(--text-primary);
    }

    .name-input:focus {
        outline: none;
        border-color: var(--accent);
    }

    .field-hint {
        font-size: 0.6875rem;
        color: var(--text-muted);
        margin-top: 2px;
    }
</style>
