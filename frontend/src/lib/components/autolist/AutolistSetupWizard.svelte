<script lang="ts">
	import { m } from '$lib/i18n';
	import { api } from '$lib/api/client';
	import { notifications } from '$lib/stores/notifications';
	import { errorMessage } from '$lib/utils/errorMessage';
	import { buildAutolistConfigPayload } from './configPayload';
	import { autolistRiskItems } from './autoAddRisks';
	import { buildDnsOptions, buildTunnelInterfaceOptions } from './options';
	import { Button, ChipMultiSelect, Dropdown, Modal, Toggle, WizardSteps } from '$lib/components/ui';
	import { TriangleAlert } from 'lucide-svelte';
	import type {
		AWGTagInfo,
		AutolistConfig,
		RouterInterface,
		SingboxRouterDNSServer,
		SingboxRouterDNSGlobals,
		SingboxRouterRuleSet,
		SingboxTunnel,
		Subscription,
	} from '$lib/types';

	interface Props {
		open: boolean;
		onclose: () => void;
		/** После успешного сохранения: invalidate статуса и списков во вкладке. */
		onsaved: () => void;
		/** fetchProbeResources вкладки: каталог туннелей, DNS и rule-sets. */
		onfetchresources: () => void;
		awgTagInfos: AWGTagInfo[];
		singboxTunnels: SingboxTunnel[];
		subscriptions: Subscription[];
		interfaces: RouterInterface[];
		dnsServers: SingboxRouterDNSServer[];
		dnsGlobals: SingboxRouterDNSGlobals | null;
		exclusionRuleSets: SingboxRouterRuleSet[];
	}

	let {
		open,
		onclose,
		onsaved,
		onfetchresources,
		awgTagInfos,
		singboxTunnels,
		subscriptions,
		interfaces,
		dnsServers,
		dnsGlobals,
		exclusionRuleSets,
	}: Props = $props();

	interface WizardDraft {
		targetInterface: string;
		/** false — только сайты (TCP/443 и DNS/SNI); true — ещё STUN/TURN UDP. */
		udpEnabled: boolean;
		/** '' — dns.final из sing-box. */
		resolveVia: string;
		autoAdd: boolean;
		riskAcknowledged: boolean;
		excludeDomains: string[];
		excludeRuleSets: string[];
	}

	/** Конфиг-основа слияния: поля, которые мастер не трогает, сохраняются. */
	let baseCfg = $state<AutolistConfig | null>(null);
	let draft = $state<WizardDraft>({
		targetInterface: '',
		udpEnabled: false,
		resolveVia: '',
		autoAdd: false,
		riskAcknowledged: false,
		excludeDomains: [],
		excludeRuleSets: [],
	});
	let current = $state(0);
	let loading = $state(false);
	let busy = $state(false);
	let loadError = $state('');
	/** Предупреждение после «Пропустить» на шаге исключений. */
	let skipWarningOpen = $state(false);
	let skipAcknowledged = $state(false);

	let loadGen = 0;
	let reloadTick = $state(0);
	$effect(() => {
		if (!open) return;
		// reloadTick — явный повтор загрузки после ошибки чтения конфига.
		void reloadTick;
		const gen = ++loadGen;
		loading = true;
		loadError = '';
		current = 0;
		skipWarningOpen = false;
		skipAcknowledged = false;
		onfetchresources();
		void (async () => {
			try {
				const cfg = await api.getAutolistConfig();
				if (gen !== loadGen) return;
				baseCfg = cfg;
				draft = {
					targetInterface: cfg.targetInterface ?? '',
					udpEnabled: cfg.udpEnabled === true,
					resolveVia: cfg.resolveVia ?? '',
					autoAdd: cfg.autoAdd === true,
					riskAcknowledged: false,
					excludeDomains: cfg.excludeDomains ?? [],
					excludeRuleSets: cfg.excludeRuleSets ?? [],
				};
				loading = false;
			} catch (e) {
				if (gen !== loadGen) return;
				loadError = errorMessage(e, m.autolist_cfg_read_err());
				loading = false;
			}
		})();
	});

	// Закрытие гасит незавершённую загрузку: ответ не должен перезаписать
	// состояние уже закрытого мастера.
	$effect(() => {
		if (!open) loadGen++;
	});

	let stepLabels = $derived(
		draft.autoAdd
			? [
					m.autolist_wizard_step_tunnel(),
					m.autolist_wizard_step_dns(),
					m.autolist_wizard_step_mode(),
					m.autolist_wizard_step_risks(),
					m.autolist_wizard_step_exclusions(),
				]
			: [
					m.autolist_wizard_step_tunnel(),
					m.autolist_wizard_step_dns(),
					m.autolist_wizard_step_mode(),
				],
	);

	/** Гейт перехода «Дальше»: туннель обязателен, риски — явное согласие. */
	let canNext = $derived.by(() => {
		if (loading || !baseCfg) return false;
		if (current === 0) return draft.targetInterface !== '';
		if (current === 3 && draft.autoAdd) return draft.riskAcknowledged;
		return true;
	});

	function go(i: number) {
		if (busy) return;
		if (i <= current) {
			current = i;
			skipWarningOpen = false;
			skipAcknowledged = false;
		} else if (i === current + 1 && canNext) {
			current = i;
		}
	}

	/** Смена режима наблюдения: значение само хранится в udpEnabled. */
	function setObserveMode(mode: string) {
		draft = { ...draft, udpEnabled: mode === 'sites-udp' };
	}

	function setMode(mode: string) {
		const autoAdd = mode === 'auto';
		// Свежий переход в AutoAdd требует свежего подтверждения рисков.
		draft = { ...draft, autoAdd, riskAcknowledged: autoAdd ? false : draft.riskAcknowledged };
	}

	function setExcludedDomains(raw: string) {
		const domains = raw
			.split(/[\s,]+/)
			.map((d) => d.trim().toLowerCase().replace(/\.$/, ''))
			.filter((d) => d !== '');
		draft = { ...draft, excludeDomains: [...new Set(domains)] };
	}

	let tunnelOptions = $derived(
		buildTunnelInterfaceOptions({
			awgTagInfos,
			singboxTunnels,
			subscriptions,
			interfaces,
			current: draft.targetInterface,
		}),
	);

	let observeOptions = $derived([
		{ value: 'sites', label: m.autolist_wizard_observe_sites(), description: m.autolist_wizard_observe_sites_desc() },
		{ value: 'sites-udp', label: m.autolist_wizard_observe_udp(), description: m.autolist_wizard_observe_udp_desc() },
	]);

	let modeOptions = $derived([
		{ value: 'manual', label: m.autolist_wizard_mode_manual(), description: m.autolist_wizard_mode_manual_desc() },
		{ value: 'auto', label: m.autolist_wizard_mode_auto(), description: m.autolist_wizard_mode_auto_desc() },
	]);

	let dnsOptions = $derived(buildDnsOptions({ dnsServers, dnsGlobals, current: draft.resolveVia }));

	let exclusionRuleSetOptions = $derived(exclusionRuleSets.map((rs) => ({ value: rs.tag, label: rs.tag })));

	let emptyExclusions = $derived(draft.excludeDomains.length === 0 && draft.excludeRuleSets.length === 0);
	let finishDisabled = $derived(!baseCfg || busy || (skipWarningOpen && !skipAcknowledged));

	$effect(() => {
		if (!emptyExclusions) {
			skipWarningOpen = false;
			skipAcknowledged = false;
		}
	});

	async function complete() {
		if (!baseCfg || finishDisabled) return;
		if (draft.autoAdd && emptyExclusions && !skipWarningOpen) {
			skipWarningOpen = true;
			return;
		}
		busy = true;
		try {
			const payload = buildAutolistConfigPayload({
				...baseCfg,
				targetInterface: draft.targetInterface,
				udpEnabled: draft.udpEnabled,
				resolveVia: draft.resolveVia,
				autoAdd: draft.autoAdd,
				// AutoAdd допустим только при включённой tunnel-пробе.
				tunnelProbe: draft.autoAdd ? true : baseCfg.tunnelProbe,
				excludeDomains: draft.excludeDomains,
				excludeRuleSets: draft.excludeRuleSets,
			});
			await api.setAutolistConfig(payload);
			notifications.success(m.autolist_wizard_saved());
			onsaved();
			onclose();
		} catch (e) {
			notifications.error(errorMessage(e, m.autolist_wizard_err()));
		} finally {
			busy = false;
		}
	}
