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

	// Source modal state
	let sourceModalOpen = $state(false);
	let sourceModalKind = $state<'subscription' | 'link'>('subscription');
	let headerProfiles = $state<Array<{ kind: string; label: string; headersText: string }>>([]);

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
			// Сохранённый туннель показывает живые узлы: предпросмотр, снятый до
			// скачивания подписки (пустой, без кэша), иначе прятал бы их до закрытия окна.
			preview = null;
			await reloadFircAll();
			notifications.success(m.firc_tunnel_edit_saved({ device: updated.device }));
			if (closeAfter) {
				fircEditTunnelId.set(null);
			}
		} catch (e: unknown) {
			notifications.error((e instanceof Error ? e.message : undefined) ?? m.firc_tunnel_edit_save_error());
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
		busyAction = 'refresh';
		try {
			if (isDirty) {
				await saveDraft(false);
			}
			await api.fircRefreshTunnel(draft.id);
			// Обновление только ставит скачивание в очередь: предпросмотр сразу после
			// него — ещё старые данные. Узлы придут в живом состоянии.
			preview = null;
			await reloadFircAll();
			notifications.success(m.firc_tunnel_edit_refresh_done({ device: draft.device }));
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
		sourceModalKind = kind;
		sourceModalOpen = true;
		void ensureSubscriptionAuxLoaded();
	}

	async function addSources(addedSources: FircTunnelSource[]) {
		if (!draft) return;
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
				? m.firc_tunnel_edit_sources_added({ count: deduped.length })
				: m.firc_tunnel_edit_source_added(),
		);
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

{#if sourceModalOpen}
	<FircTunnelEditSourceModal
		kind={sourceModalKind}
		{headerProfiles}
		onclose={() => (sourceModalOpen = false)}
		onadd={addSources}
	/>
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
