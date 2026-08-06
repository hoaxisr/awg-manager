<script lang="ts">
	// Шапка страницы «Движок»: заголовок, пилюля состояния, пилюля режима и
	// управление движком (раздел 3 спеки nav-v3 5D).
	//
	// ВРЕМЯ РАБОТЫ НЕ ПОКАЗЫВАЕМ. Макет рисует «Работает · 4д 02ч», но аптайма
	// в DTO статуса нет, и заводить ради него поле бэкенда решено не было.
	//
	// НОСИТЕЛЬ КЛИКА «СБОЙ» → EngineFatalModal — пилюля состояния. Раздел 8
	// спеки обещает клик сохранить, раздел 3 элемента не называет, а
	// стат-полоса «Движка» ячейки состояния не имеет (в отличие от эксперт-вида,
	// где клик жил на ячейке «Движок»). Пилюля — единственное место, которое
	// УЖЕ утверждает «сбой»: причина сбоя и утверждение о сбое это одно и то же,
	// разносить их по разным элементам не за чем. Вторая сегодняшняя точка
	// открытия модалки — узел «Движок» в FlowGraph — приедет вместе со
	// страницей «Мастер» (волна 5D2b), здесь её не дублируем.
	import { Button, StatusDot } from '$lib/components/ui';
	import { PageHeader } from '$lib/components/layout';
	import { api } from '$lib/api/client';
	import { singboxRouter } from '$lib/stores/singboxRouter';
	import { modeSwitch, modeSwitchBusy } from '$lib/stores/modeSwitch';
	import { notifications } from '$lib/stores/notifications';
	import { humanLabel } from '$lib/components/fakeip/switchConsequences';
	import EngineFatalModal from '$lib/components/sb-router/EngineFatalModal.svelte';
	import { canOpenEngineFatal, deriveEnginePill, normalizeRoutingMode } from './engineRunState';

	const status = singboxRouter.status;
	const settings = singboxRouter.settings;

	const enabled = $derived($status?.enabled ?? false);
	const lastError = $derived($status?.lastError ?? '');
	// Режим берёт настройки, а не статус: routingMode отдаёт только ручка
	// настроек (см. комментарий в types/sbRouter.ts).
	const mode = $derived(normalizeRoutingMode($settings?.routingMode));

	const pill = $derived(
		deriveEnginePill({
			enabled,
			active: $status?.active ?? false,
			routingMode: $settings?.routingMode,
		}),
	);
	const fatalClickable = $derived(canOpenEngineFatal(pill, lastError));

	// Подпись режима общая с экраном смены режима и бейджем сайдбара — у одного
	// состояния не должно завестись двух названий.
	const modeLabel = $derived(humanLabel(mode));

	const switchBusy = $derived(modeSwitchBusy($modeSwitch));
	let restarting = $state(false);
	let fatalOpen = $state(false);

	async function restart(): Promise<void> {
		if (restarting) return;
		restarting = true;
		try {
			await api.singboxControl('restart');
			await singboxRouter.reloadStatus();
			notifications.success('Перезапуск sing-box запущен');
		} catch (e) {
			notifications.error(e instanceof Error ? e.message : 'Не удалось перезапустить sing-box');
		} finally {
			restarting = false;
		}
	}

	// Целевой режим кнопки «Включить»: persisted routingMode. Бэкенд после
	// SwitchMode('off') оставляет routingMode прежним, поэтому выключенный
	// движок включается тем же режимом, каким работал. Выбор ДРУГОГО режима —
	// карточка «Захват трафика» (следующая задача волны); когда она появится,
	// цель приедет оттуда, а не отсюда.
	function turnOn(): void {
		modeSwitch.request(mode);
	}
</script>

<PageHeader title="Движок">
	{#snippet badges()}
		{#if fatalClickable}
			<button
				type="button"
				class="pill tone-error is-clickable"
				title={pill.hint}
				onclick={() => (fatalOpen = true)}
			>
				<StatusDot variant="error" size="sm" />
				<span class="pill-label">{pill.label}</span>
				<span class="pill-action">подробнее</span>
			</button>
		{:else}
			<span class="pill" class:tone-success={pill.tone === 'success'} class:tone-error={pill.tone === 'error'} class:tone-muted={pill.tone === 'muted'} title={pill.hint}>
				<StatusDot variant={pill.tone} size="sm" />
				<span class="pill-label">{pill.label}</span>
			</span>
		{/if}
		<span
			class="pill pill-mode"
			class:is-idle={!enabled}
			title={enabled ? 'Активный режим захвата трафика' : 'Режим, в котором движок включится'}
		>
			{modeLabel}
		</span>
	{/snippet}

	{#snippet actions()}
		<Button
			variant="ghost"
			size="sm"
			disabled={!enabled || switchBusy}
			loading={restarting}
			title={enabled ? 'Перезапустить sing-box' : 'Движок выключен — перезапускать нечего'}
			onclick={restart}
		>
			Перезапустить
		</Button>
		{#if enabled}
			<Button
				variant="danger"
				size="sm"
				disabled={switchBusy}
				onclick={() => modeSwitch.request('off')}
			>
				Выключить
			</Button>
		{:else}
			<Button variant="primary" size="sm" disabled={switchBusy} onclick={turnOn}>Включить</Button>
		{/if}
	{/snippet}
</PageHeader>

<EngineFatalModal open={fatalOpen} {lastError} onclose={() => (fatalOpen = false)} />

<style>
	.pill {
		display: inline-flex;
		align-items: center;
		gap: 6px;
		padding: 3px 10px;
		border-radius: 999px;
		border: 1px solid transparent;
		font-size: 12px;
		font-weight: 600;
		font-family: inherit;
		line-height: 1.4;
		white-space: nowrap;
	}

	.tone-success {
		background: color-mix(in srgb, var(--color-success) 16%, transparent);
		color: var(--color-success);
	}
	.tone-error {
		background: color-mix(in srgb, var(--color-error) 16%, transparent);
		color: var(--color-error);
	}
	.tone-muted {
		background: var(--bg-tertiary);
		color: var(--text-muted);
	}

	.is-clickable {
		cursor: pointer;
		border-color: color-mix(in srgb, var(--color-error) 40%, transparent);
	}
	.is-clickable:hover {
		background: color-mix(in srgb, var(--color-error) 26%, transparent);
	}

	.pill-action {
		font-weight: 400;
		text-decoration: underline;
		text-underline-offset: 2px;
		opacity: 0.85;
	}

	/* Режим — техническое имя, поэтому моноширинный, как чипы в макете. */
	.pill-mode {
		background: color-mix(in srgb, var(--accent) 16%, transparent);
		color: var(--accent);
		font-family: var(--font-mono);
		font-size: 11.5px;
	}
	.pill-mode.is-idle {
		background: var(--bg-tertiary);
		color: var(--text-muted);
	}
</style>
