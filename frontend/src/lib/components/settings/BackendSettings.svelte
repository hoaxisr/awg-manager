<script lang="ts">
	import { Modal } from '$lib/components/ui';
	import { LoadingSpinner } from '$lib/components/layout';
	import type { Settings, SystemInfo } from '$lib/types';

	interface Props {
		settings: Settings;
		systemInfo: SystemInfo | null;
		saving: boolean;
		onModeChange: (mode: 'auto' | 'kernel' | 'userspace') => void;
		onRestart: (mode: 'auto' | 'kernel' | 'userspace') => Promise<void>;
	}

	let { settings, systemInfo, saving, onModeChange, onRestart }: Props = $props();

	let showWarning = $state(false);
	let pendingMode: 'auto' | 'kernel' | 'userspace' | null = $state(null);
	let modalStep: 'confirm' | 'quiz' | 'saved' | 'restarting' = $state('confirm');
	let savedMode: 'auto' | 'kernel' | 'userspace' | null = $state(null);
	let restarting = $state(false);
	let quizAnswers: (number | null)[] = $state([null, null, null]);
	let quizError = $state(false);

	const quizQuestions = [
		{
			question: 'Что произойдет с туннелями после перехода?',
			options: ['Ничего', 'Они будут пересозданы', '42'],
			correct: 1,
		},
		{
			question: 'Будет ли видна статистика трафика в UI роутера?',
			options: ['Да', 'Нет'],
			correct: 1,
		},
		{
			question: 'Нужно ли будет заново настроить привязки политик и маршрутизации?',
			options: ['Да', 'Нет'],
			correct: 0,
		},
	];

	// Show pending selection while confirmation is open, otherwise actual setting
	let displayMode = $derived(pendingMode ?? (settings.backendMode || 'auto'));

	function handleModeSelect(mode: 'auto' | 'kernel' | 'userspace') {
		if (mode !== settings.backendMode) {
			pendingMode = mode;
			modalStep = 'confirm';
			showWarning = true;
		}
	}

	function confirmChange() {
		if (pendingMode) {
			if (isTargetKernel) {
				modalStep = 'quiz';
			} else {
				onModeChange(pendingMode);
				savedMode = pendingMode;
				pendingMode = null;
				modalStep = 'saved';
			}
		}
	}

	function submitQuiz() {
		const allCorrect = quizQuestions.every((q, i) => quizAnswers[i] === q.correct);
		if (allCorrect && pendingMode) {
			onModeChange(pendingMode);
			savedMode = pendingMode;
			pendingMode = null;
			modalStep = 'saved';
		} else {
			quizError = true;
			resetModal();
		}
	}

	async function handleRestart() {
		if (!savedMode) return;
		modalStep = 'restarting';
		restarting = true;
		try {
			await onRestart(savedMode);
		} finally {
			restarting = false;
			resetModal();
		}
	}

	function cancelChange() {
		resetModal();
	}

	function closeSaved() {
		resetModal();
	}

	function resetModal() {
		showWarning = false;
		pendingMode = null;
		savedMode = null;
		modalStep = 'confirm';
		restarting = false;
		quizAnswers = [null, null, null];
		quizError = false;
	}

	let modalTitle = $derived.by(() => {
		if (modalStep === 'restarting') return 'Перезапуск';
		if (modalStep === 'saved') return 'Режим изменён';
		if (modalStep === 'quiz') return 'Проверка';
		return 'Подтверждение';
	});

	let targetMode = $derived(pendingMode ?? savedMode);
	let isTargetKernel = $derived(targetMode === 'kernel');

	const modeLabels = {
		auto: 'Авто (рекомендуется)',
		kernel: 'Модуль ядра',
		userspace: 'Userspace'
	} as const;

	const modeDescriptions = {
		auto: 'Модуль ядра если загружен, иначе userspace',
		kernel: 'amneziawg.ko — максимальная производительность',
		userspace: 'amneziawg-go — работает без модуля ядра'
	} as const;

	let kernelStatusText = $derived.by(() => {
		if (systemInfo?.kernelModuleLoaded) return 'Загружен';
		if (systemInfo?.kernelModuleExists) return 'Не загружен';
		return 'Отсутствует';
	});

	let kernelStatusClass = $derived.by(() => {
		if (systemInfo?.kernelModuleLoaded) return 'status-loaded';
		if (systemInfo?.kernelModuleExists) return 'status-exists';
		return 'status-missing';
	});

	let activeBackendText = $derived(
		systemInfo?.activeBackend === 'kernel' ? 'Модуль ядра' : 'Userspace'
	);
