<script lang="ts">
	import { Modal } from '$lib/components/ui';
	import type { UsageLevel } from '$lib/types/usageLevel';
	import { USAGE_LEVEL_LABELS } from '$lib/types/usageLevel';

	interface Props {
		value: UsageLevel;
		saving: boolean;
		onSelect: (level: UsageLevel) => void | Promise<void>;
		initialExpanded?: boolean;
		highlighted?: boolean;
	}

	let { value, saving, onSelect, initialExpanded = false, highlighted = false }: Props = $props();

	type LevelOption = {
		value: UsageLevel;
		title: string;
		summary: string;
		includes: string[];
	};

	const OPTIONS: LevelOption[] = [
		{
			value: 'basic',
			title: USAGE_LEVEL_LABELS.basic,
			summary: 'Основные туннели, диагностика и базовая маршрутизация',
			includes: [
				'AmneziaWG-туннели',
				'Системные WireGuard-туннели',
				'Диагностика и проверки',
				'Маршрутизация: NDMS и VPN для устройств',
				'Карточка «Система»: базовые данные',
			],
		},
		{
			value: 'advanced',
			title: USAGE_LEVEL_LABELS.advanced,
			summary: 'Туннели, серверы и полная маршрутизация',
			includes: [
				'Всё из уровня "Базовый"',
				'SingBox-туннели и подписки',
				'Серверы WireGuard и DeviceProxy',
				'Маршрутизация: политики доступа, IP-адреса, Sing-box Router',
				'Веб-терминал и режим списка для AWG',
				'Системный мониторинг',
				'Карточка «Система»: добавляются данные по железу',
				'Настройка цветовой схемы',
				'Компактная ширина интерфейса (опционально)',
			],
		},
		{
			value: 'expert',
			title: USAGE_LEVEL_LABELS.expert,
			summary: 'Полный набор функций для тонкой настройки',
			includes: [
				'Всё из уровня "Расширенный"', 
			    'HydraRoute Neo', 
				'Sing-box Router', 
				'Проверка конфигурации AWG',
				'Создание API-ключа',
				'Карточка «Система»: полные подробные данные + спойлер',
			],
		},
	];

	let infoFor = $state<UsageLevel | null>(null);
	const infoOpt = $derived(infoFor ? OPTIONS.find((o) => o.value === infoFor) : null);

	let expanded = $state(false);
	$effect(() => {
		expanded = initialExpanded;
	});

	function selectLevel(level: UsageLevel) {
		if (level === value || saving) return;
		void onSelect(level);
	}

	function openInfo(e: Event, level: UsageLevel) {
		e.stopPropagation();
		infoFor = level;
	}
</script>

