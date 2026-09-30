<script lang="ts">
	import { Modal, Button, Input, SegmentedControl } from '$lib/components/ui';
	import { api } from '$lib/api/client';
	import { notifications } from '$lib/stores/notifications';
	import { Plus, Check, Globe, Layers, ArrowRight, Shield, X, AlertCircle, Info } from 'lucide-svelte';
	import type { ActiveEngineInfo, ItemRouteStatus } from '$lib/types/systemTools';

	interface Props {
		domains?: string[];
		ips?: string[];
		defaultServiceName?: string;
		activeEngines?: ActiveEngineInfo[];
		domainStatuses?: Record<string, ItemRouteStatus[]>;
		ipStatuses?: Record<string, ItemRouteStatus[]>;
		existingRules?: string[];
		onclose: () => void;
		onsuccess?: () => void;
	}

	let {
		domains = [],
		ips = [],
		defaultServiceName = '',
		activeEngines = [],
		domainStatuses = {},
		ipStatuses = {},
		existingRules = [],
		onclose,
		onsuccess,
	}: Props = $props();

	type ExportTarget = 'catalog' | 'mihomo' | 'singbox' | 'hydraroute' | 'static_route';

	// Dynamic target options based on router's active components
	const targetOptions = $derived(
		activeEngines && activeEngines.length > 0
			? activeEngines.map((e) => ({ value: e.id, label: e.label }))
			: [
					{ value: 'catalog', label: 'Каталог сервисов' },
					{ value: 'mihomo', label: 'Mihomo' },
					{ value: 'hydraroute', label: 'HydraRoute Neo' },
					{ value: 'static_route', label: 'Статический IP' },
				]
	);

	let target = $state<ExportTarget>('catalog');
	let serviceName = $state('');
	let saving = $state(false);
	let mode = $state<'append' | 'create'>('append');

	// Available groups/presets loaded from API
	let availableMihomoGroups = $state<string[]>([]);
	let availableCatalogPresets = $state<string[]>([]);

	$effect(() => {
		const client = api as unknown as { mihomoNativeGroups?: () => Promise<Array<{ name: string }>> };
		if (typeof client.mihomoNativeGroups === 'function') {
			client.mihomoNativeGroups().then((groups: Array<{ name: string }>) => {
				if (groups && groups.length > 0) {
					const names = groups.map((g: { name: string }) => g.name);
					if (!names.includes('DIRECT')) names.push('DIRECT');
					if (!names.includes('REJECT')) names.push('REJECT');
					availableMihomoGroups = names;
				} else {
					availableMihomoGroups = ['DIRECT', 'REJECT'];
				}
			}).catch(() => {
				availableMihomoGroups = ['DIRECT', 'REJECT'];
			});
		} else {
			availableMihomoGroups = ['DIRECT', 'REJECT'];
		}

		api.listPresets().then((res) => {
			if (res?.presets && res.presets.length > 0) {
				availableCatalogPresets = res.presets.map((p) => p.name);
			}
		}).catch(() => {});
	});

	// Interactive item selection
	let activeDomains = $state<string[]>([]);
	let activeIPs = $state<string[]>([]);
	let includeDomains = $state(true);
	let includeIPs = $state(true);

	$effect(() => {
		activeDomains = [...domains];
		activeIPs = [...ips];
		includeDomains = domains.length > 0;
		includeIPs = ips.length > 0;
	});

	function removeDomain(d: string) {
		activeDomains = activeDomains.filter((item) => item !== d);
	}

	function removeIP(ip: string) {
		activeIPs = activeIPs.filter((item) => item !== ip);
	}

	// Set initial target based on first active engine
	$effect(() => {
		if (activeEngines && activeEngines.length > 0) {
			const hasCurrent = activeEngines.some((e) => e.id === target);
			if (!hasCurrent) {
				target = activeEngines[0].id as ExportTarget;
			}
		}
	});

	// Target capabilities
	const targetSupportsDomains = $derived(target !== 'static_route');
	const targetSupportsIPs = true;

	// Contextual terminology
	const isCatalog = $derived(target === 'catalog');
	const entityNoun = $derived(isCatalog ? 'сервис' : 'правило');
	const entityNounPlural = $derived(isCatalog ? 'сервисах' : 'правилах');
	const entityNounAccusative = $derived(isCatalog ? 'сервис' : 'правило');

	// Active working lists taking into account target capabilities and user checkboxes
	const currentDomains = $derived(targetSupportsDomains && includeDomains ? activeDomains : []);
	const currentIPs = $derived(targetSupportsIPs && includeIPs ? activeIPs : []);

	// Matched rules SPECIFIC to the selected target
	const matchedRulesForTarget = $derived.by(() => {
		const names: string[] = [];
		const t = target;

		for (const d of currentDomains) {
			for (const st of domainStatuses[d] || []) {
				const isTargetMatch =
					st.target === t ||
					(t === 'static_route' && (st.target === 'ndms' || st.target === 'static_route'));
				if (isTargetMatch && st.ruleName && !names.includes(st.ruleName)) {
					names.push(st.ruleName);
				}
			}
		}
		for (const ip of currentIPs) {
			for (const st of ipStatuses[ip] || []) {
				const isTargetMatch =
					st.target === t ||
					(t === 'static_route' && (st.target === 'ndms' || st.target === 'static_route'));
				if (isTargetMatch && st.ruleName && !names.includes(st.ruleName)) {
					names.push(st.ruleName);
				}
			}
		}
		return names;
	});

	// Items already present in the CURRENT target
	const existingItemsForTarget = $derived.by(() => {
		const t = target;
		const doms = currentDomains.filter((d) =>
			(domainStatuses[d] || []).some(
				(st) =>
					st.target === t ||
					(t === 'static_route' && (st.target === 'ndms' || st.target === 'static_route'))
			)
		);
		const ipsList = currentIPs.filter((ip) =>
			(ipStatuses[ip] || []).some(
				(st) =>
					st.target === t ||
					(t === 'static_route' && (st.target === 'ndms' || st.target === 'static_route'))
			)
		);
		return { domains: doms, ips: ipsList, total: doms.length + ipsList.length };
	});

	// New items for the CURRENT target
	const newItemsForTarget = $derived.by(() => {
		const t = target;
		const doms = currentDomains.filter(
			(d) =>
				!(domainStatuses[d] || []).some(
					(st) =>
						st.target === t ||
						(t === 'static_route' && (st.target === 'ndms' || st.target === 'static_route'))
				)
		);
		const ipsList = currentIPs.filter(
			(ip) =>
				!(ipStatuses[ip] || []).some(
					(st) =>
						st.target === t ||
						(t === 'static_route' && (st.target === 'ndms' || st.target === 'static_route'))
				)
		);
		return { domains: doms, ips: ipsList, total: doms.length + ipsList.length };
	});

	// Keep mode in sync with availability of existing matches in current target
	$effect(() => {
		const hasMatchesInTarget = existingItemsForTarget.total > 0;
		if (!hasMatchesInTarget) {
			mode = 'create';
		}
	});

	// Update serviceName / ruleName based on target, mode, and target-specific matches
	$effect(() => {
		const currentTarget = target;
		const currentMode = mode;
		const matched = matchedRulesForTarget;

		if (currentMode === 'append') {
			if (matched.length > 0) {
				serviceName = matched[0];
			} else {
				if (currentTarget === 'catalog') {
					serviceName = defaultServiceName || 'Пользовательский сервис';
				} else if (currentTarget === 'mihomo') {
					serviceName = availableMihomoGroups[0] || 'DIRECT';
				} else {
					serviceName = defaultServiceName || 'Пользовательское правило';
				}
			}
		} else {
			// Mode create
			if (currentTarget === 'catalog' || currentTarget === 'hydraroute') {
				serviceName = defaultServiceName || (currentTarget === 'catalog' ? 'Новый сервис' : 'Новый список');
			} else if (currentTarget === 'mihomo') {
				serviceName = availableMihomoGroups[0] || 'DIRECT';
			} else {
				serviceName = defaultServiceName || 'Новое правило';
			}
		}
	});

	const effectiveExportDomains = $derived(
		mode === 'append' ? newItemsForTarget.domains : currentDomains
	);

	const effectiveExportIPs = $derived(
		mode === 'append' ? newItemsForTarget.ips : currentIPs
	);

	const totalToExport = $derived(effectiveExportDomains.length + effectiveExportIPs.length);

	async function handleExport() {
		if (totalToExport === 0) {
			notifications.warning('Не выбрано ни одного элемента для добавления');
			return;
		}

		saving = true;
		try {
			const res = await api.systemTrafficExport({
				target,
				mode,
				serviceName: serviceName.trim(),
				domains: effectiveExportDomains,
				ips: effectiveExportIPs,
				outbound: serviceName.trim() || 'DIRECT',
			});

			if (res.success) {
				notifications.success(res.message || `Добавлено: ${res.count} записей (${target})`);
				if (onsuccess) onsuccess();
				onclose();
			} else {
				notifications.error(res.message || 'Ошибка экспорта');
			}
		} catch (err: unknown) {
			const message = err instanceof Error ? err.message : String(err);
			notifications.error(message || 'Не удалось экспортировать');
		} finally {
			saving = false;
		}
	}
