<script lang="ts">
	import { Button, ConfirmModal, Dropdown } from '$lib/components/ui';
	import { api } from '$lib/api/client';
	import { notifications } from '$lib/stores/notifications';
	import { tunnels as tunnelsStore } from '$lib/stores/tunnels';
	import type { AWGTunnel, TunnelListItem } from '$lib/types';
	import { buildManagedTunnelListDropdownOptions } from '$lib/utils/routingTunnelOptions';
	import { get } from 'svelte/store';
	import { servers } from '$lib/stores/servers';
	import type { ServersSnapshot } from '$lib/stores/servers';
	import {
		buildServerPeerDropdownOptions,
		decodeServerPeerValue,
	} from '$lib/utils/serverPeerOptions';
	import { parseAWG, type AwgParsed } from '$lib/utils/awgConfAnalyzer';
	import { scoreConfig, buildFixes, type ScoreResult } from '$lib/utils/awgConfScore';
	import AwgAnalyzerResult from './AwgAnalyzerResult.svelte';
	import { ShieldCheck } from 'lucide-svelte';
	import { onMount } from 'svelte';

	interface Props {
		initialTunnelId?: string;
		embedded?: boolean;
		lockTunnelSelection?: boolean;
		onTunnelSaved?: () => void;
	}

	let {
		initialTunnelId = '',
		embedded = false,
		lockTunnelSelection = false,
		onTunnelSaved,
	}: Props = $props();

	let raw = $state('');
	let lastAnalyzedRaw = $state('');
	let loadedTunnelRaw = $state('');
	let error = $state('');
	let parsed: AwgParsed | null = $state(null);
	let result: ScoreResult | null = $state(null);
	let fixes: string[] = $state([]);
	let analyzing = $state(false);
	let fileInput: HTMLInputElement | undefined = $state();

	let tunnels = $state<TunnelListItem[]>([]);
	let selectedTunnelId = $state('');
	// Источник анализа: локальный туннель (по умолчанию) или пир AWGM-сервера.
	let sourceMode = $state<'tunnel' | 'server'>('tunnel');
	let serverSnap = $state<ServersSnapshot | null>(null);
	let serversLoading = $state(false);
	let serversLoaded = $state(false);
	let selectedPeerValue = $state('');
	let peerLoading = $state(false);
	let peerLoadError = $state('');
	let tunnelsLoading = $state(false);
	let tunnelLoading = $state(false);
	let tunnelLoadError = $state('');

	let savingTunnel = $state(false);
	let confirmSaveOpen = $state(false);

	function isEmbeddedLocked(): boolean {
		return embedded && lockTunnelSelection && !!initialTunnelId;
	}

	async function ensureEmbeddedBaseline() {
		if (!isEmbeddedLocked()) return;
		if (loadedTunnelRaw !== '') return;
		const tunnel = await api.getTunnel(initialTunnelId);
		loadedTunnelRaw = awgTunnelToConf(tunnel).trim();
	}

	function resetSelectedTunnelForExternalInput() {
		if (isEmbeddedLocked()) {
			selectedTunnelId = initialTunnelId;
			return;
		}
		selectedTunnelId = '';
	}

	async function analyze() {
		error = '';
		parsed = null;
		result = null;
		fixes = [];
		tunnelLoadError = '';

		const t = raw.trim();
		if (!t) {
			error = 'Вставьте содержимое .conf файла AmneziaWG / WireGuard';
			return;
		}

		analyzing = true;
		try {
			// Локальный разбор нужен только пути записи в туннель (parsedToTunnelUpdate).
			const p = parseAWG(t);
			const data = await api.analyzeAwgConf(t, selectedTunnelId || undefined);
			const r = scoreConfig(data);
			parsed = p;
			result = r;
			fixes = buildFixes(r.checks);
			lastAnalyzedRaw = t;
		} catch (e) {
			error = e instanceof Error ? e.message : String(e);
		} finally {
			analyzing = false;
		}
	}

	function clearAll() {
		raw = '';
		lastAnalyzedRaw = '';
		if (!isEmbeddedLocked()) {
			loadedTunnelRaw = '';
		}
		error = '';
		parsed = null;
		result = null;
		fixes = [];
		selectedTunnelId = isEmbeddedLocked() ? initialTunnelId : '';
		selectedPeerValue = '';
		peerLoadError = '';
		tunnelLoadError = '';
	}

	function awgTunnelToConf(t: AWGTunnel): string {
		const i = t.interface;
		const p = t.peer;

		const lines: string[] = [
			'[Interface]',
			i.privateKey ? `PrivateKey = ${i.privateKey}` : '',
			i.address ? `Address = ${i.address}` : '',
			i.dns ? `DNS = ${i.dns}` : '',
			i.mtu ? `MTU = ${i.mtu}` : '',
			i.jc != null ? `Jc = ${i.jc}` : '',
			i.jmin != null ? `Jmin = ${i.jmin}` : '',
			i.jmax != null ? `Jmax = ${i.jmax}` : '',
			i.s1 != null ? `S1 = ${i.s1}` : '',
			i.s2 != null ? `S2 = ${i.s2}` : '',
			i.s3 != null ? `S3 = ${i.s3}` : '',
			i.s4 != null ? `S4 = ${i.s4}` : '',
			i.h1 ? `H1 = ${i.h1}` : '',
			i.h2 ? `H2 = ${i.h2}` : '',
			i.h3 ? `H3 = ${i.h3}` : '',
			i.h4 ? `H4 = ${i.h4}` : '',
			i.i1 ? `I1 = ${i.i1}` : '',
			i.i2 ? `I2 = ${i.i2}` : '',
			i.i3 ? `I3 = ${i.i3}` : '',
			i.i4 ? `I4 = ${i.i4}` : '',
			i.i5 ? `I5 = ${i.i5}` : '',
			i.headerProtectionKey ? `HeaderProtectionKey = ${i.headerProtectionKey}` : '',
			i.contentPaddingAddition ? `ContentPaddingAddition = ${i.contentPaddingAddition}` : '',
			i.rekeyAfterTime ? `RekeyAfterTime = ${i.rekeyAfterTime}` : '',
			i.rekeyTimeout ? `RekeyTimeout = ${i.rekeyTimeout}` : '',
			i.rejectAfterTime ? `RejectAfterTime = ${i.rejectAfterTime}` : '',
			i.keepaliveTimeout ? `KeepaliveTimeout = ${i.keepaliveTimeout}` : '',
			i.maxHandshakeAttempts ? `MaxHandshakeAttempts = ${i.maxHandshakeAttempts}` : '',
			i.randomTrailers ? 'RandomTrailers = on' : '',
			i.disableCookies ? 'DisableCookies = on' : '',
			'',
			'[Peer]',
			p.publicKey ? `PublicKey = ${p.publicKey}` : '',
			p.presharedKey ? `PresharedKey = ${p.presharedKey}` : '',
			p.endpoint ? `Endpoint = ${p.endpoint}` : '',
			p.allowedIPs?.length ? `AllowedIPs = ${p.allowedIPs.join(', ')}` : '',
			p.persistentKeepalive != null ? `PersistentKeepalive = ${p.persistentKeepalive}` : '',
		];

		return lines.filter((line) => line !== '').join('\n');
	}

	function numOrCurrent(value: string | undefined, current: number): number {
		const n = value !== undefined && value !== '' ? Number(value) : NaN;
		return Number.isFinite(n) ? n : current;
	}

	function strOrCurrent(value: string | undefined, current: string): string {
		const v = value?.trim();
		return v ? v : current;
	}

	function emptyToUndefined(value: string | undefined): string | undefined {
		const v = value?.trim();
		return v ? v : undefined;
	}

	function optionalStrOrCurrent(value: string | undefined, current: string | undefined): string | undefined {
		if (value === undefined) return current;
		const v = value.trim();
		return v ? v : undefined;
	}

	function parsedToTunnelUpdate(current: AWGTunnel, parsed: AwgParsed): Partial<AWGTunnel> {
		const iface = parsed.iface;
		const peer = parsed.peer;

		return {
			interface: {
				...current.interface,

				privateKey: strOrCurrent(iface.privatekey, current.interface.privateKey),
				address: strOrCurrent(iface.address, current.interface.address),
				mtu: numOrCurrent(iface.mtu, current.interface.mtu),
				dns: iface.dns === undefined ? current.interface.dns : emptyToUndefined(iface.dns),

				jc: numOrCurrent(iface.jc, current.interface.jc),
				jmin: numOrCurrent(iface.jmin, current.interface.jmin),
				jmax: numOrCurrent(iface.jmax, current.interface.jmax),

				s1: numOrCurrent(iface.s1, current.interface.s1),
				s2: numOrCurrent(iface.s2, current.interface.s2),
				s3: numOrCurrent(iface.s3, current.interface.s3),
				s4: numOrCurrent(iface.s4, current.interface.s4),

				h1: strOrCurrent(iface.h1, current.interface.h1),
				h2: strOrCurrent(iface.h2, current.interface.h2),
				h3: strOrCurrent(iface.h3, current.interface.h3),
				h4: strOrCurrent(iface.h4, current.interface.h4),

				i1: optionalStrOrCurrent(iface.i1, current.interface.i1),
				i2: optionalStrOrCurrent(iface.i2, current.interface.i2),
				i3: optionalStrOrCurrent(iface.i3, current.interface.i3),
				i4: optionalStrOrCurrent(iface.i4, current.interface.i4),
				i5: optionalStrOrCurrent(iface.i5, current.interface.i5),

				// AWG 3.0 device params (parseAWG lowercases the keys).
				headerProtectionKey: optionalStrOrCurrent(iface.headerprotectionkey, current.interface.headerProtectionKey),
				contentPaddingAddition: optionalStrOrCurrent(iface.contentpaddingaddition, current.interface.contentPaddingAddition),
				rekeyAfterTime: optionalStrOrCurrent(iface.rekeyaftertime, current.interface.rekeyAfterTime),
				rekeyTimeout: optionalStrOrCurrent(iface.rekeytimeout, current.interface.rekeyTimeout),
				rejectAfterTime: optionalStrOrCurrent(iface.rejectaftertime, current.interface.rejectAfterTime),
				keepaliveTimeout: optionalStrOrCurrent(iface.keepalivetimeout, current.interface.keepaliveTimeout),
				maxHandshakeAttempts: optionalStrOrCurrent(iface.maxhandshakeattempts, current.interface.maxHandshakeAttempts),
			},
			peer: {
				...current.peer,

				publicKey: strOrCurrent(peer.publickey, current.peer.publicKey),
				presharedKey: optionalStrOrCurrent(peer.presharedkey, current.peer.presharedKey),
				endpoint: strOrCurrent(peer.endpoint, current.peer.endpoint),

				allowedIPs:
					peer.allowedips !== undefined && peer.allowedips.trim() !== ''
						? peer.allowedips.split(',').map((s) => s.trim()).filter(Boolean)
						: current.peer.allowedIPs,

				// AWG 3.0 допускает диапазон "min-max", поэтому значение переносим
				// строкой как есть; формат проверяют схема формы и бэкенд.
				persistentKeepalive:
					peer.persistentkeepalive !== undefined && peer.persistentkeepalive.trim() !== ''
						? peer.persistentkeepalive.trim()
						: current.peer.persistentKeepalive,
			},
		};
	}

	let rawChangedSinceAnalyze = $derived(
		parsed !== null && raw.trim() !== lastAnalyzedRaw
	);

	let rawDiffersFromLoadedTunnel = $derived(
		!!selectedTunnelId &&
		loadedTunnelRaw !== '' &&
		raw.trim() !== loadedTunnelRaw
	);

	let canSave = $derived(
		!!selectedTunnelId &&
		parsed !== null &&
		!error &&
		!savingTunnel &&
		!rawChangedSinceAnalyze &&
		rawDiffersFromLoadedTunnel
	);

	function saveToTunnel() {
		if (!selectedTunnelId) return;
		confirmSaveOpen = true;
	}

	async function doSaveToTunnel() {
		if (!selectedTunnelId) return;

		if (raw.trim() !== lastAnalyzedRaw) {
			confirmSaveOpen = false;
			notifications.error('Конфиг изменён после анализа. Нажмите «Анализировать» перед записью в туннель.');
			return;
		}

		if (loadedTunnelRaw !== '' && raw.trim() === loadedTunnelRaw) {
			confirmSaveOpen = false;
			notifications.error('Изменений относительно выбранного туннеля нет.');
			return;
		}

		savingTunnel = true;
		try {
			const currentRaw = raw.trim();
			if (!currentRaw) {
				throw new Error('Вставьте содержимое .conf файла AmneziaWG / WireGuard');
			}

			const freshParsed = parseAWG(currentRaw);
			const current = await api.getTunnel(selectedTunnelId);
			const update = parsedToTunnelUpdate(current, freshParsed);
			await tunnelsStore.update(selectedTunnelId, update);
			// Текст только что записан в туннель, значит он и есть его conf:
			// провенанс верен по построению, и предикат ниже это увидит.
			loadedTunnelRaw = currentRaw;

			await analyze();

			notifications.success('Конфиг записан в туннель');
			onTunnelSaved?.();
			confirmSaveOpen = false;
		} catch (e) {
			error = e instanceof Error ? e.message : String(e);
			notifications.error(e instanceof Error ? e.message : 'Ошибка сохранения');
		} finally {
			savingTunnel = false;
		}
	}

	async function loadServersOnce() {
		if (serversLoaded) return;
		serversLoading = true;
		peerLoadError = '';
		// servers.refetch() НЕ бросает — ошибку запроса пишет в state стора и
		// резолвится. Поэтому читаем стор после await и поднимаем его error;
		// serversLoaded оставляем false при сбое → следующее переключение
		// повторит загрузку.
		await servers.refetch();
		const st = get(servers);
		if (st.error && !st.data) {
			peerLoadError = st.error;
			serversLoading = false;
			return;
		}
		serverSnap = st.data;
		serversLoaded = true;
		serversLoading = false;
	}

	function setSource(mode: 'tunnel' | 'server') {
		sourceMode = mode;
		if (mode === 'server') {
			// Держим selectedTunnelId пустым → save-back скрыт (read-only).
			selectedTunnelId = '';
			void loadServersOnce();
		} else {
			selectedPeerValue = '';
		}
	}

	async function applyFromSelectedPeer() {
		if (!selectedPeerValue) {
			peerLoadError = '';
			return;
		}
		const { kind, serverId, pubkey } = decodeServerPeerValue(selectedPeerValue);
		peerLoading = true;
		peerLoadError = '';
		try {
			const conf =
				kind === 'system'
					? await api.getSystemServerPeerConf(serverId, pubkey)
					: await api.getManagedPeerConf(serverId, pubkey);
			raw = conf;
			analyze();
		} catch (e) {
			peerLoadError = e instanceof Error ? e.message : String(e);
		} finally {
			peerLoading = false;
		}
	}

	async function applyFromSelectedTunnel() {
		if (!selectedTunnelId) {
			tunnelLoadError = '';
			return;
		}

		tunnelLoading = true;
		tunnelLoadError = '';

		try {
			const tunnel = await api.getTunnel(selectedTunnelId);
			const conf = awgTunnelToConf(tunnel);
			loadedTunnelRaw = conf.trim();
			raw = conf;
			analyze();
		} catch (e) {
			tunnelLoadError = e instanceof Error ? e.message : String(e);
		} finally {
			tunnelLoading = false;
		}
	}

	function onPickFile(e: Event) {
		const input = e.target as HTMLInputElement;
		const file = input.files?.[0];
		if (!file) return;
		const reader = new FileReader();
		reader.onload = async () => {
			if (!isEmbeddedLocked()) {
				loadedTunnelRaw = '';
			}
			resetSelectedTunnelForExternalInput();
			if (isEmbeddedLocked()) {
				try {
					await ensureEmbeddedBaseline();
				} catch {
					loadedTunnelRaw = '';
				}
			}
			raw = String(reader.result ?? '').trim();
			analyze();
		};
		reader.readAsText(file);
		input.value = '';
	}

	function onDrop(e: DragEvent) {
		e.preventDefault();
		const file = e.dataTransfer?.files?.[0];
		if (!file) return;
		const reader = new FileReader();
		reader.onload = async () => {
			if (!isEmbeddedLocked()) {
				loadedTunnelRaw = '';
			}
			resetSelectedTunnelForExternalInput();
			if (isEmbeddedLocked()) {
				try {
					await ensureEmbeddedBaseline();
				} catch {
					loadedTunnelRaw = '';
				}
			}
			raw = String(reader.result ?? '').trim();
			analyze();
		};
		reader.readAsText(file);
	}

	function onKeydown(e: KeyboardEvent) {
		if (e.key !== 'Enter') return;
		if (!e.ctrlKey && !e.metaKey) return;
		if (!canAnalyze) return;
		e.preventDefault();
		analyze();
	}

	onMount(async () => {
		tunnelsLoading = true;
		tunnelLoadError = '';
		try {
			const snap = await api.getTunnelsAll();
			tunnels = (snap.tunnels ?? []).filter((t) => t.id && t.type !== 'singbox');
			if (initialTunnelId) {
				selectedTunnelId = initialTunnelId;
			}
			if (selectedTunnelId) {
				await applyFromSelectedTunnel();
			}
		} catch (e) {
			tunnelLoadError = e instanceof Error ? e.message : String(e);
			tunnels = [];
		} finally {
			tunnelsLoading = false;
		}
	});

	const tunnelOptions = $derived(buildManagedTunnelListDropdownOptions(tunnels));
	const serverPeerOptions = $derived(buildServerPeerDropdownOptions(serverSnap));
	const serverPlaceholder = $derived(
		serversLoading
			? 'Загрузка серверов…'
			: serverPeerOptions.length
				? 'Выберите пир сервера'
				: 'Нет доступных пиров',
	);
	const tunnelPlaceholder = $derived(
		tunnelsLoading
			? 'Загрузка туннелей…'
			: tunnelOptions.length
				? 'Выберите туннель'
				: 'Нет AWG-туннелей',
	);

	let canAnalyze = $derived(raw.trim().length > 0);
