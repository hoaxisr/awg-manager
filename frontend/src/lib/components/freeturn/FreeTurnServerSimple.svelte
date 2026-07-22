<script lang="ts">
	import { RefreshCw } from 'lucide-svelte';
	import { Button, Input, Dropdown } from '$lib/components/ui';
	import { api } from '$lib/api/client';
	import { notifications } from '$lib/stores/notifications';
	import StepPill from '$lib/components/sb-router/StepPill.svelte';
	import WizardStep from '$lib/components/sb-router/WizardStep.svelte';
	import ServerWgBind from './ServerWgBind.svelte';
	import ProcessLogBox from './ProcessLogBox.svelte';
	import LinkParamsSummary from './LinkParamsSummary.svelte';
	import { obfOptions } from './options';
	import { obfProfileHints, randomObfKeyHex } from './obfHints';
	import type { FreeTurnLinkPayload, FreeTurnProcessStatus, FreeTurnServerConfig } from '$lib/types';

	interface Props {
		server: FreeTurnServerConfig;
		running?: boolean;
		saving?: boolean;
		generating?: boolean;
		status?: FreeTurnProcessStatus;
		defaultClientListenPort?: number;
		generatedLink?: string;
		generatedPeer?: string;
		genProvider: string;
		genPeer: string;
		genMTU: number;
		genWG: string;
		genClientId: string;
		genName: string;
		onSave: (cfg: FreeTurnServerConfig) => void | Promise<void>;
		onToggle: (on: boolean) => void | Promise<void>;
		onGenerate: (
			provider: string,
			peer: string,
			mtu: number,
			wg: string,
			clientId: string,
			name: string
		) => Promise<import('$lib/types').FreeTurnGenerateLinkResult | null>;
		onCopy: (text: string) => void;
	}

	let {
		server,
		running = false,
		saving = false,
		generating = false,
		status,
		defaultClientListenPort = 9000,
		generatedLink = '',
		generatedPeer = '',
		genProvider = $bindable(),
		genPeer = $bindable(),
		genMTU = $bindable(),
		genWG = $bindable(),
		genClientId = $bindable(),
		genName = $bindable(),
		onSave,
		onToggle,
		onGenerate,
		onCopy
	}: Props = $props();

	let starting = $state(false);
	let loadingWanPeer = $state(false);
	let linkParams = $state<FreeTurnLinkPayload | null>(null);

	const listenPort = $derived.by(() => {
		const listen = server.listen?.trim() ?? '';
		if (!listen) return '56000';
		const idx = listen.lastIndexOf(':');
		return idx >= 0 ? listen.slice(idx + 1) : listen;
	});

	const step1Done = $derived(!!server.connect.trim());
	const step2Done = $derived(step1Done && !!server.listen.trim());
	const step3Done = $derived(
		step2Done && server.obfProfile !== 'none' ? !!server.obfKey?.trim() : step2Done
	);
	const canSave = $derived(step1Done && !saving && !starting);
	const canStart = $derived(step3Done && !saving && !starting);

	const obfHint = $derived(obfProfileHints[server.obfProfile] ?? '');

	function ensureObfKey() {
		if (server.obfProfile !== 'none' && !server.obfKey?.trim()) {
			server.obfKey = randomObfKeyHex();
		}
	}

	function randomClientId() {
		const bytes = new Uint8Array(16);
		crypto.getRandomValues(bytes);
		genClientId = Array.from(bytes, (b) => b.toString(16).padStart(2, '0')).join('');
	}

	async function fillWanPeer() {
		loadingWanPeer = true;
		try {
			const ip = await api.getWANIP();
			genPeer = ip.includes(':') ? ip : `${ip}:${listenPort}`;
		} catch (e) {
			notifications.error(e instanceof Error ? e.message : 'Не удалось определить WAN IP');
		} finally {
			loadingWanPeer = false;
		}
	}

	async function saveOnly() {
		if (!canSave) return;
		ensureObfKey();
		await onSave(server);
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

	async function saveAndGenerateLink() {
		if (!step3Done) return;
		ensureObfKey();
		await onSave(server);
		await generateLinkNow();
	}

	async function generateLinkNow() {
		let peerParam = genPeer.trim();
		if (peerParam && !peerParam.includes(':')) {
			peerParam = `${peerParam}:${listenPort}`;
		}
		const result = await onGenerate(genProvider, peerParam, genMTU, genWG, genClientId, genName);
		if (result?.link) {
			try {
				linkParams = await api.decodeFreeTurnLink(result.link);
			} catch {
				linkParams = null;
			}
		}
	}

	async function saveStartAndLink() {
		if (!canStart) return;
		starting = true;
		try {
			ensureObfKey();
			await onSave(server);
			if (!running) await onToggle(true);
			await generateLinkNow();
		} finally {
			starting = false;
		}
	}
</script>

<div class="ft-simple-wrap">
	<p class="ft-simple-lead">
		Сервер freeturn: WG-пир → автоматический порт → обфускация → ссылка для клиента.
	</p>

	<div class="ft-simple-steps">
		<StepPill n={1} label="WG · пир" active={true} done={step1Done} />
		<StepPill n={2} label="Порт" active={step1Done} done={step2Done} />
		<StepPill n={3} label="Obf" active={step2Done} done={step3Done} />
		<StepPill n={4} label="Ссылка" active={step3Done} done={!!generatedLink} />
	</div>

	<WizardStep n={1} title="WireGuard: сервер · пир" hint="backend (-connect) заполнится автоматически" active={true}>
		<ServerWgBind
			autoApply
			compact
			clientListenPort={defaultClientListenPort}
			onConnect={(addr) => {
				server.connect = addr;
			}}
			onPeerConf={(conf) => {
				genWG = conf;
			}}
		/>
		{#if server.connect.trim()}
			<p class="ft-readonly">
				-connect: <code>{server.connect}</code>
			</p>
		{/if}
	</WizardStep>

	<WizardStep
		n={2}
		title="Порт приёма (-listen)"
		hint="назначается автоматически при создании инстанса"
		active={step1Done}
	>
		<p class="ft-readonly">
			<code>{server.listen || '0.0.0.0:56000'}</code>
		</p>
		<p class="ft-hint">
			Порт выбирается автоматически (56000, 56001, …) — менять не нужно, если не создаёте
			несколько серверов freeturn параллельно.
		</p>
	</WizardStep>

	<WizardStep n={3} title="Обфускация" hint="маскирует freeturn-трафик от DPI" active={step2Done}>
		<Dropdown label="Профиль (-obf-profile)" bind:value={server.obfProfile} options={obfOptions} />
		{#if obfHint}
			<p class="ft-hint">{obfHint}</p>
		{/if}
		<Input
			label="Ключ (-obf-key)"
			type="password"
			bind:value={server.obfKey}
			placeholder="64 hex-символа"
		/>
		<div class="ft-inline-actions">
			<Button variant="ghost" size="sm" onclick={() => (server.obfKey = randomObfKeyHex())}>
				Сгенерировать ключ
			</Button>
		</div>
		<p class="ft-hint">
			Ключ должен совпадать у сервера и клиента — он зашивается в freeturn:// ссылку.
		</p>
	</WizardStep>

	<WizardStep n={4} title="Ссылка для клиента" hint="сохраните сервер и сгенерируйте freeturn://" active={step3Done}>
		<div class="ft-gen-peer-row">
			<Input
				label="Peer для клиента (WAN IP:порт)"
				bind:value={genPeer}
				placeholder={`пусто — авто · порт :${listenPort}`}
			/>
			<Button variant="ghost" size="sm" loading={loadingWanPeer} onclick={fillWanPeer}>WAN IP</Button>
		</div>
		<div class="ft-gen-row">
			<Input label="Провайдер" bind:value={genProvider} placeholder="vk" />
			<Input label="MTU" type="number" value={String(genMTU)} onchange={(v) => (genMTU = Number(v) || 1376)} />
			<div class="ft-gen-cid">
				<Input label="Client ID" bind:value={genClientId} placeholder="необязательно" />
				<Button variant="ghost" size="sm" onclick={randomClientId} title="Сгенерировать">
					<RefreshCw size={14} />
				</Button>
			</div>
			<Input bind:value={genName} label="Комментарий" placeholder="имя получателя" />
		</div>

		<div class="ft-simple-actions">
			<Button variant="secondary" loading={saving} disabled={!canSave} onclick={saveOnly}>Сохранить</Button>
			{#if !running}
				<Button variant="secondary" loading={starting} disabled={!canStart} onclick={startOnly}>
					Запустить
				</Button>
			{:else}
				<Button variant="secondary" onclick={() => onToggle(false)}>Остановить</Button>
			{/if}
			<Button variant="primary" loading={starting} disabled={!canStart} onclick={saveStartAndLink}>
				Сохранить, запустить и получить ссылку
			</Button>
			<Button
				variant="ghost"
				size="sm"
				loading={generating}
				disabled={!step3Done}
				onclick={saveAndGenerateLink}
			>
				Только сгенерировать ссылку
			</Button>
		</div>

		{#if generatedLink}
			<div class="ft-result">
				<div class="section-label">freeturn:// ({generatedPeer || 'peer'})</div>
				<div class="ft-link-box">{generatedLink}</div>
				<Button variant="ghost" size="sm" onclick={() => onCopy(generatedLink)}>Скопировать</Button>
				<LinkParamsSummary payload={linkParams} peer={generatedPeer} />
			</div>
		{/if}
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

	.ft-simple-actions {
		display: flex;
		flex-wrap: wrap;
		gap: 0.5rem;
		margin-top: 0.75rem;
	}

	.ft-readonly {
		margin: 0.5rem 0 0;
		font-family: var(--font-mono);
		font-size: 0.8125rem;
	}

	.ft-hint {
		font-size: 0.75rem;
		color: var(--color-text-secondary);
		margin: 0.375rem 0 0;
	}

	.ft-inline-actions {
		display: flex;
		justify-content: flex-end;
		margin-top: 0.375rem;
	}

	.ft-gen-peer-row {
		display: flex;
		gap: 0.5rem;
		align-items: flex-end;
	}

	.ft-gen-peer-row :global(.field) {
		flex: 1;
		min-width: 0;
	}

	.ft-gen-row {
		display: flex;
		flex-wrap: wrap;
		gap: 0.625rem;
		align-items: flex-end;
		margin-top: 0.625rem;
	}

	.ft-gen-cid {
		display: flex;
		gap: 0.375rem;
		align-items: flex-end;
	}

	.ft-result {
		margin-top: 0.875rem;
		padding: 0.875rem;
		border-radius: var(--radius-sm);
		border: 1px solid var(--color-border);
		background: var(--color-bg-tertiary);
	}

	.ft-link-box {
		font-family: var(--font-mono);
		font-size: 0.8125rem;
		word-break: break-all;
		margin: 0.375rem 0 0.625rem;
	}
</style>
