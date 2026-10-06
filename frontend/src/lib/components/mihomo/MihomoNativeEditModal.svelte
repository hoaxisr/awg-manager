<script lang="ts">
	import { untrack } from 'svelte';
	import { api } from '$lib/api/client';
	import { notifications } from '$lib/stores/notifications';
	import type {
		MihomoEnginePreference,
		MihomoNativeProxy,
		MihomoNativeSubscription,
		MihomoSubscriptionFormat,
	} from '$lib/types';
	import { Button, Dropdown, FormToggle, Input, Modal } from '$lib/components/ui';
	import HeadersTextarea from '$lib/components/subscriptions/HeadersTextarea.svelte';
	import ShareLinksTextarea from '$lib/components/subscriptions/ShareLinksTextarea.svelte';
	import { parseHeadersText } from '$lib/components/subscriptions/headersParser';
	import { normalizeMihomoSubscriptionFormat } from './subscriptionFormat';

	type ResourceKind = 'proxy' | 'subscription';
	type ProxySourceMode = 'uri' | 'manual';

	interface Props {
		open: boolean;
		kind: ResourceKind;
		resourceId: string;
		onclose: () => void;
		onupdated?: (resource: MihomoNativeProxy | MihomoNativeSubscription) => void | Promise<void>;
	}

	let { open, kind, resourceId, onclose, onupdated }: Props = $props();

	let loading = $state(false);
	let saving = $state(false);
	let error = $state('');
	let loadedKey = $state('');
	let loadSequence = 0;
	let initialFingerprint = $state('');

	let proxySourceMode = $state<ProxySourceMode>('uri');
	let proxyName = $state('');
	let proxyURI = $state('');
	let manualProtocol = $state('trusttunnel');
	let manualServer = $state('');
	let manualPort = $state(443);
	let manualConfig = $state('{}');

	let subscriptionFormat = $state<MihomoSubscriptionFormat>('mihomo-provider');
	let subscriptionName = $state('');
	let subscriptionURL = $state('');
	let subscriptionInline = $state('');
	let headersText = $state('');
	let refreshHours = $state(24);

	let enginePreference = $state<MihomoEnginePreference>('mihomo');
	let enabled = $state(true);

	const protocolOptions = [
		{ value: 'trusttunnel', label: 'TrustTunnel' },
		{ value: 'hysteria', label: 'Hysteria' },
		{ value: 'hysteria2', label: 'Hysteria 2' },
		{ value: 'trojan', label: 'Trojan' },
		{ value: 'tuic', label: 'TUIC' },
		{ value: 'anytls', label: 'AnyTLS' },
	];

	let currentFingerprint = $derived(JSON.stringify({
		kind,
		proxySourceMode,
		proxyName,
		proxyURI,
		manualProtocol,
		manualServer,
		manualPort,
		manualConfig,
		subscriptionFormat,
		subscriptionName,
		subscriptionURL,
		subscriptionInline,
		headersText,
		refreshHours,
		enginePreference,
		enabled,
	}));
	let dirty = $derived(!loading && initialFingerprint !== '' && currentFingerprint !== initialFingerprint);

	function serializeHeaderMap(headers?: Record<string, string[]>): string {
		if (!headers) return '';
		return Object.entries(headers)
			.flatMap(([name, values]) => (values ?? []).map((value) => `${name}: ${value}`))
			.join('\n');
	}

	function manualConfigText(config?: Record<string, unknown>): string {
		const editable = { ...(config ?? {}) };
		delete editable.name;
		delete editable.type;
		delete editable.server;
		delete editable.port;
		return JSON.stringify(editable, null, 2);
	}

	function applyProxy(proxy: MihomoNativeProxy): void {
		proxySourceMode = proxy.rawUri ? 'uri' : 'manual';
		proxyName = proxy.name;
		proxyURI = proxy.rawUri ?? '';
		manualProtocol = proxy.protocol || 'trusttunnel';
		manualServer = String(proxy.nativeConfig?.server ?? '');
		manualPort = Number(proxy.nativeConfig?.port ?? 443);
		manualConfig = manualConfigText(proxy.nativeConfig);
		enginePreference = proxy.enginePreference || 'mihomo';
		enabled = proxy.enabled !== false;
	}

	function applySubscription(subscription: MihomoNativeSubscription): void {
		subscriptionFormat = normalizeMihomoSubscriptionFormat(subscription.format, subscription);
		subscriptionName = subscription.name;
		subscriptionURL = subscription.url ?? '';
		subscriptionInline = subscription.inline ?? '';
		headersText = serializeHeaderMap(subscription.headers);
		refreshHours = subscription.refreshHours ?? 0;
		enginePreference = subscription.enginePreference || 'mihomo';
		enabled = subscription.enabled !== false;
	}

	async function loadResource(): Promise<void> {
		const sequence = ++loadSequence;
		loading = true;
		error = '';
		initialFingerprint = '';
		try {
			if (kind === 'proxy') {
				const resource = await api.mihomoNativeProxy(resourceId);
				if (sequence !== loadSequence) return;
				applyProxy(resource);
			} else {
				const resource = await api.mihomoNativeSubscription(resourceId);
				if (sequence !== loadSequence) return;
				applySubscription(resource);
			}
			if (sequence === loadSequence) initialFingerprint = currentFingerprint;
		} catch (loadError) {
			if (sequence === loadSequence) {
				error = loadError instanceof Error ? loadError.message : 'Не удалось загрузить ресурс Mihomo';
			}
		} finally {
			if (sequence === loadSequence) loading = false;
		}
	}

	$effect(() => {
		const key = open ? `${kind}:${resourceId}` : '';
		if (!key) {
			loadedKey = '';
			loadSequence++;
			return;
		}
		if (key === loadedKey) return;
		loadedKey = key;
		untrack(() => void loadResource());
	});

	function uriWithName(uri: string, name: string): string {
		const trimmed = uri.trim();
		const hash = trimmed.indexOf('#');
		const base = hash >= 0 ? trimmed.slice(0, hash) : trimmed;
		return `${base}#${encodeURIComponent(name.trim())}`;
	}

	function parseManualConfig(): Record<string, unknown> {
		let parsed: unknown;
		try {
			parsed = JSON.parse(manualConfig.trim() || '{}');
		} catch {
			throw new Error('Дополнительные параметры должны быть корректным JSON');
		}
		if (!parsed || typeof parsed !== 'object' || Array.isArray(parsed)) {
			throw new Error('Дополнительные параметры должны быть JSON-объектом');
		}
		return parsed as Record<string, unknown>;
	}

	async function save(): Promise<void> {
		if (saving || loading) return;
		error = '';
		saving = true;
		try {
			let saved: MihomoNativeProxy | MihomoNativeSubscription;
			if (kind === 'proxy') {
				if (!proxyName.trim()) throw new Error('Укажите название прокси');
				if (proxySourceMode === 'uri') {
					if (!proxyURI.trim()) throw new Error('Вставьте share-ссылку прокси');
					saved = await api.mihomoNativeUpdateProxy(resourceId, {
						uri: uriWithName(proxyURI, proxyName),
						enginePreference,
						enabled,
					});
				} else {
					if (!manualServer.trim()) throw new Error('Укажите сервер прокси');
					if (!Number.isInteger(manualPort) || manualPort < 1 || manualPort > 65535) {
						throw new Error('Порт должен быть от 1 до 65535');
					}
					saved = await api.mihomoNativeUpdateProxy(resourceId, {
						manual: {
							name: proxyName.trim(),
							protocol: manualProtocol,
							server: manualServer.trim(),
							port: manualPort,
							config: parseManualConfig(),
						},
						enginePreference,
						enabled,
					});
				}
			} else {
				if (!subscriptionName.trim()) throw new Error('Укажите название подписки');
				const normalizedFormat = normalizeMihomoSubscriptionFormat(subscriptionFormat, {
					url: subscriptionURL,
					inline: subscriptionInline,
				});
				const isInline = normalizedFormat === 'share-links';
				if (isInline && !subscriptionInline.trim()) throw new Error('Добавьте хотя бы одну share-ссылку');
				if (!isInline && !subscriptionURL.trim()) throw new Error('Укажите URL proxy-provider');
				if (!isInline && (!Number.isInteger(refreshHours) || refreshHours < 0)) {
					throw new Error('Интервал обновления должен быть целым неотрицательным числом');
				}
				saved = await api.mihomoNativeUpdateSubscription(resourceId, {
					name: subscriptionName.trim(),
					url: isInline ? undefined : subscriptionURL.trim(),
					inline: isInline ? subscriptionInline.trim() : undefined,
					format: normalizedFormat,
					enginePreference,
					refreshHours: isInline ? 0 : refreshHours,
					enabled,
					headers: isInline ? undefined : parseHeadersText(headersText),
				});
			}
			await onupdated?.(saved);
			notifications.success(kind === 'proxy' ? 'Прокси Mihomo обновлён' : 'Подписка Mihomo обновлена');
			initialFingerprint = currentFingerprint;
			onclose();
		} catch (saveError) {
			error = saveError instanceof Error ? saveError.message : 'Не удалось сохранить ресурс Mihomo';
			notifications.error(error);
		} finally {
			saving = false;
		}
	}

	function close(): void {
		if (!saving) onclose();
	}
