<script lang="ts">
	import { m } from '$lib/i18n';
	import { api } from '$lib/api/client';
	import { notifications } from '$lib/stores/notifications';
	import { Toggle, Button, Dropdown, type DropdownOption } from '$lib/components/ui';
	import { outboundOptionLabel } from '$lib/utils/deviceProxyOutboundLabel';
	import type { DeviceProxyConfig, DeviceProxyOutbound } from '$lib/types';

	interface Props {
		config: DeviceProxyConfig;
		outbounds: DeviceProxyOutbound[];
		bridgeInterfaces: { id: string; label: string }[];
		onSaved: (cfg: DeviceProxyConfig) => void;
		onCancel?: () => void;
		onSaveConfig?: (cfg: DeviceProxyConfig) => Promise<DeviceProxyConfig>;
		title?: string;
		description?: string;
		embedded?: boolean;
		/** В модалке sing-box — кнопки в footer модалки, не в карточке. */
		hideFooter?: boolean;
		saving?: boolean;
	}

	let {
		config,
		outbounds,
		bridgeInterfaces,
		onSaved,
		onCancel,
		onSaveConfig = api.saveDeviceProxyConfig.bind(api),
		title = '',
		description = '',
		embedded = false,
		hideFooter = false,
		saving = $bindable(false),
	}: Props = $props();

	// Draft is a one-time snapshot of the prop. Edits survive store
	// refreshes — reset() is the explicit resync affordance.
	// svelte-ignore state_referenced_locally
	let draft = $state<DeviceProxyConfig>(structuredClone(config));

	let listenChoice = $derived(draft.listenAll ? '__all' : draft.listenInterface);

	function setListenChoice(v: string) {
		if (v === '__all') {
			draft.listenAll = true;
			draft.listenInterface = '';
		} else {
			draft.listenAll = false;
			draft.listenInterface = v;
		}
	}

	export function reset() {
		draft = structuredClone(config);
		onCancel?.();
	}

	function generatePassword() {
		const charset = 'ABCDEFGHIJKLMNPQRSTUVWXYZabcdefghijkmnpqrstuvwxyz23456789';
		let out = '';
		const arr = new Uint32Array(16);
		crypto.getRandomValues(arr);
		for (const n of arr) out += charset[n % charset.length];
		draft.auth.password = out;
	}

	export async function save() {
		saving = true;
		try {
			const saved = await onSaveConfig(draft);
			onSaved(saved);
			notifications.success(m.device_proxy_settings_saved());
		} catch (e) {
			notifications.error(m.sb_router_common_error({ message: (e as Error).message }));
		} finally {
			saving = false;
		}
	}

	let togglingEnabled = $state(false);
	async function toggleEnabled(next: boolean) {
		if (togglingEnabled) return;
		togglingEnabled = true;
		const payload = { ...config, enabled: next };
		try {
			const saved = await onSaveConfig(payload);
			draft = structuredClone(payload);
			onSaved(saved);
			notifications.success(next ? m.device_proxy_enabled_toast() : m.device_proxy_disabled_toast());
		} catch (e) {
			notifications.error(m.sb_router_common_error({ message: (e as Error).message }));
		} finally {
			togglingEnabled = false;
		}
	}

	let grouped = $derived.by(() => {
		const direct = outbounds.filter((o) => o.kind === 'direct');
		const sb = outbounds.filter((o) => o.kind === 'singbox');
		const sub = outbounds.filter((o) => o.kind === 'subscription');
		const awg = outbounds.filter((o) => o.kind === 'awg');
		const router = outbounds.filter((o) => o.kind === 'router');
		return { direct, sb, sub, awg, router };
	});

	let listenOpts = $derived<DropdownOption[]>([
		{ value: '__all', label: m.device_proxy_listen_all() },
		...bridgeInterfaces.map((br) => ({ value: br.id, label: br.label })),
	]);

	// Подписи опций — общий outboundOptionLabel: тот же формат видят бейджи
	// карточки Inbounds (DeviceProxyCompact), расхождение тег/имя исключено.
	let outboundOpts = $derived<DropdownOption[]>([
		...grouped.direct.map((ob) => ({ value: ob.tag, label: outboundOptionLabel(ob) })),
		...grouped.sb.map((ob) => ({ value: ob.tag, label: outboundOptionLabel(ob), group: m.device_proxy_group_singbox() })),
		...grouped.sub.map((ob) => ({ value: ob.tag, label: outboundOptionLabel(ob), group: m.device_proxy_group_subscriptions() })),
		...grouped.awg.map((ob) => ({ value: ob.tag, label: outboundOptionLabel(ob), group: m.device_proxy_group_tunnels() })),
		...grouped.router.map((ob) => ({ value: ob.tag, label: outboundOptionLabel(ob), group: m.device_proxy_group_router() })),
	]);
