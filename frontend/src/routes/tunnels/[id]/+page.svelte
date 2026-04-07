<script lang="ts">
	import { page } from '$app/stores';
	import { goto } from '$app/navigation';
	import { onMount, onDestroy } from 'svelte';
	import { tunnels } from '$lib/stores/tunnels';
	import { notifications } from '$lib/stores/notifications';
	import { api } from '$lib/api/client';
	import type { AWGTunnel, SystemInfo, WANInterface, RouterInterface, TunnelListItem } from '$lib/types';
	import { PageContainer, LoadingSpinner } from '$lib/components/layout';
	import { Toggle } from '$lib/components/ui';
	import { superForm } from 'sveltekit-superforms';
	import { zod4Client } from 'sveltekit-superforms/adapters';
	import { editTunnelSchema } from '$lib/schemas/tunnel';
	import { AWGAdvancedParams, ReplaceTunnelConfigModal } from '$lib/components/tunnels';
	import TunnelEditHeader from '$lib/components/tunnels/TunnelEditHeader.svelte';

	let { data } = $props();

	// superForm is initialized once with initial data - capturing initial value is intentional
	// svelte-ignore state_referenced_locally
	const { form, errors } = superForm(data.form, {
		validators: zod4Client(editTunnelSchema),
		SPA: true,
	});

	const hints: Record<string, string> = {
		jc: 'Количество junk-пакетов, отправляемых перед handshake. Диапазон: 0-128.',
		jmin: 'Минимальный размер junk-пакета в байтах. Диапазон: 0-1280.',
		jmax: 'Максимальный размер junk-пакета в байтах. Диапазон: 0-1280.',
		s1: 'Padding для Init Handshake.',
		s2: 'Padding для Response Handshake.',
		s3: 'Padding для Transport Handshake Init.',
		s4: 'Padding для Transport Handshake Response.',
		h1: 'Кастомный заголовок для Init Handshake. Формат: число или диапазон (мин-макс).',
		h2: 'Кастомный заголовок для Response Handshake. Формат: число или диапазон (мин-макс).',
		h3: 'Кастомный заголовок для Cookie Reply. Формат: число или диапазон (мин-макс).',
		h4: 'Кастомный заголовок для Transport. Формат: число или диапазон (мин-макс).',
		i1: 'Signature пакет I1 — имитация протокола. Поддерживает CPS теги.',
		i2: 'Signature пакет I2.',
		i3: 'Signature пакет I3.',
		i4: 'Signature пакет I4.',
		i5: 'Signature пакет I5.'
	};

	type ActionStatus = 'loading' | 'success' | 'error';

	let activeTab = $state<'basic' | 'obfuscation' | 'routing'>('basic');
	let replaceModalOpen = $state(false);

	let tunnel = $state<AWGTunnel | null>(null);
	let isKernel = $derived(tunnel?.backend === 'kernel');
	let systemInfo = $state<SystemInfo | null>(null);
	let loading = $state(true);
	let saving = $state(false);

	let actionStatus = $state<ActionStatus | null>(null);

	let publicKey = $state('');

	// Split address into IPv4/IPv6 (UI only, backend uses single comma-separated string)
	let ipv4Address = $state('');
	let ipv6Address = $state('');

	function parseAddress(address: string) {
		const parts = address.split(',').map(s => s.trim());
		ipv4Address = parts.find(p => !p.includes(':')) || '';
		ipv6Address = parts.find(p => p.includes(':')) || '';
	}

	function joinAddress(): string {
		return [ipv4Address, ipv6Address].filter(Boolean).join(', ');
	}

	$effect(() => {
		$form.address = joinAddress();
	});

	// Routing tab state
	let wanInterfaces = $state<WANInterface[]>([]);
	let allInterfaces = $state<RouterInterface[]>([]);
	let showAllInterfaces = $state(false);
	let loadingAllInterfaces = $state(false);
	let allTunnels = $state<TunnelListItem[]>([]);
	let savingIsp = $state(false);

	function showActionResult(status: 'success' | 'error') {
		actionStatus = status;
		setTimeout(() => { actionStatus = null; }, 1500);
	}

	let tunnelId = $derived($page.params.id ?? '');

	// Address editable: NativeWG always (NDMS SyncAddressMTU); kernel — only before first start
	let addressDisabled = $derived.by(() => {
		if (!tunnel) return true;
		if (tunnel.backend === 'nativewg') return false;
		return tunnel.state !== 'not_created';
	});

	let ispValue = $derived(tunnel?.ispInterface || 'auto');

	let otherTunnels = $derived(allTunnels.filter(t => t.id !== tunnelId));

	function handleKeydown(e: KeyboardEvent) {
		if ((e.ctrlKey || e.metaKey) && e.key === 's') {
			e.preventDefault();
			if (!saving) handleSaveAndStart();
		}
	}

	onMount(async () => {
		window.addEventListener('keydown', handleKeydown);
		api.getSystemInfo().then(info => systemInfo = info).catch(() => null);
		await loadTunnel();
		loadWanData().catch(() => {});
	});

	onDestroy(() => {
		window.removeEventListener('keydown', handleKeydown);
	});

	async function loadTunnel() {
		if (!tunnelId) {
			notifications.error('ID туннеля не указан');
			goto('/');
			return;
		}

		loading = true;
		try {
			tunnel = await api.getTunnel(tunnelId);
			populateForm();
		} catch (e) {
			notifications.error(`Ошибка загрузки: ${(e as Error).message}`);
			goto('/');
		} finally {
			loading = false;
		}
	}

	async function loadWanData() {
		const [wans, tuns] = await Promise.all([
			api.getWANInterfaces(),
			api.listTunnels(),
		]);
		wanInterfaces = wans;
		allTunnels = tuns;
	}

	function populateForm() {
		if (!tunnel) return;

		$form.name = tunnel.name;
		parseAddress(tunnel.interface.address);
		$form.mtu = tunnel.interface.mtu || 1280;
		$form.dns = tunnel.interface.dns || '';
		$form.jc = tunnel.interface.jc ?? 4;
		$form.jmin = tunnel.interface.jmin ?? 40;
		$form.jmax = tunnel.interface.jmax ?? 70;
		$form.s1 = tunnel.interface.s1 ?? 0;
		$form.s2 = tunnel.interface.s2 ?? 0;
		$form.s3 = tunnel.interface.s3 ?? 0;
		$form.s4 = tunnel.interface.s4 ?? 0;
		$form.h1 = tunnel.interface.h1 ?? '';
		$form.h2 = tunnel.interface.h2 ?? '';
		$form.h3 = tunnel.interface.h3 ?? '';
		$form.h4 = tunnel.interface.h4 ?? '';
		$form.i1 = tunnel.interface.i1 || '';
		$form.i2 = tunnel.interface.i2 || '';
		$form.i3 = tunnel.interface.i3 || '';
		$form.i4 = tunnel.interface.i4 || '';
		$form.i5 = tunnel.interface.i5 || '';
		publicKey = tunnel.peer.publicKey;
		$form.endpoint = tunnel.peer.endpoint;
		$form.allowedIPs = tunnel.peer.allowedIPs.join(', ');
		$form.persistentKeepalive = tunnel.peer.persistentKeepalive || 25;
	}

	function buildUpdatePayload() {
		return {
			name: $form.name,
			interface: {
				...tunnel!.interface,
				address: joinAddress(),
				mtu: $form.mtu,
				dns: $form.dns || undefined,
				jc: $form.jc, jmin: $form.jmin, jmax: $form.jmax,
				s1: $form.s1, s2: $form.s2, s3: $form.s3, s4: $form.s4,
				h1: $form.h1, h2: $form.h2, h3: $form.h3, h4: $form.h4,
				i1: $form.i1 || undefined,
				i2: $form.i2 || undefined,
				i3: $form.i3 || undefined,
				i4: $form.i4 || undefined,
				i5: $form.i5 || undefined
			},
			peer: {
				...tunnel!.peer,
				endpoint: $form.endpoint,
				allowedIPs: $form.allowedIPs.split(',').map(ip => ip.trim()).filter(Boolean),
				persistentKeepalive: $form.persistentKeepalive
			}
		};
	}

	async function handleExport() {
		try {
			const blob = await api.exportTunnel(tunnelId);
			const { downloadBlob } = await import('$lib/utils/download');
			downloadBlob(blob, (tunnel?.name || tunnelId) + '.conf');
		} catch (e) {
			notifications.error('Не удалось скачать конфиг');
		}
	}

	async function handleSaveOnly() {
		if (!tunnel) return;

		saving = true;
		try {
			await tunnels.update(tunnelId, buildUpdatePayload());
			notifications.success('Туннель сохранён');
			goto('/');
		} catch (e) {
			notifications.error(`Ошибка: ${(e as Error).message}`);
		} finally {
			saving = false;
		}
	}

	async function handleSaveAndStart() {
		if (!tunnel) return;

		const isRunning = tunnel.state === 'running';
		actionStatus = 'loading';
		saving = true;
		try {
			await tunnels.update(tunnelId, buildUpdatePayload());
			if (isRunning) {
				await tunnels.restart(tunnelId);
			} else {
				await tunnels.start(tunnelId);
			}
			notifications.success(isRunning ? 'Туннель сохранён и перезапущен' : 'Туннель сохранён и запущен');
			goto('/');
		} catch (e) {
			notifications.error(`Ошибка: ${(e as Error).message}`);
			showActionResult('error');
		} finally {
			saving = false;
		}
	}

	async function toggleDefaultRoute() {
		if (!tunnel) return;
		try {
			const data = await api.toggleDefaultRoute(tunnelId);
			tunnel.defaultRoute = data.defaultRoute;
		} catch (e) {
			notifications.error(`Ошибка: ${(e as Error).message}`);
		}
	}

	async function updateIspInterface(value: string) {
		if (!tunnel) return;
		savingIsp = true;
		try {
			let ispLabel = '';
			if (value.startsWith('tunnel:')) {
				const targetId = value.replace('tunnel:', '');
				const target = allTunnels.find(t => t.id === targetId);
				ispLabel = target ? `Через ${target.name}` : value;
			} else if (value !== 'auto' && value !== '') {
				const iface = wanInterfaces.find(i => i.name === value)
					|| allInterfaces.find(i => i.name === value);
				ispLabel = iface?.label || value;
			}
			await api.updateTunnel(tunnelId, {
				ispInterface: value,
				ispInterfaceLabel: ispLabel,
			});
			tunnel.ispInterface = value === 'auto' ? '' : value;
			tunnel.ispInterfaceLabel = ispLabel;
		} catch (e) {
			notifications.error(`Ошибка: ${(e as Error).message}`);
		} finally {
			savingIsp = false;
		}
	}

	async function toggleAllInterfaces(checked: boolean) {
		showAllInterfaces = checked;
		if (checked && allInterfaces.length === 0) {
			loadingAllInterfaces = true;
			try {
				allInterfaces = await api.getAllInterfaces();
			} finally {
				loadingAllInterfaces = false;
			}
		}
	}

