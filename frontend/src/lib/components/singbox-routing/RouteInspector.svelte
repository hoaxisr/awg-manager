<script lang="ts">
	import { Modal, Button } from '$lib/components/ui';
	import { Globe } from 'lucide-svelte';
	import { api } from '$lib/api/client';
	import { notifications } from '$lib/stores/notifications';
	import { awgTags as awgTagsStore } from '$lib/stores/awgTags';
	import { subscriptionsStore } from '$lib/stores/subscriptions';
	import { m } from '$lib/i18n';
	import type {
		SingboxRouterInspectResult,
		SingboxRouterInspectMatch,
		SingboxRouterInspectProgress,
	} from '$lib/types';

	interface Props {
		open: boolean;
		engine?: 'sing-box' | 'mihomo';
		onClose: () => void;
	}

	let { open, engine = 'sing-box', onClose }: Props = $props();

	const nameContext = $derived({
		awgTags: $awgTagsStore.data,
		subscriptions: $subscriptionsStore.data,
	});

	function humanize(text?: string | null): string {
		if (!text) return '';
		let out = text;
		if (nameContext.awgTags) {
			for (const t of nameContext.awgTags) {
				if (t.tag && t.label) {
					out = out.split(`awg-sys-${t.tag}`).join(t.label);
					out = out.split(`awg-${t.tag}`).join(t.label);
					out = out.split(t.tag).join(t.label);
				}
			}
		}
		if (nameContext.subscriptions) {
			for (const s of nameContext.subscriptions) {
				if (s.selectorTag && s.label) {
					out = out.split(s.selectorTag).join(s.label);
				}
			}
		}
		out = out.replace(/awg-sys-([a-zA-Z0-9_-]+)/g, '$1');
		out = out.replace(/awg-([a-zA-Z0-9_-]+)/g, '$1');
		return out;
	}

	let inputValue = $state('');
	let port = $state<number | ''>('');
	let protocol = $state<'' | 'tcp' | 'udp'>('');
	let advancedOpen = $state(false);
	let testing = $state(false);
	let inspectRunId = $state(0);
	let inspectStartedAt = $state<number | null>(null);
	let elapsedSec = $state(0);
	let progressTimer = $state<ReturnType<typeof setInterval> | null>(null);
	let inspectStream = $state<EventSource | null>(null);
	type StepStatus = 'done' | 'current' | 'next' | 'miss' | 'matched' | 'error' | 'info';
	interface InspectorStep {
		id: string;
		label: string;
		/** Код сгенерированной подписи шага (перевод выводится при рендере). */
		labelKind?: 'wait_config' | 'finish_rule_set' | 'next_rule' | 'final' | 'wait_step';
		labelRule?: number;
		labelTotal?: number;
		status: StepStatus;
		startedAt?: number;
		finishedAt?: number;
		durationMs?: number;
		ruleIndex?: number;
		ruleTotal?: number;
		ruleSetTag?: string;
		phase?: string;
	}
	interface InspectorReport {
		totalDurationMs: number;
		checkedRules: number;
		totalRules: number;
		destination: string;
		final: string;
		matchedRule: number;
		slowestSteps: InspectorStep[];
		ruleSetSteps: InspectorStep[];
	}
	/** Сообщение backend; null — ещё не пришло (показываем запасной текст). */
	let currentProgressMessage = $state<string | null>(null);
	let previousStep = $state<InspectorStep | null>(null);
	let currentStep = $state<InspectorStep | null>(null);
	let nextStep = $state<InspectorStep | null>(null);
	let completedSteps = $state<InspectorStep[]>([]);
	let activeStepStartedAt = $state<Record<string, number>>({});
	let checkedRuleIndexes = $state<Set<number>>(new Set());
	let totalRules = $state(0);
	let currentRule = $state<number | null>(null);
	let activeRuleSetTag = $state('');
	let inspectionStartedAt = $state<number | null>(null);
	let inspectionReport = $state<InspectorReport | null>(null);
	let result = $state<SingboxRouterInspectResult | null>(null);
	let error = $state('');
	let showAllRules = $state(false);

	const examples = [
		'google.com',
		'youtube.com',
		'instagram.com',
		'8.8.8.8',
		'192.168.1.1',
	];

	function stopProgressTimer(): void {
		if (progressTimer) {
			clearInterval(progressTimer);
			progressTimer = null;
		}
		elapsedSec = 0;
	}

	function progressKind(progress: SingboxRouterInspectProgress): StepStatus {
		switch (progress.phase) {
			case 'rule_start':
			case 'rule_set_start':
			case 'rule_set_cache_check':
			case 'rule_set_download_start':
			case 'rule_set_match_start':
			case 'load_config':
				return 'current';
			case 'rule_done':
				if (/не совпало/i.test(progress.message)) return 'miss';
				if (/совпало/i.test(progress.message)) return 'matched';
				return 'done';
			case 'rule_set_match_done':
				return /не совпал/i.test(progress.message) ? 'miss' : 'matched';
			case 'terminal_match':
			case 'non_terminal_match':
			case 'rule_set_cache_hit':
			case 'rule_set_download_done':
			case 'config_loaded':
			case 'classify_input':
			case 'done':
				return 'done';
			case 'rule_set_download_error':
			case 'rule_set_match_error':
			case 'rule_set_undefined':
				return 'error';
			default:
				return 'info';
		}
	}

	function formatDuration(ms?: number): string {
		if (!ms || ms < 1000) return '';
		return m.format_duration_seconds({ seconds: (ms / 1000).toFixed(ms < 10000 ? 1 : 0) });
	}

	function stepLabel(step: InspectorStep): string {
		switch (step.labelKind) {
			case 'wait_config':
				return m.singbox_routing_inspector_next_wait_config();
			case 'finish_rule_set':
				return m.singbox_routing_inspector_next_finish_rule_set({ rule: step.labelRule ?? 0 });
			case 'next_rule':
				return m.singbox_routing_inspector_next_rule({ rule: step.labelRule ?? 0, total: step.labelTotal ?? 0 });
			case 'final':
				return m.singbox_routing_inspector_next_final();
			case 'wait_step':
				return m.singbox_routing_inspector_next_wait_step();
			default:
				return step.label;
		}
	}

	function stepMeta(step: InspectorStep): string {
		const tag = step.ruleSetTag;
		if (typeof step.ruleIndex === 'number' && step.ruleTotal) {
			return tag
				? m.singbox_routing_inspector_rule_of_with_set({ index: step.ruleIndex, total: step.ruleTotal, tag })
				: m.singbox_routing_inspector_rule_of({ index: step.ruleIndex, total: step.ruleTotal });
		}
		return tag ? m.singbox_routing_inspector_set_only({ tag }) : '';
	}

	function stepKey(progress: SingboxRouterInspectProgress): string {
		const rule = typeof progress.ruleIndex === 'number' ? `rule:${progress.ruleIndex}` : '';
		const rs = progress.ruleSetTag ? `rs:${progress.ruleSetTag}` : '';
		if (progress.phase.startsWith('rule_set_download')) return `download:${progress.ruleSetTag ?? ''}`;
		if (progress.phase.startsWith('rule_set_match')) return `match:${progress.ruleSetTag ?? ''}`;
		if (progress.phase.startsWith('rule_set_cache')) return `cache:${progress.ruleSetTag ?? ''}`;
		if (progress.phase.startsWith('rule_')) return rule || progress.phase;
		return `${progress.phase}:${rule}:${rs}`;
	}

	function stepFromProgress(progress: SingboxRouterInspectProgress, status: StepStatus): InspectorStep {
		const now = Date.now();
		const ruleIndex = typeof progress.ruleIndex === 'number' ? progress.ruleIndex : undefined;
		const ruleTotal = typeof progress.ruleTotal === 'number' ? progress.ruleTotal : undefined;
		const ruleSetTag = progress.ruleSetTag || undefined;
		let label = progress.message || progress.phase;
		return {
			id: `${stepKey(progress)}:${now}`,
			label,
			status,
			startedAt: now,
			ruleIndex,
			ruleTotal,
			ruleSetTag,
			phase: progress.phase,
		};
	}

	function stepCompletesCurrent(current: InspectorStep | null, finished: InspectorStep): boolean {
		if (!current) return false;
		if (
			typeof current.ruleIndex === 'number' &&
			typeof finished.ruleIndex === 'number' &&
			current.ruleIndex === finished.ruleIndex &&
			finished.phase === 'rule_done'
		) {
			return true;
		}
		if (
			current.ruleSetTag &&
			finished.ruleSetTag &&
			current.ruleSetTag === finished.ruleSetTag &&
			(
				finished.phase === 'rule_set_cache_hit' ||
				finished.phase === 'rule_set_download_done' ||
				finished.phase === 'rule_set_download_error' ||
				finished.phase === 'rule_set_match_done' ||
				finished.phase === 'rule_set_match_error'
			)
		) {
			return true;
		}
		if (current.phase === 'load_config' && finished.phase === 'config_loaded') return true;
		if (current.phase === 'start' && finished.phase === 'config_loaded') return true;
		return false;
	}

	function buildNextStep(): InspectorStep | null {
		if (totalRules <= 0) {
			return {
				id: `next:config:${Date.now()}`,
				label: '',
				labelKind: 'wait_config',
				status: 'next',
			};
		}
		if (currentRule !== null) {
			if (activeRuleSetTag) {
				return {
					id: `next:ruleset:${Date.now()}`,
					label: '',
					labelKind: 'finish_rule_set',
					labelRule: currentRule,
					status: 'next',
					ruleSetTag: activeRuleSetTag,
				};
			}
			if (currentRule + 1 < totalRules) {
				return {
					id: `next:rule:${Date.now()}`,
					label: '',
					labelKind: 'next_rule',
					labelRule: currentRule + 1,
					labelTotal: totalRules,
					status: 'next',
				};
			}
			return {
				id: `next:final:${Date.now()}`,
				label: '',
				labelKind: 'final',
				status: 'next',
			};
		}
		return {
			id: `next:wait:${Date.now()}`,
			label: '',
			labelKind: 'wait_step',
			status: 'next',
		};
	}

	function handleProgress(progress: SingboxRouterInspectProgress): void {
		const now = Date.now();
		currentProgressMessage = progress.message || progress.phase;
		const ruleIndex = typeof progress.ruleIndex === 'number' ? progress.ruleIndex : undefined;
		const ruleTotal = typeof progress.ruleTotal === 'number' ? progress.ruleTotal : undefined;
		const ruleSetTag = progress.ruleSetTag || undefined;

		if (typeof ruleTotal === 'number') totalRules = Math.max(totalRules, ruleTotal);
		if (progress.phase === 'rule_start' && typeof ruleIndex === 'number') currentRule = ruleIndex;
		if (progress.phase === 'rule_done' && typeof ruleIndex === 'number') {
			checkedRuleIndexes = new Set([...checkedRuleIndexes, ruleIndex]);
		}
		if (
			ruleSetTag &&
			['rule_set_start', 'rule_set_cache_check', 'rule_set_download_start', 'rule_set_match_start'].includes(progress.phase)
		) {
			activeRuleSetTag = ruleSetTag;
		}
		if (ruleSetTag && ['rule_set_match_done', 'rule_set_match_error', 'rule_set_download_error'].includes(progress.phase) && activeRuleSetTag === ruleSetTag) {
			activeRuleSetTag = '';
		}

		const activePhases = new Set([
			'start',
			'load_config',
			'rule_start',
			'rule_set_start',
			'rule_set_cache_check',
			'rule_set_download_start',
			'rule_set_match_start',
		]);
		const donePhases = new Set([
			'config_loaded',
			'classify_input',
			'rule_done',
			'rule_set_cache_hit',
			'rule_set_download_done',
			'rule_set_match_done',
			'terminal_match',
			'non_terminal_match',
			'done',
		]);
		const errorPhases = new Set(['rule_set_download_error', 'rule_set_match_error', 'rule_set_undefined']);

		if (activePhases.has(progress.phase)) {
			const key = stepKey(progress);
			activeStepStartedAt = { ...activeStepStartedAt, [key]: now };
			currentStep = stepFromProgress(progress, 'current');
			nextStep = buildNextStep();
			return;
		}

		if (donePhases.has(progress.phase) || errorPhases.has(progress.phase)) {
			const finished = stepFromProgress(progress, errorPhases.has(progress.phase) ? 'error' : progressKind(progress));
			let startKey = stepKey(progress);
			if (progress.phase === 'rule_done') startKey = `rule:${progress.ruleIndex ?? ''}`;
			if (progress.phase === 'rule_set_download_done' || progress.phase === 'rule_set_download_error') {
				startKey = `download:${progress.ruleSetTag ?? ''}`;
			}
			if (progress.phase === 'rule_set_match_done' || progress.phase === 'rule_set_match_error') {
				startKey = `match:${progress.ruleSetTag ?? ''}`;
			}
			if (progress.phase === 'rule_set_cache_hit') startKey = `cache:${progress.ruleSetTag ?? ''}`;
			const started = activeStepStartedAt[startKey];
			if (started) {
				finished.startedAt = started;
				finished.finishedAt = now;
				finished.durationMs = now - started;
			}
			previousStep = finished;
			completedSteps = [...completedSteps, finished];
			if (stepCompletesCurrent(currentStep, finished)) {
				currentStep = null;
			}
			if (progress.phase === 'done') {
				currentStep = null;
				nextStep = null;
			} else {
				nextStep = buildNextStep();
			}
		}
	}

	function buildInspectionReport(nextResult: SingboxRouterInspectResult): InspectorReport {
		const totalDurationMs = inspectionStartedAt ? Date.now() - inspectionStartedAt : elapsedSec * 1000;
		const allCompleted = completedSteps.filter((s) => s.durationMs && s.durationMs > 0);
		const slowestSteps = [...allCompleted].sort((a, b) => (b.durationMs ?? 0) - (a.durationMs ?? 0)).slice(0, 5);
		const ruleSetSteps = allCompleted.filter((s) => s.ruleSetTag || s.phase?.startsWith('rule_set')).slice(-8);
		return {
			totalDurationMs,
			checkedRules: nextResult.matches?.length ?? checkedRuleIndexes.size,
			totalRules: nextResult.matches?.length ?? totalRules,
			destination: nextResult.destination,
			final: nextResult.final || 'direct',
			matchedRule: nextResult.matchedRule,
			slowestSteps,
			ruleSetSteps,
		};
	}

	const currentRuleIndex = $derived(currentRule);
	const ruleTotal = $derived(totalRules);
	const checkedRules = $derived(checkedRuleIndexes.size);
	const ruleProgressPercent = $derived.by(() => {
		if (!ruleTotal) return 0;
		return Math.min(100, Math.round((checkedRules / ruleTotal) * 100));
	});
	const currentRuleSet = $derived(activeRuleSetTag);

	async function testRoute(): Promise<void> {
		const trimmed = inputValue.trim();
		if (!trimmed) return;
		const runId = inspectRunId + 1;
		inspectRunId = runId;

		testing = true;
		inspectStartedAt = Date.now();
		inspectionStartedAt = Date.now();
		elapsedSec = 0;
		if (progressTimer) clearInterval(progressTimer);
		progressTimer = setInterval(() => {
			if (!inspectStartedAt) return;
			elapsedSec = Math.max(0, Math.floor((Date.now() - inspectStartedAt) / 1000));
		}, 1000);
		error = '';
		result = null;
		showAllRules = false;

		try {
			previousStep = null;
			currentStep = null;
			nextStep = null;
			completedSteps = [];
			currentProgressMessage = null;
			activeStepStartedAt = {};
			checkedRuleIndexes = new Set();
			totalRules = 0;
			currentRule = null;
			activeRuleSetTag = '';
			inspectionReport = null;
			inspectStream?.close();
			const streamFn = engine === 'mihomo'
				? api.mihomoRouterInspectRouteStream.bind(api)
				: api.singboxRouterInspectRouteStream.bind(api);
			inspectStream = streamFn(
				{
					domain: trimmed,
					port: typeof port === 'number' && port > 0 ? port : undefined,
					protocol: protocol || undefined,
				},
				{
					onProgress: (progress: SingboxRouterInspectProgress) => {
						if (runId !== inspectRunId) return;
						handleProgress(progress);
					},
					onResult: (next: SingboxRouterInspectResult) => {
						if (runId !== inspectRunId) return;
						if (next.matches?.length) {
							totalRules = Math.max(totalRules, next.matches.length);
							checkedRuleIndexes = new Set(next.matches.map((match) => match.index));
						}
						inspectionReport = buildInspectionReport(next);
						result = next;
						testing = false;
						stopProgressTimer();
						inspectStream?.close();
						inspectStream = null;
					},
					onInspectError: (message: string) => {
						if (runId !== inspectRunId) return;
						error = message;
						notifications.error(m.singbox_routing_inspector_check_failed({ message }));
						testing = false;
						stopProgressTimer();
						inspectStream?.close();
						inspectStream = null;
					},
					onError: (message: string) => {
						if (runId !== inspectRunId) return;
						error = message;
						notifications.error(m.singbox_routing_inspector_check_failed({ message }));
						testing = false;
						stopProgressTimer();
						inspectStream?.close();
						inspectStream = null;
					},
				},
			);
		} catch (e) {
			if (runId !== inspectRunId) return;
			const msg = e instanceof Error ? e.message : String(e);
			error = msg;
			notifications.error(m.singbox_routing_inspector_check_failed({ message: msg }));
			testing = false;
			stopProgressTimer();
			inspectStream?.close();
			inspectStream = null;
		} finally {
			if (runId === inspectRunId) {
				// SSE callbacks finalize the run.
			}
		}
	}

	function quickTest(value: string): void {
		inputValue = value;
		testRoute();
	}

	function handleKeydown(e: KeyboardEvent): void {
		if (e.key === 'Enter' && !testing) {
			testRoute();
		}
	}

	function reset(): void {
		inspectRunId += 1;
		inspectStream?.close();
		inspectStream = null;
		stopProgressTimer();
		inputValue = '';
		port = '';
		protocol = '';
		result = null;
		error = '';
		showAllRules = false;
		advancedOpen = false;
		previousStep = null;
		currentStep = null;
		nextStep = null;
		completedSteps = [];
		currentProgressMessage = null;
		activeStepStartedAt = {};
		checkedRuleIndexes = new Set();
		totalRules = 0;
		currentRule = null;
		activeRuleSetTag = '';
		inspectionStartedAt = null;
		inspectionReport = null;
	}

	function close(): void {
		reset();
		onClose();
	}

	function actionVariant(action: string): 'route' | 'reject' | 'sniff' | 'other' {
		if (action === 'route') return 'route';
		if (action === 'reject') return 'reject';
		if (action === 'sniff' || action === 'hijack-dns') return 'sniff';
		return 'other';
	}

	function actionLabel(action: string): string {
		if (action === 'route') return 'ROUTE';
		if (action === 'reject') return 'REJECT';
		if (action === 'sniff') return 'SNIFF';
		if (action === 'hijack-dns') return 'HIJACK';
		return action.toUpperCase();
	}

	const matchedRuleData = $derived.by<SingboxRouterInspectMatch | null>(() => {
		const r = result;
		if (!r || r.matchedRule < 0) return null;
		return r.matches.find((match) => match.index === r.matchedRule) ?? null;
	});

	const isReject = $derived(result?.destination === 'REJECT');
