<script lang="ts">
	import { untrack } from 'svelte';
	import { m } from '$lib/i18n';
	import { api } from '$lib/api/client';
	import { softCompileGoRegex } from '$lib/utils/subscriptionGroupPreview';
	import {
		stripFircEmojis,
		FIRC_TUNNEL_NODE_POOL,
		type FircTunnel,
		type FircTunnelFilteredNode,
		type FircTunnelSource,
		type FircTunnelInbound,
		type FircTunnelPreview,
		type FircTunnelProbeRow,
		type FircTunnelSubUsage,
	} from '$lib/types/firc';
	import {
		fircStatus,
		fircTunnels,
		fircTunnelsState,
		fircEditTunnelId,
		fircEditTunnelSubTab,
		fircProbeResults,
		reloadFircAll,
	} from '$lib/stores/firc';
	import {
		formatFircSkipReason,
		formatFircSubError,
		getFircTunnelDiagnostic,
	} from '$lib/utils/fircDiagnostics';
	import { Button, Badge, Toggle, StatusDot, Modal } from '$lib/components/ui';
	import { notifications } from '$lib/stores/notifications';
	import {
		Activity,
		Plus,
		Trash2,
		RefreshCw,
		RotateCw,
		ArrowUp,
		ArrowDown,
		Search,
		Link2,
		Server,
		Shield,
		Network,
	} from 'lucide-svelte';

	interface DisplayNode {
		key: string;
		name: string;
		source: string;
		state: 'active' | 'down' | 'reserve' | 'excluded' | 'missing' | 'skipped';
		skipReason?: string;
		v6?: boolean;
	}

	function formatSubBytes(bytes?: number): string {
		if (bytes === undefined || bytes < 0) return '—';
		if (bytes === 0) return '0 Б';
		const units = ['Б', 'КБ', 'МБ', 'ГБ', 'ТБ'];
		let val = bytes;
		let idx = 0;
		while (val >= 1024 && idx < units.length - 1) {
			val /= 1024;
			idx++;
		}
		return `${val >= 10 || idx === 0 ? val.toFixed(0) : val.toFixed(1)} ${units[idx]}`;
	}

	function formatSubUsage(usage?: FircTunnelSubUsage): string {
		if (!usage) return '';
		const parts: string[] = [];
		if (usage.used !== undefined || usage.total !== undefined) {
			const usedStr = formatSubBytes(usage.used ?? 0);
			const totalStr = usage.total ? formatSubBytes(usage.total) : '∞';
			parts.push(`${usedStr} / ${totalStr}`);
		}
		if (usage.expire && usage.expire > 0) {
			const d = new Date(usage.expire * 1000);
			parts.push(`до ${d.toLocaleDateString('ru-RU')}`);
		}
		return parts.join(' · ');
	}

	const SUB_INTERVALS = [
		{ value: 0, label: 'Вручную' },
		{ value: 3600, label: 'Каждый час' },
		{ value: 21600, label: 'Каждые 6 часов' },
		{ value: 43200, label: 'Каждые 12 часов' },
		{ value: 86400, label: 'Раз в сутки' },
	];

	const FINGERPRINTS: { value: '' | 'chrome' | 'firefox'; label: string }[] = [
		{ value: '', label: 'По умолчанию (из ссылки)' },
		{ value: 'chrome', label: 'Chrome' },
		{ value: 'firefox', label: 'Firefox' },
	];

	function totalProbeLatencyMs(p?: FircTunnelProbeRow | null): number | null {
		if (!p || !p.ok) return null;
		if (p.firstByteMs != null) {
			return (p.handshakeMs ?? 0) + p.firstByteMs;
		}
		if (p.handshakeMs != null) {
			return p.handshakeMs;
		}
		return null;
	}

	let draft = $state<FircTunnel | null>(null);
	let baselineJson = $state('');
	let saving = $state(false);
	let busyAction = $state<'' | 'probe' | 'refresh' | 'restart'>('');
	let probeProgress = $state<{ done: number; total: number } | null>(null);
	let probingNodeKey = $state<string | null>(null);
	let nodeQuery = $state('');
	let preview = $state<FircTunnelPreview | null>(null);
	let previewError = $state('');
	let filteredOpen = $state(false);

	// Source modal state
	let sourceModalOpen = $state(false);
	let sourceModalKind = $state<'subscription' | 'link'>('subscription');
	let sourceModalName = $state('');
	let sourceModalUrl = $state('');
	let sourceModalInterval = $state(21600);
	let sourceModalHeadersText = $state('');
	let sourceModalProfileKind = $state('');
	let sourceModalDetecting = $state(false);
	let sourceModalBusy = $state(false);
	let headerProfiles = $state<Array<{ kind: string; label: string; headersText: string }>>([]);

	// Happ RSA keys modal state
	let happKeysModalOpen = $state(false);
	let happKeysText = $state('');
	let happKeysCount = $state(0);
	let happKeysBusy = $state(false);

	function parseHeadersTextToMap(text: string): Record<string, string> | undefined {
		const map: Record<string, string> = {};
		for (const rawLine of text.split(/\r?\n/)) {
			const line = rawLine.trim();
			if (!line || line.startsWith('#')) continue;
			const idx = line.indexOf(':');
			if (idx <= 0) continue;
			const k = line.slice(0, idx).trim();
			const v = line.slice(idx + 1).trim();
			if (k && v) map[k] = v;
		}
		return Object.keys(map).length > 0 ? map : undefined;
	}

	async function ensureSubscriptionAuxLoaded() {
		try {
			const [profiles, keysInfo] = await Promise.all([
				headerProfiles.length === 0 ? api.listSubscriptionHeaderProfiles().catch(() => []) : Promise.resolve(headerProfiles),
				api.getHappKeysInfo().catch(() => ({ configured: false, count: 0 })),
			]);
			if (profiles.length > 0) headerProfiles = profiles;
			happKeysCount = keysInfo?.count ?? 0;
		} catch {
			// ignore
		}
	}

	async function handleDetectSubscriptionHeaders() {
		if (!sourceModalUrl.trim() || sourceModalDetecting) return;
		sourceModalDetecting = true;
		try {
			const customHeaders = Object.entries(parseHeadersTextToMap(sourceModalHeadersText) ?? {}).map(
				([name, value]) => ({ name, value }),
			);
			const res = await api.detectSubscriptionHeaders(sourceModalUrl.trim(), customHeaders);
			if (res.normalizedUrl || res.decryptedUrl) {
				sourceModalUrl = res.normalizedUrl || res.decryptedUrl || sourceModalUrl;
			}
			if (res.headersText) {
				sourceModalHeadersText = res.headersText;
			}
			if (res.kind) {
				sourceModalProfileKind = res.kind;
			}
			notifications.success(
				`Профиль: ${res.label || res.kind || 'Авто'} · найдено узлов: ${res.serverCount ?? 0}`,
			);
		} catch (e: unknown) {
			notifications.error((e instanceof Error ? e.message : undefined) ?? 'Не удалось определить профиль подписки');
		} finally {
			sourceModalDetecting = false;
		}
	}

	function handleSelectHeaderProfile(kind: string) {
		sourceModalProfileKind = kind;
		const found = headerProfiles.find((p) => p.kind === kind);
		if (found) {
			sourceModalHeadersText = found.headersText || '';
		}
	}

	async function openHappKeysModal() {
		await ensureSubscriptionAuxLoaded();
		happKeysText = '';
		happKeysModalOpen = true;
	}

	async function saveHappKeysAction() {
		if (!happKeysText.trim() || happKeysBusy) return;
		happKeysBusy = true;
		try {
			const res = await api.saveHappKeys(happKeysText.trim());
			happKeysCount = res.count ?? 0;
			happKeysText = '';
			happKeysModalOpen = false;
			notifications.success(`Сохранено RSA-ключей Happ: ${happKeysCount}`);
		} catch (e: unknown) {
			notifications.error((e instanceof Error ? e.message : undefined) ?? 'Ошибка сохранения RSA-ключей Happ');
		} finally {
			happKeysBusy = false;
		}
	}

	async function clearHappKeysAction() {
		if (happKeysBusy) return;
		happKeysBusy = true;
		try {
			await api.clearHappKeys();
			happKeysCount = 0;
			notifications.info('Ключи Happ RSA очищены');
		} catch (e: unknown) {
			notifications.error((e instanceof Error ? e.message : undefined) ?? 'Ошибка очистки ключей Happ');
		} finally {
			happKeysBusy = false;
		}
	}

	function randomId(): string {
		const bytes = new Uint8Array(4);
		if (typeof crypto !== 'undefined' && crypto.getRandomValues) {
			crypto.getRandomValues(bytes);
		} else {
			for (let i = 0; i < 4; i++) bytes[i] = Math.floor(Math.random() * 256);
		}
		return Array.from(bytes)
			.map((b) => b.toString(16).padStart(2, '0'))
			.join('');
	}

	function cloneTunnel(t: FircTunnel): FircTunnel {
		const copy = JSON.parse(JSON.stringify(t)) as FircTunnel;
		copy.filter = copy.filter ?? '';
		copy.filterExclude = copy.filterExclude ?? '';
		return copy;
	}

	function softRegexWarning(pattern: string): string {
		const res = softCompileGoRegex(pattern.trim());
		if (res.lookaround) {
			return m.firc_filter_warn_lookaround();
		}
		if (res.invalid) {
			return m.firc_filter_warn_invalid();
		}
		return '';
	}

	$effect(() => {
		const id = $fircEditTunnelId;
		if (!id) {
			draft = null;
			baselineJson = '';
			preview = null;
			previewError = '';
			filteredOpen = false;
			nodeQuery = '';
			return;
		}
		const found = $fircTunnels.find((t) => t.id === id);
		if (found && (!draft || draft.id !== id)) {
			const c = cloneTunnel(found);
			draft = c;
			baselineJson = JSON.stringify(c);
			preview = null;
			previewError = '';
			filteredOpen = false;
		}
	});

	const isDirty = $derived(draft !== null && JSON.stringify(draft) !== baselineJson);
	const liveState = $derived(
		draft ? $fircTunnelsState.find((s) => s.id === draft!.id) : undefined,
	);
	const modalDiagnostic = $derived(
		draft
			? getFircTunnelDiagnostic({ tunnel: draft, live: liveState, status: $fircStatus })
			: null,
	);
	const tunProbes = $derived(draft ? ($fircProbeResults[draft.id] ?? {}) : {});
	const includeWarning = $derived(softRegexWarning(draft?.filter ?? ''));
	const excludeWarning = $derived(softRegexWarning(draft?.filterExclude ?? ''));
	const hasSubscriptionSource = $derived(
		(draft?.sources ?? []).some((s) => s.kind === 'subscription'),
	);

	$effect(() => {
		const tun = draft;
		if (!tun || !hasSubscriptionSource) return;
		if (includeWarning || excludeWarning) {
			previewError = '';
			return;
		}
		const saved = $fircTunnels.find((t) => t.id === tun.id);
		const curInc = tun.filter ?? '';
		const curExc = tun.filterExclude ?? '';
		const savedInc = saved?.filter ?? '';
		const savedExc = saved?.filterExclude ?? '';
		const hasPreview = untrack(() => preview !== null);
		if (!hasPreview && curInc === savedInc && curExc === savedExc) {
			return;
		}
		const payload = cloneTunnel(tun);
		const ctrl = new AbortController();
		const timer = setTimeout(async () => {
			try {
				const res = await api.fircPreviewTunnel(payload, ctrl.signal);
				if (ctrl.signal.aborted) return;
				preview = res;
				previewError = '';
			} catch (e: unknown) {
				if (e instanceof DOMException && e.name === 'AbortError') return;
				previewError =
					e instanceof Error ? e.message : m.firc_filter_preview_error();
			}
		}, 300);
		return () => {
			clearTimeout(timer);
			ctrl.abort();
		};
	});

	const filteredNodes = $derived.by((): FircTunnelFilteredNode[] => {
		const raw =
			preview !== null ? (preview.filteredNodes ?? []) : (liveState?.filteredNodes ?? []);
		return raw.map((n) => ({
			...n,
			name: stripFircEmojis(n.name || n.key),
			source: stripFircEmojis(n.source || ''),
		}));
	});

	const nodes = $derived.by((): DisplayNode[] => {
		if (!draft) return [];
		const tun = draft;
		const live = liveState?.nodes ?? [];
		const prev = preview?.nodes ?? [];
		const hiddenKeys = new Set(filteredNodes.map((n) => n.key));
		const byKey = new Map<string, DisplayNode>();

		if (preview !== null) {
			const liveByKey = new Map(live.map((ln) => [ln.key, ln]));
			for (const pn of prev) {
				const ln = liveByKey.get(pn.key);
				byKey.set(pn.key, {
					key: pn.key,
					name: stripFircEmojis(pn.name || pn.key),
					source: stripFircEmojis(pn.source || ''),
					state: ln
						? ln.state
						: pn.excluded
							? 'excluded'
							: pn.skipReason
								? 'skipped'
								: pn.missing
									? 'missing'
									: 'reserve',
					skipReason: pn.skipReason ?? ln?.skipReason,
					v6: ln?.v6,
				});
			}
		} else {
			for (const ln of live) {
				byKey.set(ln.key, {
					key: ln.key,
					name: stripFircEmojis(ln.name || ln.key),
					source: stripFircEmojis(ln.source || ''),
					state: ln.state,
					skipReason: ln.skipReason,
					v6: ln.v6,
				});
			}

			for (const pn of prev) {
				if (!byKey.has(pn.key)) {
					byKey.set(pn.key, {
						key: pn.key,
						name: stripFircEmojis(pn.name || pn.key),
						source: stripFircEmojis(pn.source || ''),
						state: pn.excluded
							? 'excluded'
							: pn.skipReason
								? 'skipped'
								: pn.missing
									? 'missing'
									: 'reserve',
						skipReason: pn.skipReason,
					});
				}
			}
		}

		for (const key of tun.order ?? []) {
			if (hiddenKeys.has(key)) continue;
			if (!byKey.has(key)) {
				const rawName = key.includes(':') ? key.slice(key.indexOf(':') + 1) : key;
				let decoded = rawName;
				try {
					decoded = decodeURIComponent(rawName);
				} catch {
					// keep raw
				}
				byKey.set(key, {
					key,
					name: stripFircEmojis(decoded),
					source: key.includes(':') ? key.slice(0, key.indexOf(':')) : '',
					state: (tun.exclude ?? []).includes(key) ? 'excluded' : 'reserve',
				});
			}
		}

		const orderIdx = new Map((tun.order ?? []).map((k, i) => [k, i]));
		return Array.from(byKey.values()).sort((a, b) => {
			const ia = orderIdx.get(a.key);
			const ib = orderIdx.get(b.key);
			if (ia !== undefined && ib !== undefined) return ia - ib;
			if (ia !== undefined) return -1;
			if (ib !== undefined) return 1;
			return a.name.localeCompare(b.name);
		});
	});

	function closeModal() {
		if (saving) return;
		fircEditTunnelId.set(null);
	}

	async function saveDraft(closeAfter = false) {
		if (!draft || saving) return;
		saving = true;
		try {
			const updated = cloneTunnel(draft);
			updated.description = stripFircEmojis(updated.description ?? '');
			updated.filter = (updated.filter ?? '').trim();
			updated.filterExclude = (updated.filterExclude ?? '').trim();
			const nextList = $fircTunnels.map((t) => (t.id === updated.id ? updated : t));
			if (!nextList.some((t) => t.id === updated.id)) {
				nextList.push(updated);
			}
			const res = await api.fircSaveTunnels(nextList);
			const finalTunnels = res?.tunnels ?? nextList;
			fircTunnels.set(finalTunnels);
			const fresh = finalTunnels.find((t) => t.id === updated.id) ?? updated;
			draft = cloneTunnel(fresh);
			baselineJson = JSON.stringify(draft);
			await reloadFircAll();
			notifications.success(`Настройки туннеля ${updated.device} сохранены`);
			if (closeAfter) {
				fircEditTunnelId.set(null);
			}
		} catch (e: unknown) {
			notifications.error((e instanceof Error ? e.message : undefined) ?? 'Ошибка сохранения туннеля FIRC');
		} finally {
			saving = false;
		}
	}

	async function handleProbe() {
		if (!draft || busyAction || probingNodeKey) return;
		const targetId = draft.id;
		const targetDev = draft.device;
		busyAction = 'probe';
		probeProgress = null;
		try {
			const payload = isDirty ? { tunnel: draft } : { id: targetId };
			const rows = await api.fircProbeTunnel(payload, {
				onProgress: (partialRows, total) => {
					if (!draft || draft.id !== targetId) return;
					probeProgress = { done: partialRows.length, total };
					if (partialRows.length > 0) {
						fircProbeResults.update((all) => {
							const nextMap: Record<string, FircTunnelProbeRow> = { ...(all[targetId] ?? {}) };
							for (const r of partialRows) {
								nextMap[r.key] = r;
							}
							return { ...all, [targetId]: nextMap };
						});
					}
				},
			});
			fircProbeResults.update((all) => {
				const nextMap: Record<string, FircTunnelProbeRow> = { ...(all[targetId] ?? {}) };
				for (const row of rows ?? []) {
					nextMap[row.key] = row;
				}
				return { ...all, [targetId]: nextMap };
			});
			notifications.success(`Проверка задержки узлов ${targetDev} завершена`);
		} catch (e: unknown) {
			notifications.error((e instanceof Error ? e.message : undefined) ?? 'Ошибка проверки узлов');
		} finally {
			busyAction = '';
			probeProgress = null;
		}
	}

	async function handleProbeNode(nodeKey: string) {
		if (!draft || busyAction || probingNodeKey) return;
		const targetId = draft.id;
		const targetDev = draft.device;
		probingNodeKey = nodeKey;
		try {
			const payload = isDirty
				? { tunnel: draft, keys: [nodeKey] }
				: { id: targetId, keys: [nodeKey] };
			const rows = await api.fircProbeTunnel(payload);
			fircProbeResults.update((all) => {
				const nextMap: Record<string, FircTunnelProbeRow> = { ...(all[targetId] ?? {}) };
				for (const row of rows ?? []) {
					nextMap[row.key] = row;
				}
				return { ...all, [targetId]: nextMap };
			});
		} catch (e: unknown) {
			notifications.error(
				(e instanceof Error ? e.message : undefined) ??
					m.firc_probe_node_error({ device: targetDev }),
			);
		} finally {
			probingNodeKey = null;
		}
	}

	async function handleRefresh() {
		if (!draft || busyAction) return;
		busyAction = 'refresh';
		try {
			if (isDirty) {
				await saveDraft(false);
			}
			await api.fircRefreshTunnel(draft.id);
			const prev = await api.fircPreviewTunnel(draft).catch(() => null);
			if (prev) preview = prev;
			await reloadFircAll();
			notifications.success(`Подписки туннеля ${draft.device} обновлены`);
		} catch (e: unknown) {
			notifications.error((e instanceof Error ? e.message : undefined) ?? 'Ошибка обновления подписки');
		} finally {
			busyAction = '';
		}
	}

	async function handleRestart() {
		if (!draft || busyAction) return;
		busyAction = 'restart';
		try {
			await api.fircRestartTunnel(draft.id);
			await reloadFircAll();
			notifications.success(`Туннель ${draft.device} перезапущен`);
		} catch (e: unknown) {
			notifications.error((e instanceof Error ? e.message : undefined) ?? 'Ошибка перезапуска туннеля');
		} finally {
			busyAction = '';
		}
	}

	function sortNodesByLatency() {
		if (!draft) return;
		const score = (n: DisplayNode): number => {
			const total = totalProbeLatencyMs(tunProbes[n.key]);
			if (total !== null) {
				return total;
			}
			if (n.state === 'active') return 1;
			if (n.state === 'reserve') return 50000;
			return 999999;
		};
		draft.order = [...nodes].sort((a, b) => score(a) - score(b)).map((n) => n.key);
	}

	function moveNode(index: number, delta: -1 | 1) {
		if (!draft) return;
		const target = index + delta;
		if (target < 0 || target >= nodes.length) return;
		const keys = nodes.map((n) => n.key);
		const [item] = keys.splice(index, 1);
		keys.splice(target, 0, item);
		draft.order = keys;
	}

	function toggleNodeIncluded(nodeKey: string, included: boolean) {
		if (!draft) return;
		const excl = new Set(draft.exclude ?? []);
		if (included) excl.delete(nodeKey);
		else excl.add(nodeKey);
		draft.exclude = Array.from(excl);
	}

	function openAddSource(kind: 'subscription' | 'link') {
		sourceModalKind = kind;
		sourceModalName = '';
		sourceModalUrl = '';
		sourceModalInterval = 21600;
		sourceModalHeadersText = '';
		sourceModalProfileKind = '';
		sourceModalOpen = true;
		void ensureSubscriptionAuxLoaded();
	}

	async function submitAddSource() {
		if (!draft || !sourceModalUrl.trim()) return;
		sourceModalBusy = true;
		try {
			const headers =
				sourceModalKind === 'subscription'
					? parseHeadersTextToMap(sourceModalHeadersText)
					: undefined;
			const defaultName = stripFircEmojis(
				sourceModalName.trim() || (sourceModalKind === 'subscription' ? 'Sub' : 'Node'),
			);
			let addedSources: FircTunnelSource[] = [];
			try {
				const normRes = await api.fircNormalizeSource({
					input: sourceModalUrl.trim(),
					name: defaultName,
					interval: sourceModalInterval,
					headers,
				});
				if (normRes?.sources?.length) {
					addedSources = normRes.sources.map((s) => ({
						...s,
						id: s.id || randomId(),
						name: stripFircEmojis(s.name || defaultName),
						interval: s.kind === 'subscription' ? (s.interval || sourceModalInterval) : undefined,
						headers: s.kind === 'subscription' ? (s.headers ?? headers) : undefined,
					}));
				}
			} catch (normErr: unknown) {
				// If explicit happ://crypt or FiniteProto v2 parsing failed, surface the error directly
				const trimmedLower = sourceModalUrl.trim().toLowerCase();
				if (
					trimmedLower.startsWith('happ://crypt') ||
					trimmedLower.startsWith('fp://') ||
					trimmedLower.startsWith('finiteproto://') ||
					trimmedLower.includes('finiteproto')
				) {
					throw normErr;
				}
			}

			if (addedSources.length === 0) {
				const fallbackSource: FircTunnelSource =
					sourceModalKind === 'subscription'
						? {
								id: randomId(),
								kind: 'subscription',
								name: defaultName,
								url: sourceModalUrl.trim(),
								interval: sourceModalInterval,
								headers,
							}
						: {
								id: randomId(),
								kind: 'link',
								name: defaultName,
								link: sourceModalUrl.trim(),
							};
				addedSources = [fallbackSource];
			}

			const existingIds = new Set(draft.sources.map((s) => s.id));
			const deduped = addedSources.map((s) =>
				existingIds.has(s.id) ? { ...s, id: randomId() } : s,
			);
			draft.sources = [...draft.sources, ...deduped];
			const prev = await api.fircPreviewTunnel(draft).catch(() => null);
			if (prev) {
				preview = prev;
				if ((draft.order?.length ?? 0) === 0 && prev.nodes?.length) {
					draft.order = prev.nodes.map((n) => n.key);
				}
			}
			sourceModalOpen = false;
			fircEditTunnelSubTab.set('nodes');
			notifications.success(
				deduped.length > 1
					? `Добавлено источников/узлов: ${deduped.length}. Нажмите «Сохранить» для применения.`
					: 'Источник добавлен. Нажмите «Сохранить» для применения.',
			);
		} catch (e: unknown) {
			notifications.error((e instanceof Error ? e.message : undefined) ?? 'Не удалось разобрать источник');
		} finally {
			sourceModalBusy = false;
		}
	}

	function removeSource(sourceId: string) {
		if (!draft) return;
		const prefix = `${sourceId}:`;
		draft.sources = draft.sources.filter((s) => s.id !== sourceId);
		draft.order = (draft.order ?? []).filter((k) => !k.startsWith(prefix));
		draft.exclude = (draft.exclude ?? []).filter((k) => !k.startsWith(prefix));
	}

	function addInbound() {
		if (!draft) return;
		const usedPorts = new Set(
			$fircTunnels.flatMap((t) => (t.inbounds ?? []).map((i) => i.port)),
		);
		for (const i of draft.inbounds ?? []) usedPorts.add(i.port);
		let port = 1099;
		while (usedPorts.has(port)) port++;
		const inb: FircTunnelInbound = {
			port,
			listen: 'br0',
			node: FIRC_TUNNEL_NODE_POOL,
			user: '',
			password: '',
		};
		draft.inbounds = [...(draft.inbounds ?? []), inb];
	}

	function removeInbound(idx: number) {
		if (!draft) return;
		draft.inbounds = (draft.inbounds ?? []).filter((_, i) => i !== idx);
	}
