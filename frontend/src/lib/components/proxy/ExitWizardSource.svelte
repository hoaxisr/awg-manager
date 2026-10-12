<script lang="ts">
	// Шаг 1 мастера «Выхода» — источник (WE-06..WE-26). Компонент только
	// показывает: разбор ссылки и выбор профиля делает мастер.
	import { m } from '$lib/i18n';
	import { Badge, Button, Dropdown, FieldHint, Input } from '$lib/components/ui';
	import { Upload } from 'lucide-svelte';
	import type { DropdownOption } from '$lib/components/ui';
	import type { ExitMode, ExitProtocol, ExitSourceKind } from './exitWizard';

	interface Props {
		link: string;
		manual: boolean;
		detected: ExitSourceKind;
		protocol: ExitProtocol;
		mode: ExitMode;
		/** Серверы подписки (WE-23) и выбранный индекс. */
		profiles?: DropdownOption[];
		profileIdx?: string;
		/** Client ID в ссылке FreeTurn — повод для WE-21. */
		ftClientId?: string;
		/** Ссылка FreeTurn принесла WireGuard-конфиг. */
		ftHasWg?: boolean;
		oninput: (v: string) => void;
		/** Ввод завершён (blur/Enter): мастер показывает отложенную ошибку разбора. */
		oncommit: () => void;
		onfile: (f: File | undefined) => void;
		ontogglemanual: () => void;
		onprotocol: (p: ExitProtocol) => void;
		onmode: (next: ExitMode) => void;
		onprofile: (idx: string) => void;
	}

	let {
		link,
		manual,
		detected,
		protocol,
		mode,
		profiles = [],
		profileIdx = '0',
		ftClientId = '',
		ftHasWg = false,
		oninput,
		oncommit,
		onfile,
		ontogglemanual,
		onprotocol,
		onmode,
		onprofile,
	}: Props = $props();

	let fileInput: HTMLInputElement | undefined = $state();
</script>

<Input
	label={m.proxy_exit_source_link_label()}
	value={link}
	{oninput}
	onchange={() => oncommit()}
	placeholder="wdtt:// · qwdtt:// · freeturn:// · https://… · JSON"
	hint={m.proxy_exit_source_link_hint()}
	disabled={manual}
	fullWidth
/>

<div class="btn-row">
	<Button variant="secondary" disabled={manual} onclick={() => fileInput?.click()}>
		{#snippet iconBefore()}<Upload size={14} strokeWidth={2.5} />{/snippet}
		{m.proxy_exit_source_file()}
	</Button>
	<Button variant="ghost" onclick={ontogglemanual}>
		{manual ? m.proxy_exit_source_back_to_link() : m.proxy_exit_source_manual()}
	</Button>
</div>

<input
	bind:this={fileInput}
	class="file-input"
	type="file"
	accept=".qwdtt,.json,application/json"
	onchange={(e) => {
		const input = e.currentTarget;
		onfile(input.files?.[0]);
		input.value = '';
	}}
/>

{#if !manual}
	<!-- svelte-ignore a11y_no_static_element_interactions -->
	<div
		class="drop-zone"
		ondragover={(e) => e.preventDefault()}
		ondrop={(e) => {
			e.preventDefault();
			onfile(e.dataTransfer?.files?.[0]);
		}}
	>
		{m.proxy_exit_source_drop()}
	</div>
{/if}

{#if manual}
	<div class="detect-box">
		<div class="grid">
			<Dropdown
				label={m.proxy_exit_source_protocol()}
				value={protocol}
				options={[
					{ value: 'wdtt', label: 'WDTT' },
					{ value: 'freeturn', label: 'FreeTurn' },
				]}
				onchange={(v) => onprotocol(v as ExitProtocol)}
				fullWidth
			/>
			{#if protocol === 'wdtt'}
				<Dropdown
					label={m.proxy_exit_source_mode()}
					value={mode}
					options={[
						{ value: 'wg', label: m.proxy_exit_source_mode_wg() },
						{ value: 'raw', label: m.proxy_exit_source_mode_raw() },
					]}
					onchange={(v) => onmode(v as ExitMode)}
					fullWidth
				/>
			{/if}
		</div>
	</div>
{:else if detected === 'unknown'}
	<div class="detect-box bad">
		<p class="detect-note">
			{m.proxy_exit_source_unknown()}
			<FieldHint
				text={m.proxy_exit_source_unknown_hint()}
				ariaLabel={m.proxy_exit_source_unknown_aria()}
			/>
		</p>
	</div>
{:else if detected === 'subscription'}
	<div class="detect-box">
		<p class="detect-note">{m.proxy_exit_source_subscription()}</p>
		<Dropdown
			label={m.proxy_exit_source_subscription_server()}
			value={profileIdx}
			options={profiles}
			onchange={onprofile}
			fullWidth
		/>
	</div>
{:else if detected === 'freeturn'}
	<div class="detect-box">
		<p class="detect-note">
			{m.proxy_exit_source_ft_profile()}
			{#if ftClientId}
				<FieldHint
					text={m.proxy_exit_source_client_id_hint()}
					ariaLabel={m.proxy_exit_source_client_id_aria()}
				/>
			{/if}
			{#if !ftHasWg}
				<FieldHint
					text={m.proxy_exit_source_no_wg_hint()}
					ariaLabel={m.proxy_exit_source_no_wg_aria()}
				/>
			{/if}
		</p>
	</div>
{:else if detected === 'wdtt'}
	<div class="detect-box">
		{#if mode === 'raw'}
			<Badge size="sm" variant="accent">WDTT · Raw</Badge>
		{:else}
			<p class="detect-note">{m.proxy_exit_source_wdtt_wg()}</p>
		{/if}
	</div>
{/if}

<style>
	.file-input {
		display: none;
	}

	.drop-zone {
		margin-top: 0.75rem;
		padding: 1rem;
		border: 1px dashed var(--color-border);
		border-radius: var(--radius);
		text-align: center;
		font-size: 0.8125rem;
		color: var(--color-text-muted);
	}

	.detect-box {
		margin-top: 0.875rem;
		padding: 0.75rem;
		border: 1px solid var(--color-border);
		background: var(--color-bg-tertiary);
		border-radius: var(--radius);
	}

	.detect-box.bad {
		border-color: var(--color-warning-border);
		background: var(--color-warning-tint);
	}

	.detect-note {
		margin: 0 0 0.5rem;
		font-size: 0.8125rem;
		color: var(--color-text-secondary);
		line-height: 1.5;
	}

	.detect-note:last-child {
		margin-bottom: 0;
	}

	.grid {
		display: grid;
		grid-template-columns: repeat(auto-fit, minmax(220px, 1fr));
		gap: 0.75rem;
	}
</style>
