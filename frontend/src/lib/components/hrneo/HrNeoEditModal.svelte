<script lang="ts">
	import type {
		AccessPolicyInterface,
		DnsRoute,
		PolicyGlobalInterface,
		RoutingTunnel,
	} from '$lib/types';
	import { SERVICE_PRESETS, type ServicePreset } from '$lib/data/presets';
	import { Modal, Button, IconButton, Dropdown, type DropdownOption } from '$lib/components/ui';
	import { ServiceIcon } from '$lib/components/dnsroutes';
	import { InterfaceList } from '$lib/components/accesspolicy';
	import HrNeoGeoTagPicker from './HrNeoGeoTagPicker.svelte';

	interface AccessPolicy {
		name: string;
		description?: string;
		interfaces?: AccessPolicyInterface[];
	}

	interface Props {
		open: boolean;
		rule: DnsRoute | null;
		tunnels: RoutingTunnel[];
		policies: AccessPolicy[];
		policyInterfaces: PolicyGlobalInterface[];
		geositeFiles: string[];
		geoipFiles: string[];
		maxelem: number;
		saving: boolean;
		initialTarget?: { kind: 'interface' | 'policy'; name: string };
		onsave: (payload: Partial<DnsRoute>) => void;
		onclose: () => void;
	}

	let {
		open,
		rule,
		tunnels,
		policies,
		policyInterfaces,
		geositeFiles,
		geoipFiles,
		maxelem,
		saving,
		initialTarget,
		onsave,
		onclose,
	}: Props = $props();

	// Only presets with inline domains can be used — HR has no subscriptions.
	let usablePresets = $derived(SERVICE_PRESETS.filter((p) => (p.domains?.length ?? 0) > 0));

	let name = $state('');
	let domainsText = $state('');
	let cidrText = $state('');
	let mode = $state<'interface' | 'policy'>('interface');
	let tunnelId = $state('');
	let policyChoice = $state<'existing' | 'new'>('existing');
	let existingPolicyName = $state('');
	let newPolicyName = $state('');
	let newPolicyIfaces = $state<AccessPolicyInterface[]>([]);

	let presetPickerOpen = $state(false);
	let selectedPreset = $state<ServicePreset | null>(null);
	let geositePickerOpen = $state(false);
	let geoipPickerOpen = $state(false);

	let attempted = $state(false);
	let wasOpen = $state(false);

	let isNew = $derived(rule === null);
	let title = $derived(isNew ? 'Новое HR правило' : `Редактирование: ${rule?.name ?? ''}`);

	$effect(() => {
		if (!open) {
			wasOpen = false;
			return;
		}
		if (wasOpen) return; // already initialised — user may be editing
		wasOpen = true;
		attempted = false;
		selectedPreset = null;
		geositePickerOpen = false;
		geoipPickerOpen = false;
		presetPickerOpen = false;
		if (rule) {
			name = rule.name;
			const allDomains = (rule.domains ?? []).filter((d) => !d.startsWith('geoip:'));
			const allSubnets = rule.subnets ?? [];
			domainsText = allDomains.join('\n');
			cidrText = allSubnets.join('\n');
			mode = rule.hrRouteMode === 'policy' ? 'policy' : 'interface';
			if (mode === 'policy') {
				policyChoice = 'existing';
				existingPolicyName = rule.hrPolicyName ?? '';
				newPolicyName = '';
				newPolicyIfaces = [];
			} else {
				// HR interface-mode rules come back from the backend with
				// route.interface = route.tunnelId = kernel iface name
				// ("nwg0"), not our internal tunnel id ("awg10"). Resolve
				// the select's value by matching any of those fields against
				// every tunnel property so the dropdown shows the right
				// option instead of blanking out.
				const route = rule.routes?.[0];
				const match = tunnels.find(
					(x) =>
						x.id === route?.tunnelId ||
						x.iface === route?.tunnelId ||
						x.iface === route?.interface,
				);
				tunnelId = match?.id ?? tunnels[0]?.id ?? '';
			}
		} else {
			name = '';
			domainsText = '';
			cidrText = '';
			if (initialTarget?.kind === 'policy') {
				mode = 'policy';
				policyChoice = 'existing';
				existingPolicyName = initialTarget.name;
			} else if (initialTarget?.kind === 'interface') {
				mode = 'interface';
				const t = tunnels.find(
					(x) => x.id === initialTarget.name || x.name === initialTarget.name || x.iface === initialTarget.name,
				);
				tunnelId = t?.id ?? tunnels[0]?.id ?? '';
			} else {
				mode = 'interface';
				tunnelId = tunnels[0]?.id ?? '';
			}
			if (!existingPolicyName) existingPolicyName = policies[0]?.name ?? '';
			newPolicyName = '';
			newPolicyIfaces = [];
		}
	});

	function applyPreset(p: ServicePreset) {
		selectedPreset = p;
		const entries = p.domains ?? [];
		const domainLines: string[] = [];
		const cidrLines: string[] = [];
		for (const e of entries) {
			if (e.startsWith('geoip:') || /^[\d.:a-fA-F]+\/\d+$/.test(e)) cidrLines.push(e);
			else domainLines.push(e);
		}
		domainsText = domainLines.join('\n');
		cidrText = cidrLines.join('\n');
		if (!name.trim()) name = p.name;
		presetPickerOpen = false;
	}

	function clearPreset() {
		selectedPreset = null;
	}

	function appendLine(which: 'domains' | 'cidr', token: string) {
		if (which === 'domains') {
			domainsText = domainsText ? `${domainsText}\n${token}` : token;
			geositePickerOpen = false;
		} else {
			cidrText = cidrText ? `${cidrText}\n${token}` : token;
			geoipPickerOpen = false;
		}
	}

	function splitLines(s: string): string[] {
		return s
			.split(/\r?\n/)
			.map((x) => x.trim())
			.filter((x) => x.length > 0);
	}

	let activeNewPolicyIfaces = $derived(newPolicyIfaces.filter((i) => !i.denied));

	// HR Neo policy naming rules (mirrors backend validateHRPolicyName):
	//  - Latin letters only (a-zA-Z), no digits / punctuation / whitespace / non-ASCII
	//  - Length 1..32
	//  - Must not match ^Policy\d+$ (reserved for system-created policies that
	//    HR Neo cannot route into)
	const HR_POLICY_NAME_RE = /^[a-zA-Z]+$/;
	const HR_POLICY_NAME_MAX = 32;
	const SYSTEM_POLICY_RE = /^Policy\d+$/;

	function hrPolicyNameError(raw: string): string {
		const v = raw.trim();
		if (v === '') return 'Введите имя политики';
		if (v.length > HR_POLICY_NAME_MAX) return `Максимум ${HR_POLICY_NAME_MAX} символов`;
		if (SYSTEM_POLICY_RE.test(v))
			return `Имя ${v} зарезервировано для системных политик Keenetic — HR Neo не может в них маршрутизировать`;
		if (!HR_POLICY_NAME_RE.test(v))
			return 'Только латинские буквы (a-z, A-Z), без цифр, пробелов и спецсимволов';
		return '';
	}

	// Filter existing policies dropdown: hide system-created ones (PolicyN).
	let hrCompatiblePolicies = $derived(policies.filter((p) => !SYSTEM_POLICY_RE.test(p.name)));

	// Interfaces available for routing — used to detect name collisions.
	let ifaceNameSet = $derived(new Set(policyInterfaces.map((i) => i.name)));

	// Warning when the chosen new-policy name matches an existing interface.
	// HR Neo will route directly to the interface instead of creating a policy.
	let newPolicyNameInterfaceHint = $derived(
		newPolicyName.trim() !== '' && ifaceNameSet.has(newPolicyName.trim())
			? `Имя совпадает с интерфейсом "${newPolicyName.trim()}". Возможно, вы хотели выбрать Target = Интерфейс.`
			: '',
	);

	// Warning when the new-policy name duplicates an existing HR-compatible policy.
	let newPolicyNameDuplicateHint = $derived(
		newPolicyName.trim() !== '' &&
			hrCompatiblePolicies.some((p) => p.name === newPolicyName.trim())
			? `Политика с именем "${newPolicyName.trim()}" уже существует. Выберите "Существующая" выше.`
			: '',
	);

	let newPolicyNameValidationError = $derived(hrPolicyNameError(newPolicyName));

	let canSave = $derived.by(() => {
		if (!name.trim()) return false;
		const d = splitLines(domainsText);
		const c = splitLines(cidrText);
		if (d.length === 0 && c.length === 0) return false;
		if (mode === 'interface') return !!tunnelId;
		if (policyChoice === 'existing') return !!existingPolicyName;
		// New policy: name must pass validation, must not duplicate an existing
		// HR policy, and at least one interface must be permitted.
		if (newPolicyNameValidationError !== '') return false;
		if (newPolicyNameDuplicateHint !== '') return false;
		return activeNewPolicyIfaces.length > 0;
	});

	// Local InterfaceList callbacks for the new-policy flow — accumulate
	// changes; the actual `ip policy permit` calls happen at Save time.
	function newPermit(iface: string, order: number) {
		const without = newPolicyIfaces.filter((i) => i.name !== iface);
		const next = [...without];
		next.splice(order, 0, { name: iface, order, denied: false });
		newPolicyIfaces = next.map((i, idx) => ({ ...i, order: idx }));
	}
	function newDeny(iface: string) {
		newPolicyIfaces = newPolicyIfaces.map((i) =>
			i.name === iface ? { ...i, denied: true } : i,
		);
	}
	function newReorder(iface: string, newOrder: number) {
		const idx = newPolicyIfaces.findIndex((i) => i.name === iface);
		if (idx < 0) return;
		const without = newPolicyIfaces.filter((_, i) => i !== idx);
		const insertAt = Math.max(0, Math.min(without.length, newOrder));
		without.splice(insertAt, 0, newPolicyIfaces[idx]);
		newPolicyIfaces = without.map((i, k) => ({ ...i, order: k }));
	}


	function handleSave() {
		attempted = true;
		if (!canSave) return;
		const manualDomains = [...splitLines(domainsText), ...splitLines(cidrText)];
		const payload: Partial<DnsRoute> = {
			name: name.trim(),
			backend: 'hydraroute',
			manualDomains,
		};
		if (mode === 'interface') {
			payload.hrRouteMode = 'interface';
			payload.routes = [{ tunnelId, interface: '', fallback: '' }];
		} else if (policyChoice === 'existing') {
			payload.hrRouteMode = 'policy';
			payload.hrPolicyName = existingPolicyName;
		} else {
			payload.hrRouteMode = 'policy';
			payload.hrPolicyName = newPolicyName.trim();
			payload.hrPolicyInterfaces = activeNewPolicyIfaces.map((i) => i.name);
		}
		onsave(payload);
	}
