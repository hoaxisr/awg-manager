<script lang="ts">
	import type { NavIcon, NavItem } from '$lib/data/navigation';

	interface Props {
		item: NavItem;
		active: boolean;
		/** Иконка плоского пункта верхнего уровня (у пунктов групп её нет). */
		icon?: NavIcon;
		/** Пункт внутри группы — с отступом под иконку группы. */
		indent?: boolean;
		/**
		 * Готовое значение бейджа справа (счётчик или режим). Источник значения
		 * объявляет модель, разбор источника в стор — `stores/navBadges.ts`.
		 * `null` — бейджа нет, и разметки под него тоже нет.
		 */
		badge?: string | null;
		/** Зелёная статус-точка (подключение — фаза 3). */
		dot?: boolean;
		onNavigate?: () => void;
	}

	let {
		item,
		active,
		icon,
		indent = false,
		badge = null,
		dot = false,
		onNavigate,
	}: Props = $props();

	const Icon = $derived(icon);
</script>

<a
	href={item.href}
	class="nav-item"
	class:active
	class:indent
	aria-current={active ? 'page' : undefined}
	onclick={onNavigate}
>
	{#if Icon}<Icon size={16} aria-hidden="true" />{/if}
	<span class="nav-item-label">{item.label}</span>
	{#if dot}<span class="nav-item-dot" aria-hidden="true"></span>{/if}
	{#if badge}<span class="nav-item-badge">{badge}</span>{/if}
</a>

<style>
	.nav-item {
		display: flex;
		align-items: center;
		gap: 0.5rem;
		padding: 0.375rem 0.5rem;
		border-radius: var(--radius-sm);
		font-size: 13px;
		color: var(--color-text-secondary);
		text-decoration: none;
		white-space: nowrap;
		transition:
			background var(--t-fast) ease,
			color var(--t-fast) ease;
	}

	.nav-item.indent {
		padding-left: 2rem;
	}

	.nav-item:hover {
		background: var(--color-bg-hover);
		color: var(--color-text-primary);
	}

	.nav-item.active {
		/* Непрозрачная смесь, а не tint: полупрозрачный фон при скролле
		   просвечивает контент страницы под сайдбаром. */
		background: color-mix(in srgb, var(--color-accent) 18%, var(--color-bg-secondary));
		color: var(--color-accent);
		font-weight: 500;
		box-shadow: inset 3px 0 0 var(--color-accent);
	}

	.nav-item-label {
		flex: 1;
		overflow: hidden;
		text-overflow: ellipsis;
	}

	.nav-item-dot {
		width: 7px;
		height: 7px;
		border-radius: 999px;
		background: var(--color-success);
		flex: none;
	}

	.nav-item-badge {
		font-family: var(--font-mono);
		font-size: 10px;
		font-weight: 600;
		padding: 1px 6px;
		border-radius: var(--radius-sm);
		background: var(--color-muted-tint);
		color: var(--color-text-muted);
		flex: none;
		/* Бейдж режима — текст переменной длины («Политики + tun»). Ужимается он,
		   а не подпись пункта: подпись отвечает на «куда я попаду». */
		max-width: 50%;
		overflow: hidden;
		text-overflow: ellipsis;
	}

	.nav-item.active .nav-item-badge {
		background: var(--color-accent-tint);
		color: var(--color-accent);
	}
</style>
