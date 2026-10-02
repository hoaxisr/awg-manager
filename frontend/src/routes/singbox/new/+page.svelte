<script lang="ts">
	import { m } from '$lib/i18n';
	import { goto } from '$app/navigation';
	import { PageContainer } from '$lib/components/layout';
	import { SingboxGhostTerminal } from '$lib/components/singbox';
	import { ArrowLeft } from 'lucide-svelte';
	import { Button } from '$lib/components/ui';
	import { notifications } from '$lib/stores/notifications';

	function onComplete(imported: number): void {
		notifications.success(m.singbox_imported_tunnels({ count: imported }));
		goto('/?tab=singbox');
	}
</script>

<svelte:head>
	<title>{m.singbox_new_title()}</title>
</svelte:head>

<PageContainer>
	<div class="sticky-header">
		<div class="header-left">
			<Button variant="ghost" size="sm" onclick={() => goto('/?tab=singbox')} iconBefore={backIcon}>
				{m.tunnels_back()}
			</Button>
			<h1 class="page-title">{m.singbox_new_title()}</h1>
		</div>
	</div>

	<p class="page-intro">
		{m.singbox_new_intro_prefix()} <code>vless://</code>, <code>hysteria2://</code>,
		<code>mieru://</code> {m.singbox_new_intro_or()} <code>mierus://</code> {m.singbox_new_intro_suffix()}
	</p>

	<SingboxGhostTerminal oncomplete={onComplete} />
</PageContainer>

{#snippet backIcon()}
	<ArrowLeft size={14} strokeWidth={2} aria-hidden="true" />
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
