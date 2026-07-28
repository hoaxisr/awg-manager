<script lang="ts">
	import {
		Button,
		Dropdown,
		ChipMultiSelect,
		SegmentedControl,
		type DropdownOption,
		type ChipOption,
		type SegmentedOption,
	} from '$lib/components/ui';
	import SingboxSettingsModal from './SingboxSettingsModal.svelte';
	import type { SingboxRouterDNSRule, SingboxRouterDNSServer, SingboxRouterRuleSet } from '$lib/types';
	import { dnsServerSubtitle } from '$lib/components/sb-router/dnsServerDetourDisplay';

	interface Props {
		rule?: SingboxRouterDNSRule;
		servers: SingboxRouterDNSServer[];
		availableRuleSets: SingboxRouterRuleSet[];
		/**
		 * Per-tag count of how many *other* DNS rules reference each rule_set.
		 * The currently edited rule must be excluded by the caller (use
		 * computeRuleSetUsage with excludeIndex=editIndex). Empty map is fine
		 * — all sets render as unused.
		 */
		ruleSetUsage?: Map<string, number>;
		/** Весь список DNS-правил — источник кандидатов для match_response. */
		rules?: SingboxRouterDNSRule[];
		/** Позиция правила в списке; undefined при добавлении (конец списка). */
		ruleIndex?: number;
		onClose: () => void;
		onSave: (rule: SingboxRouterDNSRule) => Promise<void> | void;
	}
	let {
		rule,
		servers,
		availableRuleSets,
		ruleSetUsage,
		rules,
		ruleIndex,
		onClose,
		onSave,
	}: Props = $props();

	type ActionKind = 'route' | 'block' | 'evaluate' | 'respond';

	// Сентинел «последний анонимный evaluate» (wire-форма match_response: true).
	// \0 не может встретиться в теге, набранном пользователем.
	const MR_ANON = '\u0000anon';

	/** match_response включён: false — форма «выключено» из hand-edited конфига. */
	function matchResponseOn(mr: boolean | string | undefined): boolean {
		return mr !== undefined && mr !== false;
	}

	function normalizeTags(tags: string[]): string[] {
		return [...new Set(tags.map((s) => s.trim()).filter(Boolean))];
	}

	const serverOptions = $derived<DropdownOption[]>([
		{ value: '', label: '— выберите —' },
		...servers.map((s) => ({
			value: s.tag,
			label: s.tag,
			description: dnsServerSubtitle(s),
		})),
	]);

	// svelte-ignore state_referenced_locally
	let ruleSetTags = $state<string[]>(rule?.rule_set ?? []);
	const ruleSetOptions = $derived<ChipOption[]>(
		availableRuleSets.map((rs) => ({
			value: rs.tag,
			label: rs.tag,
			usedCount: ruleSetUsage?.get(rs.tag) ?? 0,
		})),
	);
	// svelte-ignore state_referenced_locally
	let domainSuffixStr = $state((rule?.domain_suffix ?? []).join('\n'));
	// svelte-ignore state_referenced_locally
	let domainStr = $state((rule?.domain ?? []).join('\n'));
	// svelte-ignore state_referenced_locally
	let domainKeywordStr = $state((rule?.domain_keyword ?? []).join(', '));
	// svelte-ignore state_referenced_locally
	let domainRegexStr = $state((rule?.domain_regex ?? []).join('\n'));
	// svelte-ignore state_referenced_locally
	let queryTypeStr = $state((rule?.query_type ?? []).join(', '));

	function initAction(r?: SingboxRouterDNSRule): ActionKind {
		if (r?.action === 'evaluate' || r?.action === 'respond') return r.action;
		if (r?.action === 'reject' || r?.action === 'predefined') return 'block';
		return 'route';
	}
	// A rule with NO matchers matches every query = catch-all («всё остальное»).
	// We surface the simplified «catch-all» mode for route/evaluate/respond;
	// a matcher-less block is unusual and stays in the full editor.
	function isMatcherless(r?: SingboxRouterDNSRule): boolean {
		if (!r) return false;
		return !(
			(r.rule_set?.length ?? 0) > 0 ||
			(r.domain_suffix?.length ?? 0) > 0 ||
			(r.domain?.length ?? 0) > 0 ||
			(r.domain_keyword?.length ?? 0) > 0 ||
			(r.domain_regex?.length ?? 0) > 0 ||
			(r.query_type?.length ?? 0) > 0 ||
			// source_ip_cidr is a matcher too (backend dnsRuleHasMatcher counts it);
			// a source-scoped rule must not open in catch-all mode, which would drop
			// the field on save (bug #445 review).
			(r.source_ip_cidr?.length ?? 0) > 0 ||
			// match_response/ip_cidr — матчеры ответа (sing-box 1.14, backend
			// dnsRuleHasMatcher): правило с ними не catch-all, иначе поля теряются.
			matchResponseOn(r.match_response) ||
			(r.ip_cidr?.length ?? 0) > 0
		);
	}
	function initMode(r?: SingboxRouterDNSRule): 'matchers' | 'catchall' {
		return r && isMatcherless(r) && initAction(r) !== 'block' ? 'catchall' : 'matchers';
	}
	function initBlockMethod(r?: SingboxRouterDNSRule): 'nxdomain' | 'refused' | 'drop' {
		if (r?.action === 'predefined') return 'nxdomain';
		if (r?.action === 'reject' && r?.method === 'drop') return 'drop';
		return 'refused';
	}
	function initMatchResponse(r?: SingboxRouterDNSRule): string {
		const mr = r?.match_response;
		if (mr === undefined || mr === false) return '';
		return mr === true ? MR_ANON : mr;
	}
	// svelte-ignore state_referenced_locally
	let mode = $state<'matchers' | 'catchall'>(initMode(rule));
	const catchAll = $derived(mode === 'catchall');
	// svelte-ignore state_referenced_locally
	let action = $state<ActionKind>(initAction(rule));
	// svelte-ignore state_referenced_locally
	let blockMethod = $state<'nxdomain' | 'refused' | 'drop'>(initBlockMethod(rule));
	// svelte-ignore state_referenced_locally
	let server = $state(rule?.server ?? '');
	// svelte-ignore state_referenced_locally
	let tag = $state(rule?.tag ?? '');
	// svelte-ignore state_referenced_locally
	let speculative = $state(rule?.speculative ?? false);
	// svelte-ignore state_referenced_locally
	let race = $state(rule?.race ?? false);
	// svelte-ignore state_referenced_locally
	let matchResponse = $state(initMatchResponse(rule));
	// svelte-ignore state_referenced_locally
	let responseRcode = $state(rule?.response_rcode ?? '');
	// svelte-ignore state_referenced_locally
	let ipCidrStr = $state((rule?.ip_cidr ?? []).join('\n'));
	// svelte-ignore state_referenced_locally
	let responseAnswerStr = $state((rule?.response_answer ?? []).join('\n'));
	// svelte-ignore state_referenced_locally
	let responseNsStr = $state((rule?.response_ns ?? []).join('\n'));
	// svelte-ignore state_referenced_locally
	let responseExtraStr = $state((rule?.response_extra ?? []).join('\n'));

	const blockMethodOptions = [
		{ value: 'nxdomain', label: 'NXDOMAIN (нет такого домена)' },
		{ value: 'refused', label: 'REFUSED' },
		{ value: 'drop', label: 'Drop (без ответа)' },
	];

	const RCODES = [
		'NOERROR', 'FORMERR', 'SERVFAIL', 'NXDOMAIN', 'NOTIMP', 'REFUSED', 'YXDOMAIN',
		'YXRRSET', 'NXRRSET', 'NOTAUTH', 'NOTZONE', 'BADSIG', 'BADKEY', 'BADTIME',
		'BADMODE', 'BADNAME', 'BADALG', 'BADTRUNC', 'BADCOOKIE',
	];
	const rcodeOptions: DropdownOption[] = [
		{ value: '', label: '— не задан —' },
		...RCODES.map((c) => ({ value: c, label: c })),
	];

	const actionOptions: SegmentedOption<ActionKind>[] = [
		{ value: 'route', label: 'Маршрут' },
		{ value: 'block', label: 'Блокировка' },
		{ value: 'evaluate', label: 'Evaluate' },
		{ value: 'respond', label: 'Respond' },
	];
	// Правило без условий блокирует/отвечает на ВСЁ — block в catch-all не даём.
	const catchAllActionOptions = actionOptions.filter((o) => o.value !== 'block');

	const modeOptions: SegmentedOption<'matchers' | 'catchall'>[] = [
		{ value: 'matchers', label: 'По условиям' },
		{ value: 'catchall', label: 'Catch-all (всё остальное)' },
	];

	function setMode(next: 'matchers' | 'catchall'): void {
		mode = next;
		if (next === 'catchall' && action === 'block') action = 'route';
	}

	// Кандидаты match_response — только evaluate-правила ВЫШЕ редактируемого:
	// зеркалит backend firstDNSChainViolation, иначе гарантированный 4xx.
	const rulesAbove = $derived((rules ?? []).slice(0, ruleIndex ?? (rules ?? []).length));
	const matchResponseOptions = $derived.by<DropdownOption[]>(() => {
		const opts: DropdownOption[] = [{ value: '', label: 'выкл' }];
		if (rulesAbove.some((r) => r.action === 'evaluate' && !r.tag)) {
			opts.push({ value: MR_ANON, label: 'последний анонимный evaluate' });
		}
		for (const t of new Set(
			rulesAbove.filter((r) => r.action === 'evaluate' && r.tag).map((r) => r.tag as string),
		)) {
			opts.push({ value: t, label: t });
		}
		// Значение из hand-edited конфига вне кандидатов: показываем как есть,
		// иначе дропдаун выглядит пустым при выставленном поле.
		if (matchResponse && !opts.some((o) => o.value === matchResponse)) {
			opts.push({
				value: matchResponse,
				label: matchResponse === MR_ANON ? 'последний анонимный evaluate' : matchResponse,
			});
		}
		return opts;
	});
	// Кроме «выкл» вариантов нет — привязывать ответ не к чему.
	const noEvaluateAbove = $derived(matchResponseOptions.length === 1);
	// Секция «По DNS-ответу» доступна только в режиме matchers.
	const responseOn = $derived(!catchAll && matchResponse !== '');
	const speculativeOn = $derived(speculative && (action === 'route' || action === 'evaluate'));
	const raceOn = $derived(race && responseOn && action !== 'evaluate');

	let busy = $state(false);
	let error = $state('');

	// Snapshot initial state for isDirty detection
	let initialRuleSetTagsSnapshot = $state<string[]>([]);
	let initialDomainSuffixStr = $state('');
	let initialDomainStr = $state('');
	let initialDomainKeywordStr = $state('');
	let initialDomainRegexStr = $state('');
	let initialQueryTypeStr = $state('');
	let initialAction: ActionKind = $state('route');
	let initialBlockMethod: 'nxdomain' | 'refused' | 'drop' = $state('refused');
	let initialServer = $state('');
	let initialMode: 'matchers' | 'catchall' = $state('matchers');
	let initialTag = $state('');
	let initialSpeculative = $state(false);
	let initialRace = $state(false);
	let initialMatchResponse = $state('');
	let initialResponseRcode = $state('');
	let initialIpCidrStr = $state('');
	let initialResponseAnswerStr = $state('');
	let initialResponseNsStr = $state('');
	let initialResponseExtraStr = $state('');

	// Initialize snapshot when modal opens
	$effect(() => {
		if (rule) {
			initialRuleSetTagsSnapshot = [...(rule.rule_set ?? [])];
			initialDomainSuffixStr = (rule.domain_suffix ?? []).join('\n');
			initialDomainStr = (rule.domain ?? []).join('\n');
			initialDomainKeywordStr = (rule.domain_keyword ?? []).join(', ');
			initialDomainRegexStr = (rule.domain_regex ?? []).join('\n');
			initialQueryTypeStr = (rule.query_type ?? []).join(', ');
			initialAction = initAction(rule);
			initialBlockMethod = initBlockMethod(rule);
			initialServer = rule.server ?? '';
			initialMode = initMode(rule);
			initialTag = rule.tag ?? '';
			initialSpeculative = rule.speculative ?? false;
			initialRace = rule.race ?? false;
			initialMatchResponse = initMatchResponse(rule);
			initialResponseRcode = rule.response_rcode ?? '';
			initialIpCidrStr = (rule.ip_cidr ?? []).join('\n');
			initialResponseAnswerStr = (rule.response_answer ?? []).join('\n');
			initialResponseNsStr = (rule.response_ns ?? []).join('\n');
			initialResponseExtraStr = (rule.response_extra ?? []).join('\n');
		} else {
			initialRuleSetTagsSnapshot = [];
			initialDomainSuffixStr = '';
			initialDomainStr = '';
			initialDomainKeywordStr = '';
			initialDomainRegexStr = '';
			initialQueryTypeStr = '';
			initialAction = 'route';
			initialBlockMethod = 'refused';
			initialServer = '';
			initialMode = 'matchers';
			initialTag = '';
			initialSpeculative = false;
			initialRace = false;
			initialMatchResponse = '';
			initialResponseRcode = '';
			initialIpCidrStr = '';
			initialResponseAnswerStr = '';
			initialResponseNsStr = '';
			initialResponseExtraStr = '';
		}
	});

	const isDirty = $derived.by(() => {
		return (
			normalizeTags(ruleSetTags).join(',') !== normalizeTags(initialRuleSetTagsSnapshot).join(',') ||
			domainSuffixStr !== initialDomainSuffixStr ||
			domainStr !== initialDomainStr ||
			domainKeywordStr !== initialDomainKeywordStr ||
			domainRegexStr !== initialDomainRegexStr ||
			queryTypeStr !== initialQueryTypeStr ||
			action !== initialAction ||
			blockMethod !== initialBlockMethod ||
			server !== initialServer ||
			mode !== initialMode ||
			tag !== initialTag ||
			speculative !== initialSpeculative ||
			race !== initialRace ||
			matchResponse !== initialMatchResponse ||
			responseRcode !== initialResponseRcode ||
			ipCidrStr !== initialIpCidrStr ||
			responseAnswerStr !== initialResponseAnswerStr ||
			responseNsStr !== initialResponseNsStr ||
			responseExtraStr !== initialResponseExtraStr
		);
	});

	async function save(): Promise<void> {
		busy = true;
		error = '';
		try {
			const rule_set = normalizeTags(ruleSetTags);
			const domain_suffix = domainSuffixStr.split('\n').map((s) => s.trim()).filter(Boolean);
			const domain = domainStr.split('\n').map((s) => s.trim()).filter(Boolean);
			const domain_keyword = domainKeywordStr.split(',').map((s) => s.trim()).filter(Boolean);
			const domain_regex = domainRegexStr.split('\n').map((s) => s.trim()).filter(Boolean);
			const query_type = queryTypeStr.split(',').map((s) => s.trim().toUpperCase()).filter(Boolean);

			const ip_cidr = ipCidrStr.split('\n').map((s) => s.trim()).filter(Boolean);
			const response_answer = responseAnswerStr.split('\n').map((s) => s.trim()).filter(Boolean);
			const response_ns = responseNsStr.split('\n').map((s) => s.trim()).filter(Boolean);
			const response_extra = responseExtraStr.split('\n').map((s) => s.trim()).filter(Boolean);

			const hasMatcher =
				rule_set.length > 0 ||
				domain_suffix.length > 0 ||
				domain.length > 0 ||
				domain_keyword.length > 0 ||
				domain_regex.length > 0 ||
				query_type.length > 0 ||
				// match_response — полноценный матчер (backend dnsRuleHasMatcher):
				// respond с одним match_response обязан проходить гейт.
				responseOn;
			// Catch-all mode intentionally ships ZERO matchers (matches everything);
			// otherwise at least one matcher is required.
			if (!catchAll && !hasMatcher) {
				error = 'Нужен хотя бы один matcher';
				busy = false;
				return;
			}

			// In catch-all mode the matcher inputs are hidden — never serialize them,
			// even if the user had typed something before switching mode.
			const built: SingboxRouterDNSRule = catchAll
				? {}
				: {
						rule_set: rule_set.length ? rule_set : undefined,
						domain_suffix: domain_suffix.length ? domain_suffix : undefined,
						domain: domain.length ? domain : undefined,
						domain_keyword: domain_keyword.length ? domain_keyword : undefined,
						domain_regex: domain_regex.length ? domain_regex : undefined,
						query_type: query_type.length ? query_type : undefined,
					};

			// Поля ответа sing-box 1.14 живут только вместе с match_response
			// (иначе backend отвергает правило).
			if (responseOn) {
				built.match_response = matchResponse === MR_ANON ? true : matchResponse;
				if (ip_cidr.length) built.ip_cidr = ip_cidr;
				if (responseRcode) built.response_rcode = responseRcode;
				if (response_answer.length) built.response_answer = response_answer;
				if (response_ns.length) built.response_ns = response_ns;
				if (response_extra.length) built.response_extra = response_extra;
				if (raceOn) built.race = true;
			}

			if (action === 'route' || action === 'evaluate') {
				if (!server) { error = 'Выберите DNS сервер'; busy = false; return; }
				built.action = action;
				built.server = server;
				if (action === 'evaluate' && tag.trim()) built.tag = tag.trim();
				if (speculativeOn) built.speculative = true;
			} else if (action === 'respond') {
				built.action = 'respond';
			} else if (blockMethod === 'nxdomain') {
				built.action = 'predefined';
				built.rcode = 'NXDOMAIN';
			} else if (blockMethod === 'drop') {
				built.action = 'reject';
				built.method = 'drop';
			} else {
				built.action = 'reject';
				built.method = 'default';
			}

			if (built.race && built.speculative) {
				error = 'race и speculative несовместимы';
				busy = false;
				return;
			}

			await onSave(built);
		} catch (e) {
			error = (e as Error).message;
		} finally {
			busy = false;
		}
	}
