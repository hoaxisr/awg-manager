<script lang="ts">
	import type { DnsRoute, DnsRouteTarget, DnsRouteSubscription, RoutingTunnel } from '$lib/types';
	import { Modal, Button, Dropdown } from '$lib/components/ui';
	import { formatRelativeTime } from '$lib/utils/format';
	import DnsRouteDomainEditor from './DnsRouteDomainEditor.svelte';
	import ServiceIcon from './ServiceIcon.svelte';
	import IconPickerModal from './IconPickerModal.svelte';
	import { formatIconUrlHint } from '$lib/utils/custom-icon';
	import {
		buildRoutingTunnelDropdownOptions,
		findRoutingTunnelLabel,
	} from '$lib/utils/routingTunnelOptions';
	import DownloadRouteNote from '$lib/components/downloads/DownloadRouteNote.svelte';
	import CreateIcon from '$lib/components/ui/icons/CreateIcon.svelte';

	interface Props {
		open: boolean;
		route: DnsRoute | null;
		tunnels: RoutingTunnel[];
		saving: boolean;
		onsave: (data: Partial<DnsRoute>) => void;
		onclose: () => void;
		isOS5?: boolean;
		hydrarouteInstalled?: boolean;
	}

	let { open, route, tunnels: rawTunnels, saving, onsave, onclose, isOS5 = false, hydrarouteInstalled = false }: Props = $props();
	let tunnels = $derived((rawTunnels ?? []).filter(t => t.available || t.type === 'wan'));

	// Form state
	let name = $state('');
	let iconUrl = $state<string | undefined>(undefined);
	let iconPickerOpen = $state(false);
	let manualDomains = $state<string[]>([]);
	let manualText = $state('');
	let subscriptions = $state<DnsRouteSubscription[]>([]);
	let routes = $state<DnsRouteTarget[]>([]);
	let newSubUrl = $state('');
	let backend = $state<'ndms' | 'hydraroute'>('ndms');

	// New state variables
	let hrRouteMode = $state<'interface' | 'policy'>('interface');
	let hrPolicyName = $state('');
	let excludesText = $state('');
	let excludesTextareaEl = $state<HTMLTextAreaElement | null>(null);
	let hrInterfaceId = $state('');

	let showBackendSelector = $derived(isOS5 && hydrarouteInstalled);
	let isHydraRouteBackend = $derived(backend === 'hydraroute');

	// New derived
	let isHR = $derived(backend === 'hydraroute');
	let isNDMS = $derived(backend !== 'hydraroute');
	let isPolicyMode = $derived(isHR && hrRouteMode === 'policy');
	let isInterfaceMode = $derived(isHR && hrRouteMode === 'interface');

	let isInitialized = $state(false);
	let attempted = $state(false);

	// Snapshot initial state for isDirty detection
	let initialName = $state('');
	let initialManualDomains = $state<string[]>([]);
	let initialManualText = $state('');
	let initialSubscriptions = $state<DnsRouteSubscription[]>([]);
	let initialRoutes = $state<DnsRouteTarget[]>([]);
	let initialBackend = $state<'ndms' | 'hydraroute'>('ndms');
	let initialHrRouteMode = $state<'interface' | 'policy'>('interface');
	let initialHrPolicyName = $state('');
	let initialExcludesText = $state('');
	let initialHrInterfaceId = $state('');
	let initialIconUrl = $state<string | undefined>(undefined);

	let nameError = $derived(attempted && name.trim() === '');
	let routeError = $derived(attempted && routes.length === 0);

	// Reset form when modal opens
	$effect(() => {
		if (open) {
			if (!isInitialized) {
				attempted = false;
				if (route) {
					name = route.name;
					manualDomains = [...(route.manualDomains ?? [])];
					manualText = route.manualText ?? manualDomains.join('\n');
					subscriptions = (route.subscriptions ?? []).map((s) => ({ ...s }));
					routes = (route.routes ?? []).map((r) => ({ ...r }));
					backend = route.backend || (isOS5 ? 'ndms' : hydrarouteInstalled ? 'hydraroute' : 'ndms');
					hrRouteMode = route.hrRouteMode || 'interface';
					hrPolicyName = route.hrPolicyName || '';
					excludesText = route.excludesText ?? [...(route.excludes ?? []), ...(route.excludeSubnets ?? [])].join('\n');
					hrInterfaceId = (isHR && route.routes?.[0]?.tunnelId) || tunnels[0]?.id || '';
					iconUrl = route.iconUrl;
					// Capture snapshot for isDirty
					initialName = route.name;
					initialManualDomains = [...(route.manualDomains ?? [])];
					initialManualText = manualText;
					initialSubscriptions = (route.subscriptions ?? []).map((s) => ({ ...s }));
					initialRoutes = (route.routes ?? []).map((r) => ({ ...r }));
					initialBackend = backend;
					initialHrRouteMode = hrRouteMode;
					initialHrPolicyName = hrPolicyName;
					initialExcludesText = excludesText;
					initialHrInterfaceId = hrInterfaceId;
					initialIconUrl = iconUrl;
				} else {
					name = '';
					manualDomains = [];
					manualText = '';
					subscriptions = [];
					routes = [];
					backend = isOS5 ? 'ndms' : (hydrarouteInstalled ? 'hydraroute' : 'ndms');
					hrRouteMode = 'interface';
					hrPolicyName = '';
					excludesText = '';
					hrInterfaceId = tunnels[0]?.id || '';
					iconUrl = undefined;
					// Capture snapshot for isDirty (create mode defaults)
					initialName = '';
					initialManualDomains = [];
					initialManualText = '';
					initialSubscriptions = [];
					initialRoutes = [];
					initialBackend = backend;
					initialHrRouteMode = 'interface';
					initialHrPolicyName = '';
					initialExcludesText = '';
					initialHrInterfaceId = hrInterfaceId;
					initialIconUrl = undefined;
				}
				newSubUrl = '';
				newRouteTunnelId = '';
				isInitialized = true;
			}
		} else {
			isInitialized = false;
		}
	});

	// Computed
	let dedupReport = $derived(route?.lastDedupeReport);
	let hasDedups = $derived(dedupReport && dedupReport.totalRemoved > 0);

	let isEdit = $derived(route !== null);
	let title = $derived(isEdit ? `Редактирование: ${route?.name ?? ''}` : 'Новый DNS-маршрут');

	let totalDomains = $derived.by(() => {
		const manualCount = manualDomains.length;
		const subCount = subscriptions.reduce((acc, s) => acc + (s.lastCount ?? 0), 0);
		return manualCount + subCount;
	});

	let groupCount = $derived(Math.ceil(totalDomains / 300) || 0);

	let canSave = $derived(name.trim() !== '' && (isInterfaceMode ? !!hrInterfaceId : routes.length > 0));

	// isDirty: deep comparison with snapshot (edit mode) or with defaults (create mode)
	let isDirty = $derived.by(() => {
		const compareRoutes = (a: DnsRouteTarget[], b: DnsRouteTarget[]) => {
			if (a.length !== b.length) return true;
			return a.some((aRoute, i) => {
				const bRoute = b[i];
				return aRoute.tunnelId !== bRoute.tunnelId || aRoute.fallback !== bRoute.fallback;
			});
		};
		const compareSubscriptions = (a: DnsRouteSubscription[], b: DnsRouteSubscription[]) => {
			if (a.length !== b.length) return true;
			return a.some((aSub, i) => b[i]?.url !== aSub.url);
		};
		const compareDomains = (a: string[], b: string[]) => {
			if (a.length !== b.length) return true;
			return a.some((val, i) => b[i] !== val);
		};
		return (
			name !== initialName ||
			manualText !== initialManualText ||
			compareDomains(manualDomains, initialManualDomains) ||
			compareSubscriptions(subscriptions, initialSubscriptions) ||
			compareRoutes(routes, initialRoutes) ||
			backend !== initialBackend ||
			hrRouteMode !== initialHrRouteMode ||
			hrPolicyName !== initialHrPolicyName ||
			excludesText !== initialExcludesText ||
			hrInterfaceId !== initialHrInterfaceId ||
			iconUrl !== initialIconUrl
		);
	});

	// Handlers
	function handleDomainsChange(domains: string[], nextManualText: string) {
		manualDomains = domains;
		manualText = nextManualText;
	}

	function addSubscription() {
		const url = newSubUrl.trim();
		if (!url || !url.startsWith('http')) return;
		if (subscriptions.some((s) => s.url === url)) return;
		subscriptions = [...subscriptions, { url, name: url }];
		newSubUrl = '';
	}

	function removeSubscription(index: number) {
		subscriptions = subscriptions.filter((_, i) => i !== index);
	}

	// Match the inclusion rule from line 19: WAN interfaces are always "available"
	// in the logical sense (they're up by definition as long as the WAN link is
	// up), but they don't carry the t.available flag. Treat them as selectable.
	let availableTunnels = $derived(tunnels.filter((t) =>
		!routes.some((r) => r.tunnelId === t.id) && (t.available || t.type === 'wan')
	));
	let newRouteTunnelId = $state('');

	function addRoute() {
		const tunnelId = newRouteTunnelId || availableTunnels[0]?.id;
		if (!tunnelId) return;
		const tunnel = tunnels.find((t) => t.id === tunnelId);
		if (!tunnel) return;
		// Move fallback from old last route to the new one
		const fallback = currentFallback;
		const cleared = routes.map((r) => ({ ...r, fallback: '' as const }));
		routes = [...cleared, { interface: tunnel.id, tunnelId: tunnel.id, fallback }];
		newRouteTunnelId = '';
	}

	function removeRoute(index: number) {
		const fallback = currentFallback;
		const updated = routes.filter((_, i) => i !== index);
		// Ensure fallback stays on the last route
		routes = updated.map((r, i) => ({
			...r,
			fallback: i === updated.length - 1 ? fallback : ''
		}));
	}

	function moveRoute(index: number, direction: number) {
		const target = index + direction;
		if (target < 0 || target >= routes.length) return;
		// Capture current fallback before swap (it lives on the last route)
		const fallback = currentFallback;
		const updated = [...routes];
		[updated[index], updated[target]] = [updated[target], updated[index]];
		// Fallback always belongs on the last route only
		routes = updated.map((r, i) => ({
			...r,
			fallback: i === updated.length - 1 ? fallback : ''
		}));
	}

	function tunnelName(tunnelId: string): string {
		return findRoutingTunnelLabel(tunnels, tunnelId);
	}

	const hrInterfaceTunnelOpts = $derived(
		buildRoutingTunnelDropdownOptions(tunnels, {
			filter: (t) =>
				(t.type === 'managed' && t.available) || t.type === 'system' || t.type === 'wan',
		}),
	);

	const addRouteTunnelOpts = $derived(
		buildRoutingTunnelDropdownOptions(availableTunnels),
	);

	function handleFallbackChange(value: string) {
		if (routes.length === 0) return;
		const fallback: DnsRouteTarget['fallback'] = (value === 'auto' || value === 'reject') ? value : '';
		routes = routes.map((r, i) =>
			i === routes.length - 1 ? { ...r, fallback } : r
		);
	}

	let currentFallback = $derived.by(() => {
		if (routes.length === 0) return '';
		return routes[routes.length - 1].fallback ?? '';
	});

	function parseExcludesForPayload(value: string): string[] {
		return value
			.split('\n')
			.map((s) => s.trim())
			.filter((s) => s !== '' && !s.startsWith('#'));
	}

	function handleSave() {
		attempted = true;
		if (!canSave) {
			// TODO Phase 1: restore shake animation feedback on invalid submit
			return;
		}

		const parsedExcludes = parseExcludesForPayload(excludesText);

		// Build routes based on mode
		let saveRoutes = routes;
		if (isInterfaceMode) {
			saveRoutes = hrInterfaceId ? [{ tunnelId: hrInterfaceId, interface: hrInterfaceId, fallback: '' as const }] : [];
		}

		const data: Partial<DnsRoute> = {
			name: name.trim(),
			manualDomains,
			manualText,
			subscriptions,
			routes: saveRoutes,
			backend,
			excludes: isNDMS ? parsedExcludes : undefined,
			excludesText: isNDMS ? excludesText : undefined,
			hrRouteMode: isHR ? hrRouteMode : undefined,
			hrPolicyName: isPolicyMode ? (hrPolicyName || `AWG_${name.trim().replace(/\s+/g, '_')}`) : undefined,
			iconUrl: iconUrl || undefined,
		};
		onsave(data);
	}

	function handleSubKeydown(e: KeyboardEvent) {
		if (e.key === 'Enter') {
			e.preventDefault();
			addSubscription();
		}
	}

	function excludesLineOffset(lines: string[], lineIndex: number): number {
		let offset = 0;
		for (let i = 0; i < lineIndex; i++) {
			offset += lines[i].length + 1;
		}
		return offset;
	}

	function selectedExcludesLineRange(el: HTMLTextAreaElement, lines: string[]) {
		const start = el.selectionStart;
		const end = el.selectionEnd;

		let cursor = 0;
		let startLine = 0;
		let endLine = lines.length - 1;

		for (let i = 0; i < lines.length; i++) {
			const lineEnd = cursor + lines[i].length;
			if (start >= cursor && start <= lineEnd) startLine = i;
			if (end >= cursor && end <= lineEnd) {
				endLine = i;
				break;
			}
			cursor = lineEnd + 1;
		}

		return { startLine, endLine };
	}

	function toggleExcludesCommentSelection() {
		const el = excludesTextareaEl;
		if (!el) return;

		const lines = excludesText.split('\n');
		const { startLine, endLine } = selectedExcludesLineRange(el, lines);
		const selected = lines.slice(startLine, endLine + 1);

		const shouldComment = selected.some((line) => {
			const trimmed = line.trim();
			return trimmed !== '' && !trimmed.startsWith('#');
		});

		const changed = selected.map((line) => {
			if (line.trim() === '') return line;

			if (shouldComment) {
				const indent = line.match(/^\s*/)?.[0] ?? '';
				return `${indent}# ${line.slice(indent.length)}`;
			}

			return line.replace(/^(\s*)# ?/, '$1');
		});

		const nextLines = [
			...lines.slice(0, startLine),
			...changed,
			...lines.slice(endLine + 1)
		];

		excludesText = nextLines.join('\n');

		const nextStart = excludesLineOffset(nextLines, startLine);
		const nextEnd = excludesLineOffset(nextLines, endLine) + nextLines[endLine].length;

		setTimeout(() => {
			el.focus();
			el.setSelectionRange(nextStart, nextEnd);
		}, 0);
	}

	function handleExcludesKeydown(e: KeyboardEvent) {
		const isToggleComment =
			(e.ctrlKey || e.metaKey) &&
			(
				e.code === 'Slash' ||
				e.key === '/'
			);

		if (isToggleComment) {
			e.preventDefault();
			toggleExcludesCommentSelection();
		}
	}
</script>

{#snippet createIcon()}
	<CreateIcon />
{/snippet}

<Modal {open} {title} size="lg" onclose={onclose} hasUnsavedChanges={() => isDirty}>
	<!-- Name -->
	<div class="form-group" class:field-error={nameError}>
		<!-- svelte-ignore a11y_label_has_associated_control -->
		<label class="field-label">Название</label>
		<input
			class="field-input"
			type="text"
			placeholder="Заблокированные сайты"
			value={name}
			oninput={(e) => { name = (e.target as HTMLInputElement).value; }}
		/>
		<div class="error-text" class:visible={nameError}>Введите название</div>
	</div>

	<!-- Icon -->
	<div class="form-group">
		<!-- svelte-ignore a11y_label_has_associated_control -->
		<label class="field-label">Иконка</label>
		<div class="icon-row">
			<ServiceIcon {iconUrl} name={name || 'rule'} size={36} />
			<div class="icon-meta">
				{#if iconUrl}
					<div class="icon-src">Кастомная иконка</div>
					<div class="icon-hint" title={iconUrl}>{formatIconUrlHint(iconUrl)}</div>
				{:else}
					<div class="icon-src">Авто-определение по имени</div>
					<div class="icon-hint">
						{name ? `Подбирается по «${name}»` : 'Введите имя — иконка подберётся автоматически'}
					</div>
				{/if}
			</div>
			<Button variant="ghost" size="sm" onclick={() => (iconPickerOpen = true)}>
				{iconUrl ? 'Сменить иконку' : 'Выбрать иконку'}
			</Button>
		</div>
	</div>

	<!-- Backend selector -->
	{#if showBackendSelector}
		<div class="form-group">
			<Dropdown
				label="Движок маршрутизации"
				bind:value={backend}
				options={[
					{ value: 'ndms' as const, label: 'ПО роутера (NDMS)' },
					{ value: 'hydraroute' as const, label: 'HydraRoute Neo' },
				]}
				fullWidth
			/>
		</div>
	{/if}

	<!-- Manual domains -->
	<div class="form-section">
		<div class="section-title">Домены (вручную)</div>
		{#if isHydraRouteBackend}
			<span class="field-hint geo-hint">Поддерживается geosite:TAG, например geosite:GOOGLE</span>
		{/if}
		<DnsRouteDomainEditor
			domains={manualDomains}
			textValue={manualText}
			onchange={handleDomainsChange}
			allowGeoTags={isHydraRouteBackend}
		/>
	</div>

	<!-- Subscriptions -->
	<div class="form-section">
		<div class="section-title">Подписки</div>
		<DownloadRouteNote text="URL-листы будут проверены и обновлены через" />
		{#if subscriptions.length > 0}
			<div class="sub-list">
				{#each subscriptions as sub, i (sub.url)}
					<div class="sub-item">
						<div class="sub-info">
							<span class="sub-url">{sub.url}</span>
							<span class="sub-meta">
								{#if sub.lastError}
									<span class="sub-error">Ошибка: {sub.lastError}</span>
								{:else if sub.lastCount !== undefined && sub.lastCount > 0}
									<span class="sub-ok">{sub.lastCount} доменов</span>
									{#if sub.lastFetched}
										<span class="sub-time"> &middot; {formatRelativeTime(sub.lastFetched)}</span>
									{/if}
								{/if}
							</span>
						</div>
						<button class="btn-remove" onclick={() => removeSubscription(i)} title="Удалить подписку">
							&times;
						</button>
					</div>
				{/each}
			</div>
		{/if}
		<div class="sub-add">
			<input
				class="field-input"
				type="url"
				placeholder="https://example.com/domains.txt"
				value={newSubUrl}
				oninput={(e) => { newSubUrl = (e.target as HTMLInputElement).value; }}
				onkeydown={handleSubKeydown}
			/>
			<Button variant="secondary" size="sm" onclick={addSubscription} disabled={!newSubUrl.trim()}>
				Добавить
			</Button>
		</div>
	</div>

	<!-- HR Mode Tabs (only for hydraroute) -->
	{#if isHR}
		<div class="section-title">Маршрут</div>
		<div class="mode-tabs">
			<button class="mode-tab" class:active={hrRouteMode === 'interface'} onclick={() => hrRouteMode = 'interface'}>Интерфейс</button>
			<button class="mode-tab" class:active={hrRouteMode === 'policy'} onclick={() => hrRouteMode = 'policy'}>Политика</button>
		</div>
	{/if}

	<!-- HR Interface mode: single selector -->
	{#if isInterfaceMode}
		<div class="form-group">
			<Dropdown
				label="Целевой интерфейс"
				bind:value={hrInterfaceId}
				options={hrInterfaceTunnelOpts}
				hint="Трафик направляется напрямую на интерфейс (DirectRoute)"
				fullWidth
			/>
		</div>
	{/if}

	<!-- Route chain (for NDMS and HR Policy mode) -->
	{#if isNDMS || isPolicyMode}
		<div class="form-section">
			{#if isNDMS}
				<div class="section-title">Маршрут (порядок = приоритет)</div>
			{/if}
			{#if routes.length === 0}
				<p class="route-hint" class:route-hint-error={routeError}>Добавьте хотя бы один туннель для маршрутизации</p>
			{/if}
			{#if routes.length > 0}
				<div class="route-list">
					{#each routes as target, i (i)}
						<div class="route-item">
							<span class="route-index">{i + 1}.</span>
							<span class="route-name">{tunnelName(target.tunnelId)}</span>
							<span class="route-id" title={target.tunnelId}>{target.tunnelId}</span>
							<div class="route-actions">
								<button class="btn-move" onclick={() => moveRoute(i, -1)} disabled={i === 0} title="Вверх">&uarr;</button>
								<button class="btn-move" onclick={() => moveRoute(i, 1)} disabled={i === routes.length - 1} title="Вниз">&darr;</button>
								<button class="btn-remove" onclick={() => removeRoute(i)} title="Удалить">&times;</button>
							</div>
						</div>
					{/each}
				</div>
			{/if}
			{#if availableTunnels.length > 0}
				<div class="route-add">
					<div class="route-add-select">
						<Dropdown
							value={newRouteTunnelId || availableTunnels[0]?.id || ''}
							options={addRouteTunnelOpts}
							onchange={(v) => (newRouteTunnelId = v)}
							fullWidth
						/>
					</div>
					<Button variant="primary" size="sm" onclick={addRoute} iconBefore={createIcon}>
						Добавить
					</Button>
				</div>
			{/if}

			{#if isNDMS && routes.length > 0}
				<div class="fallback-group">
					<!-- svelte-ignore a11y_label_has_associated_control -->
					<label class="field-label">Если все недоступны:</label>
					<div class="fallback-options">
						<label class="fallback-option">
							<input
								type="radio"
								name="fallback"
								value="auto"
								checked={currentFallback === 'auto'}
								onchange={() => handleFallbackChange('auto')}
							/>
							<span>провайдер</span>
						</label>
						<label class="fallback-option">
							<input
								type="radio"
								name="fallback"
								value="reject"
								checked={currentFallback === 'reject'}
								onchange={() => handleFallbackChange('reject')}
							/>
							<span>эксклюзивный</span>
						</label>
					</div>
				</div>
			{/if}

			{#if isPolicyMode}
				<div class="policy-name-row">
					<span class="policy-label">Имя политики:</span>
					<input class="policy-input" value={hrPolicyName} oninput={(e) => hrPolicyName = (e.target as HTMLInputElement).value} placeholder="HydraRoute">
				</div>
				<span class="field-hint">Порядок = приоритет. Политика создаётся автоматически на роутере.</span>
			{/if}
		</div>
	{/if}

	<!-- Excludes (NDMS only) -->
	{#if isNDMS}
		<div class="form-section">
			<div class="section-title">Исключения</div>
			<textarea
				bind:this={excludesTextareaEl}
				class="field-textarea excludes-textarea"
				rows="3"
				placeholder={"# Домены, которые НЕ маршрутизировать\nexample.com\n.local\n\n# Подсети, которые НЕ маршрутизировать\n10.0.0.0/8\n2001:db8::/32"}
				value={excludesText}
				oninput={(e) => excludesText = (e.target as HTMLTextAreaElement).value}
				onkeydown={handleExcludesKeydown}
			></textarea>
			<span class="field-hint excludes-hint">Эти домены будут исключены из маршрутизации через туннель.
Комментарии начинаются с #
Ctrl+/ или Cmd+/ комментирует выбранные строки.</span>
		</div>
	{/if}

	{#if hasDedups && dedupReport}
		<details class="dedup-details">
			<summary class="dedup-summary">
				Убрано {dedupReport.totalRemoved} дублей ({dedupReport.exactDupes} точных, {dedupReport.wildcardDupes} wildcard)
			</summary>
			<div class="dedup-list">
				{#each dedupReport.items ?? [] as item}
					<div class="dedup-item">
						<code>{item.domain}</code>
						<span class="dedup-reason">
							{#if item.reason === 'exact'}
								дубль
							{:else if item.reason === 'wildcard'}
								покрыт {item.coveredBy}
							{:else}
								покрыт подсетью {item.coveredBy}
							{/if}
							{#if item.listName}
								в «{item.listName}»
							{:else if item.listId}
								в {item.listId}
							{/if}
						</span>
					</div>
				{/each}
			</div>
		</details>
	{/if}

	<!-- Summary -->
	{#if totalDomains > 0}
		<div class="summary">
			Итого: {totalDomains} доменов{#if groupCount > 1} &rarr; {groupCount} групп по 300{/if}
		</div>
	{/if}

	{#snippet actions()}
		<Button variant="secondary" onclick={onclose}>Отмена</Button>
		<!-- TODO Phase 1: shake animation on save when invalid (was class:shake={shaking}) -->
		<Button variant="primary" onclick={handleSave} loading={saving}>
			Сохранить
		</Button>
	{/snippet}
</Modal>

<IconPickerModal
	open={iconPickerOpen}
	{iconUrl}
	ruleName={name}
	onclose={() => (iconPickerOpen = false)}
	onapply={(newUrl) => {
		iconUrl = newUrl ?? undefined;
		iconPickerOpen = false;
	}}
/>

<style>
	.form-group {
		margin-bottom: 1rem;
	}

	.form-section {
		margin-bottom: 1.25rem;
	}

	.section-title {
		font-size: 0.75rem;
		font-weight: 600;
		color: var(--color-text-muted);
		text-transform: uppercase;
		letter-spacing: 0.05em;
		margin-bottom: 0.5rem;
		padding-bottom: 0.375rem;
		border-bottom: 1px solid var(--color-border);
	}

	/* Subscriptions */
	.sub-list {
		display: flex;
		flex-direction: column;
		gap: 0.5rem;
		margin-bottom: 0.5rem;
	}

	.sub-item {
		display: flex;
		align-items: flex-start;
		justify-content: space-between;
		gap: 0.5rem;
		padding: 0.5rem;
		background: var(--color-bg-secondary);
		border-radius: 6px;
	}

	.sub-info {
		display: flex;
		flex-direction: column;
		gap: 0.125rem;
		min-width: 0;
	}

	.sub-url {
		font-size: 0.75rem;
		color: var(--color-text-primary);
		font-family: ui-monospace, SFMono-Regular, 'SF Mono', Menlo, monospace;
		word-break: break-all;
	}

	.sub-meta {
		font-size: 0.6875rem;
		color: var(--color-text-muted);
	}

	.sub-ok {
		color: var(--success, #10b981);
	}

	.sub-error {
		color: var(--error, #ef4444);
	}

	.sub-time {
		color: var(--color-text-muted);
	}

	.sub-add {
		display: flex;
		gap: 0.5rem;
		align-items: center;
	}

	.sub-add :global(.field-input) {
		flex: 1;
	}

	/* Route chain */
	.route-list {
		display: flex;
		flex-direction: column;
		gap: 0.5rem;
		margin-bottom: 0.5rem;
	}

	.route-item {
		display: flex;
		align-items: center;
		gap: 0.5rem;
	}

	.route-index {
		font-size: 0.8125rem;
		color: var(--color-text-muted);
		font-weight: 500;
		width: 1.5rem;
		flex-shrink: 0;
	}

	.route-name {
		flex: 1;
		font-size: 0.8125rem;
		color: var(--color-text-primary);
		min-width: 0;
		overflow: hidden;
		text-overflow: ellipsis;
		white-space: nowrap;
	}

	.route-id {
		font-family: ui-monospace, SFMono-Regular, Menlo, Monaco, Consolas, monospace;
		font-size: 0.6875rem;
		color: var(--color-text-muted);
		background: var(--color-bg-tertiary);
		padding: 1px 6px;
		border-radius: 4px;
		flex-shrink: 0;
		max-width: 40%;
		overflow: hidden;
		text-overflow: ellipsis;
		white-space: nowrap;
	}

	.route-actions {
		display: flex;
		gap: 0.25rem;
		flex-shrink: 0;
	}

	.route-add {
		display: flex;
		gap: 0.5rem;
		align-items: center;
	}

	.route-add-select {
		flex: 1;
	}
	.route-add :global(.field-select) {
		flex: 1;
	}

	.excludes-textarea {
		font-size: 0.8125rem;
		font-family: ui-monospace, SFMono-Regular, 'SF Mono', Menlo, monospace;
	}

	.excludes-hint {
		white-space: pre-line;
	}

	.btn-move {
		background: none;
		border: 1px solid var(--color-border);
		color: var(--color-text-muted);
		font-size: 0.75rem;
		cursor: pointer;
		padding: 0.125rem 0.375rem;
		line-height: 1;
		border-radius: 4px;
	}

	.btn-move:hover:not(:disabled) {
		color: var(--color-accent);
		border-color: var(--color-accent);
	}

	.btn-move:disabled {
		opacity: 0.3;
		cursor: default;
	}

	.btn-remove {
		background: none;
		border: none;
		color: var(--color-text-muted);
		font-size: 1.25rem;
		cursor: pointer;
		padding: 0 0.375rem;
		line-height: 1;
		border-radius: 4px;
		flex-shrink: 0;
	}

	.btn-remove:hover {
		color: var(--error, #ef4444);
		background: rgba(239, 68, 68, 0.1);
	}

	/* Fallback */
	.fallback-group {
		margin-top: 0.75rem;
	}

	.fallback-options {
		display: flex;
		flex-wrap: wrap;
		gap: 0.5rem 1rem;
	}

	@media (max-width: 480px) {
		.fallback-options {
			flex-direction: column;
			gap: 0.5rem;
			align-items: flex-start;
		}
	}

	.fallback-option {
		display: inline-flex;
		align-items: center;
		gap: 0.375rem;
		font-size: 0.8125rem;
		color: var(--color-text-primary);
		cursor: pointer;
		white-space: nowrap;
	}

	.fallback-option input[type="radio"] {
		accent-color: var(--color-accent);
	}

	.route-hint {
		font-size: 0.75rem;
		color: var(--warning, #eab308);
		margin: 0 0 0.5rem 0;
	}

	.field-error :global(.field-input) {
		border-color: var(--color-error);
		box-shadow: 0 0 0 2px var(--color-error-tint);
	}

	.route-hint-error {
		color: var(--color-error);
		background: rgba(239, 68, 68, 0.08);
		padding: 0.5rem;
		border-radius: 6px;
		border: 1px solid rgba(239, 68, 68, 0.25);
	}

	.dedup-details {
		margin-top: 12px;
		border: 1px solid var(--color-border);
		border-radius: 6px;
		overflow: hidden;
	}

	.dedup-summary {
		padding: 8px 12px;
		font-size: 0.75rem;
		color: var(--warning, #f59e0b);
		cursor: pointer;
		background: var(--color-bg-hover);
	}

	.dedup-list {
		max-height: 200px;
		overflow-y: auto;
		padding: 8px 12px;
	}

	.dedup-item {
		display: flex;
		justify-content: space-between;
		gap: 8px;
		padding: 2px 0;
		font-size: 0.6875rem;
	}

	.dedup-item code {
		font-family: monospace;
		font-size: 0.625rem;
		color: var(--color-text-primary);
	}

	.dedup-reason {
		color: var(--color-text-muted);
		text-align: right;
		white-space: nowrap;
	}

	/* Summary */
	.summary {
		font-size: 0.8125rem;
		color: var(--color-text-muted);
		padding: 0.5rem 0;
		border-top: 1px dashed var(--color-border);
	}

	.field-hint {
		display: block;
		font-size: 0.6875rem;
		margin-bottom: 0.375rem;
	}

	.geo-hint {
		color: var(--color-accent);
		font-style: italic;
	}

	/* Mode tabs */
	.mode-tabs {
		display: flex;
		gap: 0;
		margin-bottom: 0.75rem;
		background: var(--color-bg-primary);
		border-radius: 6px;
		padding: 3px;
	}

	.mode-tab {
		flex: 1;
		padding: 0.375rem 0.75rem;
		text-align: center;
		font-size: 0.75rem;
		font-weight: 500;
		border-radius: 4px;
		cursor: pointer;
		border: none;
		background: transparent;
		color: var(--color-text-muted);
		font-family: inherit;
		transition: all 0.15s;
	}

	.mode-tab.active {
		background: var(--color-bg-hover);
		color: var(--color-text-primary);
		box-shadow: 0 1px 3px rgba(0, 0, 0, 0.2);
	}

	/* Policy name row */
	.policy-name-row {
		display: flex;
		align-items: center;
		gap: 0.5rem;
		margin-top: 0.75rem;
		padding: 0.5rem 0.625rem;
		background: var(--color-bg-primary);
		border: 1px solid var(--color-border);
		border-radius: 6px;
	}

	.policy-label {
		font-size: 0.75rem;
		color: var(--color-text-muted);
		white-space: nowrap;
	}

	.policy-input {
		flex: 1;
		padding: 0.25rem 0.5rem;
		border: 1px solid var(--color-border);
		border-radius: 4px;
		background: var(--color-bg-secondary);
		color: var(--color-text-primary);
		font-size: 0.8125rem;
		font-family: inherit;
	}

	.policy-input:focus {
		outline: none;
		border-color: var(--color-accent);
	}

</style>