</script>

<svelte:window onkeydown={onKeydown} />

<div class="awg-analyzer">
	<div class="privacy-banner" role="status">
		<div class="privacy-banner-icon" aria-hidden="true">
			<ShieldCheck size={18} />
		</div>
		<div class="privacy-banner-body">
			<p class="privacy-banner-title">Конфиг не покидает роутер</p>
			<p class="privacy-banner-text">
				Конфиг проверяется на роутере; ключи в ответ не возвращаются.
			</p>
			<div class="privacy-banner-tags">
				<span class="privacy-tag">Данные остаются у вас</span>
				<span class="privacy-tag privacy-tag-warning">Оценка эвристическая и не гарантирует обход DPI</span>
				<span class="privacy-tag privacy-tag-muted">Изменение параметров на свой страх и риск</span>
			</div>
		</div>
	</div>

	<div class="layout">
		<div class="col-input">
			{#if !embedded || !lockTunnelSelection}
				<div class="source-toggle">
					<button
						type="button"
						class="source-tab"
						class:active={sourceMode === 'tunnel'}
						onclick={() => setSource('tunnel')}
					>
						Локальный туннель
					</button>
					<button
						type="button"
						class="source-tab"
						class:active={sourceMode === 'server'}
						onclick={() => setSource('server')}
					>
						Сервер AWGM
					</button>
				</div>

				{#if sourceMode === 'tunnel'}
					<div class="existing-tunnel-box">
						<div class="existing-tunnel-head">
							<span class="existing-tunnel-title">Существующий AWG-туннель</span>
							<span class="existing-tunnel-note">или вставьте .conf ниже</span>
						</div>
						<div class="existing-tunnel-row">
							<div class="existing-tunnel-select">
								<Dropdown
									bind:value={selectedTunnelId}
									options={tunnelOptions}
									placeholder={tunnelPlaceholder}
									onchange={() => void applyFromSelectedTunnel()}
									disabled={tunnelsLoading || tunnelLoading || tunnelOptions.length === 0}
									fullWidth
								/>
							</div>
							{#if tunnelLoading}
								<span class="existing-tunnel-loading">Загрузка…</span>
							{/if}
						</div>
						{#if tunnelLoadError}
							<div class="warn" role="alert">{tunnelLoadError}</div>
						{/if}
					</div>
				{:else}
					<div class="existing-tunnel-box">
						<div class="existing-tunnel-head">
							<span class="existing-tunnel-title">Пир AWGM-сервера</span>
							<span class="existing-tunnel-note">
								оценивается обфускация/стойкость к DPI выдаваемого конфига, не коллизии IP/ключей
							</span>
						</div>
						<div class="existing-tunnel-row">
							<div class="existing-tunnel-select">
								<Dropdown
									bind:value={selectedPeerValue}
									options={serverPeerOptions}
									placeholder={serverPlaceholder}
									onchange={() => void applyFromSelectedPeer()}
									disabled={serversLoading || peerLoading || serverPeerOptions.length === 0}
									fullWidth
								/>
							</div>
							{#if peerLoading}
								<span class="existing-tunnel-loading">Загрузка…</span>
							{/if}
						</div>
						{#if peerLoadError}
							<div class="warn" role="alert">{peerLoadError}</div>
						{/if}
					</div>
				{/if}
			{:else}
				<div class="existing-tunnel-box embedded">
					<div class="existing-tunnel-head">
						<span class="existing-tunnel-title">Текущий AWG-туннель</span>
						<span class="existing-tunnel-note">конфиг загружен из открытого редактора</span>
					</div>
					{#if tunnelLoadError}
						<div class="warn" role="alert">{tunnelLoadError}</div>
					{/if}
				</div>
			{/if}

			<!-- svelte-ignore a11y_no_noninteractive_element_interactions -->
			<label
				class="drop"
				ondragover={(e) => e.preventDefault()}
				ondrop={onDrop}
			>
				<span class="drop-label">AWG / WireGuard .conf — вставьте или перетащите файл</span>
				<textarea
					class="ta"
					bind:value={raw}
					rows="16"
					spellcheck="false"
					autocomplete="off"
					placeholder="[Interface]&#10;PrivateKey = …&#10;…"
				></textarea>
			</label>

			<div class="bar">
				<Button variant="primary" onclick={analyze} disabled={!canAnalyze || analyzing} loading={analyzing}>Анализировать</Button>
				<Button variant="secondary" onclick={() => fileInput?.click()}>Загрузить файл</Button>
				<Button variant="ghost" onclick={clearAll}>Очистить</Button>
				{#if canSave}
					<Button variant="outline-primary" onclick={saveToTunnel} loading={savingTunnel}>
						Записать в туннель
					</Button>
				{/if}
				<span class="kbd">⌘/Ctrl+Enter</span>
			</div>
			{#if selectedTunnelId && rawChangedSinceAnalyze}
				<p class="bar-hint" role="status">
					Конфиг был изменён вручную, если вы хотите применить изменения к туннелю, сначала нажмите «Анализировать».				
					Учтите, что это может привести к нарушению работы туннеля, если вы не уверены в том, что делаете.
				</p>
			{/if}

			<input
				bind:this={fileInput}
				type="file"
				accept=".conf,.txt"
				class="sr"
				onchange={onPickFile}
			/>

			{#if error}
				<div class="err" role="alert">{error}</div>
			{/if}
		</div>

		<div class="col-results">
			{#if result}
				<AwgAnalyzerResult {result} {fixes} />
			{:else}
				<div class="results-empty">
					<p class="results-empty-title">Результаты анализа</p>
					<p class="results-empty-text">
						После нажатия «Анализировать» здесь появятся оценка, рекомендации и список проверок.
					</p>
				</div>
			{/if}
		</div>
	</div>
</div>

<ConfirmModal
	open={confirmSaveOpen}
	title="Записать конфиг в туннель?"
	message="Вы собираетесь перезаписать параметры выбранного туннеля данными из поля конфига."
	secondary="Будут обновлены параметры Interface и Peer. Если туннель сейчас работает, для применения изменений может потребоваться перезапуск. Действие необратимо без ручного восстановления старого конфига."
	confirmLabel="Записать"
	variant="danger"
	busy={savingTunnel}
	onConfirm={doSaveToTunnel}
	onClose={() => !savingTunnel && (confirmSaveOpen = false)}
/>

<style>
	.awg-analyzer {
		box-sizing: border-box;
		width: 100%;
		max-width: 1180px;
		margin: 0 auto;
		padding: 12px 16px 28px;
		color: var(--color-text-primary, var(--text-primary));
		overflow-x: clip;
	}

	.privacy-banner {
		display: flex;
		align-items: flex-start;
		gap: 12px;
		margin: 0 0 16px;
		padding: 12px 14px;
		border-radius: 12px;
		border: 1px solid
			color-mix(in srgb, var(--color-success, #22c55e) 32%, var(--color-border));
		background: linear-gradient(
			135deg,
			color-mix(in srgb, var(--color-success, #22c55e) 11%, var(--color-bg-secondary, var(--bg-secondary))),
			color-mix(in srgb, var(--color-info, #3b82f6) 7%, var(--color-bg-secondary, var(--bg-secondary)))
		);
		box-shadow: inset 0 1px 0 color-mix(in srgb, white 8%, transparent);
	}

	.privacy-banner-icon {
		flex-shrink: 0;
		display: grid;
		place-items: center;
		width: 36px;
		height: 36px;
		border-radius: 10px;
		color: var(--color-success, #22c55e);
		background: color-mix(in srgb, var(--color-success, #22c55e) 14%, transparent);
		border: 1px solid color-mix(in srgb, var(--color-success, #22c55e) 24%, transparent);
	}

	.privacy-banner-body {
		min-width: 0;
		flex: 1;
	}

	.privacy-banner-title {
		margin: 0 0 4px;
		font-size: 13px;
		font-weight: 600;
		line-height: 1.3;
		color: var(--color-text-primary, var(--text-primary));
	}

	.privacy-banner-text {
		margin: 0;
		font-size: 12px;
		line-height: 1.45;
		color: var(--color-text-secondary, var(--text-secondary));
	}

	.privacy-banner-tags {
		display: flex;
		flex-wrap: wrap;
		gap: 6px;
		margin-top: 10px;
	}

	.privacy-tag {
		display: inline-flex;
		align-items: center;
		padding: 3px 8px;
		border-radius: 999px;
		font-size: 10px;
		font-weight: 600;
		letter-spacing: 0.02em;
		line-height: 1.35;
		color: var(--color-success, #22c55e);
		background: color-mix(in srgb, var(--color-success, #22c55e) 12%, transparent);
		border: 1px solid color-mix(in srgb, var(--color-success, #22c55e) 22%, transparent);
	}

	.privacy-tag-muted {
		color: var(--color-text-muted, var(--text-muted));
		background: color-mix(in srgb, var(--color-text-muted, var(--text-muted)) 10%, transparent);
		border-color: color-mix(in srgb, var(--color-border) 80%, transparent);
	}

	.privacy-tag-warning {
		color: var(--color-warning, var(--warning));
		background: color-mix(in srgb, var(--color-warning, var(--warning)) 12%, transparent);
		border-color: color-mix(in srgb, var(--color-warning, var(--warning)) 22%, transparent);
	}

	.layout {
		display: grid;
		gap: 20px;
		grid-template-columns: 1fr;
		align-items: start;
	}

	@media (min-width: 1024px) {
		.layout {
			grid-template-columns: minmax(0, 1fr) minmax(0, 1fr);
			gap: 24px 28px;
		}

		.col-input {
			position: sticky;
			top: 1rem;
			align-self: start;
			max-height: calc(100vh - 5rem);
			display: flex;
			flex-direction: column;
			min-height: 0;
		}

		.col-input > :not(.drop) {
			flex-shrink: 0;
		}

		.drop {
			flex: 1 1 auto;
			min-height: 140px;
			display: flex;
			flex-direction: column;
			overflow: hidden;
		}

		.ta {
			flex: 1 1 auto;
			min-height: 0;
			overflow-y: auto;
			resize: none;
		}
	}

	.col-input,
	.col-results {
		min-width: 0;
	}

	.col-input {
		position: relative;
	}

	.results-empty {
		padding: 20px 18px;
		border-radius: 10px;
		border: 1px dashed var(--color-border);
		background: var(--color-bg-secondary, var(--bg-secondary));
		text-align: center;
	}

	.results-empty-title {
		margin: 0 0 8px;
		font-size: 13px;
		font-weight: 600;
		color: var(--color-text-primary, var(--text-primary));
	}

	.results-empty-text {
		margin: 0;
		font-size: 12px;
		line-height: 1.5;
		color: var(--color-text-secondary, var(--text-secondary));
	}

	.drop {
		display: block;
		padding: 12px 14px;
		background: var(--color-bg-secondary, var(--bg-secondary));
		border: 1px dashed var(--color-border);
		border-radius: 10px;
		cursor: text;
		transition: border-color 0.15s, background 0.15s;
	}

	.drop:focus-within {
		border-color: var(--color-accent, var(--accent));
		background: var(--color-bg-tertiary, var(--bg-tertiary));
	}

	.drop-label {
		display: block;
		font-size: 11px;
		font-weight: 600;
		text-transform: uppercase;
		letter-spacing: 0.06em;
		color: var(--color-text-muted, var(--text-muted));
		margin-bottom: 8px;
	}

	.ta {
		width: 100%;
		min-height: 260px;
		resize: vertical;
		border: none;
		background: transparent;
		color: var(--color-text-primary, var(--text-primary));
		font-family: var(--font-mono, ui-monospace, monospace);
		font-size: 12px;
		line-height: 1.45;
		outline: none;
	}

	@media (max-width: 640px) {
		.awg-analyzer {
			padding: 0 0 0.75rem;
			max-width: none;
		}

		.col-input,
		.col-results {
			overflow-x: clip;
		}

		.privacy-banner {
			margin-bottom: 0.75rem;
			padding: 10px 12px;
			gap: 10px;
		}

		.privacy-banner-icon {
			width: 32px;
			height: 32px;
			border-radius: 9px;
		}

		.privacy-banner-title {
			font-size: 12px;
		}

		.privacy-banner-text {
			font-size: 11px;
		}

		.privacy-banner-tags {
			margin-top: 8px;
			gap: 5px;
		}

		.layout {
			gap: 0.765rem;
		}

		.drop {
			padding: 10px 12px;
		}

		.ta {
			min-height: 130px;
			max-height: none;
			resize: none;
			overflow-y: visible;
			overscroll-behavior: contain;
		}

		.bar {
			display: grid;
			grid-template-columns: repeat(2, minmax(0, 1fr));
			width: 100%;
			gap: 0.5rem;
			margin-top: 0.75rem;
			margin-bottom: 0.75rem;
		}

		.bar :global(.btn) {
			width: 100%;
			min-width: 0;
		}

		.kbd {
			display: none;
		}

		.results-empty {
			padding: 14px 12px;
		}

		.existing-tunnel-box {
			margin-bottom: 0.75rem;
			padding: 10px 12px;
		}

		.existing-tunnel-head {
			flex-direction: column;
			align-items: flex-start;
			gap: 0.25rem;
			margin-bottom: 8px;
		}

		.existing-tunnel-row {
			flex-direction: column;
			align-items: stretch;
		}

		.existing-tunnel-select {
			width: 100%;
			min-width: 0;
		}
	}

	.ta::placeholder {
		color: var(--color-text-muted, var(--text-muted));
	}

	.bar {
		display: flex;
		flex-wrap: wrap;
		align-items: center;
		gap: 8px;
		margin-top: 12px;
		margin-bottom: 14px;
	}

	.kbd {
		margin-left: auto;
		font-size: 11px;
		padding: 4px 8px;
		color: var(--color-text-muted, var(--text-muted));
		font-family: var(--font-mono);
	}

	.bar-hint {
		margin: -2px 0 10px;
		padding: 6px 10px;
		border-radius: 6px;
		border-left: 3px solid var(--color-warning, var(--warning));
		background: color-mix(in srgb, var(--color-warning, var(--warning)) 9%, transparent);
		font-size: 12px;
		line-height: 1.45;
		color: var(--color-text-secondary, var(--text-secondary));
	}

	.warn {
		padding: 10px 12px;
		border-radius: 8px;
		background: var(--color-warning-tint);
		border: 1px solid color-mix(in srgb, var(--color-warning, var(--warning)) 35%, var(--color-border));
		color: var(--color-warning, var(--warning));
		font-size: 13px;
		margin-bottom: 12px;
	}

	.sr {
		position: absolute;
		width: 1px;
		height: 1px;
		padding: 0;
		margin: -1px;
		overflow: hidden;
		clip: rect(0, 0, 0, 0);
		clip-path: inset(50%);
		white-space: nowrap;
		border: 0;
		opacity: 0;
		pointer-events: none;
	}

	.err {
		padding: 10px 12px;
		border-radius: 8px;
		background: var(--color-error-tint);
		border: 1px solid var(--color-error-border);
		color: var(--color-error);
		font-size: 13px;
		margin-bottom: 12px;
	}

	.source-toggle {
		display: inline-flex;
		gap: 4px;
		padding: 3px;
		margin-bottom: 10px;
		background: var(--color-bg-tertiary);
		border: 1px solid var(--color-border);
		border-radius: var(--radius-md, 8px);
	}
	.source-tab {
		padding: 6px 12px;
		font-size: 13px;
		font-weight: 600;
		color: var(--color-text-muted);
		background: none;
		border: none;
		border-radius: var(--radius-sm, 6px);
		cursor: pointer;
	}
	.source-tab.active {
		color: var(--color-text-primary);
		background: var(--color-bg-secondary);
	}

	/* Existing AWG tunnel selector */
	.existing-tunnel-box {
		margin-bottom: 12px;
		padding: 12px 14px;
		border-radius: 10px;
		background: var(--color-bg-secondary, var(--bg-secondary));
		border: 1px dashed var(--color-border);
	}

	.existing-tunnel-head {
		display: flex;
		align-items: baseline;
		gap: 8px;
		margin-bottom: 10px;
	}

	.existing-tunnel-title {
		font-size: 12px;
		font-weight: 600;
		text-transform: uppercase;
		letter-spacing: 0.06em;
		color: var(--color-text-primary, var(--text-primary));
	}

	.existing-tunnel-note {
		font-size: 11px;
		color: var(--color-text-muted, var(--text-muted));
	}

	.existing-tunnel-row {
		display: flex;
		align-items: center;
		gap: 8px;
	}

	.existing-tunnel-select {
		flex: 1;
		min-width: 0;
		width: 100%;
	}

	.existing-tunnel-loading {
		flex-shrink: 0;
		font-size: 12px;
		color: var(--color-text-muted, var(--text-muted));
	}
</style>
