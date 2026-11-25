<!--
  Карточка «Прокси и обфускаторы»: бинари подсистем ставятся и снимаются
  целиком. Презентационная: запросы и статусы — у ProxyBinariesSection.
-->
<script lang="ts" module>
	import type { ProxySubsystem } from '$lib/api/proxyInstances';

	/** Строка подсистемы прокси: её бинари ставятся и снимаются целиком. */
	export interface ProxyBinaryRow {
		key: ProxySubsystem;
		label: string;
		/** Бинари подсистемы лежат на диске. */
		present: boolean;
		installAvailable: boolean;
		updateAvailable: boolean;
		installedVersion?: string;
		installVersion?: string;
		busy: boolean;
		/** Инстансы подсистемы, включая выключенные: гейт удаления. */
		instances: number;
		oninstall: () => void;
		onuninstall: () => void;
	}
</script>

<script lang="ts">
	import type { Snippet } from 'svelte';
	import { m } from '$lib/i18n';
	import { Button, ConfirmModal } from '$lib/components/ui';
	import { Waypoints } from 'lucide-svelte';
	import ComponentRow from './ComponentRow.svelte';
	import SettingsSectionLabel from './SettingsSectionLabel.svelte';

	interface Props {
		rows: ProxyBinaryRow[];
		/** Строка «Phobos в ядре» — под своей подсистемой; нет её строки — в конце. */
		phobosKernel?: Snippet;
	}

	let { rows, phobosKernel }: Props = $props();

	const hasPhobos = $derived(rows.some((p) => p.key === 'obf-phobos'));

	// Подсистема, ожидающая подтверждения удаления.
	let confirmProxy = $state<ProxyBinaryRow | null>(null);
</script>

{#if rows.length > 0 || phobosKernel}
	<div class="settings-block">
		<div class="card">
			<SettingsSectionLabel label={m.settings_proxy_binaries_title()} icon={Waypoints} tone="info" header />
			{#each rows as p (p.key)}
				<ComponentRow
					label={p.label}
					running={p.present}
					ariaLabel={p.present ? m.settings_integrations_proxy_installed_aria({ label: p.label }) : m.settings_integrations_proxy_not_installed_aria({ label: p.label })}
				>
					{#if p.present}
						<span class="component-sub">
							v{p.installedVersion ?? '?'}
							{#if p.instances > 0}· {m.settings_integrations_instances({ count: p.instances })}{/if}
						</span>
						{#if p.updateAvailable && p.installVersion}
							<span class="component-sub">{m.settings_integrations_update_available({ version: p.installVersion })}</span>
						{/if}
					{:else}
						<span class="component-sub">{m.settings_integrations_not_installed()}</span>
					{/if}
					{#snippet actions()}
						{#if p.present}
							{#if p.updateAvailable && p.installAvailable}
								<Button variant="primary" size="sm" loading={p.busy} onclick={p.oninstall}>
									{m.common_update()}
								</Button>
							{:else if !p.key.startsWith('obf-')}
								<Button variant="secondary" size="sm" href="/proxy">{m.settings_integrations_open()}</Button>
							{/if}
							<Button
								variant="outline-danger"
								size="sm"
								loading={p.busy}
								disabled={p.instances > 0}
								title={p.instances > 0 ? m.settings_integrations_remove_instances_first() : undefined}
								onclick={() => (confirmProxy = p)}
							>
								{m.common_delete()}
							</Button>
						{:else if p.installAvailable}
							<Button variant="primary" size="sm" loading={p.busy} onclick={p.oninstall}>
								{m.settings_integrations_install()}
							</Button>
						{/if}
					{/snippet}
				</ComponentRow>
				{#if p.key === 'obf-phobos'}
					{@render phobosKernel?.()}
				{/if}
			{/each}
			{#if !hasPhobos}
				{@render phobosKernel?.()}
			{/if}
		</div>
	</div>
{/if}

{#if confirmProxy}
	<ConfirmModal
		open={confirmProxy !== null}
		title={m.settings_integrations_remove_proxy_title({ label: confirmProxy.label })}
		message={m.settings_integrations_remove_proxy_message()}
		secondary={m.settings_integrations_remove_proxy_secondary()}
		confirmLabel={m.common_delete()}
		variant="danger"
		onConfirm={() => {
			const row = confirmProxy;
			confirmProxy = null;
			row?.onuninstall();
		}}
		onClose={() => (confirmProxy = null)}
	/>
{/if}
