<script lang="ts">
	import { api } from '$lib/api/client';
	import type {
		AccessPolicy,
		DnsRoute,
		GeoFileEntry,
		OversizedTag,
		PolicyGlobalInterface,
		RoutingTunnel,
	} from '$lib/types';
	import { notifications } from '$lib/stores/notifications';
	import { Modal } from '$lib/components/ui';
	import { InterfaceList } from '$lib/components/accesspolicy';
	import HrNeoTargetSidebar, {
		type TargetEntry,
		type SidebarSelection,
	} from './HrNeoTargetSidebar.svelte';
	import HrNeoRulesList from './HrNeoRulesList.svelte';
	import HrNeoGeoDataView from './HrNeoGeoDataView.svelte';
	import HrNeoSettingsView from './HrNeoSettingsView.svelte';
	import HrNeoDisabledTagsView from './HrNeoDisabledTagsView.svelte';
	import HrNeoEditModal from './HrNeoEditModal.svelte';

	interface Props {
		dnsRoutes: DnsRoute[];
		tunnels: RoutingTunnel[];
		policies: AccessPolicy[];
		policyInterfaces: PolicyGlobalInterface[];
	}

	let { dnsRoutes, tunnels, policies, policyInterfaces }: Props = $props();

	let hrRules = $derived(dnsRoutes.filter((r) => r.backend === 'hydraroute'));

	let policyOrder = $state<string[]>([]);

	async function loadPolicyOrder() {
		try {
			const cfg = await api.getHydraRouteConfig();
			policyOrder = cfg.policyOrder ?? [];
		} catch {
			policyOrder = [];
		}
	}

	$effect(() => {
		void loadPolicyOrder();
	});

	let geoFiles = $state<GeoFileEntry[]>([]);
	let selection = $state<SidebarSelection>(null);

	let editOpen = $state(false);
	let editingRule = $state<DnsRoute | null>(null);
	let editInitialTarget = $state<{ kind: 'interface' | 'policy'; name: string } | undefined>(
		undefined,
	);
	let saving = $state(false);

	let isMobile = $state(false);
	$effect(() => {
		const mq = window.matchMedia('(max-width: 640px)');
		isMobile = mq.matches;
		const handler = (e: MediaQueryListEvent) => (isMobile = e.matches);
		mq.addEventListener('change', handler);
		return () => mq.removeEventListener('change', handler);
	});

	async function loadGeoFiles() {
		try {
			geoFiles = await api.getGeoFiles();
		} catch {
			geoFiles = [];
		}
	}

	$effect(() => {
		void loadGeoFiles();
	});

	let oversizedTags = $state<OversizedTag[]>([]);
	let maxelem = $state<number>(0);
	let oversizedInstalled = $state<boolean>(false);
	let pendingOversizedRefresh: ReturnType<typeof setTimeout> | null = null;

	async function loadOversized() {
		try {
			const resp = await api.getHydraRouteOversizedTags();
			oversizedInstalled = resp.installed;
			oversizedTags = resp.tags ?? [];
			maxelem = resp.maxelem ?? 0;
		} catch {
			oversizedInstalled = false;
			oversizedTags = [];
			maxelem = 0;
		}
	}

	function scheduleOversizedRefresh(delayMs = 3000) {
		if (pendingOversizedRefresh) clearTimeout(pendingOversizedRefresh);
		pendingOversizedRefresh = setTimeout(() => {
			pendingOversizedRefresh = null;
			void loadOversized();
		}, delayMs);
	}

	$effect(() => {
		void dnsRoutes;
		void loadOversized();
	});

	$effect(() => () => {
		if (pendingOversizedRefresh) clearTimeout(pendingOversizedRefresh);
	});

	function targetOf(r: DnsRoute): { name: string; kind: 'policy' | 'interface' } | null {
		if (r.hrRouteMode === 'policy' && r.hrPolicyName) {
			return { name: r.hrPolicyName, kind: 'policy' };
		}
		const first = r.routes?.[0];
		if (!first) return null;
		return { name: first.interface || first.tunnelId, kind: 'interface' };
	}

	function isBroken(t: { name: string; kind: 'policy' | 'interface' }): boolean {
		if (t.kind === 'policy') {
			return !policies.some((p) => p.name === t.name);
		}
		// HR files store kernel iface names (nwg0, opkgtun10, ppp0). The tunnel
		// list exposes the same under `iface`. id/name never match a kernel name,
		// so comparing against those would flag every managed target broken.
		return !tunnels.some((tn) => tn.iface === t.name);
	}

	let targets = $derived.by<TargetEntry[]>(() => {
		const byName = new Map<string, TargetEntry>();
		for (const r of hrRules) {
			const t = targetOf(r);
			if (!t) continue;
			const existing = byName.get(t.name);
			if (existing) existing.ruleCount++;
			else byName.set(t.name, { name: t.name, kind: t.kind, ruleCount: 1, broken: isBroken(t) });
		}
		const entries = [...byName.values()];
		if (policyOrder.length === 0) return entries;
		const orderIdx = new Map(policyOrder.map((n, i) => [n, i]));
		const UNKNOWN = Number.MAX_SAFE_INTEGER;
		return entries
			.map((entry, origIdx) => ({ entry, idx: orderIdx.get(entry.name) ?? UNKNOWN, origIdx }))
			.sort((a, b) => a.idx - b.idx || a.origIdx - b.origIdx)
			.map((x) => x.entry);
	});

	let rulesOfSelected = $derived.by(() => {
		const sel = selection;
		if (!sel || sel.type !== 'target') return [];
		const name = sel.name;
		return hrRules.filter((r) => targetOf(r)?.name === name);
	});

	let selectedTargetEntry = $derived.by<TargetEntry | undefined>(() => {
		const sel = selection;
		if (!sel || sel.type !== 'target') return undefined;
		const name = sel.name;
		return targets.find((t) => t.name === name);
	});

	let geositeFiles = $derived(geoFiles.filter((g) => g.type === 'geosite').map((g) => g.path));
	let geoipFiles = $derived(geoFiles.filter((g) => g.type === 'geoip').map((g) => g.path));

	// Auto-select first target (or geodata) when none is selected
	$effect(() => {
		if (!selection) {
			if (targets.length > 0) selection = { type: 'target', name: targets[0].name };
			else selection = { type: 'service', item: 'geodata' };
		}
	});

	function openNewRule() {
		editingRule = null;
		editInitialTarget = undefined;
		editOpen = true;
	}

	function openNewRuleForSelectedTarget() {
		editingRule = null;
		if (selection?.type === 'target' && selectedTargetEntry) {
			editInitialTarget = { kind: selectedTargetEntry.kind, name: selection.name };
		} else {
			editInitialTarget = undefined;
		}
		editOpen = true;
	}

	function openEditRule(r: DnsRoute) {
		editingRule = r;
		editInitialTarget = undefined;
		editOpen = true;
	}

	async function handleSave(payload: Partial<DnsRoute>) {
		saving = true;
		try {
			if (editingRule) {
				await api.updateDnsRoute(editingRule.id, payload);
			} else {
				await api.createDnsRoute(payload);
			}
			editOpen = false;
			scheduleOversizedRefresh();
			// HR Neo save may have created a fresh NDMS policy and/or permitted
			// interfaces through the orchestrator. Those mutations don't flow
			// through AccessPolicyHandler, so no automatic SSE snapshot is
			// broadcast — the sidebar target flashes "broken" until the user
			// refreshes. Force a routing refresh to pull the new state in.
			api.refreshRouting().catch(() => {});
		} catch (e: unknown) {
			const msg = e instanceof Error ? e.message : String(e);
			notifications.error(msg);
		} finally {
			saving = false;
		}
	}

	let pendingDelete = $state<DnsRoute | null>(null);
	let deleting = $state(false);

	function handleDelete(r: DnsRoute) {
		pendingDelete = r;
	}

	async function confirmDelete() {
		if (!pendingDelete) return;
		const r = pendingDelete;
		deleting = true;
		try {
			await api.deleteDnsRoute(r.id);
			pendingDelete = null;
			scheduleOversizedRefresh();
		} catch (e: unknown) {
			notifications.error(e instanceof Error ? e.message : String(e));
		} finally {
			deleting = false;
		}
	}

	async function handleReorder(order: string[]) {
		policyOrder = order;
		try {
			await api.setPolicyOrder(order);
		} catch (e: unknown) {
			await loadPolicyOrder();
			notifications.error(e instanceof Error ? e.message : String(e));
		}
	}

	// When a policy target is selected, drill into its accesspolicy entry to
	// show the interface-permit list above the rules. Lets the user fix
	// missing/broken permits without leaving the HR tab.
	let selectedPolicy = $derived.by(() => {
		const sel = selection;
		if (!sel || sel.type !== 'target') return null;
		const entry = selectedTargetEntry;
		if (!entry || entry.kind !== 'policy') return null;
		return policies.find((p) => p.name === sel.name) ?? null;
	});

	// Name-parametric variants — used by both the desktop pane (bound to the
	// currently-selected target) and each mobile accordion entry (bound to
	// its own target).
	async function permitInterfaceFor(policyName: string, iface: string, order: number) {
		try {
			await api.permitPolicyInterface(policyName, iface, order);
		} catch (e: unknown) {
			notifications.error(e instanceof Error ? e.message : String(e));
		}
	}
	async function denyInterfaceFor(policyName: string, iface: string) {
		try {
			await api.denyPolicyInterface(policyName, iface);
		} catch (e: unknown) {
			notifications.error(e instanceof Error ? e.message : String(e));
		}
	}
	async function policyPermit(iface: string, order: number) {
		if (!selectedPolicy) return;
		await permitInterfaceFor(selectedPolicy.name, iface, order);
	}
	async function policyDeny(iface: string) {
		if (!selectedPolicy) return;
		await denyInterfaceFor(selectedPolicy.name, iface);
	}

	function policyByName(name: string) {
		return policies.find((p) => p.name === name) ?? null;
	}
