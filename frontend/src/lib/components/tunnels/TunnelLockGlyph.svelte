<script lang="ts">
	// Замок защиты туннеля от изменений (#818). Рисуем SVG вручную, а не берём
	// lucide Lock: дужка обязана быть отдельным путём, чтобы её поворот при
	// смене состояния анимировался, а иконка-компонент такого шва не даёт.

	interface Props {
		locked: boolean;
		onclick: () => void;
		/** dense — мелкая сетка (15px), md — карточка и список (17px). */
		size?: 'sm' | 'md';
	}

	let { locked, onclick, size = 'md' }: Props = $props();

	const px = $derived(size === 'sm' ? 15 : 17);
	const label = $derived(locked ? 'Защита включена — снять' : 'Защитить от изменений');
</script>

<button
	type="button"
	class="lock-glyph"
	class:lock-glyph--on={locked}
	title={label}
	aria-label={label}
	{onclick}
>
	<svg
		class="lock-svg"
		width={px}
		height={px}
		viewBox="0 0 24 24"
		fill="none"
		stroke="currentColor"
		stroke-width="2.2"
		stroke-linecap="round"
		stroke-linejoin="round"
		aria-hidden="true"
	>
		<rect x="4" y="11" width="16" height="10" rx="2.5" />
		<path class="lock-shackle" d="M8 11V7a4 4 0 0 1 8 0v4" />
	</svg>
</button>

<style>
	.lock-glyph {
		display: inline-flex;
		align-items: center;
		padding: 2px;
		margin-right: 4px;
		border: 0;
		background: none;
		color: var(--text-secondary);
		opacity: 0.55;
		cursor: pointer;
		line-height: 1;
		transition: opacity 0.15s, color 0.2s;
	}
	.lock-glyph:hover {
		opacity: 1;
	}
	.lock-glyph--on {
		opacity: 1;
		color: #f59e0b;
	}
	.lock-svg {
		display: block;
		overflow: visible;
	}
	.lock-shackle {
		transform-origin: 8px 11px;
		transform: rotate(-30deg) translate(0.5px, -0.5px);
		transition: transform 0.25s cubic-bezier(0.4, 0, 0.2, 1);
	}
	.lock-glyph--on .lock-shackle {
		transform: none;
	}
</style>