</script>

{#if embedded}
	<div class="form">
		<div class="field field-toggle">
			<div class="field-toggle-line">
				<div>
					<div class="lbl">{m.device_proxy_server()}</div>
					<div class="hint">{m.device_proxy_server_hint()}</div>
				</div>
				<Toggle checked={config.enabled} onchange={(v) => toggleEnabled(v)} loading={togglingEnabled} />
			</div>
		</div>

		<label class="field">
			<div class="lbl">{m.device_proxy_port()}</div>
			<input type="number" min="1024" max="65535" bind:value={draft.port} />
			<div class="hint">{m.device_proxy_port_hint()}</div>
		</label>

		<div class="field">
			<div class="lbl">{m.device_proxy_listen_label()}</div>
			<Dropdown value={listenChoice} options={listenOpts} onchange={setListenChoice} fullWidth />
			<div class="hint">{m.device_proxy_listen_hint()}</div>
		</div>

		<div class="field">
			<div class="lbl">{m.device_proxy_default_out_label()}</div>
			<Dropdown bind:value={draft.selectedOutbound} options={outboundOpts} fullWidth />
			<div class="hint">{m.device_proxy_default_out_hint()}</div>
		</div>

		<div class="field field-toggle">
			<div class="field-toggle-line">
				<div>
					<div class="lbl">{m.device_proxy_auth_label()}</div>
					<div class="hint">{m.device_proxy_auth_hint()}</div>
				</div>
				<Toggle checked={draft.auth.enabled} onchange={(v) => (draft.auth.enabled = v)} />
			</div>
		</div>

		{#if draft.auth.enabled}
			<label class="field">
				<div class="lbl">{m.device_proxy_username()}</div>
				<input type="text" bind:value={draft.auth.username} />
			</label>
			<div class="field">
				<div class="lbl">{m.device_proxy_password()}</div>
				<div class="pw-group">
					<input type="text" bind:value={draft.auth.password} />
					<Button variant="ghost" size="sm" onclick={generatePassword}>{m.common_generate()}</Button>
				</div>
			</div>
		{/if}
	</div>

	{#if !hideFooter}
		<div class="form-actions">
			<Button variant="ghost" size="md" onclick={reset} disabled={saving}>{m.device_proxy_reset()}</Button>
			<Button variant="primary" size="md" onclick={save} loading={saving}>{m.common_save()}</Button>
		</div>
	{/if}
{:else}
	<section class="card">
		{#if title || description}
			<header class="card-head">
				{#if title}<h2 class="section-title">{title}</h2>{/if}
				{#if description}<p class="section-desc">{description}</p>{/if}
			</header>
		{/if}

		<div class="card-body">
			<div class="settings-stack">
				<div class="setting-row setting-row-toggle">
					<div class="flex flex-col gap-1">
						<span class="font-medium">{m.device_proxy_server()}</span>
						<span class="setting-description">
							{m.device_proxy_server_hint()}
						</span>
					</div>
					<div class="setting-control setting-control-toggle">
						<Toggle
							checked={config.enabled}
							onchange={(v) => toggleEnabled(v)}
							loading={togglingEnabled}
						/>
					</div>
				</div>

				<div class="setting-row setting-row-field">
					<div class="flex flex-col gap-1">
						<span class="font-medium">{m.device_proxy_port()}</span>
						<span class="setting-description">{m.device_proxy_port_hint()}</span>
					</div>
					<div class="setting-control">
						<input type="number" min="1024" max="65535" bind:value={draft.port} class="num-input" />
					</div>
				</div>

				<div class="setting-row setting-row-field">
					<div class="flex flex-col gap-1">
						<span class="font-medium">{m.device_proxy_listen_label()}</span>
						<span class="setting-description">{m.device_proxy_listen_hint()}</span>
					</div>
					<div class="setting-control select">
						<Dropdown
							value={listenChoice}
							options={listenOpts}
							onchange={setListenChoice}
							fullWidth
						/>
					</div>
				</div>

				<div class="setting-row setting-row-field">
					<div class="flex flex-col gap-1">
						<span class="font-medium">{m.device_proxy_default_out_label()}</span>
						<span class="setting-description">{m.device_proxy_default_out_hint()}</span>
					</div>
					<div class="setting-control select">
						<Dropdown bind:value={draft.selectedOutbound} options={outboundOpts} fullWidth />
					</div>
				</div>

				<div class="setting-row setting-row-toggle">
					<div class="flex flex-col gap-1">
						<span class="font-medium">{m.device_proxy_auth_label()}</span>
						<span class="setting-description">{m.device_proxy_auth_hint()}</span>
					</div>
					<div class="setting-control setting-control-toggle">
						<Toggle checked={draft.auth.enabled} onchange={(v) => (draft.auth.enabled = v)} />
					</div>
				</div>

				{#if draft.auth.enabled}
					<div class="setting-row setting-row-field">
						<div class="flex flex-col gap-1">
							<span class="font-medium">{m.device_proxy_username()}</span>
						</div>
						<div class="setting-control">
							<input type="text" bind:value={draft.auth.username} class="text-input" />
						</div>
					</div>
					<div class="setting-row setting-row-field">
						<div class="flex flex-col gap-1">
							<span class="font-medium">{m.device_proxy_password()}</span>
						</div>
						<div class="setting-control">
							<div class="pw-group">
								<input type="text" bind:value={draft.auth.password} class="text-input" />
								<Button variant="ghost" size="sm" onclick={generatePassword}>
									{m.common_generate()}
								</Button>
							</div>
						</div>
					</div>
				{/if}
			</div>
		</div>

		<footer class="card-footer">
			<Button variant="ghost" size="md" onclick={reset} disabled={saving}>{m.device_proxy_reset()}</Button>
			<Button variant="primary" size="md" onclick={save} loading={saving}>{m.common_save()}</Button>
		</footer>
	</section>
{/if}

<style>
	.field-toggle-line {
		display: flex;
		align-items: flex-start;
		justify-content: space-between;
		gap: 0.75rem;
	}

	.pw-group {
		display: flex;
		gap: 0.5rem;
		align-items: center;
	}

	.form-actions {
		display: flex;
		justify-content: flex-end;
		gap: 0.5rem;
		margin-top: 0.75rem;
	}

	@media (max-width: 640px) {
		.pw-group {
			display: grid;
			grid-template-columns: minmax(0, 1fr);
			gap: 0.5rem;
			align-items: stretch;
			width: 100%;
		}

		.pw-group :global(.btn) {
			width: 100%;
		}

		.form-actions {
			display: grid;
			grid-template-columns: repeat(2, minmax(0, 1fr));
			align-items: stretch;
		}

		.form-actions :global(.btn) {
			width: 100%;
			min-width: 0;
		}
	}

	.card {
		min-width: 0;
		border: 1px solid var(--border);
		border-radius: 12px;
		background: var(--bg-secondary, var(--color-bg-secondary));
		overflow: hidden;
	}
	.card-head {
		padding: 1rem 1rem 0.875rem;
		border-bottom: 1px solid var(--border);
	}
	.card-body {
		padding: 0 1rem;
		min-width: 0;
	}
	.card-footer {
		display: flex;
		justify-content: flex-end;
		gap: 0.5rem;
		padding: 0.875rem 1rem;
		border-top: 1px solid var(--border);
		background: var(--bg-secondary, var(--color-bg-secondary));
	}
	.section-title { font-size: 1rem; font-weight: 600; margin: 0 0 0.25rem 0; }
	.section-desc { font-size: 0.8125rem; color: var(--text-muted); margin: 0; }
	.settings-stack {
		display: grid;
		gap: 0;
	}
	.setting-row {
		display: grid;
		grid-template-columns: minmax(0, 1fr) minmax(12rem, 18rem);
		align-items: center;
		gap: 0.75rem;
		padding: 0.75rem 0;
		border-bottom: 1px solid var(--border);
	}
	.setting-row:last-child {
		border-bottom: 0;
	}
	.setting-row > :first-child {
		min-width: 0;
	}
	.setting-control {
		width: 100%;
		min-width: 0;
		justify-self: stretch;
	}
	.setting-control-toggle {
		width: auto;
		justify-self: end;
	}
	.setting-description {
		color: var(--text-muted);
		font-size: 0.75rem;
		line-height: 1.35;
	}
	.num-input, .text-input {
		padding: 0.4rem 0.6rem;
		background: var(--bg-tertiary);
		border: 1px solid var(--border);
		border-radius: 4px;
		color: var(--text-primary);
		font-size: 0.8125rem;
	}
	.num-input { width: 120px; }
	.text-input { min-width: 200px; }
	.select { min-width: 240px; }

	@media (max-width: 640px) {
		.card-head {
			padding: 0.875rem 0.875rem 0.75rem;
		}

		.card-body {
			padding: 0 0.875rem;
		}

		.card-footer {
			display: grid;
			grid-template-columns: repeat(2, minmax(0, 1fr));
			gap: 0.5rem;
			padding: 0.75rem 0.875rem;
			align-items: stretch;
		}

		.card-footer :global(.btn) {
			width: 100%;
			min-width: 0;
		}

		.setting-row {
			padding: 0.875rem 0;
		}

		.setting-row-toggle {
			grid-template-columns: minmax(0, 1fr) auto;
			align-items: center;
			gap: 0.75rem;
		}

		.setting-row-field {
			grid-template-columns: minmax(0, 1fr);
			align-items: stretch;
			gap: 0.45rem;
		}

		.setting-description {
			max-width: 100%;
			overflow-wrap: anywhere;
		}

		.setting-control {
			width: 100%;
			min-width: 0;
		}

		.setting-row-toggle .setting-control {
			width: auto;
			justify-self: end;
		}

		.setting-row-toggle :global(.toggle-container),
		.setting-row-toggle :global([role='switch']) {
			justify-self: end;
		}

		.setting-row-field .num-input,
		.setting-row-field .text-input,
		.setting-row-field .select {
			width: 100%;
			min-width: 0;
			max-width: 100%;
			justify-self: stretch;
		}

		.setting-row-field .num-input,
		.setting-row-field .text-input {
			box-sizing: border-box;
			min-height: 2.25rem;
		}

		.setting-row-field :global(.dropdown),
		.setting-row-field :global(.dropdown-trigger),
		.setting-row-field :global(button) {
			max-width: 100%;
		}

		.card .pw-group {
			display: grid;
			grid-template-columns: minmax(0, 1fr);
			gap: 0.5rem;
			align-items: stretch;
			width: 100%;
		}

		.card .pw-group :global(.btn) {
			width: 100%;
		}
	}
</style>
