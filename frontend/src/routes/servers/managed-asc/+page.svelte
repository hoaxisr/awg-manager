<script lang="ts">
	import { m, uiText, type UiText } from '$lib/i18n';
	import { api } from '$lib/api/client';
	import { goto } from '$app/navigation';
	import { page } from '$app/stores';
	import { notifications } from '$lib/stores/notifications';
	import { servers } from '$lib/stores/servers';
	import type { ASCParams, ASCParamsExtended, SystemInfo } from '$lib/types';
	import { PageContainer } from '$lib/components/layout';
	import { ASCEditor } from '$lib/components/asc';
	import { applyDisabledASCState, isZeroASCState, validateASCBeforeSave } from '$lib/utils/asc-validation';
	import { ArrowLeft } from 'lucide-svelte';
	import { Button } from '$lib/components/ui';
	import { generateASCParams } from '$lib/utils/asc-generator';

	let serverId = $derived($page.url.searchParams.get('id') ?? '');

	let ascParams = $state<ASCParams | null>(null);
	let systemInfo = $state<SystemInfo | null>(null);
	let saving = $state(false);
	let loading = $state(true);
	let error = $state<UiText | null>(null);
	let generating = $state(false);

	let canClearASC = $derived.by(() => ascParams !== null && !isZeroASCState(ascParams));

	async function handleGenerateAll() {
		if (!ascParams) return;
		generating = true;
		try {
			const extended = systemInfo?.supportsExtendedASC ?? false;
			const hRanges = systemInfo?.supportsHRanges ?? false;
			const generated = generateASCParams({ extended, hRanges });

			ascParams = extended
				? ({
						...generated,
						s3: generated.s3!,
						s4: generated.s4!,
						i1: '',
						i2: '',
						i3: '',
						i4: '',
						i5: '',
					} satisfies ASCParamsExtended)
				: generated;

			notifications.success(m.servers_asc_generated());
		} catch (e) {
			notifications.error(e instanceof Error ? e.message : m.servers_asc_generate_failed());
		} finally {
			generating = false;
		}
	}

	function handleClearASC() {
		if (!ascParams) return;
		applyDisabledASCState(ascParams);
		notifications.success(m.servers_asc_prepared_to_disable());
	}

	$effect(() => {
		if (serverId) {
			loadData();
		} else {
			loading = false;
			error = () => m.servers_asc_no_server_id();
		}
	});

	async function loadData() {
		if (!serverId) return;
		loading = true;
		error = null;
		try {
			const [asc, info] = await Promise.all([
				api.getManagedServerASC(serverId),
				api.getSystemInfo(),
			]);
			ascParams = asc;
			systemInfo = info;
		} catch (e) {
			error = e instanceof Error ? e.message : () => m.servers_asc_load_failed();
		} finally {
			loading = false;
		}
	}

	async function handleSave() {
		if (!ascParams || !serverId) return;
		const validationErrors = validateASCBeforeSave(ascParams);
		if (validationErrors.length > 0) {
			notifications.error(validationErrors.join('; '));
			return;
		}
		saving = true;
		try {
			const fresh = await api.setManagedServerASC(serverId, ascParams);
			servers.applyMutationResponse(fresh);
			ascParams = await api.getManagedServerASC(serverId);
			notifications.success(m.system_tunnels_obfuscation_saved());
			try {
				const server = await api.getManagedServer(serverId);
				if (server && server.peers.length > 0) {
					const n = server.peers.length;
					notifications.warning(
						m.servers_asc_clients_reconfigure({ count: n }),
						12000,
					);
				}
			} catch {
				// Non-critical — don't block on peer count fetch
			}
		} catch (e) {
			notifications.error(e instanceof Error ? e.message : m.system_tunnels_save_failed());
		} finally {
			saving = false;
		}
	}
</script>

<svelte:head>
	<title>{m.servers_asc_page_title()}</title>
</svelte:head>

<PageContainer width="wide">
	<div class="edit-wrapper">
		<div class="sticky-header">
		<div class="header-left">
			<Button variant="ghost" size="sm" onclick={() => goto('/servers')} iconBefore={backIcon}>
				{m.common_back()}
			</Button>
			<h1 class="page-title">{m.servers_asc_heading()}</h1>
			<span class="badge-managed">{m.servers_asc_badge_managed()}</span>
		</div>
		<div class="header-actions">
			{#if canClearASC}
				<Button
					variant="secondary"
					size="md"
					onclick={handleClearASC}
					disabled={saving || generating || !ascParams}
				>
					{m.servers_asc_clear()}
				</Button>
			{/if}
			<Button
				variant="secondary"
				size="md"
				onclick={handleGenerateAll}
				disabled={!ascParams}
				loading={generating}
			>
				<span class="gen-label gen-label-desktop">{m.servers_asc_generate_params()}</span>
				<span class="gen-label gen-label-mobile">{m.common_generate()}</span>
			</Button>
			<Button
				variant="primary"
				size="md"
				onclick={handleSave}
				disabled={generating || !ascParams}
				loading={saving}
			>
				{m.common_save()}
			</Button>
		</div>
	</div>

	{#if loading}
		<div class="py-12 text-center text-surface-400">{m.tunnels_loading()}</div>
	{:else if error}
		<div class="py-12 text-center text-error-500">{uiText(error)}</div>
	{:else if ascParams}
		<div class="tab-content">
			<ASCEditor bind:params={ascParams} signatureModes="none" idPrefix="managed-" />
		</div>
	{/if}
	</div>
</PageContainer>

{#snippet backIcon()}
	<ArrowLeft size={14} strokeWidth={2} aria-hidden="true" />
{/snippet}

<style>
	.sticky-header {
		display: flex;
		align-items: center;
		justify-content: space-between;
		position: sticky;
		top: 0;
		z-index: 10;
		background: var(--bg-primary);
		padding: 0.75rem 0;
		margin-bottom: 1rem;
		border-bottom: 1px solid var(--border);
	}

	.header-left {
		display: flex;
		align-items: center;
		gap: 0.75rem;
	}

	.header-actions {
		display: flex;
		gap: 0.5rem;
	}

	.page-title {
		font-size: 1.25rem;
		font-weight: 600;
		margin: 0;
	}

	.badge-managed {
		display: inline-flex;
		align-items: center;
		padding: 2px 8px;
		font-size: 0.6875rem;
		font-weight: 500;
		border-radius: 9999px;
		background: rgba(59, 130, 246, 0.15);
		color: var(--accent);
	}

	.gen-label-mobile {
		display: none;
	}

	@media (max-width: 640px) {
		.header-actions {
			display: grid;
			grid-template-columns: repeat(2, minmax(0, 1fr));
			gap: 0.5rem;
			width: 100%;
		}

		.header-actions :global(.btn) {
			width: 100%;
			min-width: 0;
		}

		.header-actions :global(.btn:last-child) {
			grid-column: 1 / -1;
		}

		.sticky-header {
			flex-direction: column;
			gap: 0.75rem;
			align-items: stretch;
		}

		.header-left {
			flex-wrap: wrap;
		}

		.gen-label-desktop {
			display: none;
		}

		.gen-label-mobile {
			display: inline;
		}
	}
</style>
