<script lang="ts">
	import { page } from '$app/stores';
	import { goto } from '$app/navigation';
	import { onMount } from 'svelte';
	import { api } from '$lib/api/client';
	import { singboxTunnels } from '$lib/stores/singbox';
	import { PageContainer } from '$lib/components/layout';
	import { SettingsSectionLabel } from '$lib/components/settings';
	import {
		ArrowLeft,
		Boxes,
		Copy,
		Globe,
		Link2,
		Lock,
		Radio,
		ScanEye,
		UserRound,
		Waypoints,
		Zap
	} from 'lucide-svelte';
	import { Button, Dropdown } from '$lib/components/ui';
	import { BindInterfacePicker } from '$lib/components/singbox';
	import { copyToClipboard } from '$lib/utils/clipboard';
	import { notifications } from '$lib/stores/notifications';

	const EXPORTABLE_PROTOCOLS = new Set([
		'vless',
		'trojan',
		'shadowsocks',
		'hysteria2',
		'naive',
		'mieru',
	]);

	let tag = $derived($page.params.tag!);
	let loading = $state(true);
	let saving = $state(false);
	let copyingLink = $state(false);
	let linkCopied = $state(false);
	let error = $state<string | null>(null);
	// The outbound is an arbitrary sing-box JSON object whose shape depends on the
	// protocol; the editor reads/writes fields by path, so it is modelled as a
	// dynamic record rather than a fixed interface.
	type JsonRecord = Record<string, unknown>;

	let outbound = $state<JsonRecord | null>(null);
	let protocol = $state<string>('');
	let editableTag = $state('');
	let initialOutboundFingerprint = $state('');

	let canExportShareLink = $derived(EXPORTABLE_PROTOCOLS.has(protocol));
	let hasUnsavedChanges = $derived(
		outbound != null && outboundFingerprint(outbound) !== initialOutboundFingerprint,
	);
	let copyLinkTitle = $derived(
		!canExportShareLink && protocol
			? `Экспорт не поддерживается для протокола ${protocol}`
			: hasUnsavedChanges
				? 'Копирует текущие значения формы без сохранения'
				: undefined,
	);

	onMount(async () => {
		try {
			const r = await api.singboxGetTunnel(tag);
			outbound = r.outbound as JsonRecord;
			protocol = typeof outbound.type === 'string' ? outbound.type : '';
			editableTag = r.tag;
			initialOutboundFingerprint = outboundFingerprint(outbound);
		} catch (e) {
			error = e instanceof Error ? e.message : String(e);
		} finally {
			loading = false;
		}
	});

	async function save(): Promise<void> {
		if (!outbound) return;
		const nextTag = editableTag.trim();
		if (!nextTag) {
			error = 'Название / tag обязательно';
			return;
		}
		saving = true;
		error = null;
		try {
			outbound = { ...outbound, tag: nextTag };
			const tagChanged = nextTag !== tag;
			const outboundChanged = outboundFingerprint(outbound) !== initialOutboundFingerprint;
			let fresh = $singboxTunnels.data ?? [];
			if (tagChanged) {
				fresh = await api.singboxRenameTunnel(tag, nextTag);
				singboxTunnels.applyMutationResponse(fresh);
			}
			if (outboundChanged) {
				fresh = await api.singboxUpdateTunnel(nextTag, outbound);
				singboxTunnels.applyMutationResponse(fresh);
			}
			goto('/?tab=singbox');
		} catch (e) {
			error = e instanceof Error ? e.message : String(e);
		} finally {
			saving = false;
		}
	}

	function setField(path: string[], value: unknown): void {
		if (!outbound) return;
		let obj: JsonRecord = outbound;
		for (let i = 0; i < path.length - 1; i++) {
			const next = obj[path[i]];
			if (next != null && typeof next === 'object') {
				obj = next as JsonRecord;
			} else {
				const created: JsonRecord = {};
				obj[path[i]] = created;
				obj = created;
			}
		}
		obj[path[path.length - 1]] = value;
		outbound = { ...outbound };
	}

	function getField(path: string[]): unknown {
		let obj: unknown = outbound;
		for (const p of path) {
			if (obj == null || typeof obj !== 'object') return undefined;
			obj = (obj as JsonRecord)[p];
		}
		return obj;
	}

	/** Field value coerced to a string for text inputs (empty when absent). */
	function getFieldString(path: string[]): string {
		const v = getField(path);
		if (v == null) return '';
		return typeof v === 'string' ? v : String(v);
	}

	/** Field value coerced to a boolean for checkbox inputs. */
	function getFieldBool(path: string[]): boolean {
		return getField(path) === true;
	}

	function serverPortsText(value: unknown): string {
		return Array.isArray(value) ? value.map(String).join('\n') : '';
	}

	function parseServerPorts(value: string): string[] | undefined {
		const parts = value
			.split(/[\n,]+/)
			.map((v) => v.trim())
			.filter(Boolean);
		return parts.length > 0 ? parts : undefined;
	}

	function setBindInterface(value: string): void {
		if (!outbound) return;
		if (!value.trim()) {
			const { bind_interface: _removed, ...rest } = outbound;
			outbound = rest;
			return;
		}
		setField(['bind_interface'], value.trim());
	}

	function outboundFingerprint(value: JsonRecord | null): string {
		if (!value) return '';
		const { tag: _tag, ...rest } = value;
		return JSON.stringify(rest);
	}

	async function copyShareLink(): Promise<void> {
		if (!outbound || copyingLink) return;
		copyingLink = true;
		try {
			const { link } = await api.singboxExportShareLink(outbound, editableTag.trim() || tag);
			const ok = await copyToClipboard(link);
			if (ok) {
				linkCopied = true;
				notifications.success('Ссылка скопирована в буфер обмена');
				setTimeout(() => {
					linkCopied = false;
				}, 1500);
			} else {
				notifications.error('Не удалось скопировать');
			}
		} catch (e) {
			notifications.error(e instanceof Error ? e.message : String(e));
		} finally {
			copyingLink = false;
		}
	}