</script>

{#if draft}
	<Modal
		open={Boolean(draft)}
		title={`Настройки туннеля ${draft.description || draft.id} (${draft.device})`}
		width="780px"
		onclose={closeModal}
	>
		<div class="firc-modal-body">
			<div class="top-meta-bar">
				<div class="top-meta-left">
					<StatusDot
						variant={!draft.enable
							? 'muted'
							: liveState?.status === 'up'
								? 'success'
								: modalDiagnostic?.tone === 'error'
									? 'error'
									: 'warning'}
						size="md"
					/>
					<Badge variant="accent" size="sm" mono>{draft.device}</Badge>
					<input
						class="inp name-inp"
						type="text"
						placeholder="Название или описание туннеля…"
						bind:value={draft.description}
					/>
				</div>
				<div class="top-meta-right">
					<Button
						variant="secondary"
						size="sm"
						loading={busyAction === 'refresh'}
						onclick={handleRefresh}
					>
						{#snippet iconBefore()}<RefreshCw size={13} />{/snippet}
						Обновить подписки
					</Button>
					<Button
						variant="secondary"
						size="sm"
						loading={busyAction === 'restart'}
						onclick={handleRestart}
					>
						{#snippet iconBefore()}<RotateCw size={13} />{/snippet}
						Рестарт
					</Button>
				</div>
			</div>

			{#if modalDiagnostic?.reason}
				<div
					class="modal-diag-banner"
					class:is-error={modalDiagnostic.tone === 'error'}
					class:is-warning={modalDiagnostic.tone === 'warning'}
					class:is-info={modalDiagnostic.tone === 'info'}
					role="alert"
					data-testid="firc-modal-diagnostic"
				>
					{modalDiagnostic.reason}
				</div>
			{/if}

			<div class="tunnel-subtabs">
				<button
					type="button"
					class="subtab-btn"
					class:active={$fircEditTunnelSubTab === 'nodes'}
					onclick={() => fircEditTunnelSubTab.set('nodes')}
				>
					<Server size={13} />
					<span>Узлы ({nodes.length})</span>
				</button>
				<button
					type="button"
					class="subtab-btn"
					class:active={$fircEditTunnelSubTab === 'sources'}
					onclick={() => fircEditTunnelSubTab.set('sources')}
				>
					<Link2 size={13} />
					<span>Подписки и ссылки ({draft.sources.length})</span>
				</button>
				<button
					type="button"
					class="subtab-btn"
					class:active={$fircEditTunnelSubTab === 'failover'}
					onclick={() => fircEditTunnelSubTab.set('failover')}
				>
					<Shield size={13} />
					<span>Отказоустойчивость и сеть</span>
				</button>
				<button
					type="button"
					class="subtab-btn"
					class:active={$fircEditTunnelSubTab === 'inbounds'}
					onclick={() => fircEditTunnelSubTab.set('inbounds')}
				>
					<Network size={13} />
					<span>Входящие порты ({draft.inbounds?.length ?? 0})</span>
				</button>
			</div>

			{#if $fircEditTunnelSubTab === 'nodes'}
				{@const q = nodeQuery.trim().toLowerCase()}
				<div class="subtab-panel">
					<div class="nodes-toolbar">
						<div class="search-box">
							<Search size={13} />
							<input
								type="text"
								placeholder="Фильтр узлов…"
								bind:value={nodeQuery}
							/>
						</div>
						<div class="nodes-toolbar-actions">
							<Button
								variant="secondary"
								size="sm"
								loading={busyAction === 'probe'}
								disabled={Boolean(busyAction || probingNodeKey)}
								onclick={handleProbe}
							>
								Проверить задержку{#if probeProgress}{' '}({probeProgress.done}/{probeProgress.total}){/if}
							</Button>
							<Button variant="secondary" size="sm" onclick={sortNodesByLatency}>
								Сортировать по пингу
							</Button>
							<Button
								variant="ghost"
								size="sm"
								onclick={() => {
									if (draft) draft.exclude = [];
								}}
							>
								Включить все
							</Button>
						</div>
					</div>

					{#if previewError}
						<div class="preview-err">{previewError}</div>
					{/if}

					{#if nodes.length === 0}
						<div class="sub-empty">
							{#if filteredNodes.length > 0}
								{m.firc_filter_all_nodes_hidden()}
							{:else}
								Нет узлов. Перейдите во вкладку «Подписки и ссылки», чтобы добавить подписку или ссылку vless://.
							{/if}
						</div>
					{:else}
						<div class="nodes-table-wrap">
							<table class="nodes-table">
								<thead>
									<tr>
										<th style="width: 68px;">Приор.</th>
										<th>Узел</th>
										<th>Источник</th>
										<th>Статус</th>
										<th>Задержка</th>
										<th style="width: 70px; text-align: right;">В пуле</th>
									</tr>
								</thead>
								<tbody>
									{#each nodes as node, nIdx (node.key)}
										{#if !q || node.name.toLowerCase().includes(q) || node.key.toLowerCase().includes(q)}
											{@const pr = tunProbes[node.key]}
											{@const isIncluded = !(draft.exclude ?? []).includes(node.key)}
											<tr class:excluded-row={!isIncluded}>
												<td>
													<div class="prio-cell">
														<span class="prio-num">{String(nIdx + 1).padStart(2, '0')}</span>
														<div class="prio-btns">
															<button
																type="button"
																class="mini-icon-btn"
																disabled={nIdx === 0}
																onclick={() => moveNode(nIdx, -1)}
															>
																<ArrowUp size={11} />
															</button>
															<button
																type="button"
																class="mini-icon-btn"
																disabled={nIdx === nodes.length - 1}
																onclick={() => moveNode(nIdx, 1)}
															>
																<ArrowDown size={11} />
															</button>
														</div>
													</div>
												</td>
												<td class="node-name-cell">
													{node.name}
													{#if node.v6}
														<Badge variant="info" size="xs" mono title="Узел поддерживает сквозной IPv6 в туннеле">IPv6</Badge>
													{/if}
												</td>
												<td class="mono-cell">{node.source || '—'}</td>
												<td>
													{#if !isIncluded}
														<Badge variant="muted" size="xs">Исключён</Badge>
													{:else if node.state === 'active'}
														<Badge variant="success" size="xs">Активен</Badge>
													{:else if node.state === 'reserve'}
														<Badge variant="info" size="xs">Резерв</Badge>
													{:else if node.state === 'down'}
														<Badge variant="error" size="xs" title={node.skipReason}>
															Недоступен
														</Badge>
													{:else if node.state === 'skipped'}
														<div class="node-status-stack">
															<Badge variant="warning" size="xs" title={node.skipReason}>
																{m.firc_node_state_skipped()}
															</Badge>
															{#if node.skipReason}
																<span class="node-skip-reason" title={node.skipReason}>
																	{formatFircSkipReason(node.skipReason)}
																</span>
															{/if}
														</div>
													{:else}
														<Badge variant="muted" size="xs">Ожидание</Badge>
													{/if}
												</td>
												<td class="mono-cell">
													<div class="probe-cell">
														<div class="probe-vals">
															{#if pr}
																{@const totalMs = totalProbeLatencyMs(pr)}
																{#if !pr.ok}
																	<span class="probe-err" title={pr.why}>Таймаут</span>
																{:else if totalMs !== null}
																	<span
																		class="probe-ok"
																		title={m.firc_probe_total_title()}
																	>
																		{totalMs} мс
																	</span>
																	{#if pr.handshakeMs != null && pr.firstByteMs != null}
																		<span class="probe-breakdown">
																			{m.firc_probe_breakdown({
																				connect: String(pr.handshakeMs),
																				reply: String(pr.firstByteMs),
																			})}
																		</span>
																	{/if}
																{/if}
															{:else}
																<span class="muted-dash">—</span>
															{/if}
														</div>
														{#if !node.skipReason}
															<button
																type="button"
																class="mini-icon-btn probe-node-btn"
																title={m.firc_probe_single_title()}
																aria-label={m.firc_probe_single_aria({ name: node.name })}
																disabled={Boolean(busyAction || probingNodeKey)}
																onclick={() => handleProbeNode(node.key)}
															>
																{#if probingNodeKey === node.key}
																	<span class="spin"><RefreshCw size={12} /></span>
																{:else}
																	<Activity size={12} />
																{/if}
															</button>
														{/if}
													</div>
												</td>
												<td style="text-align: right;">
													<Toggle
														size="sm"
														spinner="none"
														checked={isIncluded}
														onchange={(v) => toggleNodeIncluded(node.key, v)}
													/>
												</td>
											</tr>
										{/if}
									{/each}
								</tbody>
							</table>
						</div>
					{/if}

					{#if filteredNodes.length > 0}
						<section class="filtered-section">
							<button
								type="button"
								class="filtered-toggle"
								aria-expanded={filteredOpen}
								onclick={() => (filteredOpen = !filteredOpen)}
							>
								<span class="lbl">{m.firc_filter_hidden_toggle({ count: filteredNodes.length })}</span>
								<span class="filtered-chevron" class:open={filteredOpen} aria-hidden="true">▸</span>
							</button>
							{#if filteredOpen}
								<div class="hint">
									{m.firc_filter_hidden_hint()}
								</div>
								<div class="filtered-grid">
									{#each filteredNodes as fn (fn.key)}
										<div class="filtered-card">
											<div class="filtered-title">{fn.name || fn.key}</div>
											{#if fn.source}
												<div class="filtered-meta mono">{fn.source}</div>
											{/if}
										</div>
									{/each}
								</div>
							{/if}
						</section>
					{/if}
				</div>
			{/if}

			{#if $fircEditTunnelSubTab === 'sources'}
				<div class="subtab-panel">
					<div class="sources-header">
						<span class="hint">
							Источники загружают узлы (VLESS, FiniteProto v2, VMess, Hysteria2, AnyTLS, Mieru, Trojan, Shadowsocks, TrustTunnel, NaiveProxy, SOCKS5) из подписок (включая happ://crypt, clash://, GitHub) или прямых ссылок / конфигов.
						</span>
						<div class="sources-actions">
							<Button
								variant="secondary"
								size="sm"
								onclick={() => openAddSource('subscription')}
							>
								{#snippet iconBefore()}<Plus size={13} />{/snippet}
								Добавить подписку
							</Button>
							<Button
								variant="secondary"
								size="sm"
								onclick={() => openAddSource('link')}
							>
								{#snippet iconBefore()}<Plus size={13} />{/snippet}
								Добавить узлы / конфиг
							</Button>
							<Button
								variant="ghost"
								size="sm"
								onclick={openHappKeysModal}
							>
								Ключи Happ RSA{happKeysCount > 0 ? ` (${happKeysCount})` : ''}
							</Button>
						</div>
					</div>

					{#if draft.sources.length === 0}
						<div class="sub-empty">Источники ещё не добавлены.</div>
					{:else}
						<div class="sources-list">
							{#each draft.sources as src (src.id)}
								{@const subLive =
									src.kind === 'subscription'
										? (liveState?.subscriptions?.find((s) => s.name === src.name) ??
											(liveState?.subscriptions?.length === 1
												? liveState.subscriptions[0]
												: undefined))
										: undefined}
								<div class="source-row">
									<div class="source-main">
										<div class="source-title-line">
											<Badge
												variant={src.kind === 'subscription' ? 'accent' : 'default'}
												size="xs"
											>
												{src.kind === 'subscription' ? 'Подписка' : 'Ссылка'}
											</Badge>
											<input
												class="inp compact-inp"
												type="text"
												bind:value={src.name}
												placeholder="Название источника"
											/>
											{#if src.kind === 'subscription' && src.headers && Object.keys(src.headers).length > 0}
												<Badge variant="info" size="xs" title={Object.entries(src.headers).map(([k, v]) => `${k}: ${v}`).join('\n')}>
													Заголовки ({Object.keys(src.headers).length})
												</Badge>
											{/if}
											{#if src.kind === 'subscription'}
												{@const usageText = formatSubUsage(subLive?.usage ?? undefined)}
												{#if subLive?.fetching}
													<Badge variant="info" size="xs">{m.firc_sub_fetching_badge()}</Badge>
												{/if}
												{#if subLive && subLive.nodes > 0}
													<Badge variant="default" size="xs" mono>{m.firc_sub_nodes_badge({ count: subLive.nodes })}</Badge>
												{/if}
												{#if usageText}
													<Badge variant="default" size="xs" mono title="Использование трафика и срок подписки (Subscription-Userinfo)">
														{usageText}
													</Badge>
												{/if}
											{/if}
										</div>
										{#if src.kind === 'subscription'}
											<input
												class="inp mono compact-inp"
												type="text"
												bind:value={src.url}
												placeholder="https://... или happ://crypt..."
											/>
											{#if subLive?.error}
												<div
													class="source-error-note"
													role="alert"
													title={subLive.error}
													data-testid="firc-source-error"
												>
													{formatFircSubError(subLive.error)}
												</div>
											{/if}
										{:else}
											<input
												class="inp mono compact-inp"
												type="text"
												bind:value={src.link}
												placeholder="vless://... | vmess://... | hy2://... | anytls://... | mieru://... | tt://..."
											/>
										{/if}
									</div>
									<div class="source-side">
										{#if src.kind === 'subscription'}
											<select class="inp compact-select" bind:value={src.interval}>
												{#each SUB_INTERVALS as opt}
													<option value={opt.value}>{opt.label}</option>
												{/each}
											</select>
										{/if}
										<button
											type="button"
											class="route-action-btn danger"
											title="Удалить источник"
											onclick={() => removeSource(src.id)}
										>
											<Trash2 size={14} />
										</button>
									</div>
								</div>
							{/each}
						</div>
					{/if}

					<div class="filter-section">
						<div class="filter-section-title">{m.firc_filter_section_title()}</div>
						<div class="field-grid-2">
							<div class="field">
								<label class="lbl" for="firc-filter-include">{m.firc_filter_include_label()}</label>
								<input
									id="firc-filter-include"
									class="inp mono"
									type="text"
									bind:value={draft.filter}
									placeholder="(?i)(DE|NL|🇩🇪)"
									autocomplete="off"
									spellcheck="false"
								/>
								{#if includeWarning}
									<span class="filter-warn">{includeWarning}</span>
								{/if}
							</div>
							<div class="field">
								<label class="lbl" for="firc-filter-exclude">{m.firc_filter_exclude_label()}</label>
								<input
									id="firc-filter-exclude"
									class="inp mono"
									type="text"
									bind:value={draft.filterExclude}
									placeholder={m.firc_filter_exclude_example()}
									autocomplete="off"
									spellcheck="false"
								/>
								{#if excludeWarning}
									<span class="filter-warn">{excludeWarning}</span>
								{/if}
							</div>
						</div>
						<div class="filter-hint">
							{m.firc_filter_hint_before()}<code class="mono">(?!...)</code>{m.firc_filter_hint_mid()}<code class="mono">{m.firc_filter_exclude_example()}</code>{m.firc_filter_hint_after()}
						</div>
					</div>
				</div>
			{/if}

			{#if $fircEditTunnelSubTab === 'failover'}
				<div class="subtab-panel">
					<div class="settings-grid-2">
						<div class="sub-box">
							<div class="sub-box-title">Отказоустойчивость и балансировка</div>
							<div class="field-grid-2">
								<div class="field">
									<label class="lbl" for="firc-ed-act">Активных узлов одновременно</label>
									<input
										id="firc-ed-act"
										class="inp mono"
										type="number"
										min="1"
										max="8"
										bind:value={draft.active}
									/>
								</div>
								<div class="field">
									<label class="lbl" for="firc-ed-by">Режим распределения</label>
									<select id="firc-ed-by" class="inp" bind:value={draft.by}>
										<option value="connection">По соединениям</option>
										<option value="site">По домену (сайту)</option>
										<option value="site-client">По сайту и клиенту</option>
									</select>
								</div>
							</div>
							<div class="field-grid-2">
								<div class="field">
									<label class="lbl" for="firc-ed-int">Интервал проверки (с)</label>
									<input
										id="firc-ed-int"
										class="inp mono"
										type="number"
										min="5"
										max="300"
										bind:value={draft.interval}
									/>
								</div>
								<div class="field">
									<label class="lbl" for="firc-ed-sil">Таймаут тишины (с)</label>
									<input
										id="firc-ed-sil"
										class="inp mono"
										type="number"
										min="5"
										max="300"
										bind:value={draft.silence}
									/>
								</div>
							</div>
						</div>

						<div class="sub-box">
							<div class="sub-box-title">Сетевой стек и TLS</div>
							<div class="field-grid-2">
								<div class="field">
									<label class="lbl" for="firc-ed-fp">Отпечаток uTLS (Fingerprint)</label>
									<select
										id="firc-ed-fp"
										class="inp"
										value={draft.advanced?.fingerprint ?? ''}
										onchange={(e) => {
											if (!draft) return;
											draft.advanced = {
												...draft.advanced,
												fingerprint: (e.currentTarget as HTMLSelectElement).value as
													| ''
													| 'chrome'
													| 'firefox',
											};
										}}
									>
										{#each FINGERPRINTS as fp}
											<option value={fp.value}>{fp.label}</option>
										{/each}
									</select>
								</div>
								<div class="field">
									<label class="lbl" for="firc-ed-to">Таймаут подключения (с)</label>
									<input
										id="firc-ed-to"
										class="inp mono"
										type="number"
										min="2"
										max="60"
										value={draft.advanced?.timeout ?? 8}
										oninput={(e) => {
											if (!draft) return;
											draft.advanced = {
												...draft.advanced,
												timeout: Number((e.currentTarget as HTMLInputElement).value),
											};
										}}
									/>
								</div>
							</div>
							<div class="field-row">
								<div class="field-row-text">
									<span class="lbl">Ядерный стек TCP (Kernel TCP)</span>
									<span class="hint">Ускоряет обработку TCP-соединений на роутере</span>
								</div>
								<Toggle
									size="sm"
									spinner="none"
									checked={Boolean(draft.advanced?.kernelTcp)}
									onchange={(v) => {
										if (!draft) return;
										draft.advanced = { ...draft.advanced, kernelTcp: v };
									}}
								/>
							</div>
							<div class="field-row">
								<div class="field-row-text">
									<span class="lbl">Пропускать проверку TLS-сертификата</span>
									<span class="hint">Разрешить самоподписанные сертификаты (allowInsecure)</span>
								</div>
								<Toggle
									size="sm"
									spinner="none"
									checked={Boolean(draft.advanced?.insecure)}
									onchange={(v) => {
										if (!draft) return;
										draft.advanced = { ...draft.advanced, insecure: v };
									}}
								/>
							</div>
						</div>
					</div>
				</div>
			{/if}

			{#if $fircEditTunnelSubTab === 'inbounds'}
				<div class="subtab-panel">
					<div class="sources-header">
						<span class="hint">
							Входящие порты позволяют подключаться к туннелю по протоколам SOCKS5 и HTTP (например, порт 1099 для локальных служб).
						</span>
						<Button variant="secondary" size="sm" onclick={addInbound}>
							{#snippet iconBefore()}<Plus size={13} />{/snippet}
							Добавить порт
						</Button>
					</div>

					{#if (draft.inbounds?.length ?? 0) === 0}
						<div class="sub-empty">Локальные прокси-порты не настроены.</div>
					{:else}
						<div class="sources-list">
							{#each draft.inbounds as inb, idx (idx)}
								<div class="inbound-row">
									<div class="field">
										<label class="lbl" for={`inb-l-${idx}`}>Адрес</label>
										<input
											id={`inb-l-${idx}`}
											class="inp mono compact-inp"
											type="text"
											placeholder={m.firc_inbound_listen_placeholder()}
											bind:value={inb.listen}
										/>
									</div>
									<div class="field">
										<label class="lbl" for={`inb-p-${idx}`}>Порт</label>
										<input
											id={`inb-p-${idx}`}
											class="inp mono compact-inp"
											type="number"
											min="1"
											max="65535"
											bind:value={inb.port}
										/>
									</div>
									<div class="field">
										<label class="lbl" for={`inb-n-${idx}`}>Привязка к узлу</label>
										<select id={`inb-n-${idx}`} class="inp compact-inp" bind:value={inb.node}>
											<option value={FIRC_TUNNEL_NODE_POOL}>Весь пул отказоустойчивости (*)</option>
											{#if inb.node === '' || inb.node === 'default'}
												<option value={inb.node}>Авто (весь пул)</option>
											{/if}
											{#each nodes as n (n.key)}
												<option value={n.key}>{n.name}</option>
											{/each}
										</select>
									</div>
									<div class="inbound-del">
										<button
											type="button"
											class="route-action-btn danger"
											title="Удалить порт"
											onclick={() => removeInbound(idx)}
										>
											<Trash2 size={14} />
										</button>
									</div>
								</div>
							{/each}
						</div>
					{/if}
				</div>
			{/if}
		</div>

		{#snippet actions()}
			<div class="modal-footer-bar">
				<span class="dirty-hint">
					{#if isDirty}Есть несохранённые изменения{:else}Все изменения сохранены{/if}
				</span>
				<div class="footer-btns">
					<Button variant="ghost" size="sm" onclick={closeModal}>Закрыть</Button>
					<Button
						variant="primary"
						size="sm"
						loading={saving}
						disabled={!isDirty}
						onclick={() => saveDraft(false)}
					>
						Сохранить
					</Button>
				</div>
			</div>
		{/snippet}
	</Modal>
{/if}

<Modal
	bind:open={sourceModalOpen}
	title={sourceModalKind === 'subscription' ? 'Добавить подписку (HTTPS / Happ / Clash / GitHub)' : 'Добавить узлы или конфиг'}
	size="md"
	onclose={() => (sourceModalOpen = false)}
>
	<div class="modal-form">
		<div class="field">
			<label class="lbl" for="src-modal-name">Название</label>
			<input
				id="src-modal-name"
				class="inp"
				type="text"
				placeholder={sourceModalKind === 'subscription' ? 'Например: Основная подписка' : 'Например: Нидерланды XHTTP / Hy2'}
				bind:value={sourceModalName}
			/>
		</div>
		{#if sourceModalKind === 'subscription'}
			<div class="field">
				<label class="lbl" for="src-modal-url">
					URL подписки (https://..., happ://crypt..., clash://..., GitHub)
				</label>
				<div style="display: flex; gap: 6px; align-items: center;">
					<input
						id="src-modal-url"
						class="inp mono"
						style="flex: 1;"
						type="text"
						placeholder="https://... или happ://crypt..."
						bind:value={sourceModalUrl}
					/>
					<Button
						variant="secondary"
						size="sm"
						loading={sourceModalDetecting}
						disabled={!sourceModalUrl.trim()}
						onclick={handleDetectSubscriptionHeaders}
					>
						Авто-подбор
					</Button>
				</div>
				<span class="hint">
					Поддерживаются зашифрованные ссылки <code>happ://crypt..crypt4</code>, обёртки <code>happ://</code>, <code>clash://</code>, <code>sub://</code> и ссылки на файлы GitHub/GitLab.
				</span>
			</div>
			<div class="field-grid-2">
				<div class="field">
					<label class="lbl" for="src-modal-int">Интервал автообновления</label>
					<select id="src-modal-int" class="inp" bind:value={sourceModalInterval}>
						{#each SUB_INTERVALS as opt}
							<option value={opt.value}>{opt.label}</option>
						{/each}
					</select>
				</div>
				<div class="field">
					<label class="lbl" for="src-modal-prof">Профиль клиента (User-Agent / HWID)</label>
					<select
						id="src-modal-prof"
						class="inp"
						value={sourceModalProfileKind}
						onchange={(e) => handleSelectHeaderProfile((e.currentTarget as HTMLSelectElement).value)}
					>
						<option value="">По умолчанию (tunvless)</option>
						{#each headerProfiles as prof (prof.kind)}
							<option value={prof.kind}>{prof.label}</option>
						{/each}
					</select>
				</div>
			</div>
			<div class="field">
				<label class="lbl" for="src-modal-hdr">HTTP-заголовки запроса (необязательно, Имя: Значение)</label>
				<textarea
					id="src-modal-hdr"
					class="inp mono"
					rows="3"
					placeholder="User-Agent: Happ/3.6.1&#10;X-HWID: ..."
					bind:value={sourceModalHeadersText}
				></textarea>
			</div>
		{:else}
			<div class="field">
				<label class="lbl" for="src-modal-url">
					Ссылки на узлы, Base64, TrustTunnel TOML, Mieru JSON, Clash YAML или Sing-box JSON
				</label>
				<textarea
					id="src-modal-url"
					class="inp mono"
					rows="6"
					placeholder="vless://... (в т.ч. mode=finiteproto), fp://..., vmess://..., hy2://..., anytls://..., mieru://..., tt://..., trojan://..., ss://..., Base64, TrustTunnel TOML ([endpoint]), Mieru JSON (profiles) или Clash YAML"
					bind:value={sourceModalUrl}
				></textarea>
				<span class="hint">
					Можно вставить одну или несколько ссылок (по одной на строку), Base64-пакет, клиентский конфиг TrustTunnel TOML, Mieru JSON или Clash YAML.
				</span>
			</div>
		{/if}
	</div>
	{#snippet actions()}
		<Button variant="secondary" size="sm" onclick={() => (sourceModalOpen = false)}>
			Отмена
		</Button>
		<Button
			variant="primary"
			size="sm"
			loading={sourceModalBusy}
			disabled={!sourceModalUrl.trim()}
			onclick={submitAddSource}
		>
			Добавить
		</Button>
	{/snippet}
</Modal>

<Modal
	bind:open={happKeysModalOpen}
	title="Приватные ключи Happ RSA (happ://crypt..crypt4)"
	size="md"
	onclose={() => (happKeysModalOpen = false)}
>
	<div class="modal-form">
		<span class="hint">
			Для расшифровки ссылок <code>happ://crypt</code>, <code>crypt2</code>, <code>crypt3</code>, <code>crypt4</code> укажите приватные RSA-ключи в формате PEM (PKCS#1 / PKCS#8) или JSON. Сейчас сохранено ключей: <strong>{happKeysCount}</strong>.
		</span>
		<div class="field">
			<label class="lbl" for="happ-keys-inp">Приватные ключи PEM / JSON</label>
			<textarea
				id="happ-keys-inp"
				class="inp mono"
				rows="6"
				placeholder="-----BEGIN PRIVATE KEY-----&#10;...&#10;-----END PRIVATE KEY-----"
				bind:value={happKeysText}
			></textarea>
		</div>
	</div>
	{#snippet actions()}
		{#if happKeysCount > 0}
			<Button variant="danger" size="sm" loading={happKeysBusy} onclick={clearHappKeysAction}>
				Очистить ключи
			</Button>
		{/if}
		<Button variant="secondary" size="sm" onclick={() => (happKeysModalOpen = false)}>
			Закрыть
		</Button>
		<Button
			variant="primary"
			size="sm"
			loading={happKeysBusy}
			disabled={!happKeysText.trim()}
			onclick={saveHappKeysAction}
		>
			Сохранить ключи
		</Button>
	{/snippet}
</Modal>

<style>
	.firc-modal-body {
		display: flex;
		flex-direction: column;
		gap: 12px;
	}
	.top-meta-bar {
		display: flex;
		align-items: center;
		justify-content: space-between;
		gap: 10px;
		flex-wrap: wrap;
	}
	.top-meta-left {
		display: flex;
		align-items: center;
		gap: 8px;
		flex: 1 1 240px;
	}
	.name-inp {
		max-width: 260px;
		font-weight: 600;
	}
	.top-meta-right {
		display: flex;
		align-items: center;
		gap: 6px;
	}
	.tunnel-subtabs {
		display: flex;
		flex-wrap: wrap;
		gap: 4px;
		border-bottom: 1px solid var(--border);
		padding-bottom: 8px;
	}
	.subtab-btn {
		display: inline-flex;
		align-items: center;
		gap: 6px;
		padding: 6px 12px;
		border-radius: 6px;
		border: 1px solid transparent;
		background: transparent;
		color: var(--text-secondary);
		font-size: 12px;
		font-weight: 600;
		cursor: pointer;
	}
	.subtab-btn:hover {
		color: var(--text-primary);
		background: var(--bg-tertiary);
	}
	.subtab-btn.active {
		color: var(--accent);
		background: color-mix(in srgb, var(--accent) 12%, var(--bg-tertiary));
		border-color: color-mix(in srgb, var(--accent) 35%, var(--border));
	}
	.subtab-panel {
		display: flex;
		flex-direction: column;
		gap: 10px;
	}
	.nodes-toolbar {
		display: flex;
		align-items: center;
		justify-content: space-between;
		gap: 8px;
		flex-wrap: wrap;
	}
	.search-box {
		display: flex;
		align-items: center;
		gap: 6px;
		padding: 0 10px;
		height: 30px;
		border-radius: 6px;
		border: 1px solid var(--border);
		background: var(--bg-primary);
		color: var(--text-secondary);
		flex: 1 1 180px;
		max-width: 260px;
	}
	.search-box input {
		border: 0;
		background: transparent;
		color: var(--text-primary);
		font-size: 12px;
		width: 100%;
		outline: none;
	}
	.nodes-toolbar-actions {
		display: flex;
		align-items: center;
		gap: 6px;
		flex-wrap: wrap;
	}
	.nodes-table-wrap {
		max-height: 320px;
		overflow-x: auto;
		overflow-y: auto;
		border: 1px solid var(--border);
		border-radius: 8px;
	}
	.nodes-table {
		width: 100%;
		border-collapse: collapse;
		font-size: 12px;
	}
	.nodes-table th {
		text-align: left;
		padding: 7px 10px;
		font-size: 10.5px;
		text-transform: uppercase;
		letter-spacing: 0.04em;
		color: var(--text-muted);
		background: var(--bg-tertiary);
		border-bottom: 1px solid var(--border);
		position: sticky;
		top: 0;
		z-index: 1;
	}
	.nodes-table td {
		padding: 6px 10px;
		border-bottom: 1px solid var(--border);
		vertical-align: middle;
	}
	.excluded-row {
		opacity: 0.5;
	}
	.prio-cell {
		display: inline-flex;
		align-items: center;
		gap: 4px;
	}
	.prio-num {
		font-family: var(--font-mono);
		font-size: 11px;
		color: var(--text-muted);
	}
	.prio-btns {
		display: flex;
		flex-direction: column;
	}
	.mini-icon-btn {
		border: 0;
		background: transparent;
		color: var(--text-muted);
		padding: 0;
		cursor: pointer;
		line-height: 1;
	}
	.mini-icon-btn:hover:not(:disabled) {
		color: var(--text-primary);
	}
	.mini-icon-btn:disabled {
		opacity: 0.25;
		cursor: default;
	}
	.node-name-cell {
		font-weight: 500;
		color: var(--text-primary);
	}
	.mono-cell {
		font-family: var(--font-mono);
		font-size: 11.5px;
		color: var(--text-secondary);
	}
	.probe-ok {
		color: var(--color-success, #9ece6a);
		font-weight: 600;
	}
	.probe-err {
		color: var(--color-error, #f7768e);
	}
	.sources-header {
		display: flex;
		align-items: center;
		justify-content: space-between;
		gap: 10px;
		flex-wrap: wrap;
	}
	.sources-actions {
		display: flex;
		gap: 6px;
		flex-wrap: wrap;
	}
	.sources-list {
		display: flex;
		flex-direction: column;
		gap: 8px;
	}
	.source-row {
		display: flex;
		align-items: center;
		justify-content: space-between;
		gap: 10px;
		padding: 8px 10px;
		border: 1px solid var(--border);
		border-radius: 8px;
		background: var(--bg-tertiary);
	}
	.source-main {
		display: flex;
		flex-direction: column;
		gap: 6px;
		flex: 1;
		min-width: 0;
	}
	.source-title-line {
		display: flex;
		align-items: center;
		gap: 8px;
	}
	.source-side {
		display: flex;
		align-items: center;
		gap: 6px;
	}
	.settings-grid-2 {
		display: grid;
		grid-template-columns: 1fr 1fr;
		gap: 10px;
	}
	@media (max-width: 680px) {
		.settings-grid-2 {
			grid-template-columns: 1fr;
		}
	}
	.sub-box {
		padding: 12px;
		border: 1px solid var(--border);
		border-radius: 8px;
		background: var(--bg-tertiary);
		display: flex;
		flex-direction: column;
		gap: 10px;
	}
	.sub-box-title {
		font-size: 11px;
		font-weight: 700;
		text-transform: uppercase;
		letter-spacing: 0.04em;
		color: var(--text-muted);
	}
	.field-grid-2 {
		display: grid;
		grid-template-columns: 1fr 1fr;
		gap: 8px;
	}
	.field {
		display: flex;
		flex-direction: column;
		gap: 4px;
	}
	.field-row {
		display: flex;
		align-items: center;
		justify-content: space-between;
		gap: 10px;
	}
	.field-row-text {
		display: flex;
		flex-direction: column;
		gap: 2px;
	}
	.lbl {
		font-size: 11.5px;
		color: var(--text-secondary);
		font-weight: 500;
	}
	.hint {
		font-size: 11px;
		color: var(--text-muted);
	}
	.inp {
		padding: 5px 8px;
		border-radius: 6px;
		border: 1px solid var(--border);
		background: var(--bg-primary);
		color: var(--text-primary);
		font-size: 12px;
		width: 100%;
		box-sizing: border-box;
	}
	select.inp {
		padding-right: 1.75rem;
		text-overflow: ellipsis;
	}
	.compact-select {
		width: auto;
	}
	.mono {
		font-family: var(--font-mono);
	}
	.inbound-row {
		display: grid;
		grid-template-columns: 1fr 90px 1.4fr auto;
		gap: 8px;
		align-items: end;
		padding: 8px 10px;
		border: 1px solid var(--border);
		border-radius: 8px;
		background: var(--bg-tertiary);
	}
	.inbound-row > .field {
		min-width: 0;
	}
	@media (max-width: 600px) {
		.inbound-row {
			grid-template-columns: 1fr 96px auto;
		}
		.inbound-row > .field:nth-child(3) {
			grid-column: 1 / -1;
		}
		.inbound-row > .inbound-del {
			grid-row: 1;
			grid-column: 3;
		}
	}
	.sub-empty {
		padding: 20px;
		text-align: center;
		color: var(--text-muted);
		font-size: 12px;
		border: 1px dashed var(--border);
		border-radius: 8px;
	}
	.modal-footer-bar {
		display: flex;
		align-items: center;
		justify-content: space-between;
		flex-wrap: wrap;
		width: 100%;
		gap: 12px;
	}
	.dirty-hint {
		font-size: 12px;
		color: var(--text-muted);
	}
	.footer-btns {
		display: flex;
		gap: 8px;
	}
	.modal-form {
		display: flex;
		flex-direction: column;
		gap: 10px;
	}
	.probe-cell {
		display: flex;
		align-items: center;
		justify-content: space-between;
		gap: 6px;
		min-width: 96px;
	}
	.probe-vals {
		display: flex;
		flex-direction: column;
		align-items: flex-start;
		line-height: 1.25;
	}
	.probe-breakdown {
		font-size: 10px;
		color: var(--text-muted);
		white-space: nowrap;
	}
	.probe-node-btn {
		display: inline-flex;
		align-items: center;
		justify-content: center;
		padding: 3px;
		border-radius: 4px;
		flex-shrink: 0;
	}
	.probe-node-btn:hover:not(:disabled) {
		background: var(--bg-tertiary);
	}
	.spin {
		display: inline-flex;
		animation: firc-probe-spin 1s linear infinite;
	}
	@keyframes firc-probe-spin {
		to {
			transform: rotate(360deg);
		}
	}
	.modal-diag-banner {
		padding: 8px 12px;
		border-radius: 8px;
		border: 1px solid var(--border);
		background: var(--bg-tertiary);
		color: var(--text-secondary);
		font-size: 12px;
		line-height: 1.4;
		overflow-wrap: anywhere;
	}
	.modal-diag-banner.is-error {
		background: var(--color-error-tint, rgba(247, 118, 142, 0.14));
		border-color: var(--color-error-border, rgba(247, 118, 142, 0.4));
		color: var(--color-error, #f7768e);
	}
	.modal-diag-banner.is-warning {
		background: var(--color-warning-tint, rgba(224, 175, 104, 0.14));
		border-color: var(--color-warning-border, rgba(224, 175, 104, 0.4));
		color: var(--color-warning, #e0af68);
	}
	.modal-diag-banner.is-info {
		background: var(--color-info-tint, rgba(125, 207, 255, 0.14));
		border-color: var(--color-info-border, rgba(125, 207, 255, 0.4));
		color: var(--color-info, #7dcfff);
	}
	.node-status-stack {
		display: flex;
		flex-direction: column;
		align-items: flex-start;
		gap: 3px;
	}
	.node-skip-reason {
		font-size: 11px;
		line-height: 1.3;
		color: var(--color-warning, #e0af68);
		overflow-wrap: anywhere;
	}
	.source-error-note {
		padding: 6px 8px;
		border-radius: 6px;
		border: 1px solid var(--color-error-border, rgba(247, 118, 142, 0.4));
		background: var(--color-error-tint, rgba(247, 118, 142, 0.14));
		color: var(--color-error, #f7768e);
		font-size: 11.5px;
		line-height: 1.35;
		overflow-wrap: anywhere;
	}
	.filter-section {
		display: flex;
		flex-direction: column;
		gap: 8px;
		padding: 12px;
		border: 1px solid var(--border);
		border-radius: 8px;
		background: var(--bg-tertiary);
	}
	.filter-section-title {
		font-size: 11px;
		font-weight: 700;
		text-transform: uppercase;
		letter-spacing: 0.04em;
		color: var(--text-muted);
	}
	.filter-warn {
		font-size: 11px;
		color: var(--color-warning, #e0af68);
	}
	.filter-hint {
		font-size: 11px;
		color: var(--text-muted);
		line-height: 1.45;
	}
	.preview-err {
		padding: 8px 10px;
		border-radius: 6px;
		border: 1px solid color-mix(in srgb, var(--color-error, #f7768e) 45%, var(--border));
		background: color-mix(in srgb, var(--color-error, #f7768e) 10%, var(--bg-tertiary));
		color: var(--color-error, #f7768e);
		font-size: 11.5px;
	}
	.filtered-section {
		display: flex;
		flex-direction: column;
		gap: 8px;
		padding: 10px 12px;
		border: 1px solid var(--border);
		border-radius: 8px;
		background: var(--bg-tertiary);
	}
	.filtered-toggle {
		display: flex;
		align-items: center;
		justify-content: space-between;
		width: 100%;
		padding: 0;
		border: 0;
		background: transparent;
		cursor: pointer;
		text-align: left;
	}
	.filtered-chevron {
		font-size: 12px;
		color: var(--text-muted);
		transition: transform 0.15s ease;
	}
	.filtered-chevron.open {
		transform: rotate(90deg);
	}
	.filtered-grid {
		display: grid;
		grid-template-columns: repeat(auto-fill, minmax(180px, 1fr));
		gap: 6px;
		max-height: 180px;
		overflow-y: auto;
	}
	.filtered-card {
		padding: 6px 8px;
		border: 1px solid var(--border);
		border-radius: 6px;
		background: var(--bg-primary);
		opacity: 0.8;
		min-width: 0;
	}
	.filtered-title {
		font-size: 11.5px;
		font-weight: 500;
		color: var(--text-primary);
		overflow: hidden;
		text-overflow: ellipsis;
		white-space: nowrap;
	}
	.filtered-meta {
		font-size: 10.5px;
		color: var(--text-muted);
		overflow: hidden;
		text-overflow: ellipsis;
		white-space: nowrap;
	}
</style>
