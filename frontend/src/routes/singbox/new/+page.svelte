<script lang="ts">
	import { goto } from '$app/navigation';
	import { PageContainer } from '$lib/components/layout';
	import { SingboxGhostTerminal } from '$lib/components/singbox';
	import { Button } from '$lib/components/ui';
	import { notifications } from '$lib/stores/notifications';

	function onComplete(imported: number): void {
		notifications.success(
			imported === 1 ? 'Импортирован 1 туннель' : `Импортировано ${imported} туннелей`,
		);
		goto('/?tab=singbox');
	}
</script>

<svelte:head>
	<title>Новый Sing-box туннель</title>
</svelte:head>

<PageContainer>
	<div class="sticky-header">
		<div class="header-left">
			<Button variant="ghost" size="sm" onclick={() => goto('/')} iconBefore={backIcon}>
				Назад
			</Button>
			<h1 class="page-title">Новый Sing-box туннель</h1>
		</div>
	</div>

	<p class="page-intro">
		Вставьте одну или несколько ссылок <code>vless://</code>, <code>hysteria2://</code>
		или <code>naive+https://</code> — каждая на своей строке.
	</p>

	<SingboxGhostTerminal oncomplete={onComplete} />
</PageContainer>

{#snippet backIcon()}
	<svg viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2">
		<path d="M19 12H5M12 19l-7-7 7-7" />
	</svg>
{/snippet}

<style>
	.sticky-header {
		display: flex;
		align-items: center;
		justify-content: space-between;
		margin-bottom: 1rem;
	}

	.header-left {
		display: flex;
		align-items: center;
		gap: 0.75rem;
	}

	.page-title {
		font-size: 1.125rem;
		font-weight: 600;
		margin: 0;
	}

	.page-intro {
		color: var(--text-muted);
		font-size: 0.875rem;
		margin: 0 0 1rem 0;
	}

	.page-intro code {
		font-family: var(--font-mono, monospace);
		background: var(--bg-secondary);
		padding: 1px 6px;
		border-radius: 3px;
		font-size: 0.8125rem;
	}
</style>