</script>

{#snippet actionFields(serverLabel: string)}
	{#if action === 'route' || action === 'evaluate'}
		<label class="field">
			<div class="lbl">{serverLabel}</div>
			<Dropdown bind:value={server} options={serverOptions} fullWidth />
		</label>
		{#if action === 'evaluate'}
			<label class="field">
				<div class="lbl">Тег ответа</div>
				<input bind:value={tag} placeholder="rd" />
			</label>
		{/if}
		<label class="toggle">
			<input type="checkbox" bind:checked={speculative} />
			<span>Спекулятивный запрос</span>
		</label>
	{:else if action === 'block'}
		<label class="field">
			<div class="lbl">Метод блокировки</div>
			<Dropdown bind:value={blockMethod} options={blockMethodOptions} fullWidth />
		</label>
	{/if}
{/snippet}

<SingboxSettingsModal
	title={rule ? 'Редактировать DNS правило' : 'Новое DNS правило'}
	onClose={onClose}
	size="lg"
	hasUnsavedChanges={() => isDirty}
>
	<div class="form">
		<div class="section-label">Тип правила</div>
		<SegmentedControl
			value={mode}
			options={modeOptions}
			ariaLabel="Тип DNS правила"
			onchange={(next) => setMode(next)}
		/>

		{#if catchAll}
			<div class="warn">
				Правило без условий совпадает со <b>всеми</b> запросами. Любые правила
				<b>ниже</b> него в списке игнорируются — держите его последним.
			</div>
			<div class="action-section">
				<div class="section-label">Действие</div>
				<SegmentedControl
					value={action}
					options={catchAllActionOptions}
					ariaLabel="Действие DNS правила"
					onchange={(next) => (action = next)}
				/>
				{@render actionFields('DNS сервер (для всех запросов)')}
			</div>
			<div class="hint">
				Для простого запасного сервера обычно достаточно «Final-сервера» в «DNS по умолчанию».
				Если задать и его, и catch-all правило — правило важнее (проверяется раньше final).
			</div>
		{:else}
			<div class="section-label">Matchers (минимум один)</div>

			<!-- div, не label: клик по любой не-интерактивной части label активирует
			     его первый labelable-элемент — крестик ПЕРВОГО чипа, т.е. клик по
			     названию любого rule-set удалял первый (bug #446). -->
			<div class="field">
				<div class="lbl">Rule sets</div>
				<ChipMultiSelect
					values={ruleSetTags}
					options={ruleSetOptions}
					onchange={(next) => (ruleSetTags = next)}
					placeholder="не выбрано"
					allowOrphans
				/>
			</div>

			<label class="field">
				<div class="lbl">Domain suffix</div>
				<textarea bind:value={domainSuffixStr} rows="3" placeholder="по одному на строке: .youtube.com"></textarea>
			</label>

			<label class="field">
				<div class="lbl">Domain (точное совпадение)</div>
				<textarea bind:value={domainStr} rows="2" placeholder="example.com"></textarea>
			</label>

			<label class="field">
				<div class="lbl">Domain keyword (через запятую)</div>
				<input bind:value={domainKeywordStr} placeholder="tracker, analytics" />
			</label>

			<label class="field">
				<div class="lbl">Domain regex (по строке)</div>
				<textarea bind:value={domainRegexStr} rows="2" placeholder={"^ads?\\d+\\."}></textarea>
			</label>

			<label class="field">
				<div class="lbl">Query type (через запятую)</div>
				<input bind:value={queryTypeStr} placeholder="A, AAAA, HTTPS" />
			</label>

			<div class="action-section">
				<div class="section-label">Действие</div>
				<SegmentedControl
					value={action}
					options={actionOptions}
					ariaLabel="Действие DNS правила"
					onchange={(next) => (action = next)}
				/>

				{@render actionFields('DNS сервер')}
			</div>

			<section class="form-section form-section-divided">
				<div class="section-label">По DNS-ответу</div>
				<label class="field">
					<div class="lbl">Ответ evaluate</div>
					<Dropdown bind:value={matchResponse} options={matchResponseOptions} fullWidth />
				</label>
				{#if noEvaluateAbove}
					<div class="hint">
						Нет evaluate-правил выше — сначала создайте правило с действием Evaluate
					</div>
				{/if}

				{#if responseOn}
					<label class="field">
						<div class="lbl">Rcode</div>
						<Dropdown bind:value={responseRcode} options={rcodeOptions} fullWidth />
					</label>

					<label class="field">
						<div class="lbl">IP ответа (CIDR, по строке)</div>
						<textarea bind:value={ipCidrStr} rows="2" placeholder="10.0.0.0/8"></textarea>
					</label>

					<details>
						<summary>RR-записи</summary>
						<label class="field">
							<div class="lbl">Answer (по строке)</div>
							<textarea bind:value={responseAnswerStr} rows="2"></textarea>
						</label>
						<label class="field">
							<div class="lbl">NS (по строке)</div>
							<textarea bind:value={responseNsStr} rows="2"></textarea>
						</label>
						<label class="field">
							<div class="lbl">Extra (по строке)</div>
							<textarea bind:value={responseExtraStr} rows="2"></textarea>
						</label>
					</details>

					{#if action !== 'evaluate'}
						<label class="toggle">
							<input type="checkbox" bind:checked={race} />
							<span>Race</span>
						</label>
					{/if}
				{/if}
			</section>
		{/if}

		{#if error}<div class="error">{error}</div>{/if}
	</div>

	{#snippet actions()}
		<Button variant="ghost" size="md" onclick={onClose} type="button">Отмена</Button>
		<Button variant="primary" size="md" onclick={save} disabled={busy} loading={busy} type="button">
			Сохранить
		</Button>
	{/snippet}
</SingboxSettingsModal>
