<script lang="ts">
	import { m } from '$lib/i18n';
	import { Button, Dropdown, IconButton, Input, Toggle } from '$lib/components/ui';
	import { Plus, Trash2 } from 'lucide-svelte';
	import type { GroupForm } from '$lib/utils/fircGroups';
	import {
		DNS_ONLY_IFACE,
		DOH_PRESETS,
		MAX_RESOLVE_SERVERS,
		isDohUrl,
		isDuplicateServer,
		serverError,
	} from '$lib/utils/fircResolve';

	interface Props {
		form: GroupForm;
		/** Демон понимает адреса DoH и группы «только DNS». */
		dohSupported: boolean;
		/** DoH можно вписывать: демон его понимает или адрес DoH у группы уже есть. */
		allowDoh: boolean;
	}

	let { form = $bindable(), dohSupported, allowDoh }: Props = $props();

	let dnsOnly = $derived(form.iface === DNS_ONLY_IFACE);
	let showServers = $derived(dnsOnly || (form.dnsMode === 'own' && form.iface !== 'block'));
	let hasDoh = $derived(form.dnsServers.some(isDohUrl));
	let full = $derived(form.dnsServers.filter((s) => s.trim()).length >= MAX_RESOLVE_SERVERS);
	let modeOptions = $derived([
		{ value: 'auto' as const, label: m.firc_groups_form_dns_auto() },
		{ value: 'own' as const, label: m.firc_groups_form_dns_own() },
		{ value: 'off' as const, label: m.firc_groups_form_dns_off() },
	]);

	function rowError(index: number): string {
		const server = form.dnsServers[index];
		if (!server.trim()) return '';
		return serverError(server, allowDoh) ||
			(isDuplicateServer(form.dnsServers, index) ? m.firc_resolve_err_duplicate() : '');
	}

	/** Пресет занимает первую пустую строку, иначе добавляет новую. */
	function addPreset(url: string) {
		const empty = form.dnsServers.findIndex((s) => !s.trim());
		if (empty >= 0) form.dnsServers[empty] = url;
		else form.dnsServers.push(url);
	}

	function removeServer(index: number) {
		form.dnsServers.splice(index, 1);
		if (form.dnsServers.length === 0) form.dnsServers.push('');
	}
</script>

{#if dnsOnly}
	<div class="title">{m.firc_resolve_title()}</div>
	<p class="hint">{m.firc_dns_only_hint()}</p>
{:else}
	<Dropdown
		id="firc-gm-dns"
		label={m.firc_groups_form_dns()}
		options={modeOptions}
		bind:value={form.dnsMode}
		disabled={form.iface === 'block'}
		fullWidth
	/>
{/if}

{#if showServers}
	<div class="servers">
		{#each form.dnsServers as _, i (i)}
			<div class="server-row">
				<Input
					bind:value={form.dnsServers[i]}
					placeholder={allowDoh ? '1.1.1.1 · https://1.1.1.1/dns-query' : '1.1.1.1'}
					ariaLabel={m.firc_resolve_server_aria({ n: i + 1 })}
					error={rowError(i)}
					fullWidth
				/>
				<IconButton
					ariaLabel={m.firc_resolve_remove()}
					title={m.firc_resolve_remove()}
					onclick={() => removeServer(i)}
				>
					<Trash2 size={14} />
				</IconButton>
			</div>
		{/each}
	</div>

	<div class="adders">
		<Button
			variant="ghost"
			disabled={form.dnsServers.length >= MAX_RESOLVE_SERVERS}
			onclick={() => form.dnsServers.push('')}
		>
			{#snippet iconBefore()}<Plus size={13} />{/snippet}
			{m.firc_resolve_add()}
		</Button>
		{#if dohSupported}
			{#each DOH_PRESETS as preset (preset.url)}
				<Button
					disabled={full || form.dnsServers.includes(preset.url)}
					title={preset.url}
					onclick={() => addPreset(preset.url)}
				>
					{m.firc_resolve_preset({ name: preset.name })}
				</Button>
			{/each}
		{/if}
	</div>

	{#if dnsOnly}
		<div class="toggle-row">
			<div>
				<div class="title">{m.firc_resolve_fallback()}</div>
				<p class="hint">
					{form.dnsFallback ? m.firc_resolve_fallback_on_hint() : m.firc_resolve_fallback_off_hint()}
				</p>
			</div>
			<Toggle
				checked={form.dnsFallback}
				onchange={(v) => (form.dnsFallback = v)}
				ariaLabel={m.firc_resolve_fallback()}
			/>
		</div>
	{:else}
		<p class="hint">{m.firc_groups_form_dns_servers_hint()}</p>
	{/if}
	{#if hasDoh}
		<p class="hint">{m.firc_resolve_hint_literal()}</p>
		<p class="hint">{m.firc_resolve_hint_downgrade()}</p>
	{/if}
{/if}
{#if !dohSupported}
	<p class="hint">{m.firc_dns_only_need_update()}</p>
{/if}

<style>
	.title {
		font-size: 13px;
		font-weight: 600;
		color: var(--text-primary);
	}

	.hint {
		margin: 0;
		font-size: 11.5px;
		color: var(--text-muted);
		line-height: 1.4;
	}

	.servers {
		display: flex;
		flex-direction: column;
		gap: 6px;
	}

	.server-row {
		display: flex;
		align-items: flex-start;
		gap: 6px;
	}

	.toggle-row {
		display: flex;
		align-items: center;
		justify-content: space-between;
		gap: 12px;
	}

	.adders {
		display: flex;
		flex-wrap: wrap;
		gap: 6px;
	}
</style>