</script>

<Modal {open} title={m.autolist_wizard_title()} {onclose} width="720px">
	<p class="wizard-subtitle">{m.autolist_wizard_subtitle()}</p>
	{#if loading}
		<p class="text-sm opacity-60 py-6">…</p>
	{:else if loadError}
		<div class="flex flex-col gap-3 py-4">
			<p class="text-sm text-error break-all">{loadError}</p>
			<div>
				<Button variant="secondary" size="sm" onclick={() => reloadTick++}>{m.common_retry()}</Button>
			</div>
		</div>
	{:else}
		<div>
				<WizardSteps steps={stepLabels} {current} {canNext} {busy} ongo={go}>
					{#snippet finish()}
						<Button variant="primary" size="md" onclick={() => void complete()} disabled={finishDisabled} loading={busy}>
							{m.autolist_wizard_finish()}
						</Button>
					{/snippet}
					{#if current === 0}
						<div class="flex flex-col gap-4">
							<div class="flex flex-col gap-1.5">
								<span class="text-sm font-medium">{m.autolist_cfg_target()}</span>
								<Dropdown
									value={draft.targetInterface}
									options={tunnelOptions}
									placeholder={m.autolist_cfg_target_placeholder()}
									onchange={(v) => (draft = { ...draft, targetInterface: v })}
									fullWidth
								/>
								<span class="text-xs opacity-60">{m.autolist_wizard_tunnel_required()}</span>
							</div>
							<div class="flex flex-col gap-1.5">
								<span class="text-sm font-medium">{m.autolist_wizard_observe_label()}</span>
								<Dropdown
									value={draft.udpEnabled ? 'sites-udp' : 'sites'}
									options={observeOptions}
									onchange={setObserveMode}
									fullWidth
								/>
								{#if draft.udpEnabled}
									<span class="text-xs text-warning">{m.autolist_wizard_observe_udp_desc()}</span>
								{/if}
							</div>
						</div>
					{:else if current === 1}
						<div class="flex flex-col gap-1.5">
							<span class="text-sm font-medium">{m.autolist_cfg_dns()}</span>
							<Dropdown
								value={draft.resolveVia}
								options={dnsOptions}
								onchange={(v) => (draft = { ...draft, resolveVia: v })}
								fullWidth
							/>
							<span class="text-xs opacity-60">{m.autolist_cfg_dns_hint()}</span>
						</div>
					{:else if current === 2}
						<div class="flex flex-col gap-3">
							<div class="flex flex-col gap-1.5">
								<span class="text-sm font-medium">{m.autolist_wizard_mode_label()}</span>
								<Dropdown
									value={draft.autoAdd ? 'auto' : 'manual'}
									options={modeOptions}
									onchange={setMode}
									fullWidth
								/>
							</div>
							{#if draft.autoAdd}
								<div class="autolist-warning flex items-start gap-2.5 rounded-lg bg-warning/10 border border-warning/30">
									<TriangleAlert size={18} class="text-warning shrink-0 mt-0.5" aria-hidden="true" />
									<p class="text-sm">{m.autolist_wizard_mode_auto_warning()}</p>
								</div>
							{/if}
						</div>
					{:else if current === 3}
						<div class="flex flex-col gap-3">
							<span class="text-sm font-semibold">{m.autolist_wizard_risks_title()}</span>
							<div class="autolist-warning flex flex-col gap-3 rounded-lg bg-error/10 border border-error/30">
								<div class="flex items-start gap-2.5">
									<TriangleAlert size={18} class="text-error shrink-0 mt-0.5" aria-hidden="true" />
									<p class="text-sm font-semibold">{m.autolist_wizard_risks_intro()}</p>
								</div>
								<ul class="risk-list text-sm">
									{#each autolistRiskItems() as item (item)}
										<li>{item}</li>
									{/each}
								</ul>
								<div class="risk-ack flex items-start justify-between gap-3">
									<span class="text-sm">{m.autolist_wizard_risks_ack()}</span>
									<Toggle
										checked={draft.riskAcknowledged}
										controlled
										onchange={(v) => (draft = { ...draft, riskAcknowledged: v })}
										ariaLabel={m.autolist_wizard_risks_ack()}
									/>
								</div>
							</div>
						</div>
					{:else if current === 4}
						<div class="flex flex-col gap-4">
							<p class="text-sm opacity-80">{m.autolist_wizard_exclusions_hint()}</p>
							<div class="flex flex-col gap-1.5">
								<span class="text-sm font-medium">{m.autolist_cfg_exclude_domains()}</span>
								<textarea
									class="input-control font-mono"
									rows="3"
									value={draft.excludeDomains.join('\n')}
									oninput={(e) => setExcludedDomains(e.currentTarget.value)}
									placeholder="example.com, sub.example.org"
								></textarea>
								<span class="text-xs opacity-60">{m.autolist_cfg_exclude_domains_hint()}</span>
							</div>
							<div class="flex flex-col gap-1.5">
								<span class="text-sm font-medium">{m.autolist_cfg_exclude_rulesets()}</span>
								<ChipMultiSelect
									values={draft.excludeRuleSets}
									options={exclusionRuleSetOptions}
									onchange={(values) => (draft = { ...draft, excludeRuleSets: values })}
									allowOrphans
								/>
								<span class="text-xs opacity-60">{m.autolist_cfg_exclude_rulesets_hint()}</span>
							</div>
							{#if skipWarningOpen}
								<div class="autolist-warning flex flex-col gap-3 rounded-lg bg-error/10 border border-error/30">
									<div class="flex items-start gap-2.5">
										<TriangleAlert size={18} class="text-error shrink-0 mt-0.5" aria-hidden="true" />
										<div class="flex flex-col gap-1">
											<span class="text-sm font-medium">{m.autolist_wizard_skip_warning_title()}</span>
											<p class="text-sm opacity-80">{m.autolist_wizard_skip_warning_text()}</p>
										</div>
									</div>
									<div class="flex items-start justify-between gap-3">
										<span class="text-sm">{m.autolist_wizard_skip_ack()}</span>
										<Toggle
											checked={skipAcknowledged}
											controlled
											onchange={(v) => (skipAcknowledged = v)}
											ariaLabel={m.autolist_wizard_skip_ack()}
										/>
									</div>
								</div>
							{/if}
						</div>
					{/if}
				</WizardSteps>
		</div>
	{/if}
</Modal>

<style>
	.wizard-subtitle {
		margin: 0 0 0.75rem;
		font-size: var(--fs-sm, 0.8125rem);
		color: var(--text-muted);
	}
	.risk-list {
		margin: 0;
		padding: 0;
		list-style: none;
		display: flex;
		flex-direction: column;
		gap: 0.5rem;
		line-height: 1.45;
	}
	.risk-list li {
		position: relative;
		padding-left: 1rem;
	}
	.risk-list li::before {
		content: '•';
		position: absolute;
		left: 0;
		color: var(--color-error);
	}
	.risk-ack {
		border-top: 1px solid color-mix(in srgb, var(--color-error) 30%, transparent);
		padding-top: 0.75rem;
		margin-top: 0.25rem;
		align-items: center;
	}
	/* Тот же отступ предупреждений, что в AutolistTab: p-3 не покрывается
	   генерацией utility для этой вкладки. */
	.autolist-warning {
		padding: 0.875rem 1rem;
	}
</style>
