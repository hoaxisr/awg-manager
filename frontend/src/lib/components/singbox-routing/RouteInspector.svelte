<script lang="ts">
	import { Modal, Button } from '$lib/components/ui';
	import { api } from '$lib/api/client';
	import { notifications } from '$lib/stores/notifications';
	import type {
		SingboxRouterInspectResult,
		SingboxRouterInspectMatch,
		SingboxRouterInspectProgress,
	} from '$lib/types';

	interface Props {
		open: boolean;
		onClose: () => void;
	}

	let { open, onClose }: Props = $props();

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
		status: StepStatus;
		meta?: string;
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
	let currentProgressMessage = $state('Ожидаем ответ backend…');
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
		return `${(ms / 1000).toFixed(ms < 10000 ? 1 : 0)} сек`;
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
		let label = progress.message || formatProgressFallback(progress);
		let meta = '';
		if (typeof ruleIndex === 'number' && ruleTotal) {
			meta = `Правило #${ruleIndex} из ${ruleTotal}`;
		}
		if (ruleSetTag) {
			meta = meta ? `${meta} · rule_set: ${ruleSetTag}` : `rule_set: ${ruleSetTag}`;
		}
		return {
			id: `${stepKey(progress)}:${now}`,
			label,
			status,
			meta,
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
		if (currentProgressMessage === 'Инспектор завершил проверку') return null;
		if (totalRules <= 0) {
			return {
				id: `next:config:${Date.now()}`,
				label: 'Ожидаем загрузку конфигурации',
				status: 'next',
				meta: '',
			};
		}
		if (currentRule !== null) {
			if (activeRuleSetTag) {
				return {
					id: `next:ruleset:${Date.now()}`,
					label: `Завершить проверку rule_set и перейти к результату правила #${currentRule + 1}`,
					status: 'next',
					meta: activeRuleSetTag ? `rule_set: ${activeRuleSetTag}` : '',
				};
			}
			if (currentRule + 1 < totalRules) {
				return {
					id: `next:rule:${Date.now()}`,
					label: `Далее: правило #${currentRule + 2} из ${totalRules}`,
					status: 'next',
					meta: '',
				};
			}
			return {
				id: `next:final:${Date.now()}`,
				label: 'Далее: финальное решение маршрута',
				status: 'next',
				meta: '',
			};
		}
		return {
			id: `next:wait:${Date.now()}`,
			label: 'Ожидаем следующий шаг проверки',
			status: 'next',
			meta: '',
		};
	}

	function handleProgress(progress: SingboxRouterInspectProgress): void {
		const now = Date.now();
		currentProgressMessage = progress.message || formatProgressFallback(progress);
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

	function formatProgressFallback(progress: SingboxRouterInspectProgress): string {
		switch (progress.phase) {
			case 'rule_start':
				return `Проверяем правило #${progress.ruleIndex ?? 0} из ${progress.ruleTotal ?? 0}`;
			case 'rule_done':
				return `Проверка правила #${(progress.ruleIndex ?? 0) + 1} завершена`;
			case 'rule_set_start':
				return `Проверяем rule_set ${progress.ruleSetTag ?? ''}`.trim();
			case 'rule_set_match_start':
				return `Проверяем rule_set ${progress.ruleSetTag ?? ''} через sing-box…`.trim();
			default:
				return progress.phase;
		}
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
			currentProgressMessage = 'Ожидаем ответ backend…';
			activeStepStartedAt = {};
			checkedRuleIndexes = new Set();
			totalRules = 0;
			currentRule = null;
			activeRuleSetTag = '';
			inspectionReport = null;
			inspectStream?.close();
			inspectStream = api.singboxRouterInspectRouteStream(
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
					onResult: (next) => {
						if (runId !== inspectRunId) return;
						if (next.matches?.length) {
							totalRules = Math.max(totalRules, next.matches.length);
							checkedRuleIndexes = new Set(next.matches.map((m) => m.index));
						}
						inspectionReport = buildInspectionReport(next);
						result = next;
						testing = false;
						stopProgressTimer();
						inspectStream?.close();
						inspectStream = null;
					},
					onInspectError: (message) => {
						if (runId !== inspectRunId) return;
						error = message;
						notifications.error(`Не удалось проверить маршрут: ${message}`);
						testing = false;
						stopProgressTimer();
						inspectStream?.close();
						inspectStream = null;
					},
					onError: (message) => {
						if (runId !== inspectRunId) return;
						error = message;
						notifications.error(`Не удалось проверить маршрут: ${message}`);
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
			notifications.error(`Не удалось проверить маршрут: ${msg}`);
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
		currentProgressMessage = 'Ожидаем ответ backend…';
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
		return r.matches.find((m) => m.index === r.matchedRule) ?? null;
	});

	const isReject = $derived(result?.destination === 'REJECT');
</script>

<Modal {open} title="Инспектор маршрутов" size="xl" onclose={close}>
	<div class="inspector">
		<!-- Input section -->
		<section class="card input-section">
			<label for="inspector-input" class="field-label">
				Домен или IP
			</label>
			<div class="input-row">
				<input
					id="inspector-input"
					type="text"
					bind:value={inputValue}
					onkeydown={handleKeydown}
					placeholder="google.com или 8.8.8.8"
					class="text-input"
					autocomplete="off"
				/>
				<Button
					variant="primary"
					onclick={testRoute}
					disabled={testing || !inputValue.trim()}
				>
					{testing ? 'Проверяем…' : 'Проверить'}
				</Button>
			</div>

			<button
				type="button"
				class="advanced-toggle"
				onclick={() => (advancedOpen = !advancedOpen)}
			>
				{advancedOpen ? 'Скрыть' : 'Показать'} дополнительные параметры
			</button>

			{#if advancedOpen}
				<div class="advanced-row">
					<label class="adv-field">
						<span class="adv-label">Порт</span>
						<input
							type="number"
							min="0"
							max="65535"
							bind:value={port}
							placeholder="опционально"
							class="text-input"
						/>
					</label>
					<label class="adv-field">
						<span class="adv-label">Протокол</span>
						<select bind:value={protocol} class="select-input">
							<option value="">не задан</option>
							<option value="tcp">tcp</option>
							<option value="udp">udp</option>
						</select>
					</label>
				</div>
			{/if}

			<div class="quick-row">
				<span class="quick-label">Быстрая проверка:</span>
				{#each examples as ex (ex)}
					<button
						type="button"
						class="chip"
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
						<div class="progress-title">Идёт проверка маршрута</div>
						<div class="progress-message">{currentProgressMessage}</div>
					</div>
					<div class="progress-elapsed">Общее время: {elapsedSec} сек</div>
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
						<span class="progress-pill">Правила: {checkedRules} из {ruleTotal}</span>
					{/if}
					{#if typeof currentRuleIndex === 'number' && ruleTotal > 0}
						<span class="progress-pill">Текущее правило: #{currentRuleIndex}</span>
					{/if}
					{#if currentRuleSet}
						<span class="progress-pill">Rule-set: <code>{currentRuleSet}</code></span>
					{/if}
				</div>
				<div class="progress-hint">Инспектор симулирует правила и может проверять rule_set через sing-box.</div>
				<div class="step-stack" aria-label="Ход проверки">
					{#if previousStep}
						<div class="step-card step-{previousStep.status}">
							<span class="step-dot"></span>
							<div class="step-content">
								<div class="step-kicker">Проверено</div>
								<div class="step-label">{previousStep.label}</div>
								<div class="step-meta">
									{#if previousStep.meta}<span>{previousStep.meta}</span>{/if}
									{#if previousStep.durationMs}<span>{formatDuration(previousStep.durationMs)}</span>{/if}
								</div>
							</div>
						</div>
					{/if}
					{#if currentStep}
						<div class="step-card step-current">
							<span class="step-dot"></span>
							<div class="step-content">
								<div class="step-kicker">Сейчас</div>
								<div class="step-label">{currentStep.label}</div>
								{#if currentStep.meta}<div class="step-meta"><span>{currentStep.meta}</span></div>{/if}
							</div>
						</div>
					{/if}
					{#if nextStep}
						<div class="step-card step-next">
							<span class="step-dot"></span>
							<div class="step-content">
								<div class="step-kicker">Далее</div>
								<div class="step-label">{nextStep.label}</div>
								{#if nextStep.meta}<div class="step-meta"><span>{nextStep.meta}</span></div>{/if}
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
						<div class="input-type">{result.inputType === 'domain' ? 'домен' : 'IP-адрес'}</div>
					</div>
					<div class="arrow">→</div>
					<div
						class="dest-block"
						class:dest-reject={isReject}
						class:dest-final={result.matchedRule < 0 && !isReject}
					>
						<div class="dest-value">{result.destination}</div>
						<div class="dest-meta">
							{#if result.matchedRule >= 0}
								Сработало правило #{result.matchedRule}
							{:else}
								Дефолтный outbound (final: {result.final || 'direct'})
							{/if}
						</div>
					</div>
				</div>

				{#if matchedRuleData}
					<div class="match-detail">
						<div class="match-header">
							<span class="rule-num">Правило #{matchedRuleData.index}</span>
							<span class="badge badge-{actionVariant(matchedRuleData.action)}">
								{actionLabel(matchedRuleData.action)}
							</span>
							{#if matchedRuleData.outbound}
								<span class="match-outbound">→ {matchedRuleData.outbound}</span>
							{/if}
						</div>
						{#if matchedRuleData.reason}
							<div class="match-reason">{matchedRuleData.reason}</div>
						{/if}
						{#if matchedRuleData.conditions && matchedRuleData.conditions.length}
							<div class="match-conditions">
								<span class="cond-label">Условия:</span>
								{matchedRuleData.conditions.join(', ')}
							</div>
						{/if}
					</div>
				{:else}
					<div class="match-detail no-match">
						<span>
							Ни одно правило не сработало — трафик пойдёт через
							<strong>{result.final || 'direct'}</strong>.
						</span>
					</div>
				{/if}
			</section>

			{#if result.note}
				<div class="note-banner">
					<strong>Примечание:</strong>
					{result.note}
				</div>
			{/if}

			{#if inspectionReport}
				<section class="card inspect-report">
					<div class="report-header">
						<div>
							<div class="report-title">Отчёт проверки</div>
							<div class="report-subtitle">Проверено правил: {inspectionReport.checkedRules} из {inspectionReport.totalRules}</div>
						</div>
						<div class="report-duration">{formatDuration(inspectionReport.totalDurationMs) || 'менее 1 сек'}</div>
					</div>
					<div class="report-grid">
						<div class="report-item">
							<span>Решение</span>
							<strong>{inspectionReport.destination}</strong>
						</div>
						<div class="report-item">
							<span>Сработало</span>
							<strong>
								{inspectionReport.matchedRule >= 0
									? `Правило #${inspectionReport.matchedRule}`
									: `Final: ${inspectionReport.final}`}
							</strong>
						</div>
					</div>
					{#if inspectionReport.slowestSteps.length > 0}
						<div class="report-section">
							<div class="report-section-title">Самые долгие этапы</div>
							{#each inspectionReport.slowestSteps as step (step.id)}
								<div class="report-row">
									<span>{step.label}</span>
									<strong>{formatDuration(step.durationMs) || 'менее 1 сек'}</strong>
								</div>
							{/each}
						</div>
					{/if}
					{#if inspectionReport.ruleSetSteps.length > 0}
						<div class="report-section">
							<div class="report-section-title">Rule-set проверки</div>
							{#each inspectionReport.ruleSetSteps as step (step.id)}
								<div class="report-row">
									<span>{step.label}</span>
									<strong>{formatDuration(step.durationMs) || 'менее 1 сек'}</strong>
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
					{showAllRules ? 'Скрыть' : 'Показать'} разбор всех правил ({result.matches.length})
				</button>
			{/if}

			{#if showAllRules}
				<section class="card walkthrough">
					<header class="walkthrough-header">Порядок проверки правил</header>
					<ul class="walkthrough-list">
						{#each result.matches as m (m.index)}
							<li
								class="walkthrough-row"
								class:row-matched={m.matched}
								class:row-non-final={m.matched &&
									(m.action === 'sniff' || m.action === 'hijack-dns')}
							>
								<div class="row-head">
									<span class="row-index">#{m.index}</span>
									<span class="badge badge-{actionVariant(m.action)}">
										{actionLabel(m.action)}
									</span>
									{#if m.outbound}
										<span class="row-outbound">→ {m.outbound}</span>
									{/if}
									<span class="row-status">
										{#if m.matched}
											{#if m.action === 'sniff' || m.action === 'hijack-dns'}
												совпало (не финальное)
											{:else}
												совпало
											{/if}
										{:else}
											не совпало
										{/if}
									</span>
								</div>
								{#if m.conditions && m.conditions.length}
									<div class="row-conditions">{m.conditions.join(' · ')}</div>
								{/if}
								{#if m.reason}
									<div class="row-reason">{m.reason}</div>
								{/if}
							</li>
						{/each}
						<li class="walkthrough-row row-final">
							<div class="row-head">
								<span class="row-index">∞</span>
								<span class="badge badge-other">FINAL</span>
								<span class="row-outbound">→ {result.final || 'direct'}</span>
								<span class="row-status">используется, если ни одно правило не подходит</span>
							</div>
						</li>
					</ul>
				</section>
			{/if}
		{:else if !error && !testing}
			<div class="empty-state">
				Введите домен или IP-адрес — инспектор покажет, через какой outbound пойдёт
				трафик и какое правило сработает. Это симуляция, sing-box не вызывается.
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

	.chip {
		padding: 0.25rem 0.55rem;
		font-size: 12px;
		font-family: ui-monospace, SFMono-Regular, Menlo, Monaco, Consolas, monospace;
		background: var(--color-bg-primary);
		color: var(--color-text-secondary);
		border: 1px solid var(--color-border);
		border-radius: 999px;
		cursor: pointer;
	}

	.chip:hover:not(:disabled) {
		background: var(--color-bg-hover);
		color: var(--color-text-primary);
	}

	.chip:disabled {
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