<div class="card" class:highlighted>
	<div class="section-label">Общие</div>
	<div class="setting-row level-header-row">
		<div class="flex flex-col gap-1">
			<span class="font-medium">Уровень использования</span>
			<span class="setting-description">
				Скрывает разделы, которые вам не нужны. Данные при понижении уровня не удаляются.
			</span>
		</div>
		<button
			type="button"
			class="level-expand-control"
			aria-expanded={expanded}
			aria-controls="usage-level-picker"
			aria-label="Показать или скрыть выбор уровня"
			onclick={() => (expanded = !expanded)}
		>
			<span class="current-level">{USAGE_LEVEL_LABELS[value]}</span>
			<svg
				class="chevron"
				class:open={expanded}
				viewBox="0 0 24 24"
				fill="none"
				stroke="currentColor"
				stroke-width="2"
				aria-hidden="true"
			>
				<polyline points="6 9 12 15 18 9" />
			</svg>
		</button>
	</div>

	{#if expanded}
		<div id="usage-level-picker" class="level-picker">
			<div
				class="level-grid"
				role="radiogroup"
				aria-label="Уровень использования"
				aria-busy={saving}
			>
				{#each OPTIONS as opt (opt.value)}
					{@const selected = value === opt.value}
					<button
						type="button"
						role="radio"
						aria-checked={selected}
						class="level-card"
						class:selected
						disabled={saving}
						onclick={() => selectLevel(opt.value)}
					>
						<span
							class="info-btn"
							role="button"
							tabindex="0"
							aria-label={`Подробнее про уровень «${opt.title}»`}
							onclick={(e) => openInfo(e, opt.value)}
							onkeydown={(e) => {
								if (e.key === 'Enter' || e.key === ' ') {
									openInfo(e, opt.value);
								}
							}}
						>
							<svg viewBox="0 0 24 24" aria-hidden="true">
								<circle cx="12" cy="12" r="10" />
								<line x1="12" y1="11" x2="12" y2="17" />
								<circle cx="12" cy="7.5" r="0.8" />
							</svg>
						</span>

						<div class="level-title">{opt.title}</div>

						{#if selected}
							<span class="level-check" aria-hidden="true">
								<svg viewBox="0 0 24 24"><polyline points="20 6 9 17 4 12" /></svg>
							</span>
						{/if}
					</button>
				{/each}
			</div>
		</div>
	{/if}
</div>

<Modal
	open={infoFor !== null}
	title={infoOpt ? `Уровень: ${infoOpt.title}` : ''}
	size="md"
	onclose={() => (infoFor = null)}
>
	{#if infoOpt}
		<div class="level-info-panel">
			<div class="level-info-summary">
				<span class="level-info-eyebrow">Кратко</span>
				<p>{infoOpt.summary}</p>
			</div>

			<div class="level-info-section">
				<h3>Что включает</h3>
				<ul class="level-info-list">
					{#each infoOpt.includes as item}
						<li class="level-info-item">
							<span class="level-info-bullet" aria-hidden="true">
								<svg viewBox="0 0 24 24">
									<path d="M20 6 9 17l-5-5" />
								</svg>
							</span>
							<span>{item}</span>
						</li>
					{/each}
				</ul>
			</div>
		</div>
	{/if}
</Modal>

<style>
	.level-header-row {
		align-items: center;
	}

	.level-expand-control {
		display: inline-flex;
		align-items: center;
		gap: 0.5rem;
		flex-shrink: 0;
		background: transparent;
		border: 0;
		padding: 0;
		margin: 0;
		color: var(--color-text-muted);
		font-size: 0.8125rem;
		cursor: pointer;
	}

	.level-expand-control:focus-visible {
		outline: 2px solid var(--color-accent);
		outline-offset: 2px;
		border-radius: var(--radius-sm);
	}

	.current-level {
		color: var(--color-text-secondary);
		white-space: nowrap;
		overflow: hidden;
		text-overflow: ellipsis;
		max-width: 8rem;
	}

	.chevron {
		width: 14px;
		height: 14px;
		transition: transform var(--t-fast) ease;
	}

	.chevron.open {
		transform: rotate(180deg);
	}

	.level-picker {
		border-top: 1px solid var(--color-border);
	}

	.level-picker .level-grid {
		padding-top: 0.875rem;
	}

	.level-grid {
		display: grid;
		grid-template-columns: repeat(3, 1fr);
		gap: 0.5rem;
	}
	@media (max-width: 480px) {
		.level-grid {
			grid-template-columns: 1fr;
		}
	}

	.level-card {
		position: relative;
		text-align: center;
		padding: 0.625rem 0.5rem;
		background: var(--color-bg-tertiary);
		border: 1px solid var(--color-border);
		border-radius: var(--radius-md);
		color: inherit;
		font: inherit;
		cursor: pointer;
		transition:
			border-color var(--t-fast) ease,
			background var(--t-fast) ease;
	}
	.level-card:hover:not(:disabled):not(.selected) {
		background: var(--color-bg-hover);
		border-color: var(--color-border-strong);
	}
	.level-card:focus-visible {
		outline: 2px solid var(--color-accent);
		outline-offset: 2px;
	}
	.level-card.selected {
		border-color: var(--color-accent);
		background: var(--color-accent-tint);
	}
	.level-card:disabled {
		opacity: 0.6;
		cursor: not-allowed;
	}

	.level-title {
		font-weight: 600;
		font-size: 0.875rem;
		padding: 0 1.25rem;
		white-space: nowrap;
		overflow: hidden;
		text-overflow: ellipsis;
	}

	.level-check {
		position: absolute;
		top: 0.375rem;
		left: 0.375rem;
		width: 14px;
		height: 14px;
		color: var(--color-accent);
	}
	.level-check svg {
		width: 100%;
		height: 100%;
		fill: none;
		stroke: currentColor;
		stroke-width: 2;
	}

	.info-btn {
		position: absolute;
		top: 0.375rem;
		right: 0.375rem;
		width: 14px;
		height: 14px;
		color: var(--color-text-muted);
		cursor: pointer;
		display: inline-flex;
		align-items: center;
		justify-content: center;
		border-radius: 50%;
	}
	.info-btn:hover {
		color: var(--color-text-primary);
	}
	.info-btn:focus-visible {
		outline: 2px solid var(--color-accent);
		outline-offset: 2px;
	}
	.info-btn svg {
		width: 12px;
		height: 12px;
		fill: none;
		stroke: currentColor;
		stroke-width: 2;
	}

	.level-info-panel {
		display: flex;
		flex-direction: column;
		gap: 1rem;
	}

	.level-info-summary {
		padding: 0.875rem 1rem;
		background: var(--color-bg-tertiary);
		border: 1px solid var(--color-border);
		border-radius: var(--radius-md);
	}

	.level-info-eyebrow {
		display: inline-flex;
		margin-bottom: 0.375rem;
		font-size: 0.75rem;
		font-weight: 600;
		letter-spacing: 0.04em;
		text-transform: uppercase;
		color: var(--color-text-muted);
	}

	.level-info-summary p {
		margin: 0;
		font-size: 1rem;
		line-height: 1.5;
		color: var(--color-text-primary);
	}

	.level-info-section h3 {
		margin: 0 0 0.75rem;
		font-size: 0.9375rem;
		font-weight: 700;
		color: var(--color-text-primary);
	}

	.level-info-list {
		list-style: none;
		margin: 0;
		padding: 0;
		display: flex;
		flex-direction: column;
		gap: 0.625rem;
	}

	.level-info-item {
		display: flex;
		align-items: flex-start;
		gap: 0.75rem;
		padding: 0.75rem 0.875rem;
		background: var(--color-bg-tertiary);
		border: 1px solid var(--color-border);
		border-radius: var(--radius-md);
		line-height: 1.45;
		color: var(--color-text-secondary);
	}

	.level-info-bullet {
		display: inline-flex;
		align-items: center;
		justify-content: center;
		width: 1.25rem;
		height: 1.25rem;
		flex: 0 0 1.25rem;
		margin-top: 0.0625rem;
		border-radius: 999px;
		background: var(--color-accent-tint);
		color: var(--color-accent);
	}

	.level-info-bullet svg {
		width: 0.875rem;
		height: 0.875rem;
		fill: none;
		stroke: currentColor;
		stroke-linecap: round;
		stroke-linejoin: round;
		stroke-width: 2;
	}

	.card.highlighted {
		animation: usage-level-glow 2.8s ease-out forwards;
	}

	@keyframes usage-level-glow {
		0%   { box-shadow: none; }
		12%  { box-shadow: 0 0 0 3px color-mix(in srgb, var(--color-accent) 55%, transparent), 0 0 18px 2px color-mix(in srgb, var(--color-accent) 22%, transparent); }
		30%  { box-shadow: 0 0 0 1px color-mix(in srgb, var(--color-accent) 20%, transparent); }
		48%  { box-shadow: 0 0 0 3px color-mix(in srgb, var(--color-accent) 40%, transparent), 0 0 14px 2px color-mix(in srgb, var(--color-accent) 15%, transparent); }
		65%  { box-shadow: 0 0 0 1px color-mix(in srgb, var(--color-accent) 15%, transparent); }
		82%  { box-shadow: 0 0 0 2px color-mix(in srgb, var(--color-accent) 22%, transparent), 0 0 8px 1px color-mix(in srgb, var(--color-accent) 10%, transparent); }
		100% { box-shadow: none; }
	}
</style>
