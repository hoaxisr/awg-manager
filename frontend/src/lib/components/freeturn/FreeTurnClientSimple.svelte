<script lang="ts">
	import { Button, Input, Dropdown } from '$lib/components/ui';
	import StepPill from '$lib/components/sb-router/StepPill.svelte';
	import WizardStep from '$lib/components/sb-router/WizardStep.svelte';
	import ProcessLogBox from './ProcessLogBox.svelte';
	import LinkParamsSummary from './LinkParamsSummary.svelte';
	import { browserOptions } from './options';
	import { api } from '$lib/api/client';
	import type { FreeTurnClientConfig, FreeTurnLinkPayload, FreeTurnProcessStatus } from '$lib/types';

	interface Props {
		client: FreeTurnClientConfig;
		running?: boolean;
		saving?: boolean;
		status?: FreeTurnProcessStatus;
		onSave: (cfg: FreeTurnClientConfig) => void | Promise<void>;
		onToggle: (on: boolean) => void | Promise<void>;
		onImportLink: (link: string) => void | Promise<void>;
	}

	let {
		client,
		running = false,
		saving = false,
		status,
		onSave,
		onToggle,
		onImportLink
	}: Props = $props();

	let importLink = $state('');
	let importing = $state(false);
	let starting = $state(false);
	let linkParams = $state<FreeTurnLinkPayload | null>(null);

	const linksCount = $derived(
		client.links ? client.links.split(',').filter((s) => s.trim()).length : 0
	);

	const step1Done = $derived(!!client.peer.trim());
	const step2Done = $derived(step1Done && !!client.links?.trim());
	const step3Done = $derived(
		step2Done && client.streams > 0 && client.streamsPerCred > 0 && !!client.browser
	);
	const canSave = $derived((step1Done || step2Done) && !saving && !starting);
	const canStart = $derived(step3Done && !saving && !starting);

	async function applyImport() {
		const link = importLink.trim();
		if (!link) return;
		importing = true;
		try {
			await onImportLink(link);
			try {
				linkParams = await api.decodeFreeTurnLink(link);
			} catch {
				linkParams = null;
			}
			importLink = '';
		} finally {
			importing = false;
		}
	}

	async function saveOnly() {
		if (!canSave) return;
		await onSave(client);
	}

	async function startOnly() {
		if (!canStart || running) return;
		starting = true;
		try {
			await onToggle(true);
		} finally {
			starting = false;
		}
	}

	async function saveAndStart() {
		if (!canStart) return;
		starting = true;
		try {
			await onSave(client);
			if (!running) await onToggle(true);
		} finally {
			starting = false;
		}
	}
</script>