</script>

<div class="hrneo-tab" class:mobile={isMobile}>
	{#if !isMobile}
		<HrNeoTargetSidebar
			{targets}
			selected={selection}
			geoSiteCount={geositeFiles.length}
			geoIPCount={geoipFiles.length}
			oversizedCount={oversizedInstalled ? oversizedTags.length : 0}
			onselect={(sel) => (selection = sel)}
			onreorder={handleReorder}
			onnewrule={openNewRule}
		/>

		<div class="pane-container">
			{#if selection?.type === 'target' && selectedTargetEntry}
				{#if selectedPolicy}
					<section class="policy-interfaces-panel">
						<header class="panel-header">
							<h3>Интерфейсы политики</h3>
							<span class="hint">Изменения сохраняются сразу через RCI</span>
						</header>
						<InterfaceList
							interfaces={selectedPolicy.interfaces ?? []}
							availableInterfaces={policyInterfaces}
							onpermit={policyPermit}
							ondeny={policyDeny}
							onreorder={policyPermit}
							onupdate={() => {}}
						/>
					</section>
				{/if}
				<HrNeoRulesList
					target={selection.name}
					targetKind={selectedTargetEntry.kind}
					rules={rulesOfSelected}
					onaddrule={openNewRuleForSelectedTarget}
					oneditrule={openEditRule}
					ondeleterule={handleDelete}
				/>
			{:else if selection?.type === 'service' && selection.item === 'geodata'}
				<HrNeoGeoDataView files={geoFiles} onrefresh={loadGeoFiles} />
			{:else if selection?.type === 'service' && selection.item === 'disabled-tags'}
				<HrNeoDisabledTagsView tags={oversizedTags} {maxelem} />
			{:else if selection?.type === 'service' && selection.item === 'settings'}
				<HrNeoSettingsView />
			{/if}
		</div>
	{:else}
		<!-- Mobile: accordion — each target expandable -->
		<div class="mobile-stack">
			{#each targets as t, i (t.name)}
				{@const pol = t.kind === 'policy' ? policyByName(t.name) : null}
				<details open={i === 0}>
					<summary>
						<span class="num">{i + 1}</span>
						<span class="tname">{t.name}</span>
						<span class="tmeta">{t.kind} · {t.ruleCount}</span>
					</summary>
					<div class="acc-body">
						{#if pol}
							<section class="policy-interfaces-panel">
								<header class="panel-header">
									<h3>Интерфейсы политики</h3>
									<span class="hint">Изменения сохраняются сразу через RCI</span>
								</header>
								<InterfaceList
									interfaces={pol.interfaces ?? []}
									availableInterfaces={policyInterfaces}
									onpermit={(iface, order) => permitInterfaceFor(t.name, iface, order)}
									ondeny={(iface) => denyInterfaceFor(t.name, iface)}
									onreorder={(iface, order) => permitInterfaceFor(t.name, iface, order)}
									onupdate={() => {}}
								/>
							</section>
						{/if}
						<HrNeoRulesList
							target={t.name}
							targetKind={t.kind}
							rules={hrRules.filter((r) => targetOf(r)?.name === t.name)}
							onaddrule={() => {
								editInitialTarget = { kind: t.kind, name: t.name };
								editingRule = null;
								editOpen = true;
							}}
							oneditrule={openEditRule}
							ondeleterule={handleDelete}
						/>
					</div>
				</details>
			{/each}

			<details>
				<summary>Гео-данные ({geoFiles.length})</summary>
				<div class="acc-body"><HrNeoGeoDataView files={geoFiles} onrefresh={loadGeoFiles} /></div>
			</details>

			<details>
				<summary>Настройки демона</summary>
				<div class="acc-body"><HrNeoSettingsView /></div>
			</details>
		</div>
	{/if}
</div>

<HrNeoEditModal
	open={editOpen}
	rule={editingRule}
	{tunnels}
	{policies}
	{policyInterfaces}
	{geositeFiles}
	{geoipFiles}
	{maxelem}
	{saving}
	initialTarget={editInitialTarget}
	onsave={handleSave}
	onclose={() => (editOpen = false)}
/>

{#if pendingDelete}
	<Modal
		open={true}
		title="Удалить HR правило"
		size="sm"
		onclose={() => (pendingDelete = null)}
	>
		<p class="confirm-text">
			Удалить правило <strong>{pendingDelete.name}</strong>?
		</p>
		<p class="confirm-hint">
			Запись пропадёт из <code>domain.conf</code> и <code>ip.list</code>.
			HR Neo будет перезапущен автоматически.
		</p>
		{#snippet actions()}
			<button
				class="btn btn-secondary"
				onclick={() => (pendingDelete = null)}
				disabled={deleting}>Отмена</button
			>
			<button class="btn btn-danger" onclick={confirmDelete} disabled={deleting}>
				{deleting ? 'Удаление…' : 'Удалить'}
			</button>
		{/snippet}
	</Modal>
{/if}

<style>
	.confirm-text {
		margin: 0 0 8px;
		color: var(--text-primary);
	}
	.confirm-hint {
		margin: 0;
		color: var(--text-muted);
		font-size: 0.8125rem;
	}
	.confirm-hint code {
		background: var(--bg-tertiary);
		padding: 0 4px;
		border-radius: 3px;
		font-family: ui-monospace, monospace;
		font-size: 0.75rem;
	}
	.hrneo-tab {
		display: grid;
		grid-template-columns: 240px 1fr;
		gap: 16px;
		align-items: start;
	}
	.hrneo-tab.mobile {
		grid-template-columns: 1fr;
		gap: 8px;
	}
	.pane-container {
		background: var(--bg-secondary);
		border: 1px solid var(--border);
		border-radius: 8px;
		padding: 16px;
		min-height: 320px;
	}
	.mobile-stack {
		display: flex;
		flex-direction: column;
		gap: 6px;
	}
	details {
		background: var(--bg-secondary);
		border: 1px solid var(--border);
		border-radius: 8px;
		overflow: hidden;
	}
	summary {
		cursor: pointer;
		padding: 10px 12px;
		display: flex;
		gap: 10px;
		align-items: center;
		font-size: 0.875rem;
		color: var(--text-primary);
		list-style: none;
	}
	summary::-webkit-details-marker {
		display: none;
	}
	.num {
		color: var(--accent);
		font-weight: 700;
		width: 18px;
		text-align: center;
	}
	.tname {
		flex: 1;
		font-weight: 600;
	}
	.tmeta {
		color: var(--text-muted);
		font-size: 0.75rem;
	}
	.acc-body {
		padding: 12px;
		border-top: 1px solid var(--border);
	}

	.policy-interfaces-panel {
		margin-bottom: 16px;
		padding: 12px 14px;
		background: var(--bg-tertiary);
		border: 1px solid var(--border);
		border-radius: 8px;
	}
	.panel-header {
		display: flex;
		align-items: baseline;
		justify-content: space-between;
		gap: 10px;
		margin-bottom: 10px;
	}
	.panel-header h3 {
		margin: 0;
		font-size: 0.875rem;
		color: var(--text-primary);
		font-weight: 600;
	}
	.panel-header .hint {
		color: var(--text-muted);
		font-size: 0.75rem;
		font-style: italic;
	}
</style>
