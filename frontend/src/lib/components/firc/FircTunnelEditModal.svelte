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
	import { getFircTunnelDiagnostic } from '$lib/utils/fircDiagnostics';
	import { Button, Modal } from '$lib/components/ui';
	import { notifications } from '$lib/stores/notifications';
	import {
		totalProbeLatencyMs,
		randomId,
		cloneTunnel,
		isEmptyUncachedPreview,
		prunePreviewSource,
		buildFilteredNodes,
		buildDisplayNodes,
		type DisplayNode,
	} from './fircTunnelEdit';
	import FircTunnelEditHeader from './FircTunnelEditHeader.svelte';
	import FircTunnelEditNodesTab from './FircTunnelEditNodesTab.svelte';
	import FircTunnelEditSourcesTab from './FircTunnelEditSourcesTab.svelte';
	import FircTunnelEditFailoverTab from './FircTunnelEditFailoverTab.svelte';
	import FircTunnelEditInboundsTab from './FircTunnelEditInboundsTab.svelte';
	import FircTunnelEditSourceModal from './FircTunnelEditSourceModal.svelte';
	import FircTunnelEditHappKeysModal from './FircTunnelEditHappKeysModal.svelte';
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
	let previewEpoch = 0;
	let cancelEffectPreview: (() => void) | null = null;
	let cancelDirectPreview: (() => void) | null = null;
	let activeDirectPreviewKey = '';
	let deferredDirectPreview = false;
	let destroyed = false;

	function cancelAllPendingPreviews() {
		const eff = cancelEffectPreview;
		const dir = cancelDirectPreview;
		cancelEffectPreview = null;
		cancelDirectPreview = null;
		activeDirectPreviewKey = '';
		eff?.();
		dir?.();
	}

	function clearPreview() {
		previewEpoch++;
		deferredDirectPreview = false;
		cancelAllPendingPreviews();
		preview = null;
		previewError = '';
	}

	$effect(() => () => {
		destroyed = true;
		previewEpoch++;
		deferredDirectPreview = false;
		cancelAllPendingPreviews();
	});

	// Source modal state
	interface ActiveSourceModal {
		session: number;
		kind: 'subscription' | 'link';
		onclose: () => void;
		onadd: (sources: FircTunnelSource[]) => Promise<void>;
	}
	let sourceModalSession = 0;
	let activeSourceModal = $state<ActiveSourceModal | null>(null);
	let headerProfiles = $state<Array<{ kind: string; label: string; headersText: string }>>([]);

	function closeSourceModal() {
		sourceModalSession++;
		activeSourceModal = null;
	}

	// Happ RSA keys modal state
	let happKeysModalOpen = $state(false);
	let happKeysCount = $state(0);

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

	async function openHappKeysModal() {
		await ensureSubscriptionAuxLoaded();
		happKeysModalOpen = true;
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
			clearPreview();
			closeSourceModal();
			filteredOpen = false;
			nodeQuery = '';
			return;
		}
		const found = $fircTunnels.find((t) => t.id === id);
		if (found && (!draft || draft.id !== id)) {
			const c = cloneTunnel(found);
			draft = c;
			baselineJson = JSON.stringify(c);
			clearPreview();
			closeSourceModal();
			filteredOpen = false;
		}
	});

	const isDirty = $derived(draft !== null && JSON.stringify(draft) !== baselineJson);
	const liveState = $derived(
		draft ? $fircTunnelsState.find((s) => s.id === draft!.id) : undefined,
	);
	const liveSubStamp = $derived(
		(liveState?.subscriptions ?? [])
			.map((s) => `${s.name}:${s.lastOk}:${s.nodes}:${s.fetching ? 1 : 0}`)
			.join('|'),
	);
	const previewSourcesKey = $derived(
		(draft?.sources ?? [])
			.map((s) => {
				const hdrs = s.headers
					? Object.keys(s.headers)
							.sort()
							.map((k) => `${k}=${s.headers![k]}`)
							.join('&')
					: '';
				return `${s.id}|${s.kind}|${s.name ?? ''}|${s.url ?? ''}|${s.link ?? ''}|${hdrs}`;
			})
			.join('\n'),
	);
	const previewDraftKey = $derived(
		draft
			? `${draft.id}\0${draft.filter ?? ''}\0${draft.filterExclude ?? ''}\0${draft.advanced?.insecure ? 1 : 0}\0${previewSourcesKey}`
			: '',
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
		if (!tun) return;
		const hasSubs = hasSubscriptionSource;
		const incWarn = includeWarning;
		const excWarn = excludeWarning;
		const draftKey = previewDraftKey;
		const hasSources = tun.sources.length > 0;
		const hasPreview = untrack(() => preview !== null);
		void liveSubStamp;
		if (!hasSubs && !hasPreview && !deferredDirectPreview) return;
		if (!hasSources) return;
		if (incWarn || excWarn) {
			previewError = '';
			return;
		}
		const saved = $fircTunnels.find((t) => t.id === tun.id);
		const curInc = tun.filter ?? '';
		const curExc = tun.filterExclude ?? '';
		const savedInc = saved?.filter ?? '';
		const savedExc = saved?.filterExclude ?? '';
		if (!hasPreview && !deferredDirectPreview && curInc === savedInc && curExc === savedExc) {
			return;
		}
		if (activeDirectPreviewKey !== '' && draftKey === activeDirectPreviewKey) {
			return;
		}
		const payload = untrack(() => cloneTunnel(tun));
		const scheduledEpoch = previewEpoch;
		const ctrl = new AbortController();
		const cancel = () => {
			clearTimeout(timer);
			ctrl.abort();
		};
		const timer = setTimeout(async () => {
			try {
				if (destroyed || ctrl.signal.aborted || scheduledEpoch !== previewEpoch) return;
				const prevDir = cancelDirectPreview;
				cancelDirectPreview = null;
				activeDirectPreviewKey = '';
				if (prevDir && preview === null) {
					deferredDirectPreview = true;
				}
				prevDir?.();
				const epoch = ++previewEpoch;
				try {
					const res = await api.fircPreviewTunnel(payload, ctrl.signal);
					if (destroyed || ctrl.signal.aborted || epoch !== previewEpoch) return;
					deferredDirectPreview = false;
					if (isEmptyUncachedPreview(res)) {
						preview = null;
						previewError = '';
						return;
					}
					preview = res;
					previewError = '';
				} catch (e: unknown) {
					if (destroyed || ctrl.signal.aborted || epoch !== previewEpoch) return;
					if (e instanceof DOMException && e.name === 'AbortError') return;
					previewError =
						e instanceof Error ? e.message : m.firc_filter_preview_error();
				}
			} finally {
				if (cancelEffectPreview === cancel) {
					cancelEffectPreview = null;
				}
			}
		}, 300);
		cancelEffectPreview = cancel;
		return () => {
			cancel();
			if (cancelEffectPreview === cancel) {
				cancelEffectPreview = null;
			}
		};
	});

	const filteredNodes = $derived.by((): FircTunnelFilteredNode[] =>
		buildFilteredNodes(preview, liveState),
	);

	const nodes = $derived.by((): DisplayNode[] =>
		draft ? buildDisplayNodes(draft, liveState, preview, filteredNodes) : [],
	);

	function closeModal() {
		if (saving) return;
		fircEditTunnelId.set(null);
	}

	async function saveDraft(closeAfter = false): Promise<boolean> {
		if (!draft || saving) return false;
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
			clearPreview();
			fircTunnels.set(finalTunnels);
			const fresh = finalTunnels.find((t) => t.id === updated.id) ?? updated;
			draft = cloneTunnel(fresh);
			baselineJson = JSON.stringify(draft);
			await reloadFircAll();
			notifications.success(m.firc_tunnel_edit_saved({ device: updated.device }));
			if (closeAfter) {
				fircEditTunnelId.set(null);
			}
			return true;
		} catch (e: unknown) {
			notifications.error((e instanceof Error ? e.message : undefined) ?? m.firc_tunnel_edit_save_error());
			return false;
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
			if (rows.length === 0) {
				notifications.info(m.firc_tunnel_edit_probe_empty({ device: targetDev }));
			} else {
				notifications.success(m.firc_tunnel_edit_probe_done({ device: targetDev }));
			}
		} catch (e: unknown) {
			notifications.error((e instanceof Error ? e.message : undefined) ?? m.firc_tunnel_edit_probe_error());
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
		const targetId = draft.id;
		const targetDev = draft.device;
		busyAction = 'refresh';
		try {
			if (isDirty) {
				const saved = await saveDraft(false);
				if (!saved || !draft || draft.id !== targetId) return;
			}
			clearPreview();
			await api.fircRefreshTunnel(targetId);
			await reloadFircAll();
			notifications.success(m.firc_tunnel_edit_refresh_done({ device: targetDev }));
		} catch (e: unknown) {
			notifications.error((e instanceof Error ? e.message : undefined) ?? m.firc_tunnel_edit_refresh_error());
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
			notifications.success(m.firc_tunnel_edit_restart_done({ device: draft.device }));
		} catch (e: unknown) {
			notifications.error((e instanceof Error ? e.message : undefined) ?? m.firc_tunnel_edit_restart_error());
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
		if (!draft) return;
		const session = ++sourceModalSession;
		const targetId = draft.id;
		activeSourceModal = {
			session,
			kind,
			onclose: () => {
				if (sourceModalSession === session) closeSourceModal();
			},
			onadd: (sources) => addSources(sources, targetId, session),
		};
		void ensureSubscriptionAuxLoaded();
	}

	async function previewDraftSources(targetId: string, initOrder = false): Promise<boolean> {
		if (destroyed || !draft || draft.id !== targetId) return false;
		cancelAllPendingPreviews();
		const epoch = ++previewEpoch;
		if (includeWarning || excludeWarning) {
			if (draft.sources.length > 0 && (preview === null || !hasSubscriptionSource)) {
				deferredDirectPreview = true;
			}
			return true;
		}
		const payload = cloneTunnel(draft);
		const draftKey = previewDraftKey;
		const startSubStamp = liveSubStamp;
		const ctrl = new AbortController();
		const cancel = () => ctrl.abort();
		cancelDirectPreview = cancel;
		activeDirectPreviewKey = draftKey;
		let completed = false;
		let needsFollowUp = false;
		try {
			const prev = await api.fircPreviewTunnel(payload, ctrl.signal).catch(() => null);
			if (destroyed || ctrl.signal.aborted || epoch !== previewEpoch || !draft || draft.id !== targetId) {
				return false;
			}
			if (previewDraftKey !== draftKey) {
				if (
					draft.sources.length > 0 &&
					!(includeWarning || excludeWarning) &&
					cancelEffectPreview === null
				) {
					needsFollowUp = true;
				} else if (draft.sources.length > 0 && (preview === null || !hasSubscriptionSource)) {
					deferredDirectPreview = true;
				}
			} else {
				completed = true;
				deferredDirectPreview = false;
				if (prev) {
					if (isEmptyUncachedPreview(prev)) {
						preview = null;
						previewError = '';
					} else {
						preview = prev;
						previewError = '';
						if (initOrder && (draft.order?.length ?? 0) === 0 && prev.nodes?.length) {
							draft.order = prev.nodes.map((n) => n.key);
						}
					}
				}
				needsFollowUp = prev !== null && hasSubscriptionSource && liveSubStamp !== startSubStamp;
			}
		} finally {
			if (cancelDirectPreview === cancel) {
				cancelDirectPreview = null;
			}
			if (activeDirectPreviewKey === draftKey && epoch === previewEpoch) {
				activeDirectPreviewKey = '';
			}
		}
		if (needsFollowUp) {
			void previewDraftSources(targetId, false);
		}
		return completed;
	}

	async function addSources(
		addedSources: FircTunnelSource[],
		targetId: string,
		modalSession: number,
	) {
		if (destroyed || !draft || draft.id !== targetId || sourceModalSession !== modalSession) {
			return;
		}
		const existingIds = new Set(draft.sources.map((s) => s.id));
		const deduped = addedSources.map((s) =>
			existingIds.has(s.id) ? { ...s, id: randomId() } : s,
		);
		draft.sources = [...draft.sources, ...deduped];
		const stillCurrent = await previewDraftSources(targetId, true);
		if (destroyed) return;
		const sameModal = sourceModalSession === modalSession;
		if (sameModal) {
			closeSourceModal();
		}
		if (!stillCurrent || !draft || draft.id !== targetId) return;
		if (sameModal) {
			fircEditTunnelSubTab.set('nodes');
		}
		notifications.success(
			deduped.length > 1
				? m.firc_tunnel_edit_sources_added({ count: deduped.length })
				: m.firc_tunnel_edit_source_added(),
		);
	}

	function removeSource(sourceId: string) {
		if (!draft) return;
		const targetId = draft.id;
		const prefix = `${sourceId}:`;
		const prevSources = draft.sources;
		draft.sources = prevSources.filter((s) => s.id !== sourceId);
		draft.order = (draft.order ?? []).filter((k) => !k.startsWith(prefix));
		draft.exclude = (draft.exclude ?? []).filter((k) => !k.startsWith(prefix));
		if (draft.sources.length === 0) {
			clearPreview();
			return;
		}
		const hadPreview = preview !== null;
		const hadDirect = cancelDirectPreview !== null;
		const hadDeferred = deferredDirectPreview;
		if (hadPreview) {
			preview = prunePreviewSource(preview, sourceId, prevSources);
		}
		if (hadPreview || hadDirect || hadDeferred) {
			void previewDraftSources(targetId, false);
		}
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
		title={m.firc_tunnel_edit_title({ name: draft.description || draft.id, device: draft.device })}
		width="780px"
		onclose={closeModal}
	>
		<div class="firc-modal-body">
			<FircTunnelEditHeader
				bind:draft={() => draft!, (v) => (draft = v)}
				{liveState}
				diagnostic={modalDiagnostic}
				{busyAction}
				nodeCount={nodes.length}
				onrefresh={handleRefresh}
				onrestart={handleRestart}
			/>

			{#if $fircEditTunnelSubTab === 'nodes'}
				<FircTunnelEditNodesTab
					exclude={draft.exclude ?? []}
					{nodes}
					{filteredNodes}
					{tunProbes}
					{previewError}
					{busyAction}
					{probingNodeKey}
					{probeProgress}
					bind:nodeQuery
					bind:filteredOpen
					onprobe={handleProbe}
					onprobenode={handleProbeNode}
					onsort={sortNodesByLatency}
					onincludeall={() => {
						if (draft) draft.exclude = [];
					}}
					onmove={moveNode}
					ontoggle={toggleNodeIncluded}
				/>
			{/if}

			{#if $fircEditTunnelSubTab === 'sources'}
				<FircTunnelEditSourcesTab
					bind:draft={() => draft!, (v) => (draft = v)}
					{liveState}
					{happKeysCount}
					{includeWarning}
					{excludeWarning}
					onaddsource={openAddSource}
					onhappkeys={openHappKeysModal}
					onremovesource={removeSource}
				/>
			{/if}

			{#if $fircEditTunnelSubTab === 'failover'}
				<FircTunnelEditFailoverTab bind:draft={() => draft!, (v) => (draft = v)} />
			{/if}

			{#if $fircEditTunnelSubTab === 'inbounds'}
				<FircTunnelEditInboundsTab
					bind:draft={() => draft!, (v) => (draft = v)}
					{nodes}
					onadd={addInbound}
					onremove={removeInbound}
				/>
			{/if}
		</div>

		{#snippet actions()}
			<div class="modal-footer-bar">
				<span class="dirty-hint">
					{#if isDirty}{m.zapret_hostlist_unsaved_changes()}{:else}{m.firc_tunnel_edit_all_saved()}{/if}
				</span>
				<div class="footer-btns">
					<Button variant="ghost" size="sm" onclick={closeModal}>{m.common_close()}</Button>
					<Button
						variant="primary"
						size="sm"
						loading={saving}
						disabled={!isDirty}
						onclick={() => saveDraft(false)}
					>
						{m.common_save()}
					</Button>
				</div>
			</div>
		{/snippet}
	</Modal>
{/if}

{#if activeSourceModal}
	{#key activeSourceModal.session}
		<FircTunnelEditSourceModal
			kind={activeSourceModal.kind}
			{headerProfiles}
			onclose={activeSourceModal.onclose}
			onadd={activeSourceModal.onadd}
		/>
	{/key}
{/if}

{#if happKeysModalOpen}
	<FircTunnelEditHappKeysModal bind:count={happKeysCount} onclose={() => (happKeysModalOpen = false)} />
{/if}

<style>
	.firc-modal-body {
		display: flex;
		flex-direction: column;
		gap: 12px;
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
</style>