</script>

<svelte:head>
	<title>{tunnel?.name || 'Туннель'} - AWG Manager</title>
</svelte:head>

{#if loading}
	<PageContainer maxWidth="lg">
		<div class="flex flex-col items-center gap-4 p-12 text-secondary">
			<LoadingSpinner size="lg" message="Загрузка..." />
		</div>
	</PageContainer>
{:else if tunnel}
	<PageContainer maxWidth="xl" padding={false}>
	<div class="edit-wrapper">
		<TunnelEditHeader
			tunnelName={tunnel.name ?? ''}
			tunnelState={tunnel.state ?? 'stopped'}
			{saving}
			{actionStatus}
			onReplace={() => replaceModalOpen = true}
			onExport={handleExport}
			onSaveOnly={handleSaveOnly}
			onSaveAndStart={handleSaveAndStart}
		/>

		<div class="tab-bar">
			<button class="tab" class:active={activeTab === 'basic'} onclick={() => activeTab = 'basic'}>Основное</button>
			<button class="tab" class:active={activeTab === 'obfuscation'} onclick={() => activeTab = 'obfuscation'}>Обфускация</button>
			{#if !isKernel}
				<button class="tab" class:active={activeTab === 'routing'} onclick={() => activeTab = 'routing'}>Маршрутизация</button>
			{/if}
		</div>

		<div class="tab-content">
			{#if activeTab === 'basic'}
				<form class="tab-form" onsubmit={(e) => { e.preventDefault(); handleSaveAndStart(); }}>
					<section class="form-section">
						<h2 class="section-title">Название</h2>
						<div class="flex flex-col gap-1.5">
							<label class="label" for="name">Название туннеля</label>
							<input type="text" id="name" class="input" bind:value={$form.name} />
							{#if $errors.name}<p class="text-xs text-error-500 mt-1">{$errors.name}</p>{/if}
						</div>
					</section>

					<section class="form-section">
						<h2 class="section-title">Интерфейс [Interface]</h2>
						<div class="inline-fields">
							<div class="flex flex-col gap-1.5" style="flex:1">
								<label class="label" for="address-v4">IPv4 адрес</label>
								<input type="text" id="address-v4" class="input" bind:value={ipv4Address} disabled={addressDisabled} placeholder="10.0.0.2/32" />
							</div>
							<div class="flex flex-col gap-1.5" style="width:120px">
								<label class="label" for="mtu">MTU</label>
								<input type="number" id="mtu" class="input" bind:value={$form.mtu} />
								{#if $errors.mtu}<p class="text-xs text-error-500 mt-1">{$errors.mtu}</p>{/if}
							</div>
						</div>
						<div class="flex flex-col gap-1.5" style="margin-top:12px">
							<label class="label" for="address-v6">IPv6 адрес</label>
							<input type="text" id="address-v6" class="input" bind:value={ipv6Address} disabled={addressDisabled} placeholder="fd00::2/128 (необязательно)" />
						</div>
						{#if addressDisabled}
							<p class="field-hint">Адрес нельзя изменить для запущенного туннеля в режиме kernel</p>
						{/if}
						{#if $errors.address}<p class="text-xs text-error-500 mt-1">{$errors.address}</p>{/if}
						<div class="flex flex-col gap-1.5" style="margin-top:12px">
							<label class="label" for="dns">DNS</label>
							<input type="text" id="dns" class="input" bind:value={$form.dns} placeholder="1.1.1.1, 8.8.8.8" />
							<p class="field-hint">DNS-серверы через запятую. Применяются на роутере при старте туннеля.</p>
						</div>
					</section>

					<section class="form-section">
						<h2 class="section-title">Сервер [Peer]</h2>
						<div class="flex flex-col gap-1.5 pubkey-row">
							<span class="label">Публичный ключ</span>
							<code class="pubkey-value">{publicKey}</code>
						</div>
						<div class="flex flex-col gap-1.5" style="margin-bottom:12px">
							<label class="label" for="endpoint">Endpoint</label>
							<input type="text" id="endpoint" class="input" bind:value={$form.endpoint} />
							{#if $errors.endpoint}<p class="text-xs text-error-500 mt-1">{$errors.endpoint}</p>{/if}
						</div>
						<div class="inline-fields">
							<div class="flex flex-col gap-1.5" style="flex:1">
								<label class="label" for="allowedIPs">AllowedIPs</label>
								<input type="text" id="allowedIPs" class="input" bind:value={$form.allowedIPs} />
								{#if $errors.allowedIPs}<p class="text-xs text-error-500 mt-1">{$errors.allowedIPs}</p>{/if}
							</div>
							<div class="flex flex-col gap-1.5" style="width:120px">
								<label class="label" for="persistentKeepalive">Keepalive</label>
								<input type="number" id="persistentKeepalive" class="input" bind:value={$form.persistentKeepalive} />
								{#if $errors.persistentKeepalive}<p class="text-xs text-error-500 mt-1">{$errors.persistentKeepalive}</p>{/if}
							</div>
						</div>
					</section>
				</form>

			{:else if activeTab === 'obfuscation'}
				<div class="tab-form">
					<AWGAdvancedParams
						bind:form={$form}
						errors={$errors}
						{hints}
					/>
				</div>

			{:else if activeTab === 'routing'}
				<div class="tab-form">
					<section class="form-section">
						<h2 class="section-title">Подключение (ISP)</h2>
						<p class="section-hint">Через какой WAN-интерфейс роутер будет подключаться к серверу VPN. По умолчанию используется основной интернет-канал.</p>
						<select
							class="input"
							style="width:100%"
							value={ispValue}
							onchange={(e) => updateIspInterface((e.target as HTMLSelectElement).value)}
							disabled={savingIsp}
						>
							<option value="auto">Автоматически</option>
							{#each wanInterfaces as iface}
								<option value={iface.name}>{iface.label} ({iface.name})</option>
							{/each}
							{#if showAllInterfaces}
								{#each allInterfaces.filter(i => !wanInterfaces.some(w => w.name === i.name)) as iface}
									<option value={iface.name}>{iface.label} ({iface.name})</option>
								{/each}
							{/if}
							{#if otherTunnels.length > 0}
								<optgroup label="Через туннель">
									{#each otherTunnels as t}
										<option value="tunnel:{t.id}">{t.name}</option>
									{/each}
								</optgroup>
							{/if}
						</select>
						<div class="advanced-toggle">
							<Toggle
								checked={showAllInterfaces}
								onchange={toggleAllInterfaces}
								loading={loadingAllInterfaces}
								label="Показать все интерфейсы"
								hint="Включая внутренние интерфейсы роутера"
								size="sm"
							/>
						</div>
					</section>

					<section class="form-section">
						<h2 class="section-title">Маршрут по умолчанию</h2>
						<div class="setting-row">
							<div class="flex flex-col gap-1">
								<span class="font-medium">Default route</span>
								<span class="setting-description">NDMS default route через интерфейс туннеля</span>
							</div>
							<Toggle
								checked={tunnel.defaultRoute}
								onchange={() => toggleDefaultRoute()}
							/>
						</div>
					</section>
				</div>
			{/if}
		</div>
	</div>
	</PageContainer>

	{#if tunnel}
		<ReplaceTunnelConfigModal
			bind:open={replaceModalOpen}
			tunnelId={tunnel.id}
			tunnelName={tunnel.name}
			tunnelState={tunnel.state ?? 'stopped'}
			backendLabel={tunnel.backend === 'nativewg' ? 'NativeWG' : 'Kernel'}
			ndmsName={tunnel.interfaceName ?? tunnel.id}
			onclose={() => replaceModalOpen = false}
		/>
	{/if}
{/if}

<style>
	.text-secondary {
		color: var(--text-secondary);
	}

	.edit-wrapper {
		max-width: 1200px;
		width: 100%;
	}

	/* Tab bar */
	.tab-bar {
		display: flex;
		border-bottom: 2px solid var(--border);
	}

	.tab {
		padding: 10px 20px;
		font-size: 13px;
		font-weight: 500;
		color: var(--text-muted);
		background: none;
		border: none;
		border-bottom: 2px solid transparent;
		margin-bottom: -2px;
		cursor: pointer;
		transition: color 0.15s;
	}

	.tab:hover {
		color: var(--text-secondary);
	}

	.tab.active {
		color: var(--text-primary);
		font-weight: 600;
		border-bottom-color: var(--accent);
	}

	.tab-content {
		padding: 20px 0;
	}

	.tab-form {
		display: flex;
		flex-direction: column;
		gap: 20px;
	}

	.section-hint {
		color: var(--text-muted);
		font-size: 0.8125rem;
		margin: 4px 0 12px 0;
	}

	.advanced-toggle {
		margin-top: 12px;
		padding-top: 12px;
		border-top: 1px solid var(--border);
	}

	/* Inline fields row (e.g. Address + MTU, AllowedIPs + Keepalive) */
	.inline-fields {
		display: flex;
		gap: 12px;
		align-items: flex-start;
	}

	.form-section {
		background: var(--bg-secondary);
		border: 1px solid var(--border);
		border-radius: var(--radius);
		padding: 16px;
	}

	.section-title {
		font-size: 14px;
		font-weight: 600;
		padding-bottom: 10px;
		border-bottom: 1px solid var(--border);
	}

	.field-hint {
		margin-top: 4px;
		font-size: 12px;
		color: var(--text-muted);
		line-height: 1.5;
	}

	.pubkey-row {
		margin-bottom: 16px;
		padding-bottom: 16px;
		border-bottom: 1px solid var(--border);
	}

	.pubkey-value {
		font-family: monospace;
		font-size: 12px;
		color: var(--text-muted);
		word-break: break-all;
		padding: 6px 10px;
		background: var(--bg-tertiary);
		border-radius: 4px;
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

	.input:disabled {
		background: var(--bg-tertiary);
		color: var(--text-muted);
		cursor: not-allowed;
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

	@media (max-width: 600px) {
		.inline-fields {
			flex-direction: column;
		}

		.inline-fields > div {
			width: 100% !important;
		}
	}
</style>