<div class="ft-simple-wrap">
	<p class="ft-simple-lead">
		Импорт freeturn:// → VK-ссылки → потоки и капча → запуск клиента.
	</p>

	<div class="ft-simple-steps">
		<StepPill n={1} label="freeturn://" active={true} done={step1Done} />
		<StepPill n={2} label="VK-ссылки" active={step1Done} done={step2Done} />
		<StepPill n={3} label="Потоки" active={step2Done} done={step3Done} />
		<StepPill n={4} label="Запуск" active={step3Done} done={running} />
	</div>

	<WizardStep n={1} title="Ссылка freeturn://" hint="с сервера freeturn на VPS" active={true}>
		<p class="ft-hint">
			Вставьте ссылку — заполнятся peer, obf, ключ и другие параметры клиента.
		</p>
		<div class="ft-import-row">
			<Input bind:value={importLink} placeholder="freeturn://…" />
			<Button variant="primary" size="sm" loading={importing} disabled={!importLink.trim()} onclick={applyImport}>
				Импорт
			</Button>
		</div>
		{#if client.peer.trim()}
			<p class="ft-readonly">peer: <code>{client.peer}</code></p>
		{/if}
		<LinkParamsSummary payload={linkParams} peer={client.peer} />
	</WizardStep>

	<WizardStep
		n={2}
		title="Ссылки VK Calls (-links)"
		hint="через запятую, каждая — отдельный пул кредов"
		active={step1Done}
	>
		<textarea
			class="ft-simple-textarea"
			bind:value={client.links}
			placeholder="https://vk.com/call/join/…"
			rows="4"
		></textarea>
		{#if linksCount > 0}
			<p class="ft-hint">
				{linksCount} {linksCount === 1 ? 'ссылка' : linksCount < 5 ? 'ссылки' : 'ссылок'} —
				столько же независимых пулов TURN-кредов.
			</p>
		{/if}
	</WizardStep>

	<WizardStep
		n={3}
		title="Потоки и капча"
		hint="-n, -streams-per-cred, -browser"
		active={step2Done}
	>
		<div class="ft-simple-grid">
			<Input
				label="Потоков TURN (-n)"
				type="number"
				value={String(client.streams)}
				onchange={(v) => (client.streams = Math.max(1, Number(v) || 0))}
			/>
			<Input
				label="Потоков на кред (-streams-per-cred)"
				type="number"
				value={String(client.streamsPerCred)}
				onchange={(v) => (client.streamsPerCred = Math.max(1, Number(v) || 0))}
			/>
		</div>
		<p class="ft-hint">
			Суммарно до {linksCount * client.streamsPerCred || client.streamsPerCred} потоков на все
			ссылки (×{linksCount || 1} кред{linksCount === 1 ? '' : 'а'}). Каждый поток может
			потребовать отдельную VK-капчу.
		</p>

		<Dropdown
			label="Браузер для авто-капчи (-browser)"
			bind:value={client.browser}
			options={browserOptions}
		/>
		<p class="ft-hint">
			Headless-браузер на роутере для автоматического решения VK Smart Captcha. Если не
			справится — freeturn откроет ручную капчу (окно в awg-manager с авто-открытием).
		</p>

		<p class="ft-readonly">
			listen: <code>{client.listen || '127.0.0.1:9000'}</code> (назначается автоматически; учитываются порты AWG-плиток)
		</p>
	</WizardStep>

	<WizardStep n={4} title="Запустить клиент" hint="сохранение и включение инстанса" active={step3Done}>
		<div class="ft-simple-actions">
			<Button variant="secondary" loading={saving} disabled={!canSave} onclick={saveOnly}>Сохранить</Button>
			{#if !running}
				<Button variant="secondary" loading={starting} disabled={!canStart} onclick={startOnly}>
					Запустить
				</Button>
				<Button variant="primary" loading={starting} disabled={!canStart} onclick={saveAndStart}>
					Сохранить и запустить
				</Button>
			{:else}
				<Button variant="secondary" onclick={() => onToggle(false)}>Остановить</Button>
				<Button variant="primary" loading={saving} disabled={!canSave} onclick={saveOnly}>
					Сохранить настройки
				</Button>
			{/if}
		</div>
	</WizardStep>

	<ProcessLogBox log={status?.log} />
</div>

<style>
	.ft-simple-wrap {
		display: flex;
		flex-direction: column;
		gap: 0.25rem;
	}

	.ft-simple-lead {
		margin: 0 0 0.75rem;
		font-size: 0.8125rem;
		color: var(--color-text-secondary);
	}

	.ft-simple-steps {
		display: flex;
		flex-wrap: wrap;
		gap: 0.5rem;
		margin-bottom: 0.75rem;
	}

	.ft-import-row {
		display: grid;
		grid-template-columns: 1fr auto;
		gap: 0.5rem;
		margin-top: 0.5rem;
	}

	.ft-simple-grid {
		display: grid;
		grid-template-columns: repeat(auto-fit, minmax(10rem, 1fr));
		gap: 0.625rem;
		margin-top: 0.5rem;
	}

	.ft-simple-textarea {
		width: 100%;
		font-family: var(--font-mono);
		font-size: 0.75rem;
		padding: 0.5rem 0.625rem;
		border: 1px solid var(--color-border);
		border-radius: var(--radius-sm);
		background: var(--color-bg-primary);
		resize: vertical;
	}

	.ft-readonly {
		margin: 0.625rem 0 0;
		font-family: var(--font-mono);
		font-size: 0.8125rem;
	}

	.ft-hint {
		font-size: 0.75rem;
		color: var(--color-text-secondary);
		margin: 0.375rem 0 0;
	}

	.ft-simple-actions {
		display: flex;
		flex-wrap: wrap;
		gap: 0.5rem;
		margin-top: 0.75rem;
	}
</style>
