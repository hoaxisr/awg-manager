<script lang="ts" module>
	/** Survives tab unmount on /routing so sidebar target/service selection is restored. */
	let persistedSelection: import('./HrNeoTargetSidebar.svelte').SidebarSelection = null;
</script>

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
	import { Modal, StoreStatusBadge, Button } from '$lib/components/ui';
	import { dnsRoutesStore, invalidateAllRouting, routingTunnelsStore } from '$lib/stores/routing';
	import { InterfaceList } from '$lib/components/accesspolicy';
	import HrNeoTargetSidebar, {
		type TargetEntry,
		type SidebarSelection,
	} from './HrNeoTargetSidebar.svelte';
	import HrNeoRulesList from './HrNeoRulesList.svelte';
	import HrNeoSettingsView from './HrNeoSettingsView.svelte';
	import HrNeoDisabledTagsView from './HrNeoDisabledTagsView.svelte';
	import HrNeoEditModal from './HrNeoEditModal.svelte';
	import { IconPickerModal, ServiceCatalogModal } from '$lib/components/dnsroutes';
	import type { CatalogPreset } from '$lib/types';
	import { hrNeoCatalogPresetFilter } from '$lib/utils/catalog-preset';

	interface Props {
		dnsRoutes: DnsRoute[];
		tunnels: RoutingTunnel[];
		policies: AccessPolicy[];
		policyInterfaces: PolicyGlobalInterface[];
		editRuleId?: string;
		editRuleCounter?: number;
	}

	let {
		dnsRoutes,
		tunnels,
		policies,
		policyInterfaces,
		editRuleId = '',
		editRuleCounter = 0,
	}: Props = $props();

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
	// svelte-ignore state_referenced_locally
	let selection = $state<SidebarSelection>(persistedSelection);

	$effect(() => {
		persistedSelection = selection;
	});

	let hrNeoDataReady = $derived(
		($dnsRoutesStore.data !== null || $dnsRoutesStore.lastFetchedAt > 0 || $dnsRoutesStore.status === 'error') &&
			($routingTunnelsStore.data !== null || $routingTunnelsStore.lastFetchedAt > 0 || $routingTunnelsStore.status === 'error'),
	);

	let editOpen = $state(false);
	let editingRule = $state<DnsRoute | null>(null);
	let editInitialTarget = $state<{ kind: 'interface' | 'policy'; name: string } | undefined>(
		undefined,
	);
	let editInitialPreset = $state<CatalogPreset | null>(null);
	let catalogOpen = $state(false);
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
			geoFiles = (await api.getGeoFiles()) ?? [];
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

	/** Debounce HR Neo restarts when toggling several rules quickly. */
	const HR_TOGGLE_DEBOUNCE_MS = 600;
	let optimisticEnabled = $state<Record<string, boolean>>({});
	let pendingToggleTimers = new Map<string, ReturnType<typeof setTimeout>>();
	/** Per-rule generation — stale API responses are ignored after a newer toggle. */
	let toggleGeneration = new Map<string, number>();
	let hrToggleLoadingId = $state<string | null>(null);

	function ruleForCard(rule: DnsRoute): DnsRoute {
		if (rule.id in optimisticEnabled) {
			return { ...rule, enabled: optimisticEnabled[rule.id] };
		}
		return rule;
	}

	function scheduleHrRuleToggle(rule: DnsRoute, enabled: boolean) {
		optimisticEnabled = { ...optimisticEnabled, [rule.id]: enabled };
		const gen = (toggleGeneration.get(rule.id) ?? 0) + 1;
		toggleGeneration.set(rule.id, gen);
		const prev = pendingToggleTimers.get(rule.id);
		if (prev) clearTimeout(prev);
		pendingToggleTimers.set(
			rule.id,
			setTimeout(() => {
				pendingToggleTimers.delete(rule.id);
				void flushHrRuleToggle(rule.id, gen);
			}, HR_TOGGLE_DEBOUNCE_MS),
		);
	}

	async function flushHrRuleToggle(id: string, gen: number) {
		if (toggleGeneration.get(id) !== gen) return;
		const enabled = optimisticEnabled[id];
		if (enabled === undefined) return;

		hrToggleLoadingId = id;
		try {
			const fresh = await api.setDnsRouteEnabled(id, enabled);
			if (toggleGeneration.get(id) !== gen) return;
			dnsRoutesStore.applyMutationResponse(fresh);
			const next = { ...optimisticEnabled };
			delete next[id];
			optimisticEnabled = next;
		} catch (e: unknown) {
			if (toggleGeneration.get(id) !== gen) return;
			const next = { ...optimisticEnabled };
			delete next[id];
			optimisticEnabled = next;
			notifications.error(e instanceof Error ? e.message : String(e));
		} finally {
			if (toggleGeneration.get(id) === gen) {
				hrToggleLoadingId = null;
			}
		}
	}

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
		for (const t of pendingToggleTimers.values()) clearTimeout(t);
		pendingToggleTimers.clear();
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
		const tunnelNameByIface = new Map(
			tunnels
				.filter((tn) => !!tn.iface)
				.map((tn) => [tn.iface as string, tn.name]),
		);
		const byName = new Map<string, TargetEntry>();
		for (const r of hrRules) {
			const t = targetOf(r);
			if (!t) continue;
			const tunnelName = t.kind === 'interface' ? tunnelNameByIface.get(t.name) : undefined;
			const existing = byName.get(t.name);
			if (existing) existing.ruleCount++;
			else
				byName.set(t.name, {
					name: t.name,
					kind: t.kind,
					ruleCount: 1,
					displayName: tunnelName,
					broken: isBroken(t),
				});
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
	// Auto-select first target (policy or interface) or settings once routing
	// data is known. Without the ready gate, a cold ?tab=hrneo load sees
	// targets=[] briefly and sticks on settings even after rules arrive.
	$effect(() => {
		if (selection) return;
		if (!hrNeoDataReady) return;
		if (targets.length > 0) selection = { type: 'target', name: targets[0].name };
		else selection = { type: 'service', item: 'settings' };
	});

	// Drop a stale persisted target (policy or interface) if it vanished.
	$effect(() => {
		const sel = selection;
		if (!sel || sel.type !== 'target') return;
		if (!hrNeoDataReady) return;
		if (targets.some((t) => t.name === sel.name)) return;
		selection = null;
	});

	function openNewRule() {
		editingRule = null;
		editInitialTarget = undefined;
		editInitialPreset = null;
		editOpen = true;
	}

	function openNewRuleForSelectedTarget(preset: CatalogPreset | null = null) {
		editingRule = null;
		if (selection?.type === 'target' && selectedTargetEntry) {
			editInitialTarget = { kind: selectedTargetEntry.kind, name: selection.name };
		} else {
			editInitialTarget = undefined;
		}
		editInitialPreset = preset;
		editOpen = true;
	}

	function openCatalogForTarget() {
		catalogOpen = true;
	}

	function handleCatalogConfirm(presets: CatalogPreset[]) {
		catalogOpen = false;
		const preset = presets[0];
		if (!preset) return;
		openNewRuleForSelectedTarget(preset);
	}

	function openCatalogFromEdit() {
		catalogOpen = true;
	}

	function handleCatalogPickFromEdit(presets: CatalogPreset[]) {
		catalogOpen = false;
		if (presets.length === 0) return;
		editInitialPreset = presets[0];
	}

	function openEditRule(r: DnsRoute) {
		editingRule = r;
		editInitialTarget = undefined;
		editOpen = true;
	}

	// Open edit modal when a search result is clicked on the routing page.
	// Capture counter at mount to skip the initial value on tab re-mount.
	// svelte-ignore state_referenced_locally
	const initialEditCounter = editRuleCounter;
	$effect(() => {
		if (editRuleCounter > initialEditCounter && editRuleId) {
			const rule = hrRules.find((r) => r.id === editRuleId);
			if (rule) {
				// Auto-select the rule's target so when the modal closes the
				// user lands on the right pane instead of the first target.
				const t = targetOf(rule);
				if (t) selection = { type: 'target', name: t.name };
				openEditRule(rule);
			}
		}
	});

	async function handleSave(payload: Partial<DnsRoute>) {
		saving = true;
		try {
			if (editingRule) {
				await api.updateDnsRoute(editingRule.id, payload);
			} else {
				await api.createDnsRoute(payload);
			}
			scheduleOversizedRefresh();
			api.refreshRouting().catch(() => {});

			editOpen = false;
			editInitialPreset = null;
		} catch (e: unknown) {
			const msg = e instanceof Error ? e.message : String(e);
			notifications.error(msg);
		} finally {
			saving = false;
		}
	}

	let pendingDelete = $state<DnsRoute | null>(null);
	let iconPickerOpen = $state(false);
	let pickingForRule = $state<DnsRoute | null>(null);
	let deleting = $state(false);

	function handleDelete(r: DnsRoute) {
		pendingDelete = r;
	}

	function openIconPicker(rule: DnsRoute) {
		pickingForRule = rule;
		iconPickerOpen = true;
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

	function handleInterfaceListUpdate() {
		invalidateAllRouting();
	}
</script>

<div class="hrneo-status-row">
	<StoreStatusBadge store={dnsRoutesStore} />
</div>

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
							addPickerVariant="panel"
							onpermit={policyPermit}
							ondeny={policyDeny}
							onreorder={policyPermit}
							onupdate={handleInterfaceListUpdate}
						/>
					</section>
				{/if}
				<HrNeoRulesList
					target={selection.name}
					targetKind={selectedTargetEntry.kind}
					rules={rulesOfSelected}
					displayRule={ruleForCard}
					toggleLoadingId={hrToggleLoadingId}
					ontogglerule={scheduleHrRuleToggle}
					oncatalog={openCatalogForTarget}
					onmanual={() => openNewRuleForSelectedTarget(null)}
					oneditrule={openEditRule}
					ondeleterule={handleDelete}
					oniconrule={openIconPicker}
				/>
			{:else if selection?.type === 'service' && selection.item === 'disabled-tags'}
				<HrNeoDisabledTagsView tags={oversizedTags} {maxelem} />
			{:else if selection?.type === 'service' && selection.item === 'settings'}
				<HrNeoSettingsView />
			{/if}
		</div>
	{:else}
		<!-- Mobile: accordion — each target expandable -->
		<div class="mobile-stack">
			<button type="button" class="mobile-add-rule" onclick={openNewRule}>+ Новое правило</button>
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
									addPickerVariant="panel"
									onpermit={(iface, order) => permitInterfaceFor(t.name, iface, order)}
									ondeny={(iface) => denyInterfaceFor(t.name, iface)}
									onreorder={(iface, order) => permitInterfaceFor(t.name, iface, order)}
									onupdate={handleInterfaceListUpdate}
								/>
							</section>
						{/if}
						<HrNeoRulesList
							target={t.name}
							targetKind={t.kind}
							rules={hrRules.filter((r) => targetOf(r)?.name === t.name)}
							displayRule={ruleForCard}
							toggleLoadingId={hrToggleLoadingId}
							ontogglerule={scheduleHrRuleToggle}
							oncatalog={() => {
								selection = { type: 'target', name: t.name };
								openCatalogForTarget();
							}}
							onmanual={() => {
								selection = { type: 'target', name: t.name };
								openNewRuleForSelectedTarget(null);
							}}
							oneditrule={openEditRule}
							ondeleterule={handleDelete}
							oniconrule={openIconPicker}
						/>
					</div>
				</details>
			{/each}

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
	initialPreset={editInitialPreset}
	onpickcatalog={() => {
		if (editOpen) openCatalogFromEdit();
		else openCatalogForTarget();
	}}
	onsave={handleSave}
	onclose={() => {
		editOpen = false;
		editInitialPreset = null;
	}}
/>

<ServiceCatalogModal
	bind:open={catalogOpen}
	title="Каталог сервисов"
	presetFilter={hrNeoCatalogPresetFilter}
	footer="none"
	multiple={false}
	confirmLabel="Выбрать"
	onclose={() => (catalogOpen = false)}
	onconfirm={(presets) => {
		if (editOpen) {
			handleCatalogPickFromEdit(presets);
		} else {
			handleCatalogConfirm(presets);
		}
	}}
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
			<Button variant="secondary" onclick={() => (pendingDelete = null)} disabled={deleting}>
				Отмена
			</Button>
			<Button variant="danger" onclick={confirmDelete} loading={deleting}>
				Удалить
			</Button>
		{/snippet}
	</Modal>
{/if}

{#if pickingForRule}
	<IconPickerModal
		open={iconPickerOpen}
		iconUrl={pickingForRule.iconUrl}
		ruleName={pickingForRule.name}
		onclose={() => {
			iconPickerOpen = false;
			pickingForRule = null;
		}}
		onapply={async (newUrl) => {
			if (!pickingForRule) return;
			const rule = pickingForRule;
			iconPickerOpen = false;
			pickingForRule = null;
			try {
				await api.updateDnsRoute(rule.id, { ...rule, iconUrl: newUrl ?? undefined });
				invalidateAllRouting();
				notifications.success(newUrl ? 'Иконка изменена' : 'Иконка сброшена');
			} catch (e: unknown) {
				notifications.error(e instanceof Error ? e.message : 'Не удалось обновить иконку');
			}
		}}
	/>
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
	.hrneo-status-row {
		display: flex;
		justify-content: flex-end;
		margin-bottom: 8px;
	}
	.hrneo-status-row:empty {
		display: none;
	}
	.hrneo-tab {
		display: grid;
		grid-template-columns: 240px 1fr;
		gap: 16px;
		align-items: start;
	}
	.hrneo-tab.mobile {
		grid-template-columns: minmax(0, 1fr);
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
	.mobile-add-rule {
		padding: 10px;
		background: transparent;
		border: 1px dashed var(--border-hover);
		border-radius: 6px;
		color: var(--accent);
		font-family: inherit;
		font-size: 0.875rem;
		cursor: pointer;
	}
	.mobile-add-rule:hover {
		border-color: var(--accent);
		background: var(--bg-tertiary);
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
		min-width: 0;
		font-weight: 600;
		white-space: nowrap;
		overflow: hidden;
		text-overflow: ellipsis;
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
		/* Issue #214 Sc1: на 357px viewport h3 + hint не помещались, и
		 * hint "Изменения сохраняются сразу через RCI" обрезался эллипсисом.
		 * flex-wrap позволяет hint'у перенестись на следующую строку,
		 * а min-width: 0 на детях разрешает переносы внутри hint'а. */
		flex-wrap: wrap;
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
		min-width: 0;
	}
</style>