</script>

<Modal {open} {title} size="lg" {onclose}>
	<!-- Preset bar -->
	<div class="preset-bar">
		<div class="preset-bar-left">
			{#if selectedPreset}
				<ServiceIcon name={selectedPreset.name} size={24} />
				<div class="preset-bar-info">
					<div class="preset-bar-name">{selectedPreset.name}</div>
					<div class="preset-bar-meta">{selectedPreset.domains?.length ?? 0} записей</div>
				</div>
				<IconButton ariaLabel="Очистить пресет" onclick={clearPreset}>×</IconButton>
			{:else}
				<span class="preset-bar-label">Пресет не выбран</span>
			{/if}
		</div>
		<Button variant="secondary" size="sm" onclick={() => (presetPickerOpen = !presetPickerOpen)}>
			{presetPickerOpen ? 'Скрыть каталог' : 'Выбрать из каталога'}
		</Button>
	</div>

	{#if presetPickerOpen}
		<div class="preset-catalog">
			{#each usablePresets as p (p.id)}
				<button type="button" class="preset-card" onclick={() => applyPreset(p)}>
					<ServiceIcon name={p.name} size={36} />
					<div class="preset-card-body">
						<div class="preset-card-name">{p.name}</div>
						<div class="preset-card-meta">{p.domains?.length ?? 0} записей</div>
					</div>
				</button>
			{/each}
		</div>
	{/if}

	<!-- Name -->
	<div class="form-group" class:field-error={attempted && !name.trim()}>
		<label class="field-label" for="hr-rule-name">Название</label>
		<input
			id="hr-rule-name"
			class="field-input"
			type="text"
			placeholder="Youtube"
			bind:value={name}
		/>
		{#if attempted && !name.trim()}<div class="error-text">Введите название</div>{/if}
	</div>

	<!-- Domains -->
	<section class="form-section">
		<header class="section-row">
			<div class="section-row-label">
				<span class="section-row-title">Домены</span>
				<span class="section-row-count">{splitLines(domainsText).length}</span>
			</div>
			<div class="section-row-tools">
				<span class="badge-mono">domain.conf</span>
				<Button
					variant="ghost"
					size="sm"
					onclick={() => (geositePickerOpen = !geositePickerOpen)}
				>
					+ geosite:TAG
				</Button>
			</div>
		</header>

		{#if geositePickerOpen}
			<HrNeoGeoTagPicker
				kind="geosite"
				files={geositeFiles}
				onpick={(t) => appendLine('domains', t)}
				onclose={() => (geositePickerOpen = false)}
			/>
		{/if}

		<textarea class="field-textarea" rows="8" bind:value={domainsText}
			placeholder="youtube.com&#10;.googlevideo.com&#10;geosite:GOOGLE"
		></textarea>
		<div class="form-hint">
			Домены · .суффикс · geosite:TAG — строкой на запись. Записываются через запятую в одну строку.
		</div>
	</section>

	<!-- CIDR -->
	<section class="form-section">
		<header class="section-row">
			<div class="section-row-label">
				<span class="section-row-title">CIDR</span>
				<span class="section-row-count">{splitLines(cidrText).length}</span>
			</div>
			<div class="section-row-tools">
				<span class="badge-mono">ip.list</span>
				<Button
					variant="ghost"
					size="sm"
					onclick={() => (geoipPickerOpen = !geoipPickerOpen)}
				>
					+ geoip:TAG
				</Button>
			</div>
		</header>

		{#if geoipPickerOpen}
			<HrNeoGeoTagPicker
				kind="geoip"
				files={geoipFiles}
				{maxelem}
				onpick={(t) => appendLine('cidr', t)}
				onclose={() => (geoipPickerOpen = false)}
			/>
		{/if}

		<textarea class="field-textarea" rows="5" bind:value={cidrText}
			placeholder="10.0.0.0/8&#10;2001:db8::/32&#10;geoip:RU"
		></textarea>
		<div class="form-hint">CIDR · geoip:TAG — строкой на запись. Блок в ip.list.</div>
	</section>

	<!-- Target -->
	<section class="form-section">
		<div class="field-label">Target</div>
		<div class="seg-tabs">
			<button
				type="button"
				class="seg-tab"
				class:active={mode === 'interface'}
				onclick={() => (mode = 'interface')}>Интерфейс</button
			>
			<button
				type="button"
				class="seg-tab"
				class:active={mode === 'policy'}
				onclick={() => (mode = 'policy')}>Политика</button
			>
		</div>

		{#if mode === 'interface'}
			{@const tunnelOpts: DropdownOption[] = tunnels.map((t) => ({
				value: t.id,
				label: t.name + (t.iface ? ` · ${t.iface}` : ''),
			}))}
			<Dropdown bind:value={tunnelId} options={tunnelOpts} fullWidth />
		{:else}
			<div class="radio-block">
				<label class="radio-option" class:active={policyChoice === 'existing'}>
					<input type="radio" bind:group={policyChoice} value="existing" />
					<span>Существующая</span>
				</label>
				<label class="radio-option" class:active={policyChoice === 'new'}>
					<input type="radio" bind:group={policyChoice} value="new" />
					<span>Новая</span>
				</label>
			</div>

			{#if policyChoice === 'existing'}
				{#if hrCompatiblePolicies.length === 0}
					<div class="form-hint muted">
						Нет HR-совместимых политик. Создайте новую.
						{#if policies.length > hrCompatiblePolicies.length}
							Системные политики Keenetic (<code>PolicyN</code>) не отображаются —
							HR Neo не может маршрутизировать в них.
						{/if}
					</div>
				{:else}
					{@const policyOpts: DropdownOption[] = hrCompatiblePolicies.map((p) => ({
						value: p.name,
						label: p.name + (p.description ? ` (${p.description})` : ''),
					}))}
					<Dropdown bind:value={existingPolicyName} options={policyOpts} fullWidth />
					<div class="form-hint">
						Интерфейсы политики редактируются на её карточке в сайдбаре.
					</div>
				{/if}
			{:else}
				<div class="policy-card">
					<div class="policy-card-header">Новая политика Keenetic</div>
					<div class="form-group" class:field-error={attempted && newPolicyNameValidationError !== ''}>
						<label class="field-label" for="hr-new-policy-name">Имя политики</label>
						<input
							id="hr-new-policy-name"
							class="field-input"
							type="text"
							placeholder="Streaming"
							maxlength={HR_POLICY_NAME_MAX}
							bind:value={newPolicyName}
						/>
						{#if attempted && newPolicyNameValidationError !== ''}
							<div class="error-text">{newPolicyNameValidationError}</div>
						{:else if newPolicyNameDuplicateHint !== ''}
							<div class="error-text">{newPolicyNameDuplicateHint}</div>
						{:else if newPolicyNameInterfaceHint !== ''}
							<div class="warn-text">{newPolicyNameInterfaceHint}</div>
						{:else}
							<div class="form-hint">
								Только латинские буквы (a–Z), до {HR_POLICY_NAME_MAX} символов.
							</div>
						{/if}
					</div>
					<div class="form-group">
						<InterfaceList
							interfaces={newPolicyIfaces}
							availableInterfaces={policyInterfaces}
							onpermit={newPermit}
							ondeny={newDeny}
							onreorder={newReorder}
							onupdate={() => {}}
						/>
					</div>
					<div class="form-hint">
						Политика и привязки создаются после нажатия «Сохранить».
					</div>
				</div>
			{/if}
		{/if}
	</section>

	{#snippet actions()}
		<Button variant="secondary" onclick={onclose}>Отмена</Button>
		<Button variant="primary" onclick={handleSave} disabled={!canSave} loading={saving}>
			Сохранить
		</Button>
	{/snippet}
</Modal>

<style>
	.preset-bar {
		display: flex;
		align-items: center;
		justify-content: space-between;
		gap: 12px;
		padding: 10px 12px;
		background: var(--color-bg-secondary);
		border: 1px solid var(--color-border);
		border-radius: 8px;
		margin-bottom: 14px;
	}
	.preset-bar-left {
		display: flex;
		align-items: center;
		gap: 10px;
		min-width: 0;
	}
	.preset-bar-info {
		display: flex;
		flex-direction: column;
		min-width: 0;
	}
	.preset-bar-name {
		font-weight: 600;
		color: var(--color-text-primary);
	}
	.preset-bar-meta {
		color: var(--color-text-muted);
		font-size: 0.75rem;
	}
	.preset-bar-label {
		color: var(--color-text-muted);
		font-size: 0.875rem;
	}

	.preset-catalog {
		display: grid;
		grid-template-columns: repeat(2, 1fr);
		gap: 6px;
		padding: 10px;
		background: var(--color-bg-secondary);
		border: 1px solid var(--color-border);
		border-radius: 8px;
		margin-bottom: 14px;
		max-height: 260px;
		overflow-y: auto;
	}
	.preset-card {
		display: flex;
		align-items: center;
		gap: 8px;
		padding: 8px;
		background: var(--color-bg-tertiary);
		border: 1px solid var(--color-border);
		border-radius: 6px;
		cursor: pointer;
		text-align: left;
		font-family: inherit;
		color: var(--color-text-primary);
		transition: border-color 0.15s;
	}
	.preset-card:hover {
		border-color: var(--color-accent);
	}
	.preset-card-body {
		display: flex;
		flex-direction: column;
		min-width: 0;
	}
	.preset-card-name {
		font-weight: 600;
	}
	.preset-card-meta {
		color: var(--color-text-muted);
		font-size: 0.75rem;
	}

	.form-section {
		margin-bottom: 14px;
	}

	.section-row {
		display: flex;
		justify-content: space-between;
		align-items: center;
		margin-bottom: 6px;
	}
	.section-row-label {
		display: flex;
		align-items: center;
		gap: 6px;
	}
	.section-row-title {
		font-size: 0.8125rem;
		font-weight: 600;
		color: var(--color-text-primary);
	}
	.section-row-count {
		color: var(--color-accent);
		font-weight: 600;
	}
	.section-row-tools {
		display: flex;
		align-items: center;
		gap: 8px;
	}

	.badge-mono {
		background: var(--color-bg-tertiary);
		color: var(--color-text-muted);
		font-size: 0.6875rem;
		padding: 2px 6px;
		border-radius: 10px;
		font-family: ui-monospace, monospace;
	}

	.seg-tabs {
		display: flex;
		gap: 2px;
		background: var(--color-bg-tertiary);
		padding: 3px;
		border-radius: 6px;
		margin-bottom: 8px;
	}
	.seg-tab {
		flex: 1;
		padding: 6px 12px;
		text-align: center;
		border-radius: 4px;
		border: none;
		background: transparent;
		color: var(--color-text-muted);
		cursor: pointer;
		font-family: inherit;
		font-size: 0.8125rem;
	}
	.seg-tab.active {
		background: var(--color-bg-hover);
		color: var(--color-text-primary);
	}

	.radio-block {
		display: flex;
		flex-wrap: wrap;
		gap: 8px;
		margin-bottom: 10px;
	}
	.radio-option {
		display: inline-flex;
		align-items: center;
		gap: 8px;
		padding: 8px 12px;
		background: var(--color-bg-tertiary);
		border: 1px solid var(--color-border);
		border-radius: 6px;
		cursor: pointer;
		white-space: nowrap;
	}
	.radio-option.active {
		border-color: var(--color-accent);
		background: var(--color-bg-hover);
	}
	.radio-option input[type='radio'] {
		accent-color: var(--color-accent);
	}
	.radio-option span {
		color: var(--color-text-primary);
		font-size: 0.875rem;
	}

	.policy-card {
		border: 1px solid var(--color-accent);
		border-radius: 8px;
		padding: 12px;
		background: linear-gradient(180deg, rgba(122, 162, 247, 0.08) 0%, transparent 100%);
	}
	.policy-card-header {
		color: var(--color-accent);
		font-size: 0.6875rem;
		font-weight: 700;
		text-transform: uppercase;
		letter-spacing: 0.06em;
		margin-bottom: 10px;
		padding-bottom: 6px;
		border-bottom: 1px solid var(--color-accent);
	}

	.form-hint.muted {
		color: var(--color-text-muted);
	}
	.error-text {
		color: var(--color-error);
		font-size: 0.75rem;
		margin-top: 4px;
	}
	.warn-text {
		color: var(--warning, #f59e0b);
		font-size: 0.75rem;
		margin-top: 4px;
	}
	.field-error :global(.field-input) {
		border-color: var(--color-error);
	}

	@media (max-width: 640px) {
		.preset-catalog {
			grid-template-columns: 1fr;
		}
	}
</style>
