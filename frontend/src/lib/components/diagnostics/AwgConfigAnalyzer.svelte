<script lang="ts">
	import { Button, ConfirmModal, Dropdown } from '$lib/components/ui';
	import { api } from '$lib/api/client';
	import { notifications } from '$lib/stores/notifications';
	import { tunnels as tunnelsStore } from '$lib/stores/tunnels';
	import type { AWGTunnel, TunnelListItem } from '$lib/types';
	import { buildManagedTunnelListDropdownOptions } from '$lib/utils/routingTunnelOptions';
	import {
		parseAWG,
		detectVersion,
		runChecks,
		calcScores,
		buildFixes,
		buildConfigSummary,
		buildUpgradeHints,
		getVerdict,
		dpiLabel,
		camouflageFromI1,
		scoreRingDashArray,
		type AwgParsed,
		type AwgVersionInfo,
		type AwgCheck,
		type AwgScores,
		type AwgVerdict,
		type AwgSummaryRow,
	} from '$lib/utils/awgConfAnalyzer';
	import { ShieldCheck, TrendingUp, CircleAlert } from 'lucide-svelte';
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
	let version: AwgVersionInfo | null = $state(null);
	let checks: AwgCheck[] = $state([]);
	let awgScores = $state<AwgScores | null>(null);
	let verdict: AwgVerdict | null = $state(null);
	let fixes: string[] = $state([]);
	let camouflage = $state<'LOW' | 'MEDIUM' | 'HIGH'>('LOW');
	let fileInput: HTMLInputElement | undefined = $state();

	let tunnels = $state<TunnelListItem[]>([]);
	let selectedTunnelId = $state('');
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

	function analyze() {
		error = '';
		parsed = null;
		version = null;
		checks = [];
		awgScores = null;
		verdict = null;
		fixes = [];
		tunnelLoadError = '';

		const t = raw.trim();
		if (!t) {
			error = 'Вставьте содержимое .conf файла AmneziaWG / WireGuard';
			return;
		}

		try {
			const p = parseAWG(t);
			const v = detectVersion(p.iface);
			const c = runChecks(p.iface, p.peer, v);
			const s = calcScores(c, p.iface, v);
			const f = buildFixes(c, p.iface, p.peer, v);
			const ver = getVerdict(s.total);
			const cam = camouflageFromI1(p.iface);

			parsed = p;
			version = v;
			checks = c;
			awgScores = s;
			verdict = ver;
			fixes = f;
			camouflage = cam;
			lastAnalyzedRaw = t;
		} catch (e) {
			error = e instanceof Error ? e.message : String(e);
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
		version = null;
		checks = [];
		awgScores = null;
		verdict = null;
		fixes = [];
		camouflage = 'LOW';
		selectedTunnelId = isEmbeddedLocked() ? initialTunnelId : '';
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

				persistentKeepalive:
					peer.persistentkeepalive !== undefined && peer.persistentkeepalive !== ''
						? numOrCurrent(peer.persistentkeepalive, current.peer.persistentKeepalive ?? 25)
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

			const v = detectVersion(freshParsed.iface);
			const c = runChecks(freshParsed.iface, freshParsed.peer, v);
			const s = calcScores(c, freshParsed.iface, v);
			const f = buildFixes(c, freshParsed.iface, freshParsed.peer, v);
			version = v;
			checks = c;
			awgScores = s;
			fixes = f;
			verdict = getVerdict(s.total);
			camouflage = camouflageFromI1(freshParsed.iface);
			parsed = freshParsed;
			error = '';
			tunnelLoadError = '';
			lastAnalyzedRaw = currentRaw;
			loadedTunnelRaw = currentRaw;

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
	const tunnelPlaceholder = $derived(
		tunnelsLoading
			? 'Загрузка туннелей…'
			: tunnelOptions.length
				? 'Выберите туннель'
				: 'Нет AWG-туннелей',
	);

	let parsedLines = $derived.by(() => {
		if (!parsed) return [] as { key: string; value: string }[];
		const { iface, peer } = parsed;
		const rows: [string, string][] = [
			['privatekey', iface.privatekey ? `${iface.privatekey.slice(0, 16)}…` : '—'],
			['address', iface.address || '—'],
			['dns', iface.dns || '—'],
			['mtu', iface.mtu || '—'],
			[
				'jc/jmin/jmax',
				[iface.jc, iface.jmin, iface.jmax].map((v) => v ?? '—').join(' / '),
			],
			['s1/s2', [iface.s1, iface.s2].map((v) => v ?? '—').join(' / ')],
			[
				's3/s4',
				iface.s3 || iface.s4 ? [iface.s3, iface.s4].map((v) => v ?? '—').join(' / ') : '—',
			],
			['h1/h2/h3/h4', [iface.h1, iface.h2, iface.h3, iface.h4].map((v) => v ?? '—').join(' / ')],
			[
				'i1',
				iface.i1 ? `${iface.i1.slice(0, 40)}${iface.i1.length > 40 ? '…' : ''}` : '—',
			],
			['endpoint', peer.endpoint || '—'],
			['publickey', peer.publickey ? `${peer.publickey.slice(0, 16)}…` : '—'],
			['allowedips', peer.allowedips || '—'],
			['keepalive', peer.persistentkeepalive || '—'],
		];
		return rows
			.filter(([, v]) => v && v !== '—')
			.map(([key, value]) => ({ key, value }));
	});

	let categories = $derived([...new Set(checks.map((c) => c.cat))]);
	let canAnalyze = $derived(raw.trim().length > 0);

	const icons: Record<string, string> = {
		pass: '✓',
		warn: '!',
		fail: '✗',
		info: 'i',
	};

	let dpiL = $derived.by(() => {
		const s = awgScores;
		if (!s) return { text: '—', color: 'var(--color-text-muted, var(--text-muted))' };
		return dpiLabel(s.dpi);
	});

	let summaryRows = $derived.by((): AwgSummaryRow[] => {
		if (!parsed || !version) return [];
		return buildConfigSummary(parsed.iface, parsed.peer, version);
	});

	let upgradeHints = $derived.by(() => {
		if (!parsed || !version) return [] as string[];
		return buildUpgradeHints(parsed.iface, version);
	});
</script>

<svelte:window onkeydown={onKeydown} />

<div class="awg-analyzer">
	<div class="privacy-banner" role="status">
		<div class="privacy-banner-icon" aria-hidden="true">
			<ShieldCheck size={18} />
		</div>
		<div class="privacy-banner-body">
			<p class="privacy-banner-title">Анализ только в браузере</p>
			<p class="privacy-banner-text">
				Конфиг обрабатывается локально и не отправляется на сервер.
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
				<Button variant="primary" onclick={analyze} disabled={!canAnalyze}>Анализировать</Button>
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
			{#if version && awgScores && verdict && parsed}
		<section class="card ver">
			<span class="ver-badge">{version.ver}</span>
			<p class="ver-desc">{version.desc}</p>
		</section>

		{#if upgradeHints.length > 0}
			<section
				class="card fixes"
				aria-labelledby="awg-upgrade-h"
				style:--awg-fix-accent={verdict.color}
				style:--awg-fix-tint={verdict.tint}
			>
				<div class="fixes-head">
					<span class="fixes-head-icon" aria-hidden="true">
						<TrendingUp size={18} />
					</span>
					<h3 id="awg-upgrade-h" class="fixes-h">Как усилить</h3>
					<span class="fixes-count">{upgradeHints.length}</span>
				</div>
				<ul class="fix-list">
					{#each upgradeHints as hint, idx (idx)}
						<li class="fix-item">
							<span class="fix-bullet" aria-hidden="true">→</span>
							<span class="fix-text">{hint}</span>
						</li>
					{/each}
				</ul>
			</section>
		{/if}

		<section class="card score">
			<div class="score-row">
				<div class="ring-hold">
					<svg
						class="ring"
						viewBox="0 0 120 120"
						aria-hidden="true"
						focusable="false"
					>
						<circle class="ring-track" cx="60" cy="60" r="50" />
						<circle
							class="ring-fill"
							cx="60"
							cy="60"
							r="50"
							stroke-dasharray={scoreRingDashArray(awgScores.total)}
							stroke={verdict.color}
						/>
						<text x="60" y="53" class="ring-num" text-anchor="middle">{awgScores.total}</text>
						<text x="60" y="68" class="ring-sub" text-anchor="middle">{version.ver}</text>
					</svg>
				</div>
				<div class="verdict">
					<span class="verdict-badge" style:background={verdict.tint} style:border-color={verdict.color} style:color={verdict.color}>
						{verdict.label}
					</span>
					<p class="verdict-text">{verdict.text}</p>
				</div>
			</div>

			<div class="minis">
				<div class="mini">
					<div class="mini-l">Версия</div>
					<div class="mini-v accent">{version.ver}</div>
				</div>
				{#if version.obfLevel}
					<div class="mini">
						<div class="mini-l">CPS / обфускация</div>
						<div class="mini-v soft">{version.obfLevel}</div>
					</div>
				{/if}
				{#if version.protocol}
					<div class="mini">
						<div class="mini-l">Протокол I1</div>
						<div class="mini-v soft">{version.protocol}</div>
					</div>
				{/if}
				<div class="mini">
					<div class="mini-l">DPI риск</div>
					<div class="mini-v" style:color={dpiL.color}>{dpiL.text}</div>
				</div>
				<div class="mini">
					<div class="mini-l">Stealth</div>
					<div class="mini-v soft">{awgScores.stealth}%</div>
				</div>
				<div class="mini">
					<div class="mini-l">Балл</div>
					<div class="mini-v">{awgScores.total}%</div>
				</div>
				<div class="mini">
					<div class="mini-l">Камуфляж</div>
					<div class="mini-v soft">{camouflage}</div>
				</div>
			</div>
		</section>

		{#if summaryRows.length > 0}
			<section class="card summary" aria-labelledby="awg-summary-h">
				<h3 id="awg-summary-h" class="block-h">Что это за конфиг</h3>
				<dl class="summary-dl">
					{#each summaryRows as row (`${row.label}-${row.value}`)}
						<div class="summary-row">
							<dt>{row.label}</dt>
							<dd>{row.value}</dd>
						</div>
					{/each}
				</dl>
			</section>
		{/if}

		{#if fixes.length > 0}
			<section
				class="card fixes"
				style:--awg-fix-accent={verdict.color}
				style:--awg-fix-tint={verdict.tint}
			>
				<div class="fixes-head">
					<span class="fixes-head-icon" aria-hidden="true">
						<CircleAlert size={18} />
					</span>
					<h3 class="fixes-h">Рекомендации</h3>
					<span class="fixes-count">{fixes.length}</span>
				</div>
				<ul class="fix-list">
					{#each fixes as line, idx (idx)}
						<li class="fix-item">
							<span class="fix-bullet" aria-hidden="true">→</span>
							<span class="fix-text">{line}</span>
						</li>
					{/each}
				</ul>
			</section>
		{/if}

		{#if parsedLines.length > 0}
			<section class="card prewrap">
				<h3 class="block-h">Параметры (усечено)</h3>
				<pre class="mono">{#each parsedLines as row (`${row.key}-${row.value}`)}
<span class="k">{row.key.padEnd(14)}</span>= {row.value}
{/each}</pre>
			</section>
		{/if}

		{#each categories as cat (cat)}
			<h4 class="cat">{cat}</h4>
			<div class="grid">
				{#each checks.filter((c) => c.cat === cat) as c (c.title + c.value)}
					<div class="check check-{c.status}">
						<div class="check-ic">{icons[c.status] ?? '?'}</div>
						<div class="check-body">
							<div class="check-t">{c.title}</div>
							<code class="check-val">{c.value}</code>
							<div class="check-d">{c.detail}</div>
						</div>
						<div class="check-w">{c.max > 0 ? `${c.pts}/${c.max}` : ''}</div>
					</div>
				{/each}
			</div>
		{/each}
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

		.card {
			padding: 12px;
			margin-bottom: 0.765rem;
		}

		.results-empty {
			padding: 14px 12px;
		}

		.summary-row {
			grid-template-columns: 1fr;
			gap: 0.25rem;
		}

		.minis {
			grid-template-columns: repeat(2, minmax(0, 1fr));
		}

		.score-row {
			flex-direction: column;
			align-items: flex-start;
		}

		.ring-hold {
			width: 100px;
			height: 100px;
		}

		.ring {
			width: 100px;
			height: 100px;
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

	.card {
		background: var(--color-bg-secondary, var(--bg-secondary));
		border: 1px solid var(--color-border);
		border-radius: 10px;
		padding: 14px 16px;
		margin-bottom: 12px;
	}

	.ver {
		display: flex;
		flex-wrap: wrap;
		align-items: flex-start;
		gap: 12px 16px;
	}

	.ver-badge {
		flex-shrink: 0;
		padding: 6px 14px;
		border-radius: 999px;
		font-weight: 700;
		font-size: 13px;
		background: var(--color-bg-tertiary, var(--bg-tertiary));
		border: 1px solid var(--color-border);
		color: var(--color-text-primary, var(--text-primary));
	}

	.ver-desc {
		margin: 0;
		flex: 1;
		min-width: 200px;
		font-size: 13px;
		line-height: 1.5;
		color: var(--color-text-secondary, var(--text-secondary));
	}

	.summary-dl {
		margin: 4px 0 0;
	}

	.summary-row {
		display: grid;
		grid-template-columns: minmax(7.5rem, 36%) 1fr;
		gap: 4px 12px;
		padding: 7px 0;
		border-bottom: 1px solid var(--color-border);
		font-size: 12px;
		line-height: 1.45;
	}

	.summary-row:last-child {
		border-bottom: none;
		padding-bottom: 0;
	}

	.summary-row dt {
		margin: 0;
		font-weight: 600;
		color: var(--color-text-muted, var(--text-muted));
	}

	.summary-row dd {
		margin: 0;
		color: var(--color-text-primary, var(--text-primary));
		word-break: break-word;
	}

	.score-row {
		display: flex;
		flex-wrap: wrap;
		align-items: center;
		gap: 20px 28px;
		margin-bottom: 16px;
	}

	/* Inline SVG in a flex row otherwise leaves a “frame”/gap under the circle. */
	.ring-hold {
		flex-shrink: 0;
		width: 120px;
		height: 120px;
		display: flex;
		align-items: center;
		justify-content: center;
		line-height: 0;
		user-select: none;
		-webkit-user-select: none;
	}

	.ring {
		display: block;
		width: 120px;
		height: 120px;
		overflow: visible;
		outline: none;
		border: none;
		box-shadow: none;
		/* Clip square SVG paint bounds so no faint rectangular halo / selection box. */
		clip-path: circle(50% at 50% 50%);
		-webkit-tap-highlight-color: transparent;
	}

	.ring:focus,
	.ring:focus-visible,
	.ring-hold:focus-visible {
		outline: none;
	}

	.ring-track {
		fill: none;
		stroke: var(--color-border);
		stroke-width: 10;
	}

	.ring-fill {
		fill: none;
		stroke-width: 10;
		stroke-linecap: round;
		transform: rotate(-90deg);
		transform-box: fill-box;
		transform-origin: center;
		transition: stroke-dasharray 0.45s ease, stroke 0.25s ease;
	}

	.ring-num {
		font-size: 22px;
		font-weight: 800;
		fill: var(--color-text-primary, var(--text-primary));
	}

	.ring-sub {
		font-size: 9px;
		fill: var(--color-text-muted, var(--text-muted));
	}

	.verdict {
		flex: 1;
		min-width: 200px;
	}

	.verdict-badge {
		display: inline-block;
		padding: 5px 14px;
		border-radius: 999px;
		font-weight: 700;
		font-size: 14px;
		border: 1px solid transparent;
		margin-bottom: 8px;
	}

	.verdict-text {
		margin: 0;
		font-size: 13px;
		line-height: 1.5;
		color: var(--color-text-secondary, var(--text-secondary));
	}

	.minis {
		display: grid;
		grid-template-columns: repeat(auto-fill, minmax(130px, 1fr));
		gap: 8px;
	}

	.mini {
		background: var(--color-settings-control-bg, var(--color-bg-tertiary, var(--bg-tertiary)));
		border: 1px solid var(--color-border);
		border-radius: 8px;
		padding: 8px 10px;
	}

	.mini-l {
		font-size: 10px;
		text-transform: uppercase;
		letter-spacing: 0.06em;
		color: var(--color-text-muted, var(--text-muted));
		margin-bottom: 4px;
	}

	.mini-v {
		font-size: 15px;
		font-weight: 700;
		color: var(--color-text-primary, var(--text-primary));
	}

	.mini-v.accent {
		color: var(--color-accent, var(--accent));
	}

	.mini-v.soft {
		color: var(--color-text-secondary, var(--text-secondary));
	}

	.block-h {
		margin: 0 0 10px;
		font-size: 11px;
		font-weight: 700;
		text-transform: uppercase;
		letter-spacing: 0.08em;
		color: var(--color-accent, var(--accent));
	}

	.card.fixes {
		--awg-fix-accent: var(--color-success, var(--success));
		--awg-fix-tint: var(--color-success-tint);
		border-color: color-mix(in srgb, var(--awg-fix-accent) 28%, var(--color-border));
		background: linear-gradient(
			165deg,
			color-mix(in srgb, var(--awg-fix-accent) 12%, var(--color-bg-secondary, var(--bg-secondary))) 0%,
			var(--color-bg-secondary, var(--bg-secondary)) 55%
		);
	}

	.fixes-head {
		display: flex;
		align-items: center;
		gap: 10px;
		margin-bottom: 12px;
	}

	.fixes-head-icon {
		display: flex;
		align-items: center;
		justify-content: center;
		width: 34px;
		height: 34px;
		border-radius: 10px;
		flex-shrink: 0;
		color: var(--awg-fix-accent);
		background: var(--awg-fix-tint);
		border: 1px solid color-mix(in srgb, var(--awg-fix-accent) 35%, transparent);
	}

	.fixes-h {
		margin: 0;
		flex: 1;
		min-width: 0;
		font-size: 11px;
		font-weight: 700;
		text-transform: uppercase;
		letter-spacing: 0.08em;
		color: var(--awg-fix-accent);
	}

	.fixes-count {
		flex-shrink: 0;
		font-size: 11px;
		font-weight: 700;
		padding: 4px 10px;
		border-radius: 999px;
		font-variant-numeric: tabular-nums;
		background: var(--awg-fix-tint);
		color: var(--awg-fix-accent);
		border: 1px solid color-mix(in srgb, var(--awg-fix-accent) 30%, transparent);
	}

	.fix-list {
		margin: 0;
		padding: 0;
		list-style: none;
		display: flex;
		flex-direction: column;
		gap: 8px;
	}

	.fix-item {
		display: flex;
		align-items: flex-start;
		gap: 10px;
		padding: 11px 12px;
		border-radius: 9px;
		background: var(--color-bg-tertiary, var(--bg-tertiary));
		border: 1px solid var(--color-border);
		transition: border-color 0.15s ease, background 0.15s ease;
	}

	.fix-item:hover {
		border-color: color-mix(in srgb, var(--awg-fix-accent) 35%, var(--color-border));
		background: color-mix(in srgb, var(--awg-fix-tint) 40%, var(--color-bg-tertiary, var(--bg-tertiary)));
	}

	.fix-bullet {
		flex-shrink: 0;
		width: 24px;
		height: 24px;
		display: flex;
		align-items: center;
		justify-content: center;
		margin-top: 1px;
		border-radius: 7px;
		font-size: 13px;
		font-weight: 700;
		line-height: 1;
		color: var(--awg-fix-accent);
		background: var(--awg-fix-tint);
		border: 1px solid color-mix(in srgb, var(--awg-fix-accent) 25%, transparent);
	}

	.fix-text {
		flex: 1;
		min-width: 0;
		font-size: 13px;
		line-height: 1.5;
		color: var(--color-text-primary, var(--text-primary));
		white-space: pre-line;
	}

	.prewrap .mono {
		margin: 0;
		font-family: var(--font-mono);
		font-size: 12px;
		line-height: 1.5;
		color: var(--color-text-secondary, var(--text-secondary));
		white-space: pre-wrap;
		word-break: break-word;
	}

	.prewrap .k {
		color: var(--color-accent, var(--accent));
	}

	.cat {
		margin: 18px 0 8px;
		font-size: 11px;
		font-weight: 700;
		text-transform: uppercase;
		letter-spacing: 0.1em;
		color: var(--color-text-muted, var(--text-muted));
		display: flex;
		align-items: center;
		gap: 8px;
	}

	.cat::after {
		content: '';
		flex: 1;
		height: 1px;
		background: var(--color-border);
	}

	.grid {
		display: grid;
		gap: 8px;
	}

	.check {
		display: flex;
		align-items: flex-start;
		gap: 10px;
		padding: 10px 12px;
		background: var(--color-bg-secondary, var(--bg-secondary));
		border: 1px solid var(--color-border);
		border-radius: 8px;
		border-left-width: 3px;
	}

	.check-pass {
		border-left-color: var(--color-success, var(--success));
	}
	.check-warn {
		border-left-color: var(--color-warning, var(--warning));
	}
	.check-fail {
		border-left-color: var(--color-error, var(--error));
	}
	.check-info {
		border-left-color: var(--color-accent, var(--accent));
	}

	.check-ic {
		width: 22px;
		height: 22px;
		border-radius: 50%;
		display: flex;
		align-items: center;
		justify-content: center;
		font-size: 11px;
		font-weight: 800;
		flex-shrink: 0;
		margin-top: 2px;
	}

	.check-pass .check-ic {
		background: var(--color-success-tint);
		color: var(--color-success);
	}
	.check-warn .check-ic {
		background: var(--color-warning-tint);
		color: var(--color-warning);
	}
	.check-fail .check-ic {
		background: var(--color-error-tint);
		color: var(--color-error);
	}
	.check-info .check-ic {
		background: var(--color-accent-tint);
		color: var(--color-accent, var(--accent));
	}

	.check-body {
		flex: 1;
		min-width: 0;
	}

	.check-t {
		font-weight: 600;
		font-size: 13px;
		color: var(--color-text-primary, var(--text-primary));
		margin-bottom: 2px;
	}

	.check-val {
		display: inline-block;
		margin-top: 2px;
		padding: 1px 6px;
		border-radius: 4px;
		background: var(--color-bg-tertiary, var(--bg-tertiary));
		font-family: var(--font-mono);
		font-size: 11px;
		color: var(--color-text-secondary, var(--text-secondary));
		word-break: break-all;
		max-width: 100%;
	}

	.check-d {
		margin-top: 4px;
		font-size: 12px;
		line-height: 1.4;
		color: var(--color-text-secondary, var(--text-secondary));
	}

	.check-w {
		font-size: 11px;
		color: var(--color-text-muted, var(--text-muted));
		font-family: var(--font-mono);
		flex-shrink: 0;
		align-self: center;
		min-width: 36px;
		text-align: right;
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