</script>

<svelte:head>
	<title>{tag} — Sing-box</title>
</svelte:head>

<PageContainer width="wide">
	<div class="edit-wrapper">
	<div class="sticky-header">
		<div class="header-left">
			<Button variant="ghost" size="sm" onclick={() => goto('/?tab=singbox')} iconBefore={backIcon}>
				Назад
			</Button>
			<h1 class="page-title">{tag}</h1>
			{#if protocol}
				<span class="badge-protocol">{protocol}</span>
			{/if}
		</div>
		<div class="header-actions">
			<Button
				variant="secondary"
				size="md"
				onclick={copyShareLink}
				disabled={!outbound || !canExportShareLink}
				loading={copyingLink}
				iconBefore={copyIcon}
				title={copyLinkTitle}
			>
				{linkCopied ? 'Скопировано' : 'Копировать ссылку'}
			</Button>
			<Button
				variant="primary"
				size="md"
				onclick={save}
				disabled={!outbound}
				loading={saving}
			>
				Сохранить
			</Button>
		</div>
	</div>

	{#if loading}
		<div class="py-12 text-center text-surface-400">Загрузка...</div>
	{:else if !outbound}
		<div class="py-12 text-center text-error-500">{error ?? 'Туннель не найден'}</div>
	{:else}
		<form class="tab-form" onsubmit={(e) => { e.preventDefault(); save(); }}>
				<section class="card tunnel-section">
				<SettingsSectionLabel label="Основные параметры" icon={Globe} tone="slate" header />

				<div class="form-group">
					<label class="label" for="tag">Название / tag</label>
					<input
						id="tag"
						class="input"
						bind:value={editableTag}
						autocomplete="off"
					/>
				</div>

				<div class="form-group">
					<label class="label" for="server">Сервер</label>
					<input
						id="server"
						class="input"
						value={getFieldString(['server'])}
						oninput={(e) => setField(['server'], (e.target as HTMLInputElement).value)}
					/>
				</div>

				<div class="form-group">
					<label class="label" for="server_port">Порт</label>
					<input
						id="server_port"
						class="input"
						type="number"
						value={getFieldString(['server_port'])}
						oninput={(e) => setField(['server_port'], parseInt((e.target as HTMLInputElement).value, 10))}
					/>
				</div>
			</section>

			{#if protocol === 'vless'}
				<section class="card tunnel-section">
					<SettingsSectionLabel label="VLESS" icon={Link2} tone="purple" header />

					<div class="form-group">
						<label class="label" for="uuid">UUID</label>
						<input
							id="uuid"
							class="input"
							value={getFieldString(['uuid'])}
							oninput={(e) => setField(['uuid'], (e.target as HTMLInputElement).value)}
						/>
					</div>

					<div class="form-group">
						<label class="label" for="flow">Flow</label>
						<input
							id="flow"
							class="input"
							value={getFieldString(['flow'])}
							oninput={(e) => setField(['flow'], (e.target as HTMLInputElement).value)}
						/>
					</div>
				</section>

				{#if getField(['tls', 'reality'])}
					<section class="card tunnel-section">
						<SettingsSectionLabel label="Reality" icon={ScanEye} tone="indigo" header />

						<div class="form-group">
							<label class="label" for="reality_pubkey">Public Key</label>
							<input
								id="reality_pubkey"
								class="input"
								value={getFieldString(['tls', 'reality', 'public_key'])}
								oninput={(e) => setField(['tls', 'reality', 'public_key'], (e.target as HTMLInputElement).value)}
							/>
						</div>

						<div class="form-group">
							<label class="label" for="reality_short_id">Short ID</label>
							<input
								id="reality_short_id"
								class="input"
								value={getFieldString(['tls', 'reality', 'short_id'])}
								oninput={(e) => setField(['tls', 'reality', 'short_id'], (e.target as HTMLInputElement).value)}
							/>
						</div>
					</section>
				{/if}

				<section class="card tunnel-section">
					<SettingsSectionLabel label="TLS" icon={Lock} tone="blue" header />

					<div class="form-group">
						<label class="label" for="sni">SNI</label>
						<input
							id="sni"
							class="input"
							value={getFieldString(['tls', 'server_name'])}
							oninput={(e) => setField(['tls', 'server_name'], (e.target as HTMLInputElement).value)}
						/>
					</div>

					<div class="form-group">
						<Dropdown
							id="fingerprint"
							label="Fingerprint"
							value={getFieldString(['tls', 'utls', 'fingerprint'])}
							options={[
								{ value: '', label: '—' },
								{ value: 'chrome', label: 'chrome' },
								{ value: 'firefox', label: 'firefox' },
								{ value: 'safari', label: 'safari' },
								{ value: 'edge', label: 'edge' },
							]}
							onchange={(v) => setField(['tls', 'utls', 'fingerprint'], v)}
							fullWidth
						/>
					</div>
				</section>

				{#if getField(['transport', 'type']) === 'grpc'}
					<section class="card tunnel-section">
						<SettingsSectionLabel label="Transport (gRPC)" icon={Waypoints} tone="teal" header />

						<div class="form-group">
							<label class="label" for="grpc_service">Service Name</label>
							<input
								id="grpc_service"
								class="input"
								value={getFieldString(['transport', 'service_name'])}
								oninput={(e) => setField(['transport', 'service_name'], (e.target as HTMLInputElement).value)}
							/>
						</div>
					</section>
				{/if}

				{#if getField(['transport', 'type']) === 'ws'}
					<section class="card tunnel-section">
						<SettingsSectionLabel label="Transport (WebSocket)" icon={Radio} tone="orange" header />
						<p class="section-hint">Параметры импортированы из ссылки и редактированию не подлежат.</p>

						<div class="form-group">
							<label class="label" for="ws_path">Path</label>
							<input id="ws_path" class="input" value={getFieldString(['transport', 'path']) || '/'} readonly />
						</div>

						{#if getField(['transport', 'headers', 'Host'])}
							<div class="form-group">
								<label class="label" for="ws_host">Host header</label>
								<input id="ws_host" class="input" value={getFieldString(['transport', 'headers', 'Host'])} readonly />
							</div>
						{/if}

						{#if getField(['transport', 'early_data_header_name'])}
							<div class="form-group">
								<label class="label" for="ws_ed">Early Data Header</label>
								<input id="ws_ed" class="input" value={getFieldString(['transport', 'early_data_header_name'])} readonly />
							</div>
						{/if}
					</section>
				{/if}

			{:else if protocol === 'trojan'}
				<section class="card tunnel-section">
					<SettingsSectionLabel label="Trojan" icon={Link2} tone="orange" header />

					<div class="form-group">
						<label class="label" for="trojan_password">Пароль</label>
						<input
							id="trojan_password"
							class="input"
							type="password"
							value={getFieldString(['password'])}
							oninput={(e) => setField(['password'], (e.target as HTMLInputElement).value)}
						/>
					</div>
				</section>

				<section class="card tunnel-section">
					<SettingsSectionLabel label="TLS" icon={Lock} tone="blue" header />

					<div class="form-group">
						<label class="label" for="trojan_sni">SNI</label>
						<input
							id="trojan_sni"
							class="input"
							value={getFieldString(['tls', 'server_name'])}
							oninput={(e) => setField(['tls', 'server_name'], (e.target as HTMLInputElement).value)}
						/>
					</div>

					<div class="form-group">
						<Dropdown
							id="trojan_fingerprint"
							label="Fingerprint"
							value={getFieldString(['tls', 'utls', 'fingerprint'])}
							options={[
								{ value: '', label: '—' },
								{ value: 'chrome', label: 'chrome' },
								{ value: 'firefox', label: 'firefox' },
								{ value: 'safari', label: 'safari' },
								{ value: 'edge', label: 'edge' },
							]}
							onchange={(v) => setField(['tls', 'utls', 'fingerprint'], v)}
							fullWidth
						/>
					</div>

					<label class="checkbox-label">
						<input
							type="checkbox"
							checked={getFieldBool(['tls', 'insecure'])}
							onchange={(e) => setField(['tls', 'insecure'], (e.target as HTMLInputElement).checked)}
						/>
						<span>Insecure (пропустить проверку сертификата)</span>
					</label>
				</section>

				{#if getField(['transport', 'type']) === 'grpc'}
					<section class="card tunnel-section">
						<SettingsSectionLabel label="Transport (gRPC)" icon={Waypoints} tone="teal" header />

						<div class="form-group">
							<label class="label" for="trojan_grpc_service">Service Name</label>
							<input
								id="trojan_grpc_service"
								class="input"
								value={getFieldString(['transport', 'service_name'])}
								oninput={(e) => setField(['transport', 'service_name'], (e.target as HTMLInputElement).value)}
							/>
						</div>
					</section>
				{/if}

				{#if getField(['transport', 'type']) === 'ws'}
					<section class="card tunnel-section">
						<SettingsSectionLabel label="Transport (WebSocket)" icon={Radio} tone="orange" header />
						<p class="section-hint">Параметры импортированы из ссылки и редактированию не подлежат.</p>

						<div class="form-group">
							<label class="label" for="trojan_ws_path">Path</label>
							<input id="trojan_ws_path" class="input" value={getFieldString(['transport', 'path']) || '/'} readonly />
						</div>

						{#if getField(['transport', 'headers', 'Host'])}
							<div class="form-group">
								<label class="label" for="trojan_ws_host">Host header</label>
								<input id="trojan_ws_host" class="input" value={getFieldString(['transport', 'headers', 'Host'])} readonly />
							</div>
						{/if}
					</section>
				{/if}

			{:else if protocol === 'shadowsocks'}
				<section class="card tunnel-section">
					<SettingsSectionLabel label="Shadowsocks" icon={ScanEye} tone="slate" header />

					<div class="form-group">
						<label class="label" for="ss_method">Метод (cipher)</label>
						<input
							id="ss_method"
							class="input"
							value={getFieldString(['method'])}
							oninput={(e) => setField(['method'], (e.target as HTMLInputElement).value)}
							placeholder="aes-256-gcm"
						/>
					</div>

					<div class="form-group">
						<label class="label" for="ss_password">Пароль</label>
						<input
							id="ss_password"
							class="input"
							type="password"
							value={getFieldString(['password'])}
							oninput={(e) => setField(['password'], (e.target as HTMLInputElement).value)}
						/>
					</div>

					<div class="form-group">
						<label class="label" for="ss_plugin">Plugin</label>
						<input
							id="ss_plugin"
							class="input"
							value={getFieldString(['plugin'])}
							oninput={(e) => setField(['plugin'], (e.target as HTMLInputElement).value)}
							placeholder="obfs-local, v2ray-plugin…"
						/>
					</div>

					<div class="form-group">
						<label class="label" for="ss_plugin_opts">Plugin opts</label>
						<textarea
							id="ss_plugin_opts"
							class="input textarea"
							rows="2"
							value={getFieldString(['plugin_opts'])}
							oninput={(e) => setField(['plugin_opts'], (e.target as HTMLTextAreaElement).value)}
							placeholder="obfs=http;obfs-host=example.com"
						></textarea>
					</div>
				</section>

			{:else if protocol === 'hysteria2'}
				<section class="card tunnel-section">
					<SettingsSectionLabel label="Hysteria2" icon={Zap} tone="pink" header />

					<div class="form-group">
						<label class="label" for="password">Пароль</label>
						<input
							id="password"
							class="input"
							type="password"
							value={getFieldString(['password'])}
							oninput={(e) => setField(['password'], (e.target as HTMLInputElement).value)}
						/>
					</div>
				</section>

				<section class="card tunnel-section">
					<SettingsSectionLabel label="TLS" icon={Lock} tone="blue" header />

					<div class="form-group">
						<label class="label" for="hy2_sni">SNI</label>
						<input
							id="hy2_sni"
							class="input"
							value={getFieldString(['tls', 'server_name'])}
							oninput={(e) => setField(['tls', 'server_name'], (e.target as HTMLInputElement).value)}
						/>
					</div>

					<label class="checkbox-label">
						<input
							type="checkbox"
							checked={getFieldBool(['tls', 'insecure'])}
							onchange={(e) => setField(['tls', 'insecure'], (e.target as HTMLInputElement).checked)}
						/>
						<span>Insecure (пропустить проверку сертификата)</span>
					</label>
				</section>

			{:else if protocol === 'naive'}
				<section class="card tunnel-section">
					<SettingsSectionLabel label="NaiveProxy" icon={UserRound} tone="green" header />

					<div class="form-group">
						<label class="label" for="username">Пользователь</label>
						<input
							id="username"
							class="input"
							value={getFieldString(['username'])}
							oninput={(e) => setField(['username'], (e.target as HTMLInputElement).value)}
						/>
					</div>

					<div class="form-group">
						<label class="label" for="naive_password">Пароль</label>
						<input
							id="naive_password"
							class="input"
							type="password"
							value={getFieldString(['password'])}
							oninput={(e) => setField(['password'], (e.target as HTMLInputElement).value)}
						/>
					</div>
				</section>
			{:else if protocol === 'mieru'}
				<section class="card tunnel-section">
					<SettingsSectionLabel label="Mieru" icon={Boxes} tone="indigo" header />

					<div class="form-group">
						<label class="label" for="mieru_username">Пользователь</label>
						<input
							id="mieru_username"
							class="input"
							value={getFieldString(['username'])}
							oninput={(e) => setField(['username'], (e.target as HTMLInputElement).value)}
						/>
					</div>

					<div class="form-group">
						<label class="label" for="mieru_password">Пароль</label>
						<input
							id="mieru_password"
							class="input"
							type="password"
							value={getFieldString(['password'])}
							oninput={(e) => setField(['password'], (e.target as HTMLInputElement).value)}
						/>
					</div>

					<div class="form-group">
						<Dropdown
							id="mieru_transport"
							label="Transport"
							value={getFieldString(['transport']) || 'TCP'}
							options={[
								{ value: 'TCP', label: 'TCP' },
								{ value: 'UDP', label: 'UDP' },
							]}
							onchange={(v) => setField(['transport'], v)}
							fullWidth
						/>
					</div>

					<div class="form-group">
						<label class="label" for="mieru_server_ports">Дополнительные порты / диапазоны</label>
						<textarea
							id="mieru_server_ports"
							class="input textarea"
							rows="3"
							value={serverPortsText(getField(['server_ports']))}
							oninput={(e) => setField(['server_ports'], parseServerPorts((e.target as HTMLTextAreaElement).value))}
						></textarea>
					</div>

					<div class="form-group">
						<label class="label" for="mieru_multiplexing">Multiplexing</label>
						<input
							id="mieru_multiplexing"
							class="input"
							value={getFieldString(['multiplexing'])}
							oninput={(e) => setField(['multiplexing'], (e.target as HTMLInputElement).value)}
						/>
					</div>

					<div class="form-group">
						<label class="label" for="mieru_traffic_pattern">Traffic pattern</label>
						<textarea
							id="mieru_traffic_pattern"
							class="input textarea"
							rows="3"
							value={getFieldString(['traffic_pattern'])}
							oninput={(e) => setField(['traffic_pattern'], (e.target as HTMLTextAreaElement).value)}
						></textarea>
					</div>
				</section>
			{/if}

			{#if error}
				<div class="error-msg">{error}</div>
			{/if}

			<section class="card tunnel-section egress-section">
				<SettingsSectionLabel label="Исходящий интерфейс" icon={Waypoints} tone="slate" header />
				<BindInterfacePicker
					value={getFieldString(['bind_interface'])}
					onchange={setBindInterface}
					hint="Принудительно направляет dial этого прокси через выбранный uplink (модем, WAN, Wi‑Fi)."
				/>
			</section>

			<div class="form-actions">
				<Button variant="secondary" size="sm" onclick={() => goto('/?tab=singbox')}>Отмена</Button>
				<Button variant="primary" size="sm" type="submit" loading={saving}>
					Сохранить
				</Button>
			</div>
		</form>
	{/if}
	</div>
</PageContainer>

{#snippet backIcon()}
	<ArrowLeft size={14} strokeWidth={2} aria-hidden="true" />
{/snippet}

{#snippet copyIcon()}
	<Copy size={14} strokeWidth={2} aria-hidden="true" />
{/snippet}

<style>
	.sticky-header {
		display: flex;
		align-items: center;
		justify-content: space-between;
		position: sticky;
		top: 0;
		z-index: 10;
		background: var(--bg-primary);
		padding: 0.75rem 0;
		margin-bottom: 1rem;
		border-bottom: 1px solid var(--border);
	}

	.header-left {
		display: flex;
		align-items: center;
		gap: 0.75rem;
	}

	.header-actions {
		display: flex;
		align-items: center;
		gap: 0.5rem;
	}

	.page-title {
		font-size: 1.25rem;
		font-weight: 600;
		margin: 0;
	}

	.badge-protocol {
		display: inline-flex;
		align-items: center;
		padding: 2px 8px;
		font-size: 0.6875rem;
		font-weight: 500;
		border-radius: 9999px;
		background: rgba(148, 163, 184, 0.15);
		color: var(--text-muted);
	}

	.tunnel-section {
		background: var(--color-settings-surface-bg);
	}

	.tunnel-section :global(.settings-section-label.header) {
		margin-bottom: 12px;
	}

	.section-hint {
		font-size: 12px;
		color: var(--text-muted);
		margin: 0 0 12px;
	}

	.form-group {
		display: flex;
		flex-direction: column;
		gap: 6px;
		margin-bottom: 12px;
	}

	.form-group:last-child {
		margin-bottom: 0;
	}

	.label {
		font-size: 13px;
		font-weight: 500;
		color: var(--text-secondary);
	}

	.input {
		padding: 8px 12px;
		font-size: 13px;
		background: var(--bg-primary);
		border: 1px solid var(--border);
		border-radius: 6px;
		color: var(--text-primary);
		transition: border-color 0.15s;
	}

	.input:focus {
		outline: none;
		border-color: var(--accent);
	}

	.input[type="number"] {
		-moz-appearance: textfield;
		appearance: textfield;
	}

	.input[type="number"]::-webkit-outer-spin-button,
	.input[type="number"]::-webkit-inner-spin-button {
		-webkit-appearance: none;
		margin: 0;
	}

	.checkbox-label {
		display: inline-flex;
		align-items: center;
		gap: 8px;
		font-size: 13px;
		color: var(--text-primary);
		cursor: pointer;
	}

	.checkbox-label input[type="checkbox"] {
		accent-color: var(--accent);
	}

	.error-msg {
		color: var(--error);
		font-size: 13px;
		margin-bottom: 1rem;
	}

	.form-actions {
		display: flex;
		gap: 8px;
		justify-content: flex-end;
		margin-top: 1rem;
	}

	@media (max-width: 640px) {
		.sticky-header {
			flex-direction: column;
			gap: 0.75rem;
			align-items: stretch;
		}

		.header-left {
			flex-wrap: wrap;
		}

		.header-actions {
			width: 100%;
		}

		.header-actions :global(.btn) {
			flex: 1 1 0;
			min-width: 0;
		}
	}
</style>
