<script lang="ts">
	import { page } from '$app/stores';
	import { goto } from '$app/navigation';
	import { onMount, onDestroy } from 'svelte';
	import { tunnels } from '$lib/stores/tunnels';
	import { notifications } from '$lib/stores/notifications';
	import { api } from '$lib/api/client';
	import type { AWGTunnel, SystemInfo } from '$lib/types';
	import { PageContainer, LoadingSpinner } from '$lib/components/layout';
	import { Toggle } from '$lib/components/ui';
	import { superForm } from 'sveltekit-superforms';
	import { zod4Client } from 'sveltekit-superforms/adapters';
	import { editTunnelSchema } from '$lib/schemas/tunnel';
	import { AWGAdvancedParams } from '$lib/components/tunnels';
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

	let tunnel = $state<AWGTunnel | null>(null);
	let systemInfo = $state<SystemInfo | null>(null);
	let loading = $state(true);
	let saving = $state(false);

	let actionStatus = $state<ActionStatus | null>(null);

	let publicKey = $state('');

	function showActionResult(status: 'success' | 'error') {
		actionStatus = status;
		setTimeout(() => { actionStatus = null; }, 1500);
	}

	let tunnelId = $derived($page.params.id ?? '');

	// Address editable: always in userspace; kernel — only before first start
	// (OpkgTun doesn't exist yet, SetAddress on existing OpkgTun fails)
	let addressDisabled = $derived.by(() => {
		if (!systemInfo || !tunnel) return true;
		if (systemInfo.activeBackend === 'userspace') return false;
		return tunnel.state !== 'not_created';
	});

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

	function populateForm() {
		if (!tunnel) return;

		$form.name = tunnel.name;
		$form.address = tunnel.interface.address;
		$form.mtu = tunnel.interface.mtu || 1280;
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
		// Keep this separate for readonly display
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
				address: $form.address,
				mtu: $form.mtu,
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
			onSaveOnly={handleSaveOnly}
			onSaveAndStart={handleSaveAndStart}
		/>

		<div class="content-layout">
			<!-- Left column: core WireGuard settings -->
			<form class="left-column" onsubmit={(e) => { e.preventDefault(); handleSaveAndStart(); }}>
				<section class="form-section">
					<h2 class="section-title">Основные настройки</h2>
					<div class="flex flex-col gap-1.5">
						<label class="label" for="name">Название туннеля</label>
						<input type="text" id="name" class="input" bind:value={$form.name} />
						{#if $errors.name}<p class="text-xs text-error-500 mt-1">{$errors.name}</p>{/if}
					</div>
				</section>

				<section class="form-section">
					<h2 class="section-title">Интерфейс [Interface]</h2>
					<div class="form-row">
						<div class="flex flex-col gap-1.5">
							<label class="label" for="address">Адрес</label>
							<input type="text" id="address" class="input" bind:value={$form.address} disabled={addressDisabled} />
							{#if addressDisabled}
								<p class="field-hint">Адрес нельзя изменить для запущенного туннеля в режиме kernel</p>
							{/if}
							{#if $errors.address}<p class="text-xs text-error-500 mt-1">{$errors.address}</p>{/if}
						</div>
						<div class="flex flex-col gap-1.5">
							<label class="label" for="mtu">MTU</label>
							<input type="number" id="mtu" class="input" bind:value={$form.mtu} style="max-width: 120px;" />
							{#if $errors.mtu}<p class="text-xs text-error-500 mt-1">{$errors.mtu}</p>{/if}
						</div>
					</div>
				</section>

				<section class="form-section">
					<h2 class="section-title">Сервер [Peer]</h2>
					<div class="flex flex-col gap-1.5 pubkey-row">
						<span class="label">Публичный ключ</span>
						<code class="pubkey-value">{publicKey}</code>
					</div>
					<div class="form-row">
						<div class="flex flex-col gap-1.5">
							<label class="label" for="endpoint">Endpoint</label>
							<input type="text" id="endpoint" class="input" bind:value={$form.endpoint} />
							{#if $errors.endpoint}<p class="text-xs text-error-500 mt-1">{$errors.endpoint}</p>{/if}
						</div>
					</div>
					<div class="form-row">
						<div class="flex flex-col gap-1.5">
							<label class="label" for="allowedIPs">AllowedIPs</label>
							<input type="text" id="allowedIPs" class="input" bind:value={$form.allowedIPs} />
							{#if $errors.allowedIPs}<p class="text-xs text-error-500 mt-1">{$errors.allowedIPs}</p>{/if}
						</div>
						<div class="flex flex-col gap-1.5">
							<label class="label" for="persistentKeepalive">PersistentKeepalive</label>
							<input type="number" id="persistentKeepalive" class="input" bind:value={$form.persistentKeepalive} style="max-width: 100px;" />
							{#if $errors.persistentKeepalive}<p class="text-xs text-error-500 mt-1">{$errors.persistentKeepalive}</p>{/if}
						</div>
					</div>
				</section>
				<section class="form-section">
					<h2 class="section-title">Дополнительно</h2>
					<div class="setting-row">
						<div class="flex flex-col gap-1">
							<span class="font-medium">Маршрут по умолчанию</span>
							<span class="setting-description">NDMS default route через интерфейс туннеля</span>
						</div>
						<Toggle
							checked={tunnel.defaultRoute}
							onchange={() => toggleDefaultRoute()}
						/>
					</div>
				</section>
			</form>

			<!-- Right column: AWG obfuscation params -->
			<aside class="right-column">
				<div class="card">
					<h3 class="card-title">Обфускация AWG</h3>
					<AWGAdvancedParams
						bind:form={$form}
						errors={$errors}
						{hints}
						compact={true}
					/>
				</div>
			</aside>
		</div>
	</div>
	</PageContainer>
{/if}

<style>
	.text-secondary {
		color: var(--text-secondary);
	}

	.edit-wrapper {
		max-width: 1200px;
		width: 100%;
	}

	/* Two-column layout: left=core settings, right=testing+AWG */
	.content-layout {
		display: grid;
		grid-template-columns: 1fr 420px;
		gap: 24px;
		align-items: start;
	}

	.left-column {
		display: flex;
		flex-direction: column;
		gap: 20px;
	}

	.right-column {
		display: flex;
		flex-direction: column;
		gap: 16px;
		position: sticky;
		top: 136px;
	}

	.card {
		background: var(--bg-secondary);
		border: 1px solid var(--border);
		border-radius: var(--radius);
		padding: 16px;
		display: flex;
		flex-direction: column;
		gap: 10px;
	}

	.card-title {
		font-size: 14px;
		font-weight: 600;
		padding-bottom: 10px;
		border-bottom: 1px solid var(--border);
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

	.form-row {
		display: grid;
		grid-template-columns: repeat(2, 1fr);
		gap: 16px;
		margin-bottom: 16px;
	}

	.form-row:last-child {
		margin-bottom: 0;
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

	@media (max-width: 960px) {
		.content-layout {
			grid-template-columns: 1fr;
		}

		.right-column {
			position: static;
			order: -1;
		}
	}

	@media (max-width: 600px) {
		.form-row {
			grid-template-columns: 1fr;
		}
	}
</style>