</script>

<Modal open={true} title="Экспорт доменов и IP в маршрутизацию" {onclose}>
	<div class="export-modal-body">
		<!-- Deduplication Status Alert (Specific to Selected Target) -->
		{#if existingItemsForTarget.total > 0}
			<div class="dedup-banner">
				<div class="dedup-icon">
					<Check size={16} />
				</div>
				<div class="dedup-text">
					<div class="dedup-title">
						{#if target === 'catalog'}
							В каталоге уже настроено: <strong>{existingItemsForTarget.total}</strong> из {currentDomains.length + currentIPs.length}
						{:else if target === 'mihomo'}
							В правилах Mihomo уже есть: <strong>{existingItemsForTarget.total}</strong> из {currentDomains.length + currentIPs.length}
						{:else if target === 'singbox'}
							В правилах Sing-box уже есть: <strong>{existingItemsForTarget.total}</strong> из {currentDomains.length + currentIPs.length}
						{:else if target === 'hydraroute'}
							В списках HydraRoute найдено: <strong>{existingItemsForTarget.total}</strong> из {currentDomains.length + currentIPs.length}
						{:else}
							В статических маршрутах найдено: <strong>{existingItemsForTarget.ips.length}</strong> из {currentIPs.length} IP
						{/if}
					</div>
					<div class="dedup-desc">
						{#if matchedRulesForTarget.length > 0}
							Найдено в: <em>{matchedRulesForTarget.join(', ')}</em>.
						{/if}
						Система исключит дубликаты и добавит только <strong>{newItemsForTarget.total}</strong> новых записей.
					</div>
				</div>
			</div>
		{:else}
			<div class="dedup-banner neutral">
				<div class="dedup-icon info">
					<Info size={16} />
				</div>
				<div class="dedup-text">
					<div class="dedup-title">
						{#if target === 'catalog'}
							Новый сервис для каталога ({currentDomains.length + currentIPs.length} записей)
						{:else if target === 'mihomo'}
							Новое правило маршрутизации Mihomo ({currentDomains.length + currentIPs.length} записей)
						{:else if target === 'hydraroute'}
							Новый список для HydraRoute ({currentDomains.length + currentIPs.length} записей)
						{:else}
							Новые статические маршруты ({currentIPs.length} IP)
						{/if}
					</div>
					<div class="dedup-desc">
						В выбранном назначении еще нет таких записей. Все выбранные элементы будут добавлены.
					</div>
				</div>
			</div>
		{/if}

		<!-- Target Engine Selector -->
		<div class="target-selector">
			<label class="section-label" for="target-control">Назначение маршрутизации:</label>
			<div id="target-control">
				<SegmentedControl
					options={targetOptions}
					value={target}
					ariaLabel="Назначение маршрутизации"
					onchange={(v) => (target = v as ExportTarget)}
				/>
			</div>
		</div>

		{#if existingItemsForTarget.total > 0 && newItemsForTarget.total > 0}
			<!-- Append vs Create Mode Selector with Contextual Terminology -->
			<div class="mode-selector">
				<label class="section-label" for="mode-control">Режим добавления:</label>
				<div id="mode-control">
					<SegmentedControl
						options={[
							{ value: 'append', label: `Дополнить ${entityNounAccusative} (+${newItemsForTarget.total})` },
							{ value: 'create', label: target === 'catalog' ? `Создать новый сервис (${currentDomains.length + currentIPs.length})` : `Создать новое правило (${currentDomains.length + currentIPs.length})` },
						]}
						value={mode}
						ariaLabel="Режим добавления"
						onchange={(v) => (mode = v as 'append' | 'create')}
					/>
				</div>
			</div>
		{/if}

		<!-- Target Specific Inputs -->
		{#if target === 'catalog'}
			<div class="target-desc">
				<Layers size={15} class="desc-icon" />
				<div>
					<strong>Каталог сервисов:</strong> универсальный набор правил, который можно использовать в любом активном движке (Mihomo, Sing-box, HydraRoute).
				</div>
			</div>

			<div class="field-group">
				<label class="field-label" for="service-name-input">
					{#if mode === 'append'}
						Сервис в каталоге для дополнения:
					{:else}
						Название нового сервиса в каталоге:
					{/if}
				</label>
				<Input
					id="service-name-input"
					placeholder="Например: Российские сервисы, Smart TV"
					bind:value={serviceName}
				/>
				{#if mode === 'append' && matchedRulesForTarget.length > 1}
					<div class="matched-suggestions">
						<span class="matched-suggestions-label">Найденные сервисы:</span>
						{#each matchedRulesForTarget as rName}
							<button
								type="button"
								class="suggestion-chip"
								class:active={serviceName === rName}
								onclick={() => (serviceName = rName)}
							>
								{rName}
							</button>
						{/each}
					</div>
				{/if}
			</div>
		{:else if target === 'mihomo'}
			<div class="target-desc">
				<Globe size={15} class="desc-icon" />
				<div>
					<strong>Маршрутизация Mihomo:</strong> добавит правила (<code>DOMAIN-SUFFIX</code>, <code>IP-CIDR</code>) в конфигурацию Mihomo.
				</div>
			</div>

			<div class="field-group">
				<label class="field-label" for="mihomo-group-input">
					{#if mode === 'append' && matchedRulesForTarget.length > 0}
						Прокси-группа / Outbound для дополнения:
					{:else}
						Куда направить трафик (Прокси-группа / Outbound):
					{/if}
				</label>
				<Input
					id="mihomo-group-input"
					placeholder="Например: Задний вход :), DIRECT, REJECT"
					bind:value={serviceName}
				/>
				{#if availableMihomoGroups.length > 0}
					<div class="matched-suggestions">
						<span class="matched-suggestions-label">Доступные группы:</span>
						{#each availableMihomoGroups as gName}
							<button
								type="button"
								class="suggestion-chip"
								class:active={serviceName === gName}
								onclick={() => (serviceName = gName)}
							>
								{gName}
							</button>
						{/each}
					</div>
				{/if}
			</div>
		{:else if target === 'singbox'}
			<div class="target-desc">
				<Globe size={15} class="desc-icon" />
				<div>
					<strong>Маршрутизация Sing-box:</strong> добавит правила (<code>domain_suffix</code>, <code>ip_cidr</code>) в конфигурацию Sing-box.
				</div>
			</div>

			<div class="field-group">
				<label class="field-label" for="singbox-outbound-input">
					{#if mode === 'append'}
						Правило Sing-box для дополнения:
					{:else}
						Куда направить (Outbound Sing-box):
					{/if}
				</label>
				<Input
					id="singbox-outbound-input"
					placeholder="Например: Proxy, direct"
					bind:value={serviceName}
				/>
			</div>
		{:else if target === 'hydraroute'}
			<div class="target-desc">
				<Shield size={15} class="desc-icon" />
				<div>
					<strong>HydraRoute Neo:</strong> добавит правила (домены и IP-подсети) в списки маршрутизации Keenetic.
				</div>
			</div>

			<div class="field-group">
				<label class="field-label" for="hydra-list-input">
					{#if mode === 'append'}
						Список HydraRoute для дополнения:
					{:else}
						Название нового списка HydraRoute:
					{/if}
				</label>
				<Input
					id="hydra-list-input"
					placeholder="Например: unblock, bypass"
					bind:value={serviceName}
				/>
			</div>
		{:else}
			<div class="target-desc">
				<ArrowRight size={15} class="desc-icon" />
				<div>
					<strong>Статические IP-маршруты:</strong> добавит выбранные IP-адреса в системную таблицу маршрутизации роутера.
				</div>
			</div>

			<div class="field-group">
				<label class="field-label" for="static-interface-input">
					Интерфейс / Шлюз (куда направить IP-маршруты):
				</label>
				<Input
					id="static-interface-input"
					placeholder="Например: default, nwg0, WireGuard"
					bind:value={serviceName}
				/>
			</div>
		{/if}

		<!-- Category Filter & Items List -->
		<div class="items-selection-section">
			<div class="selection-header">
				<span class="section-label">Состав для добавления:</span>
				<div class="type-toggles">
					{#if targetSupportsDomains && activeDomains.length > 0}
						<label class="type-toggle-label">
							<input
								type="checkbox"
								checked={includeDomains}
								onchange={(e) => (includeDomains = e.currentTarget.checked)}
							/>
							<span>Домены ({activeDomains.length})</span>
						</label>
					{/if}
					{#if targetSupportsIPs && activeIPs.length > 0}
						<label class="type-toggle-label">
							<input
								type="checkbox"
								checked={includeIPs}
								onchange={(e) => (includeIPs = e.currentTarget.checked)}
							/>
							<span>IP-адреса ({activeIPs.length})</span>
						</label>
					{/if}
				</div>
			</div>

			<div class="chips-box">
				<!-- Domains Section -->
				{#if targetSupportsDomains}
					{#if includeDomains && activeDomains.length > 0}
						<div class="chip-group">
							<div class="chip-group-header">
								<span>Домены:</span>
								{#if mode === 'append'}
									<span class="chip-sub-hint">({newItemsForTarget.domains.length} новых)</span>
								{/if}
							</div>
							<div class="chips-list">
								{#each activeDomains as d}
									{@const isPresentInTarget = (domainStatuses[d] || []).some(
										(st) =>
											st.target === target ||
											(target === 'static_route' && (st.target === 'ndms' || st.target === 'static_route'))
									)}
									{@const isFilteredOut = mode === 'append' && isPresentInTarget}
									<div class="interactive-chip domain" class:filtered-out={isFilteredOut}>
										<span class="chip-name">{d}</span>
										{#if isPresentInTarget}
											<span class="chip-badge present">в {entityNounPlural}</span>
										{:else}
											<span class="chip-badge new">+ новый</span>
										{/if}
										<button
											type="button"
											class="chip-close"
											title="Исключить домен"
											onclick={() => removeDomain(d)}
										>
											<X size={12} />
										</button>
									</div>
								{/each}
							</div>
						</div>
					{/if}
				{:else}
					<div class="engine-limitation-notice">
						<Info size={14} />
						<span>Статическая маршрутизация роутера применяется только к IP-адресам. Домены ({activeDomains.length}) автоматически исключены.</span>
					</div>
				{/if}

				<!-- IPs Section -->
				{#if targetSupportsIPs}
					{#if includeIPs && activeIPs.length > 0}
						<div class="chip-group">
							<div class="chip-group-header">
								<span>IP-адреса:</span>
								{#if mode === 'append'}
									<span class="chip-sub-hint">({newItemsForTarget.ips.length} новых)</span>
								{/if}
							</div>
							<div class="chips-list">
								{#each activeIPs as ip}
									{@const isPresentInTarget = (ipStatuses[ip] || []).some(
										(st) =>
											st.target === target ||
											(target === 'static_route' && (st.target === 'ndms' || st.target === 'static_route'))
									)}
									{@const isFilteredOut = mode === 'append' && isPresentInTarget}
									<div class="interactive-chip ip" class:filtered-out={isFilteredOut}>
										<span class="chip-name font-mono">{ip}</span>
										{#if isPresentInTarget}
											<span class="chip-badge present">в {entityNounPlural}</span>
										{:else}
											<span class="chip-badge new">+ новый</span>
										{/if}
										<button
											type="button"
											class="chip-close"
											title="Исключить IP"
											onclick={() => removeIP(ip)}
										>
											<X size={12} />
										</button>
									</div>
								{/each}
							</div>
						</div>
					{/if}
				{:else}
					<div class="engine-limitation-notice">
						<Info size={14} />
						<span>HydraRoute Neo управляет только списками доменных имен. IP-адреса ({activeIPs.length}) автоматически исключены.</span>
					</div>
				{/if}

				{#if totalToExport === 0}
					<div class="empty-selection-notice">
						Все выбранные элементы уже есть в {entityNounPlural} или исключены.
					</div>
				{/if}
			</div>
		</div>
	</div>

	<div class="modal-footer">
		<Button variant="secondary" onclick={onclose}>Отмена</Button>
		<Button
			variant="primary"
			loading={saving}
			disabled={totalToExport === 0}
			onclick={handleExport}
		>
			<Plus size={15} />
			{#if target === 'catalog'}
				Добавить в каталог ({totalToExport})
			{:else if target === 'mihomo'}
				Добавить в Mihomo ({totalToExport})
			{:else if target === 'hydraroute'}
				Добавить в HydraRoute ({totalToExport})
			{:else if target === 'static_route'}
				Добавить IP-маршруты ({totalToExport})
			{:else}
				Добавить ({totalToExport})
			{/if}
		</Button>
	</div>
</Modal>

<style>
	.export-modal-body {
		display: flex;
		flex-direction: column;
		gap: 1rem;
		padding: 0.25rem 0;
	}

	.dedup-banner {
		display: flex;
		align-items: flex-start;
		gap: 0.65rem;
		padding: 0.65rem 0.85rem;
		background: var(--color-success-tint, rgba(16, 185, 129, 0.06));
		border: 1px solid var(--color-success-border, rgba(16, 185, 129, 0.18));
		border-radius: var(--radius-sm, 6px);
		font-size: 0.82rem;
	}

	.dedup-banner.neutral {
		background: var(--color-bg-secondary);
		border-color: var(--color-border);
	}

	.dedup-icon {
		color: var(--color-success, #10b981);
		flex-shrink: 0;
		margin-top: 0.1rem;
	}

	.dedup-icon.info {
		color: var(--color-accent);
	}

	.dedup-title {
		font-weight: 600;
		color: var(--color-text-primary);
		margin-bottom: 0.1rem;
	}

	.dedup-desc {
		font-size: 0.78rem;
		color: var(--color-text-secondary);
		line-height: 1.35;
	}

	.target-selector,
	.mode-selector {
		display: flex;
		flex-direction: column;
		gap: 0.35rem;
	}

	.section-label {
		font-size: 0.74rem;
		font-weight: 600;
		color: var(--color-text-secondary);
		text-transform: uppercase;
		letter-spacing: 0.04em;
	}

	.field-group {
		display: flex;
		flex-direction: column;
		gap: 0.35rem;
	}

	.field-label {
		font-size: 0.8rem;
		font-weight: 500;
		color: var(--color-text-primary);
	}

	.matched-suggestions {
		display: flex;
		align-items: center;
		flex-wrap: wrap;
		gap: 0.35rem;
		margin-top: 0.2rem;
	}

	.matched-suggestions-label {
		font-size: 0.72rem;
		color: var(--color-text-muted);
	}

	.suggestion-chip {
		padding: 0.1rem 0.4rem;
		background: var(--color-bg-secondary);
		border: 1px solid var(--color-border);
		border-radius: 4px;
		font-size: 0.74rem;
		color: var(--color-text-secondary);
		cursor: pointer;
		transition: border-color 0.15s ease, color 0.15s ease;
	}

	.suggestion-chip:hover,
	.suggestion-chip.active {
		border-color: var(--color-accent);
		color: var(--color-accent);
	}

	.target-desc {
		display: flex;
		align-items: flex-start;
		gap: 0.55rem;
		padding: 0.55rem 0.75rem;
		background: var(--color-bg-secondary);
		border: 1px solid var(--color-border);
		border-radius: var(--radius-sm, 6px);
		font-size: 0.8rem;
		line-height: 1.4;
		color: var(--color-text-primary);
	}

	.target-desc :global(.desc-icon) {
		flex-shrink: 0;
		margin-top: 0.15rem;
		color: var(--color-accent);
	}

	.items-selection-section {
		display: flex;
		flex-direction: column;
		gap: 0.45rem;
	}

	.selection-header {
		display: flex;
		justify-content: space-between;
		align-items: center;
		flex-wrap: wrap;
		gap: 0.5rem;
	}

	.type-toggles {
		display: flex;
		align-items: center;
		gap: 0.85rem;
	}

	.type-toggle-label {
		display: inline-flex;
		align-items: center;
		gap: 0.35rem;
		font-size: 0.78rem;
		font-weight: 500;
		color: var(--color-text-primary);
		cursor: pointer;
	}

	.chips-box {
		display: flex;
		flex-direction: column;
		gap: 0.75rem;
		max-height: 200px;
		overflow-y: auto;
		padding: 0.6rem;
		background: var(--color-bg-primary);
		border: 1px solid var(--color-border);
		border-radius: var(--radius-sm, 6px);
	}

	.chip-group {
		display: flex;
		flex-direction: column;
		gap: 0.35rem;
	}

	.chip-group-header {
		display: flex;
		align-items: center;
		gap: 0.4rem;
		font-size: 0.72rem;
		font-weight: 600;
		color: var(--color-text-muted);
		text-transform: uppercase;
	}

	.chip-sub-hint {
		font-size: 0.7rem;
		color: var(--color-text-secondary);
		font-weight: normal;
		text-transform: none;
	}

	.chips-list {
		display: flex;
		flex-wrap: wrap;
		gap: 0.35rem;
	}

	.interactive-chip {
		display: inline-flex;
		align-items: center;
		gap: 0.35rem;
		padding: 0.15rem 0.45rem;
		border-radius: 4px;
		font-size: 0.76rem;
		background: var(--color-bg-secondary);
		border: 1px solid var(--color-border);
		color: var(--color-text-primary);
		transition: opacity 0.15s ease;
	}

	.interactive-chip.filtered-out {
		opacity: 0.55;
		background: var(--color-bg-tertiary);
	}

	.chip-name {
		font-size: 0.76rem;
	}

	.chip-badge {
		font-size: 0.66rem;
		padding: 0.02rem 0.25rem;
		border-radius: 3px;
	}

	.chip-badge.present {
		background: var(--color-bg-tertiary);
		border: 1px solid var(--color-border);
		color: var(--color-text-muted);
	}

	.chip-badge.new {
		background: var(--color-bg-secondary);
		border: 1px solid var(--color-border);
		color: var(--color-text-secondary);
	}

	.chip-close {
		display: inline-flex;
		align-items: center;
		justify-content: center;
		padding: 0.1rem;
		background: transparent;
		border: none;
		border-radius: 3px;
		color: var(--color-text-muted);
		cursor: pointer;
		transition: color 0.15s ease, background 0.15s ease;
	}

	.chip-close:hover {
		color: var(--color-error, #ef4444);
		background: rgba(239, 68, 68, 0.1);
	}

	.engine-limitation-notice {
		display: flex;
		align-items: center;
		gap: 0.45rem;
		padding: 0.4rem 0.6rem;
		background: var(--color-bg-secondary);
		border: 1px dashed var(--color-border);
		border-radius: 4px;
		font-size: 0.75rem;
		color: var(--color-text-muted);
		line-height: 1.35;
	}

	.empty-selection-notice {
		padding: 0.5rem;
		font-size: 0.78rem;
		color: var(--color-text-muted);
		text-align: center;
	}

	.modal-footer {
		display: flex;
		justify-content: flex-end;
		gap: 0.65rem;
		margin-top: 1rem;
		padding-top: 0.85rem;
		border-top: 1px solid var(--color-border);
	}
</style>
