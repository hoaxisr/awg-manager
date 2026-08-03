<script lang="ts">
	import { goto } from '$app/navigation';
	import { PageContainer } from '$lib/components/layout';
	import { SingboxGhostTerminal } from '$lib/components/singbox';
	import { ArrowLeft } from 'lucide-svelte';
	import { Button } from '$lib/components/ui';
	import { notifications } from '$lib/stores/notifications';
	import { pluralize, TUNNEL_WORDS } from '$lib/utils/pluralize';

	function onComplete(imported: number): void {
		const verb = imported === 1 ? 'Импортирован' : 'Импортировано';
		notifications.success(`${verb} ${pluralize(imported, TUNNEL_WORDS)}`);
		goto('/sb/tunnels');
	}
</script>

<svelte:head>
	<title>Новый Sing-box туннель</title>
</svelte:head>

<PageContainer>
	<div class="import-column">
	<div class="sticky-header">
		<div class="header-left">
			<Button variant="ghost" size="sm" onclick={() => goto('/sb/tunnels')} iconBefore={backIcon}>
				Назад
			</Button>
			<h1 class="page-title">Новый Sing-box туннель</h1>
		</div>
	</div>

	<p class="page-intro">
		Вставьте одну или несколько ссылок <code>vless://</code>, <code>hysteria2://</code>,
		<code>mieru://</code> или <code>mierus://</code> — каждая на своей строке.
	</p>

	<SingboxGhostTerminal oncomplete={onComplete} />
	</div>
</PageContainer>

{#snippet backIcon()}
	<ArrowLeft size={14} strokeWidth={2} aria-hidden="true" />
{/snippet}

<style>
	/* Страница теперь во всю ширину оболочки, а тут одно поле для вставки
	   ссылок: без ограничителя на 2560px оно растягивается лентой через весь
	   экран. Сужаем внутри страницы, ширина политики не касается.

	   900px против 700px у парной /awg/tunnels/new — не произвол: там форма из
	   подписанных полей, здесь моноширинное поле, куда вставляют vless:// и
	   hysteria2:// ссылки в одну строку. На 700px такая ссылка переносится
	   заметно раньше, и вставленное становится нечитаемым. */
	.import-column {
		max-width: 900px;
		margin-inline: auto;
	}

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
