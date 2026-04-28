<script lang="ts">
	interface Props {
		componentName?: string;
	}
	let { componentName = 'Модули ядра подсистемы сетевой фильтрации' }: Props = $props();
</script>

<div class="banner">
	<div class="title">Требуется компонент Keenetic</div>
	<div class="body">
		Установите компонент <strong>{componentName}</strong> через веб-интерфейс роутера:
		<em>Общие настройки → Настройка компонентов → Компоненты</em>. После установки перезагрузите роутер.
	</div>
</div>

<style>
	.banner {
		background: rgba(224, 175, 104, 0.12);
		border-left: 4px solid var(--warning);
		padding: 1rem 1.25rem;
		margin-bottom: 1rem;
		border-radius: 4px;
		color: var(--text);
	}
	.title {
		font-weight: 600;
		margin-bottom: 0.25rem;
		color: var(--warning);
	}
	.body {
		font-size: 0.85rem;
		line-height: 1.5;
		color: var(--text-secondary, var(--text));
	}
</style>