</script>

<div>
	<div class="header">
		<span class="font-medium">Режим работы</span>
		<div class="header-status">
			<span class="status-badge {kernelStatusClass}">{kernelStatusText}</span>
			<span class="status-separator">·</span>
			<span class="active-backend">{activeBackendText}</span>
		</div>
	</div>

	<div class="mode-list">
		{#each (['auto', 'kernel', 'userspace'] as const) as mode}
			{@const isDisabled = saving || (mode === 'kernel' && !systemInfo?.kernelModuleLoaded)}
			<label class="mode-item" class:disabled={isDisabled} class:selected={displayMode === mode}>
				<input
					type="radio"
					name="backendMode"
					value={mode}
					checked={displayMode === mode}
					disabled={isDisabled}
					onchange={() => handleModeSelect(mode)}
				/>
				<span class="mode-label">{modeLabels[mode]}</span>
				<span class="mode-desc">{modeDescriptions[mode]}</span>
			</label>
		{/each}
	</div>

	{#if !systemInfo?.kernelModuleLoaded && systemInfo?.kernelModuleExists}
		<div class="notice">
			Модуль ядра установлен, но не загружен. Будет загружен при следующем запуске.
		</div>
	{:else if !systemInfo?.kernelModuleExists && !systemInfo?.kernelModuleLoaded}
		<div class="notice">
			Модуль ядра не найден. Установите пакет с модулем ядра для вашей модели роутера.
		</div>
	{/if}
</div>

<Modal open={showWarning} title={modalTitle} onclose={modalStep === 'restarting' ? () => {} : cancelChange}>
	{#if modalStep === 'confirm'}
		<div class="modal-description">
			<p>Переключить режим на <strong>{pendingMode ? modeLabels[pendingMode] : ''}</strong>?</p>
			{#if isTargetKernel}
				<p>При использовании модуля ядра статистика туннеля не будет отображаться в веб-интерфейсе роутера. Статистику можно посмотреть в AWG Manager.</p>
			{/if}
			<p class="warning-text">Интерфейсы туннелей будут пересозданы. Привязки к политикам маршрутизации и маршрутам в интерфейсе роутера будут сброшены.</p>
			<p>Конфигурация туннелей (ключи, endpoint, параметры) сохранится.</p>
		</div>
	{:else if modalStep === 'quiz'}
		<div class="quiz">
			{#each quizQuestions as q, qi}
				<div class="quiz-question">
					<p class="quiz-question-text">{qi + 1}. {q.question}</p>
					<div class="quiz-options">
						{#each q.options as option, oi}
							<label class="quiz-option" class:selected={quizAnswers[qi] === oi}>
								<input
									type="radio"
									name="quiz-{qi}"
									checked={quizAnswers[qi] === oi}
									onchange={() => quizAnswers[qi] = oi}
								/>
								<span>{option}</span>
							</label>
						{/each}
					</div>
				</div>
			{/each}
		</div>
	{:else if modalStep === 'saved'}
		<div class="modal-description">
			<p>Режим изменён на <strong>{savedMode ? modeLabels[savedMode] : ''}</strong>.</p>
			<p>Для применения необходим перезапуск awg-manager.</p>
			<p class="warning-text">Интерфейсы туннелей будут удалены и созданы заново.</p>
		</div>
	{:else if modalStep === 'restarting'}
		<div class="restart-status">
			<LoadingSpinner size="md" />
			<p>Перезапуск awg-manager...</p>
		</div>
	{/if}

	{#snippet actions()}
		{#if modalStep === 'confirm'}
			<button class="btn btn-secondary" onclick={cancelChange}>Отмена</button>
			<button class="btn btn-primary" onclick={confirmChange} disabled={saving}>
				{saving ? 'Сохранение...' : 'Подтвердить'}
			</button>
		{:else if modalStep === 'quiz'}
			<button class="btn btn-secondary" onclick={cancelChange}>Отмена</button>
			<button class="btn btn-primary" onclick={submitQuiz} disabled={quizAnswers.some(a => a === null)}>
				Продолжить
			</button>
		{:else if modalStep === 'saved'}
			<button class="btn btn-secondary" onclick={closeSaved}>Позже</button>
			<button class="btn btn-warning" onclick={handleRestart}>
				Перезапустить
			</button>
		{/if}
	{/snippet}
</Modal>

<style>
	.header {
		display: flex;
		align-items: baseline;
		justify-content: space-between;
		margin-bottom: 1rem;
		flex-wrap: wrap;
		gap: 0.5rem;
	}

	.header-status {
		display: flex;
		align-items: center;
		gap: 0.5rem;
	}

	.status-badge {
		padding: 0.125rem 0.5rem;
		border-radius: 4px;
		font-size: 0.75rem;
		font-weight: 500;
	}

	.status-loaded {
		background: var(--success-bg, rgba(34, 197, 94, 0.1));
		color: var(--success, #22c55e);
	}

	.status-exists {
		background: var(--warning-bg, rgba(234, 179, 8, 0.1));
		color: var(--warning, #eab308);
	}

	.status-missing {
		background: var(--bg-tertiary);
		color: var(--text-muted);
	}

	.status-separator {
		color: var(--text-muted);
	}

	.active-backend {
		font-size: 0.8125rem;
		color: var(--text-secondary);
	}

	.mode-list {
		display: flex;
		flex-direction: column;
		gap: 0.125rem;
	}

	.mode-item {
		display: flex !important;
		align-items: baseline;
		gap: 0.5rem;
		padding: 0.5rem 0.25rem;
		margin-bottom: 0;
		border-radius: 6px;
		cursor: pointer;
		transition: background 0.15s;
	}

	.mode-item:hover:not(.disabled) {
		background: var(--bg-tertiary);
	}

	.mode-item.selected {
		background: var(--bg-tertiary);
	}

	.mode-item.disabled {
		opacity: 0.4;
		cursor: not-allowed;
	}

	.mode-item input[type="radio"] {
		width: auto;
		padding: 0;
		border: none;
		background: none;
		accent-color: var(--accent);
		flex-shrink: 0;
	}

	:global([data-theme="light"]) .mode-item input[type="radio"] {
		filter: invert(1);
	}

	.mode-label {
		font-weight: 500;
		color: var(--text-primary);
		white-space: nowrap;
	}

	.mode-desc {
		font-size: 0.8125rem;
		color: var(--text-muted);
	}

	.notice {
		margin-top: 0.75rem;
		padding: 0.625rem 0.75rem;
		background: var(--bg-tertiary);
		border-radius: 6px;
		font-size: 0.8125rem;
		color: var(--text-secondary);
	}

	.modal-description {
		display: flex;
		flex-direction: column;
		gap: 0.5rem;
		color: var(--text-secondary);
	}

	.modal-description p {
		margin: 0;
	}

	.warning-text {
		color: var(--warning, #eab308);
		font-weight: 500;
	}

	.quiz {
		display: flex;
		flex-direction: column;
		gap: 1rem;
	}

	.quiz-question-text {
		margin: 0 0 0.375rem;
		color: var(--text-primary);
		font-weight: 500;
		font-size: 0.875rem;
	}

	.quiz-options {
		display: flex;
		flex-direction: column;
		gap: 0.125rem;
	}

	.quiz-option {
		display: flex !important;
		align-items: center;
		gap: 0.5rem;
		padding: 0.375rem 0.5rem;
		margin-bottom: 0;
		border-radius: 6px;
		cursor: pointer;
		font-size: 0.8125rem;
		color: var(--text-secondary);
		transition: background 0.15s;
	}

	.quiz-option:hover {
		background: var(--bg-tertiary);
	}

	.quiz-option.selected {
		background: var(--bg-tertiary);
		color: var(--text-primary);
	}

	.quiz-option input[type="radio"] {
		width: auto;
		padding: 0;
		border: none;
		background: none;
		accent-color: var(--accent);
		flex-shrink: 0;
	}

	:global([data-theme="light"]) .quiz-option input[type="radio"] {
		filter: invert(1);
	}

	.restart-status {
		display: flex;
		flex-direction: column;
		align-items: center;
		gap: 1rem;
		padding: 1.5rem 0;
		color: var(--text-secondary);
	}

	.restart-status p {
		margin: 0;
	}

</style>