</script>

<Modal
	{open}
	title={kind === 'proxy' ? 'Изменить прокси Mihomo' : 'Изменить подписку Mihomo'}
	size="lg"
	onclose={close}
	hasUnsavedChanges={() => dirty}
>
	{#if loading}
		<div class="loading-state"><span class="spinner"></span><span>Загружаем настройки…</span></div>
	{:else}
		<form class="edit-form" onsubmit={(event) => { event.preventDefault(); void save(); }}>
			{#if error}<div class="error-box" role="alert">{error}</div>{/if}

			<div class="enabled-row">
				<div>
					<strong>{enabled ? 'Ресурс включён' : 'Ресурс выключен'}</strong>
					<span>{enabled ? 'Участвует в конфигурации и маршрутизации Mihomo' : 'Сохранён, но исключён из конфигурации'}</span>
				</div>
				<FormToggle bind:checked={enabled} disabled={saving} />
			</div>

			{#if kind === 'proxy'}
				<Input label="Название" bind:value={proxyName} required fullWidth disabled={saving} />
				{#if proxySourceMode === 'uri'}
					<label class="field-block">
						<span class="field-label">Share-ссылка</span>
						<ShareLinksTextarea bind:value={proxyURI} rows={6} disabled={saving} placeholder="vless://… / hysteria2://… / tuic://…" />
						<span class="field-hint">Название синхронизируется с фрагментом ссылки после символа #.</span>
					</label>
				{:else}
					<div class="field-grid">
						<Dropdown label="Протокол" bind:value={manualProtocol} options={protocolOptions} fullWidth disabled={saving} />
						<Input label="Сервер" bind:value={manualServer} required fullWidth disabled={saving} />
					</div>
					<label class="field-block">
						<span class="field-label">Порт</span>
						<input class="native-input" type="number" min="1" max="65535" bind:value={manualPort} disabled={saving} />
					</label>
					<label class="field-block">
						<span class="field-label">Дополнительные параметры Mihomo (JSON)</span>
						<textarea class="code-editor" bind:value={manualConfig} rows="10" spellcheck="false" disabled={saving}></textarea>
						<span class="field-hint">Поля name, type, server и port задаются выше. Остальные поля проверяются по выбранному протоколу.</span>
					</label>
				{/if}
			{:else}
				<Input label="Название" bind:value={subscriptionName} required fullWidth disabled={saving} />
				{#if subscriptionFormat === 'share-links'}
					<label class="field-block">
						<span class="field-label">Серверы подписки</span>
						<ShareLinksTextarea bind:value={subscriptionInline} rows={10} disabled={saving} placeholder={'vless://…\nhysteria2://…\ntuic://…'} />
						<span class="field-hint">Одна share-ссылка на строку. Состав группы будет пересобран при сохранении.</span>
					</label>
				{:else}
					<Input label="URL proxy-provider" type="url" bind:value={subscriptionURL} required fullWidth disabled={saving} />
					<HeadersTextarea bind:value={headersText} />
					<label class="field-block">
						<span class="field-label">Автообновление, часов</span>
						<input class="native-input" type="number" min="0" bind:value={refreshHours} disabled={saving} />
						<span class="field-hint">0 — обновлять только вручную.</span>
					</label>
				{/if}
			{/if}
		</form>
	{/if}

	{#snippet actions()}
		<Button variant="ghost" disabled={saving} onclick={close}>Отмена</Button>
		<Button variant="primary" loading={saving} disabled={loading || saving} onclick={save}>Сохранить</Button>
	{/snippet}
</Modal>

<style>
	.edit-form{display:flex;flex-direction:column;gap:1rem}.loading-state{min-height:180px;display:flex;align-items:center;justify-content:center;gap:.65rem;color:var(--color-text-muted)}.spinner{width:18px;height:18px;border:2px solid var(--color-border);border-top-color:var(--color-accent);border-radius:50%;animation:spin .8s linear infinite}@keyframes spin{to{transform:rotate(360deg)}}.error-box{padding:.75rem .85rem;border:1px solid var(--color-error-border);border-radius:var(--radius-sm);background:var(--color-error-bg);color:var(--color-error);font-size:13px;overflow-wrap:anywhere}.enabled-row{display:flex;align-items:center;justify-content:space-between;gap:1rem;padding:.8rem .9rem;border:1px solid var(--color-border);border-radius:var(--radius-sm);background:var(--color-bg-tertiary)}.enabled-row>div{display:flex;flex-direction:column;gap:.15rem}.enabled-row strong{font-size:13px}.enabled-row span{font-size:12px;color:var(--color-text-muted)}.field-grid{display:grid;grid-template-columns:minmax(0,.8fr) minmax(0,1.2fr);gap:.75rem}.field-block{display:flex;flex-direction:column;gap:.3rem;min-width:0}.field-label{font-size:13px;font-weight:500;color:var(--color-text-secondary)}.field-hint{font-size:12px;color:var(--color-text-muted);line-height:1.4}.native-input,.code-editor{box-sizing:border-box;width:100%;border:1px solid var(--color-border);border-radius:var(--radius-sm);background:var(--color-bg-primary);color:var(--color-text-primary);font:inherit;font-size:13px;padding:.45rem .625rem}.native-input:focus,.code-editor:focus{outline:none;border-color:var(--color-accent)}.native-input:disabled,.code-editor:disabled{opacity:.55}.code-editor{min-height:180px;resize:vertical;font-family:var(--font-mono,ui-monospace,monospace);line-height:1.45;tab-size:2}@media(max-width:620px){.field-grid{grid-template-columns:1fr}.enabled-row{align-items:flex-start}}
</style>
