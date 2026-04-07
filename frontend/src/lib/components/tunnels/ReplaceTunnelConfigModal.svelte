<script lang="ts">
    import { Modal } from '$lib/components/ui';
    import { api } from '$lib/api/client';
    import { notifications } from '$lib/stores/notifications';
    import { decodeVpnLink, isVpnLink } from '$lib/utils/vpnlink';

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
    let activeTab = $state<'file' | 'paste' | 'link'>('file');
    let fileInput = $state<HTMLInputElement>();
    let dragOver = $state(false);
    let linkInput = $state('');
    let linkPreview = $state('');
    let linkError = $state('');

    // Reset state when modal opens
    $effect(() => {
        if (open) {
            importContent = '';
            newName = tunnelName;
            activeTab = 'file';
            linkInput = '';
            linkPreview = '';
            linkError = '';
            loading = false;
        }
    });

    function handleFileSelect(event: Event) {
        const input = event.target as HTMLInputElement;
        if (input.files && input.files[0]) {
            readFile(input.files[0]);
        }
    }

    function handleDrop(event: DragEvent) {
        event.preventDefault();
        dragOver = false;
        if (event.dataTransfer?.files && event.dataTransfer.files[0]) {
            readFile(event.dataTransfer.files[0]);
        }
    }

    function readFile(file: File) {
        const reader = new FileReader();
        reader.onload = (e) => {
            const content = e.target?.result as string;
            if (content) {
                importContent = content;
                notifications.success(`Файл "${file.name}" загружен`);
            }
        };
        reader.onerror = () => {
            notifications.error('Не удалось прочитать файл');
        };
        reader.readAsText(file);
    }

    function handleLinkInput() {
        linkError = '';
        linkPreview = '';
        if (!linkInput.trim()) {
            importContent = '';
            return;
        }
        try {
            const result = decodeVpnLink(linkInput);
            linkPreview = result.config;
            importContent = result.config;
        } catch (e) {
            linkError = e instanceof Error ? e.message : 'Ошибка декодирования';
            importContent = '';
        }
    }

    async function handleReplace() {
        let content = importContent.trim();
        if (!content) return;

        // Auto-detect vpn:// in paste tab
        if (isVpnLink(content)) {
            try {
                const result = decodeVpnLink(content);
                content = result.config;
            } catch (e) {
                notifications.error(e instanceof Error ? e.message : 'Ошибка декодирования vpn:// ссылки');
                return;
            }
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
            <svg viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2" width="16" height="16">
                <path d="M10.29 3.86L1.82 18a2 2 0 001.71 3h16.94a2 2 0 001.71-3L13.71 3.86a2 2 0 00-3.42 0z"/>
                <line x1="12" y1="9" x2="12" y2="13"/><line x1="12" y1="17" x2="12.01" y2="17"/>
            </svg>
            Туннель будет остановлен, переконфигурирован и запущен автоматически. Все правила маршрутизации сохранятся.
        </div>
    {/if}

    <div class="tabs">
        <button class="tab" class:tab-active={activeTab === 'file'} onclick={() => activeTab = 'file'}>
            <svg viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2" width="16" height="16">
                <path d="M21 15v4a2 2 0 0 1-2 2H5a2 2 0 0 1-2-2v-4"/>
                <polyline points="17 8 12 3 7 8"/><line x1="12" y1="3" x2="12" y2="15"/>
            </svg>
            Файл
        </button>
        <button class="tab" class:tab-active={activeTab === 'paste'} onclick={() => activeTab = 'paste'}>
            <svg viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2" width="16" height="16">
                <path d="M16 4h2a2 2 0 0 1 2 2v14a2 2 0 0 1-2 2H6a2 2 0 0 1-2-2V6a2 2 0 0 1 2-2h2"/>
                <rect x="8" y="2" width="8" height="4" rx="1" ry="1"/>
            </svg>
            Вставить текст
        </button>
        <button class="tab" class:tab-active={activeTab === 'link'} onclick={() => activeTab = 'link'}>
            <svg viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2" width="16" height="16">
                <path d="M10 13a5 5 0 0 0 7.54.54l3-3a5 5 0 0 0-7.07-7.07l-1.72 1.71"/>
                <path d="M14 11a5 5 0 0 0-7.54-.54l-3 3a5 5 0 0 0 7.07 7.07l1.71-1.71"/>
            </svg>
            Ссылка
        </button>
    </div>

    <div class="tab-content">
        {#if activeTab === 'file'}
            <div
                class="file-drop-zone"
                class:drag-over={dragOver}
                class:has-content={!!importContent.trim()}
                ondrop={handleDrop}
                ondragover={(e) => { e.preventDefault(); dragOver = true; }}
                ondragleave={() => dragOver = false}
                role="button"
                tabindex="0"
                onclick={() => fileInput?.click()}
                onkeydown={(e) => e.key === 'Enter' && fileInput?.click()}
            >
                <input type="file" accept=".conf" bind:this={fileInput} onchange={handleFileSelect} style="display: none">
                {#if importContent.trim()}
                    <div class="drop-content">
                        <svg viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="1.5" width="36" height="36">
                            <polyline points="20 6 9 17 4 12"/>
                        </svg>
                        <div class="drop-text">
                            <p class="drop-title">Файл загружен</p>
                            <p class="drop-hint">Нажмите чтобы заменить</p>
                        </div>
                    </div>
                {:else}
                    <div class="drop-content">
                        <svg viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="1.5" width="36" height="36">
                            <path d="M21 15v4a2 2 0 0 1-2 2H5a2 2 0 0 1-2-2v-4"/>
                            <polyline points="17 8 12 3 7 8"/><line x1="12" y1="3" x2="12" y2="15"/>
                        </svg>
                        <div class="drop-text">
                            <p class="drop-title">Перетащите .conf файл сюда</p>
                            <p class="drop-hint">или нажмите для выбора</p>
                        </div>
                    </div>
                {/if}
            </div>
        {:else if activeTab === 'paste'}
            <textarea
                class="config-textarea"
                bind:value={importContent}
                placeholder={"[Interface]\nPrivateKey = ...\nAddress = 10.0.0.2/32\n\n[Peer]\nPublicKey = ...\nEndpoint = vpn.example.com:51820\nAllowedIPs = 0.0.0.0/0"}
            ></textarea>
        {:else if activeTab === 'link'}
            <textarea
                class="config-textarea link-input"
                bind:value={linkInput}
                oninput={handleLinkInput}
                placeholder="Вставьте vpn:// ссылку из AmneziaVPN"
            ></textarea>
            {#if linkError}
                <p class="link-error">{linkError}</p>
            {/if}
            {#if linkPreview}
                <textarea class="config-textarea link-preview" readonly value={linkPreview}></textarea>
            {/if}
        {/if}
    </div>

    <div class="name-field">
        <label class="field-label" for="replace-name">Имя туннеля</label>
        <input type="text" id="replace-name" class="name-input" bind:value={newName} placeholder={tunnelName}>
        <div class="field-hint">Оставьте без изменений чтобы сохранить текущее имя</div>
    </div>

    {#snippet actions()}
        <button class="btn btn-secondary" onclick={onclose} disabled={loading}>Отмена</button>
        <button class="btn btn-primary" onclick={handleReplace} disabled={loading || !importContent.trim()}>
            {#if loading}
                <span class="spinner"></span>
            {/if}
            Заменить
        </button>
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

    .replace-warning svg {
        flex-shrink: 0;
        margin-top: 1px;
    }

    .tabs {
        display: flex;
        border-bottom: 1px solid var(--border);
        gap: 0;
    }

    .tab {
        display: flex;
        align-items: center;
        gap: 6px;
        padding: 8px 14px;
        font-size: 0.8125rem;
        font-weight: 500;
        color: var(--text-muted);
        background: none;
        border: none;
        border-bottom: 2px solid transparent;
        cursor: pointer;
        transition: all 0.15s;
        margin-bottom: -1px;
    }

    .tab:hover {
        color: var(--text-secondary);
    }

    .tab-active {
        color: var(--accent);
        border-bottom-color: var(--accent);
    }

    .tab-content {
        margin-top: 0;
    }

    .config-textarea {
        width: 100%;
        min-height: 180px;
        padding: 12px;
        font-family: monospace;
        font-size: 0.75rem;
        line-height: 1.5;
        background: var(--bg-primary);
        border: 1px solid var(--border);
        border-top: none;
        border-radius: 0 0 8px 8px;
        color: var(--text-primary);
        resize: vertical;
    }

    .config-textarea:focus {
        outline: none;
        border-color: var(--accent);
    }

    .config-textarea::placeholder {
        color: var(--text-muted);
    }

    .file-drop-zone {
        min-height: 140px;
        border: 2px dashed var(--border);
        border-top: 2px dashed var(--border);
        border-radius: 0 0 8px 8px;
        display: flex;
        align-items: center;
        justify-content: center;
        cursor: pointer;
        transition: all 0.15s;
    }

    .file-drop-zone:hover {
        border-color: var(--accent);
        background: var(--bg-tertiary);
    }

    .file-drop-zone.drag-over {
        border-color: var(--accent);
        background: rgba(122, 162, 247, 0.1);
    }

    .file-drop-zone.has-content {
        border-color: var(--success);
        border-style: solid;
    }

    .file-drop-zone.has-content svg {
        color: var(--success);
    }

    .drop-content {
        display: flex;
        align-items: center;
        gap: 12px;
    }

    .drop-content svg {
        color: var(--text-muted);
        flex-shrink: 0;
    }

    .drop-title {
        font-size: 0.875rem;
        font-weight: 500;
        color: var(--text-primary);
        margin-bottom: 2px;
    }

    .drop-hint {
        font-size: 0.75rem;
        color: var(--text-muted);
    }

    .link-input {
        min-height: 80px;
    }

    .link-error {
        font-size: 0.75rem;
        color: var(--error);
        margin: 6px 0 0;
    }

    .link-preview {
        min-height: 120px;
        margin-top: 8px;
        border: 1px solid var(--border);
        border-radius: 8px;
        opacity: 0.85;
        cursor: default;
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

    .spinner {
        width: 14px;
        height: 14px;
        border: 2px solid currentColor;
        border-top-color: transparent;
        border-radius: 50%;
        animation: spin 0.8s linear infinite;
        margin-right: 6px;
    }

    @keyframes spin {
        to { transform: rotate(360deg); }
    }
</style>
