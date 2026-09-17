<script lang="ts">
	/**
	 * Индикатор сохранения конфигурации РОУТЕРА (не нашей).
	 *
	 * Всё, что панель меняет через RCI — интерфейсы, маршруты, политики, ACL —
	 * попадает в running-config роутера, то есть живёт в памяти до перезагрузки.
	 * Чтобы правки пережили ребут, роутеру надо отдельно сказать
	 * `system configuration save`; это делает SaveCoordinator с дебаунсом.
	 *
	 * Без индикатора отказ записи невидим: пользователь настроил, всё работает,
	 * он перезагружает роутер — и настроек нет. Выглядит как «панель врёт».
	 *
	 * Состояния приходят с бэкенда как есть (internal/ndms/command/status.go):
	 * idle | pending | saving | error | failed.
	 */
	import { onMount } from 'svelte';
	import { StatusDot, type StatusDotVariant } from '$lib/components/ui';
	import { saveStatus } from '$lib/stores/saveStatus';
	import { formatRelativeTime } from '$lib/utils/format';

	// Подписка нужна не только ради значения: именно она включает стор в
	// реестре — без подписчика createPollingStore не делает даже первой
	// выборки, и событие от координатора уходило бы в никуда.
	// Имя НЕ `state`: в разметке `$state` читается как обращение к стору
	// (и как руна), svelte-check это отвергает.
	const saveState = $derived($saveStatus.data?.state ?? 'idle');
	const pending = $derived($saveStatus.data?.pendingCount ?? 0);
	const lastError = $derived($saveStatus.data?.lastError ?? '');
	const lastSaveAt = $derived($saveStatus.data?.lastSaveAt ?? '');

	// «Всё сохранено» — состояние покоя, и подсвечивать его нечем: зелёная
	// точка в шапке постоянно горела бы ни о чём. Показываем ТОЛЬКО когда есть
	// что сказать.
	const visible = $derived(saveState !== 'idle');

	const variant = $derived<StatusDotVariant>(
		saveState === 'failed' ? 'error'
			: saveState === 'error' ? 'warning'
			: saveState === 'saving' ? 'info'
			: 'warning', // pending
	);

	const label = $derived(
		saveState === 'saving' ? 'Сохраняю конфигурацию роутера…'
			: saveState === 'pending' ? `Конфигурация роутера не сохранена${pending > 0 ? ` (${pending})` : ''}`
			: saveState === 'error' ? 'Сохранение не удалось, будут повторы'
			: saveState === 'failed' ? 'Сохранить конфигурацию не удалось — правки пропадут при перезагрузке'
			: '',
	);

	const hint = $derived(
		[
			label,
			lastError ? `Ошибка: ${lastError}` : '',
			lastSaveAt ? `Последнее сохранение: ${formatRelativeTime(lastSaveAt)}` : '',
		]
			.filter(Boolean)
			.join('\n'),
	);

	let mounted = $state(false);
	onMount(() => {
		mounted = true;
	});
</script>

{#if mounted && visible}
	<span class="save-led" title={hint} aria-label={label} role="img">
		<StatusDot {variant} size="sm" pulse={saveState === 'saving'} ariaLabel={label} />
	</span>
{/if}

<style>
	.save-led {
		display: inline-flex;
		align-items: center;
		margin-left: 2px;
		cursor: help;
	}
</style>