</script>

<Modal {open} title={m.singbox_routing_inspector_title({ engine: engine === 'mihomo' ? 'Mihomo' : 'sing-box' })} size="xl" onclose={close}>
	<div class="inspector">
		<!-- Input section -->
		<section class="card input-section">
			<label for="inspector-input" class="field-label">
				{m.singbox_routing_inspector_input_label()}
			</label>
			<div class="input-row">
				<input
					id="inspector-input"
					type="text"
					bind:value={inputValue}
					onkeydown={handleKeydown}
					placeholder={m.singbox_routing_inspector_input_placeholder()}
					class="text-input"
					autocomplete="off"
				/>
				<Button
					variant="primary"
					onclick={testRoute}
					disabled={testing || !inputValue.trim()}
				>
					{testing ? m.singbox_routing_inspector_checking() : m.common_check()}
				</Button>
			</div>

			<button
				type="button"
				class="advanced-toggle"
				onclick={() => (advancedOpen = !advancedOpen)}
			>
				{advancedOpen ? m.singbox_routing_inspector_hide_advanced() : m.singbox_routing_inspector_show_advanced()}
			</button>

			{#if advancedOpen}
				<div class="advanced-row">
					<label class="adv-field">
						<span class="adv-label">{m.singbox_routing_inspector_port()}</span>
						<input
							type="number"
							min="0"
							max="65535"
							bind:value={port}
							placeholder={m.singbox_routing_inspector_port_placeholder()}
							class="text-input"
						/>
					</label>
					<label class="adv-field">
						<span class="adv-label">{m.singbox_routing_inspector_protocol()}</span>
						<select bind:value={protocol} class="select-input">
							<option value="">{m.singbox_routing_inspector_protocol_unset()}</option>
							<option value="tcp">tcp</option>
							<option value="udp">udp</option>
						</select>
					</label>
				</div>
			{/if}

			<div class="quick-row">
				<span class="quick-label">{m.singbox_routing_inspector_quick_check()}</span>
				{#each examples as ex (ex)}
					<button
						type="button"
						class="example-chip"
						onclick={() => quickTest(ex)}
						disabled={testing}
					>
						{ex}
					</button>
				{/each}
			</div>
		</section>

		{#if testing}
			<section class="card progress-card" aria-live="polite">
				<div class="progress-header-row">
					<div>
						<div class="progress-title">{m.singbox_routing_inspector_in_progress_title()}</div>
						<div class="progress-message">{currentProgressMessage ?? m.singbox_routing_inspector_waiting_backend()}</div>
					</div>
					<div class="progress-elapsed">{m.singbox_routing_inspector_total_time({ seconds: elapsedSec })}</div>
				</div>
				{#if ruleTotal > 0}
					<div class="progress-bar-wrap">
						<div class="progress-bar" style={`--progress-width: ${ruleProgressPercent}%`}>
							<div class="progress-bar-fill"></div>
						</div>
					</div>
				{/if}
				<div class="progress-summary">
					{#if ruleTotal > 0}
						<span class="progress-pill">{m.singbox_routing_inspector_rules_progress({ checked: checkedRules, total: ruleTotal })}</span>
					{/if}
					{#if typeof currentRuleIndex === 'number' && ruleTotal > 0}
						<span class="progress-pill">{m.singbox_routing_inspector_current_rule({ index: currentRuleIndex })}</span>
					{/if}
					{#if currentRuleSet}
						<span class="progress-pill">Rule-set: <code>{currentRuleSet}</code></span>
					{/if}
				</div>
				<div class="progress-hint">{m.singbox_routing_inspector_progress_hint()}</div>
				<div class="step-stack" aria-label={m.singbox_routing_inspector_steps_aria()}>
					{#if previousStep}
						<div class="step-card step-{previousStep.status}">
							<span class="step-dot"></span>
							<div class="step-content">
								<div class="step-kicker">{m.singbox_routing_inspector_step_checked()}</div>
								<div class="step-label">{stepLabel(previousStep)}</div>
								<div class="step-meta">
									{#if stepMeta(previousStep)}<span>{stepMeta(previousStep)}</span>{/if}
									{#if previousStep.durationMs}<span>{formatDuration(previousStep.durationMs)}</span>{/if}
								</div>
							</div>
						</div>
					{/if}
					{#if currentStep}
						<div class="step-card step-current">
							<span class="step-dot"></span>
							<div class="step-content">
								<div class="step-kicker">{m.singbox_routing_inspector_step_now()}</div>
								<div class="step-label">{stepLabel(currentStep)}</div>
								{#if stepMeta(currentStep)}<div class="step-meta"><span>{stepMeta(currentStep)}</span></div>{/if}
							</div>
						</div>
					{/if}
					{#if nextStep}
						<div class="step-card step-next">
							<span class="step-dot"></span>
							<div class="step-content">
								<div class="step-kicker">{m.singbox_routing_inspector_step_next()}</div>
								<div class="step-label">{stepLabel(nextStep)}</div>
								{#if stepMeta(nextStep)}<div class="step-meta"><span>{stepMeta(nextStep)}</span></div>{/if}
							</div>
						</div>
					{/if}
				</div>
			</section>
		{/if}

		{#if error}
			<div class="error-banner">{error}</div>
		{/if}

		{#if result}
			<!-- Big result card -->
			<section class="card result-card">
				<div class="result-row">
					<div class="input-block">
						<div class="input-value">{result.input}</div>
						<div class="input-type">{result.inputType === 'domain' ? m.singbox_routing_inspector_type_domain() : m.singbox_routing_inspector_type_ip()}</div>
					</div>
					<div class="arrow">→</div>
					<div
						class="dest-block"
						class:dest-reject={isReject}
						class:dest-final={result.matchedRule < 0 && !isReject}
					>
						<div class="dest-value">{humanize(result.destination)}</div>
						<div class="dest-meta">
							{#if result.matchedRule >= 0}
								{m.singbox_routing_inspector_matched_rule({ index: result.matchedRule })}
							{:else}
								{m.singbox_routing_inspector_default_outbound({ final: humanize(result.final || 'direct') })}
							{/if}
						</div>
					</div>
				</div>

				{#if matchedRuleData}
					<div class="match-detail">
						<div class="match-header">
							<span class="rule-num">{m.singbox_routing_inspector_rule_num({ index: matchedRuleData.index })}</span>
							<span class="badge badge-{actionVariant(matchedRuleData.action)}">
								{actionLabel(matchedRuleData.action)}
							</span>
							{#if matchedRuleData.outbound}
								<span class="match-outbound">→ {humanize(matchedRuleData.outbound)}</span>
							{/if}
						</div>
						{#if matchedRuleData.reason}
							<div class="match-reason">{matchedRuleData.reason}</div>
						{/if}
						{#if matchedRuleData.conditions && matchedRuleData.conditions.length}
							<div class="match-conditions">
								<span class="cond-label">{m.singbox_routing_inspector_conditions()}</span>
								{matchedRuleData.conditions.join(', ')}
							</div>
						{/if}
					</div>
				{:else}
					<div class="match-detail no-match">
						<span>
							{m.singbox_routing_inspector_no_match_prefix()}
							<strong>{humanize(result.final || 'direct')}</strong>.
						</span>
					</div>
				{/if}
			</section>

			{#if result.dns}
				<section class="card dns-card">
					<div class="dns-header">
						<div class="dns-title-group">
							<Globe size={15} class="dns-icon" />
							<span class="dns-title">{m.singbox_routing_inspector_dns_title()}</span>
						</div>
						<span class="badge {result.dns.isRemoteDNS ? 'badge-route' : 'badge-sniff'}">
							{result.dns.isRemoteDNS ? m.singbox_routing_inspector_dns_remote() : m.singbox_routing_inspector_dns_local()}
						</span>
					</div>
					<div class="dns-grid">
						<div class="dns-field">
							<span class="dns-label">{m.singbox_routing_inspector_dns_target_server()}</span>
							<strong class="dns-server-name">{result.dns.server}</strong>
							{#if result.dns.serverAddress}
								<span class="dns-server-addr">({result.dns.serverAddress})</span>
							{/if}
						</div>
						{#if result.dns.policy}
							<div class="dns-field">
								<span class="dns-label">{m.singbox_routing_inspector_dns_policy()}</span>
								<code class="dns-code">{result.dns.policy}</code>
							</div>
						{/if}
						{#if result.dns.reason}
							<div class="dns-field">
								<span class="dns-label">{m.singbox_routing_inspector_dns_reason()}</span>
								<span class="dns-reason-text">{result.dns.reason}</span>
							</div>
						{/if}
					</div>
				</section>
			{/if}

			{#if result.note}
				<div class="note-banner">
					<strong>{m.singbox_routing_inspector_note()}</strong>
					{result.note}
				</div>
			{/if}

			{#if inspectionReport}
				<section class="card inspect-report">
					<div class="report-header">
						<div>
							<div class="report-title">{m.singbox_routing_inspector_report_title()}</div>
							<div class="report-subtitle">{m.singbox_routing_inspector_report_checked_rules({ checked: inspectionReport.checkedRules, total: inspectionReport.totalRules })}</div>
						</div>
						<div class="report-duration">{formatDuration(inspectionReport.totalDurationMs) || m.singbox_routing_inspector_less_than_second()}</div>
					</div>
					<div class="report-grid">
						<div class="report-item">
							<span>{m.singbox_routing_inspector_report_decision()}</span>
							<strong>{humanize(inspectionReport.destination)}</strong>
						</div>
						<div class="report-item">
							<span>{m.singbox_routing_inspector_report_matched()}</span>
							<strong>
								{inspectionReport.matchedRule >= 0
									? m.singbox_routing_inspector_rule_num({ index: inspectionReport.matchedRule })
									: `Final: ${humanize(inspectionReport.final)}`}
							</strong>
						</div>
					</div>
					{#if inspectionReport.slowestSteps.length > 0}
						<div class="report-section">
							<div class="report-section-title">{m.singbox_routing_inspector_report_slowest()}</div>
							{#each inspectionReport.slowestSteps as step (step.id)}
								<div class="report-row">
									<span>{stepLabel(step)}</span>
									<strong>{formatDuration(step.durationMs) || m.singbox_routing_inspector_less_than_second()}</strong>
								</div>
							{/each}
						</div>
					{/if}
					{#if inspectionReport.ruleSetSteps.length > 0}
						<div class="report-section">
							<div class="report-section-title">{m.singbox_routing_inspector_report_rule_set_checks()}</div>
							{#each inspectionReport.ruleSetSteps as step (step.id)}
								<div class="report-row">
									<span>{stepLabel(step)}</span>
									<strong>{formatDuration(step.durationMs) || m.singbox_routing_inspector_less_than_second()}</strong>
								</div>
							{/each}
						</div>
					{/if}
				</section>
			{/if}

			{#if result.matches.length > 0}
				<button
					type="button"
					class="walkthrough-toggle"
					onclick={() => (showAllRules = !showAllRules)}
				>
					{showAllRules
						? m.singbox_routing_inspector_walkthrough_hide({ count: result.matches.length })
						: m.singbox_routing_inspector_walkthrough_show({ count: result.matches.length })}
				</button>
			{/if}

			{#if showAllRules}
				<section class="card walkthrough">
					<header class="walkthrough-header">{m.singbox_routing_inspector_walkthrough_title()}</header>
					<ul class="walkthrough-list">
						{#each result.matches as match (match.index)}
							<li
								class="walkthrough-row"
								class:row-matched={match.matched}
								class:row-non-final={match.matched &&
									(match.action === 'sniff' || match.action === 'hijack-dns')}
							>
								<div class="row-head">
									<span class="row-index">#{match.index}</span>
									<span class="badge badge-{actionVariant(match.action)}">
										{actionLabel(match.action)}
									</span>
									{#if match.outbound}
										<span class="row-outbound">→ {humanize(match.outbound)}</span>
									{/if}
									<span class="row-status">
										{#if match.matched}
											{#if match.action === 'sniff' || match.action === 'hijack-dns'}
												{m.singbox_routing_inspector_row_matched_non_final()}
											{:else}
												{m.singbox_routing_inspector_row_matched()}
											{/if}
										{:else}
											{m.singbox_routing_inspector_row_not_matched()}
										{/if}
									</span>
								</div>
								{#if match.conditions && match.conditions.length}
									<div class="row-conditions">{match.conditions.join(' · ')}</div>
								{/if}
								{#if match.reason}
									<div class="row-reason">{match.reason}</div>
								{/if}
							</li>
						{/each}
						<li class="walkthrough-row row-final">
							<div class="row-head">
								<span class="row-index">∞</span>
								<span class="badge badge-other">FINAL</span>
								<span class="row-outbound">→ {humanize(result.final || 'direct')}</span>
								<span class="row-status">{m.singbox_routing_inspector_row_final_hint()}</span>
							</div>
						</li>
					</ul>
				</section>
			{/if}
		{:else if !error && !testing}
			<div class="empty-state">
				{m.singbox_routing_inspector_empty({ engine: engine === 'mihomo' ? 'Mihomo' : 'sing-box' })}
			</div>
		{/if}
	</div>
</Modal>

<style>
	.inspector {
		display: flex;
		flex-direction: column;
		gap: 0.875rem;
	}

	.card {
		background: var(--color-bg-tertiary);
		border: 1px solid var(--color-border);
		border-radius: var(--radius-sm);
		padding: 0.875rem 1rem;
	}

	.field-label {
		display: block;
		font-size: 12px;
		color: var(--color-text-secondary);
		margin-bottom: 0.4rem;
	}

	.input-row {
		display: flex;
		gap: 0.5rem;
		align-items: stretch;
	}

	.text-input,
	.select-input {
		flex: 1;
		min-width: 0;
		padding: 0.5rem 0.75rem;
		background: var(--color-bg-primary);
		border: 1px solid var(--color-border);
		border-radius: var(--radius-sm);
		color: var(--color-text-primary);
		font-family: ui-monospace, SFMono-Regular, Menlo, Monaco, Consolas, monospace;
		font-size: 13px;
		box-sizing: border-box;
	}

	.text-input:focus,
	.select-input:focus {
		outline: none;
		border-color: var(--color-accent);
		box-shadow: 0 0 0 2px var(--color-accent-tint);
	}

	.advanced-toggle {
		margin-top: 0.6rem;
		padding: 0;
		background: none;
		border: none;
		color: var(--color-text-muted);
		font-size: 12px;
		cursor: pointer;
		text-align: left;
	}

	.advanced-toggle:hover {
		color: var(--color-text-secondary);
	}

	.advanced-row {
		display: grid;
		grid-template-columns: 1fr 1fr;
		gap: 0.5rem;
		margin-top: 0.5rem;
	}

	.adv-field {
		display: flex;
		flex-direction: column;
		gap: 0.25rem;
	}

	.adv-label {
		font-size: 11px;
		color: var(--color-text-muted);
	}

	.quick-row {
		display: flex;
		flex-wrap: wrap;
		align-items: center;
		gap: 0.4rem;
		margin-top: 0.75rem;
	}

	.quick-label {
		font-size: 11px;
		color: var(--color-text-muted);
	}

	/* Не .chip: имя занято утилитой Skeleton и app.css — их свойства протекали сюда. */
	.example-chip {
		display: inline-flex;
		align-items: center;
		gap: 0.5rem;
		padding: 0.25rem 0.55rem;
		font-size: 12px;
		font-family: ui-monospace, SFMono-Regular, Menlo, Monaco, Consolas, monospace;
		background: var(--color-bg-primary);
		color: var(--color-text-secondary);
		border: 1px solid var(--color-border);
		border-radius: 999px;
		font-weight: 600;
		line-height: calc(1 / 0.75);
		white-space: nowrap;
		cursor: pointer;
		transition: background var(--t-fast) ease, color var(--t-fast) ease, border-color var(--t-fast) ease;
	}

	.example-chip:hover:not(:disabled) {
		background: var(--color-bg-hover);
		color: var(--color-text-primary);
		border-color: var(--color-border-hover);
	}

	.example-chip:disabled {
		opacity: 0.5;
		cursor: not-allowed;
	}

	.error-banner {
		padding: 0.6rem 0.75rem;
		background: var(--color-error-tint);
		border: 1px solid var(--color-error-border);
		border-radius: var(--radius-sm);
		color: var(--color-error);
		font-size: 13px;
	}

	.note-banner {
		padding: 0.6rem 0.75rem;
		background: var(--color-warning-tint);
		border: 1px solid var(--color-warning-border);
		border-radius: var(--radius-sm);
		color: var(--color-text-primary);
		font-size: 12px;
		line-height: 1.5;
	}

	.note-banner strong {
		color: var(--color-warning);
	}

	.result-card {
		display: flex;
		flex-direction: column;
		gap: 0.875rem;
	}

	.result-row {
		display: grid;
		grid-template-columns: 1fr auto 1fr;
		align-items: center;
		gap: 0.75rem;
	}

	.input-block,
	.dest-block {
		display: flex;
		flex-direction: column;
		gap: 0.2rem;
		min-width: 0;
	}

	.dest-block {
		text-align: right;
	}

	.input-value,
	.dest-value {
		font-family: ui-monospace, SFMono-Regular, Menlo, Monaco, Consolas, monospace;
		font-size: 18px;
		color: var(--color-text-primary);
		word-break: break-all;
	}

	.dest-value {
		color: var(--color-success);
		font-weight: 600;
	}

	.dest-block.dest-reject .dest-value {
		color: var(--color-error);
	}

	.dest-block.dest-final .dest-value {
		color: var(--color-text-secondary);
	}

	.input-type,
	.dest-meta {
		font-size: 11px;
		color: var(--color-text-muted);
	}

	.arrow {
		font-size: 22px;
		color: var(--color-text-muted);
	}

	.match-detail {
		padding-top: 0.75rem;
		border-top: 1px solid var(--color-border);
		display: flex;
		flex-direction: column;
		gap: 0.4rem;
	}

	.match-detail.no-match {
		display: block;
		color: var(--color-text-secondary);
		font-size: 13px;
		line-height: 1.5;
	}

	.match-detail.no-match strong {
		display: inline;
		font-family: ui-monospace, SFMono-Regular, Menlo, Monaco, Consolas, monospace;
		color: var(--color-text-primary);
		font-weight: 600;
	}

	.match-header {
		display: flex;
		flex-wrap: wrap;
		align-items: center;
		gap: 0.5rem;
	}

	.rule-num {
		font-weight: 600;
		color: var(--color-text-primary);
	}

	.match-outbound,
	.row-outbound {
		font-family: ui-monospace, SFMono-Regular, Menlo, Monaco, Consolas, monospace;
		font-size: 12px;
		color: var(--color-text-secondary);
	}

	.match-reason {
		font-size: 12px;
		color: var(--color-success);
	}

	.match-conditions {
		font-size: 11px;
		color: var(--color-text-muted);
	}

	.cond-label {
		color: var(--color-text-muted);
		margin-right: 0.25rem;
	}

	.badge {
		display: inline-block;
		padding: 0.1rem 0.45rem;
		font-size: 10px;
		font-weight: 600;
		font-family: ui-monospace, SFMono-Regular, Menlo, Monaco, Consolas, monospace;
		border-radius: 4px;
		border: 1px solid transparent;
	}

	.badge-route {
		background: var(--color-success-tint);
		color: var(--color-success);
		border-color: var(--color-success-border);
	}

	.badge-reject {
		background: var(--color-error-tint);
		color: var(--color-error);
		border-color: var(--color-error-border);
	}

	.badge-sniff {
		background: var(--color-info-tint);
		color: var(--color-info);
		border-color: var(--color-info-border);
	}

	.badge-other {
		background: var(--color-muted-tint);
		color: var(--color-text-secondary);
		border-color: var(--color-border);
	}

	.walkthrough-toggle {
		align-self: center;
		padding: 0.4rem 0.75rem;
		background: none;
		border: 1px solid var(--color-border);
		border-radius: var(--radius-sm);
		color: var(--color-text-secondary);
		font-size: 12px;
		cursor: pointer;
	}

	.walkthrough-toggle:hover {
		background: var(--color-bg-hover);
		color: var(--color-text-primary);
	}

	.walkthrough {
		padding: 0;
		overflow: hidden;
	}

	.walkthrough-header {
		padding: 0.6rem 0.875rem;
		background: var(--color-bg-secondary);
		border-bottom: 1px solid var(--color-border);
		font-size: 12px;
		color: var(--color-text-secondary);
		font-weight: 600;
	}

	.walkthrough-list {
		list-style: none;
		margin: 0;
		padding: 0;
		max-height: 360px;
		overflow-y: auto;
	}

	.walkthrough-row {
		padding: 0.55rem 0.875rem;
		border-bottom: 1px solid var(--color-border);
		display: flex;
		flex-direction: column;
		gap: 0.25rem;
	}

	.walkthrough-row:last-child {
		border-bottom: none;
	}

	.walkthrough-row.row-matched {
		background: color-mix(in srgb, var(--color-success) 6%, transparent);
	}

	.walkthrough-row.row-non-final {
		background: color-mix(in srgb, var(--color-info) 6%, transparent);
	}

	.walkthrough-row.row-final {
		background: var(--color-bg-secondary);
	}

	.row-head {
		display: flex;
		flex-wrap: wrap;
		align-items: center;
		gap: 0.5rem;
		font-size: 12px;
	}

	.row-index {
		display: inline-flex;
		align-items: center;
		justify-content: center;
		min-width: 1.5rem;
		font-family: ui-monospace, SFMono-Regular, Menlo, Monaco, Consolas, monospace;
		color: var(--color-text-muted);
	}

	.row-status {
		margin-left: auto;
		font-size: 11px;
		color: var(--color-text-muted);
	}

	.row-matched .row-status {
		color: var(--color-success);
	}

	.row-non-final .row-status {
		color: var(--color-info);
	}

	.row-conditions {
		font-size: 11px;
		color: var(--color-text-muted);
		font-family: ui-monospace, SFMono-Regular, Menlo, Monaco, Consolas, monospace;
		padding-left: 2rem;
	}

	.row-reason {
		font-size: 11px;
		color: var(--color-text-secondary);
		padding-left: 2rem;
	}

	.empty-state {
		padding: 1rem;
		text-align: center;
		color: var(--color-text-secondary);
		font-size: 13px;
		line-height: 1.5;
		border: 1px dashed var(--color-border);
		border-radius: var(--radius-sm);
	}

	.progress-card {
		display: flex;
		flex-direction: column;
		gap: 0.75rem;
	}

	.progress-header-row {
		display: flex;
		justify-content: space-between;
		gap: 1rem;
		align-items: flex-start;
	}

	.progress-title {
		font-size: 13px;
		font-weight: 700;
		color: var(--color-text-primary);
	}

	.progress-message {
		font-size: 13px;
		color: var(--color-text-primary);
	}

	.progress-elapsed {
		font-size: 11px;
		color: var(--color-text-muted);
		white-space: nowrap;
	}

	.progress-hint {
		font-size: 11px;
		color: var(--color-text-muted);
	}

	.progress-bar {
		height: 6px;
		border-radius: 999px;
		background: var(--color-bg-primary);
		overflow: hidden;
		border: 1px solid var(--color-border);
	}

	.progress-bar-fill {
		height: 100%;
		width: var(--progress-width);
		border-radius: inherit;
		background: var(--color-accent);
		transition: width 180ms ease;
	}

	.progress-summary {
		display: flex;
		flex-wrap: wrap;
		gap: 0.5rem;
		font-size: 11px;
		color: var(--color-text-muted);
	}

	.progress-pill {
		border: 1px solid var(--color-border);
		background: var(--color-bg-primary);
		border-radius: 999px;
		padding: 0.2rem 0.5rem;
	}

	.step-stack {
		display: flex;
		flex-direction: column;
		gap: 0.45rem;
	}

	.step-card {
		display: grid;
		grid-template-columns: 0.75rem 1fr;
		gap: 0.55rem;
		align-items: start;
		padding: 0.55rem 0.65rem;
		border-radius: var(--radius-sm);
		background: var(--color-bg-primary);
		border: 1px solid transparent;
		transition: transform 160ms ease, border-color 160ms ease, background 160ms ease;
	}

	.step-current {
		border-color: var(--color-accent);
		background: color-mix(in srgb, var(--color-accent) 8%, var(--color-bg-primary));
	}

	.step-next {
		opacity: 0.7;
		border-style: dashed;
		border-color: var(--color-border);
	}

	.step-done,
	.step-matched {
		background: color-mix(in srgb, var(--color-success) 6%, var(--color-bg-primary));
	}

	.step-miss {
		background: var(--color-bg-primary);
	}

	.step-error {
		background: var(--color-error-tint);
		border-color: var(--color-error-border);
	}

	.step-dot {
		width: 0.5rem;
		height: 0.5rem;
		margin-top: 0.35rem;
		border-radius: 999px;
		background: var(--color-text-muted);
	}

	.step-current .step-dot {
		background: var(--color-accent);
		box-shadow: 0 0 0 3px var(--color-accent-tint);
	}

	.step-matched .step-dot,
	.step-done .step-dot {
		background: var(--color-success);
	}

	.step-error .step-dot {
		background: var(--color-error);
	}

	.step-kicker {
		font-size: 10px;
		text-transform: uppercase;
		letter-spacing: 0.04em;
		color: var(--color-text-muted);
		margin-bottom: 0.1rem;
	}

	.step-label {
		font-size: 12px;
		color: var(--color-text-primary);
		font-weight: 600;
	}

	.step-meta {
		display: flex;
		flex-wrap: wrap;
		gap: 0.45rem;
		margin-top: 0.15rem;
		font-size: 10px;
		color: var(--color-text-muted);
	}

	.dns-card {
		padding: 12px 16px;
		background: var(--color-bg-secondary, #252530);
		border: 1px solid var(--color-border, #2e2e38);
		border-radius: var(--radius-md, 8px);
		display: flex;
		flex-direction: column;
		gap: 8px;
	}

	.dns-header {
		display: flex;
		justify-content: space-between;
		align-items: center;
		padding-bottom: 6px;
		border-bottom: 1px solid var(--color-border, #2e2e38);
	}

	.dns-title-group {
		display: flex;
		align-items: center;
		gap: 6px;
	}

	:global(.dns-icon) {
		color: var(--accent, #3b82f6);
		flex-shrink: 0;
	}

	.dns-title {
		font-size: 13px;
		font-weight: 600;
		color: var(--color-text-primary, #ffffff);
	}

	.dns-grid {
		display: flex;
		flex-direction: column;
		gap: 6px;
		font-size: 12px;
	}

	.dns-field {
		display: flex;
		align-items: baseline;
		gap: 6px;
		flex-wrap: wrap;
		line-height: 1.4;
	}

	.dns-label {
		color: var(--color-text-muted, #9ba1a6);
		font-weight: 500;
		min-width: 140px;
	}

	.dns-server-name {
		color: var(--color-text-primary, #ffffff);
		font-weight: 600;
	}

	.dns-server-addr {
		color: var(--color-text-muted, #9ba1a6);
		font-size: 11px;
	}

	.dns-code {
		padding: 1px 5px;
		border-radius: 4px;
		background: var(--color-bg-primary, #1e1e24);
		color: var(--accent, #3b82f6);
		font-family: ui-monospace, SFMono-Regular, Menlo, Monaco, Consolas, monospace;
		font-size: 11px;
	}

	.dns-reason-text {
		color: var(--color-text-secondary, #9ba1a6);
	}

	.inspect-report {
		display: flex;
		flex-direction: column;
		gap: 0.75rem;
	}

	.report-header {
		display: flex;
		justify-content: space-between;
		gap: 1rem;
		align-items: flex-start;
	}

	.report-title {
		font-size: 13px;
		font-weight: 700;
		color: var(--color-text-primary);
	}

	.report-subtitle {
		font-size: 11px;
		color: var(--color-text-muted);
	}

	.report-duration {
		font-size: 12px;
		font-family: ui-monospace, SFMono-Regular, Menlo, Monaco, Consolas, monospace;
		color: var(--color-text-primary);
	}

	.report-grid {
		display: grid;
		grid-template-columns: 1fr 1fr;
		gap: 0.5rem;
	}

	.report-item {
		padding: 0.5rem 0.6rem;
		border: 1px solid var(--color-border);
		border-radius: var(--radius-sm);
		background: var(--color-bg-primary);
	}

	.report-item span {
		display: block;
		font-size: 10px;
		color: var(--color-text-muted);
		margin-bottom: 0.15rem;
	}

	.report-item strong {
		font-size: 12px;
		color: var(--color-text-primary);
		font-family: ui-monospace, SFMono-Regular, Menlo, Monaco, Consolas, monospace;
	}

	.report-section {
		display: flex;
		flex-direction: column;
		gap: 0.35rem;
	}

	.report-section-title {
		font-size: 11px;
		font-weight: 600;
		color: var(--color-text-secondary);
	}

	.report-row {
		display: flex;
		justify-content: space-between;
		gap: 0.75rem;
		padding: 0.35rem 0.5rem;
		border-radius: var(--radius-sm);
		background: var(--color-bg-primary);
		font-size: 11px;
	}

	.report-row span {
		color: var(--color-text-secondary);
		min-width: 0;
		overflow-wrap: anywhere;
	}

	.report-row strong {
		color: var(--color-text-primary);
		white-space: nowrap;
	}

	@media (max-width: 640px) {
		.progress-header-row {
			flex-direction: column;
		}

		.progress-elapsed {
			white-space: normal;
		}

		.report-grid {
			grid-template-columns: 1fr;
		}

		.report-header {
			flex-direction: column;
		}
	}
</style>
