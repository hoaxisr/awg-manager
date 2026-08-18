<script lang="ts">
	// Шаг 1 мастера «Выхода» — источник (WE-06..WE-26). Компонент только
	// показывает: разбор ссылки и выбор профиля делает мастер.
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
		onmode: (m: ExitMode) => void;
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
	label="Ссылка или URL подписки"
	value={link}
	{oninput}
	onchange={() => oncommit()}
	placeholder="wdtt:// · qwdtt:// · freeturn:// · https://…"
	hint="Протокол определится по ссылке"
	disabled={manual}
	fullWidth
/>

<div class="btn-row">
	<Button variant="secondary" disabled={manual} onclick={() => fileInput?.click()}>
		{#snippet iconBefore()}<Upload size={14} strokeWidth={2.5} />{/snippet}
		Файл .qwdtt
	</Button>
	<Button variant="ghost" onclick={ontogglemanual}>
		{manual ? 'Вернуться к ссылке' : 'Создать вручную'}
	</Button>
</div>

<input
	bind:this={fileInput}
	class="file-input"
	type="file"
	accept=".qwdtt"
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
		Или перетащите сюда файл профиля
	</div>
{/if}

{#if manual}
	<div class="detect-box">
		<div class="grid">
			<Dropdown
				label="Протокол"
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
					label="Режим"
					value={mode}
					options={[
						{ value: 'wg', label: 'WG — трафик через AWG-туннель' },
						{ value: 'raw', label: 'Raw — свой интерфейс OpkgTun' },
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
			Схема ссылки не распознана
			<FieldHint
				text="Ожидаются wdtt://, qwdtt://, freeturn:// или http(s):// для подписки."
				ariaLabel="Подсказка: схема ссылки"
			/>
		</p>
	</div>
{:else if detected === 'subscription'}
	<div class="detect-box">
		<p class="detect-note">Подписка</p>
		<Dropdown
			label="Сервер из подписки"
			value={profileIdx}
			options={profiles}
			onchange={onprofile}
			fullWidth
		/>
	</div>
{:else if detected === 'freeturn'}
	<div class="detect-box">
		<p class="detect-note">
			Профиль FreeTurn
			{#if ftClientId}
				<FieldHint
					text="В ссылке есть Client ID. Если у сервера включён список разрешённых, владелец сервера должен внести именно этот ID."
					ariaLabel="Подсказка: Client ID"
				/>
			{/if}
			{#if !ftHasWg}
				<FieldHint
					text="В ссылке нет WireGuard-конфига — вставьте клиентский .conf на шаге «Куда направить трафик»."
					ariaLabel="Подсказка: WireGuard-конфиг"
				/>
			{/if}
		</p>
	</div>
{:else if detected === 'wdtt'}
	<div class="detect-box">
		{#if mode === 'raw'}
			<Badge size="sm" variant="accent">WDTT · Raw</Badge>
		{:else}
			<p class="detect-note">Профиль WDTT · режим WG</p>
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
