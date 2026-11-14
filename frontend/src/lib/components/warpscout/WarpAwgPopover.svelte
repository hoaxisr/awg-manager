<script lang="ts">
	import { Shield, Sparkles, Square } from 'lucide-svelte';
	import { api } from '$lib/api/client';
	import { m } from '$lib/i18n';
	import { notifications } from '$lib/stores/notifications';
	import { Badge, Button, Dropdown } from '$lib/components/ui';
	import type { DropdownOption } from '$lib/components/ui';
	import type { WarpAWGParams, WarpFindJunkStatus, WarpObfuscationSource } from '$lib/types/warpscout';
	import WarpChipPopover from './WarpChipPopover.svelte';
	import {
		FIND_JUNK_PROFILES,
		LIMITS,
		SIGNATURE_PROFILES,
		canApplyFindJunk,
		hasUsableFindJunkBest,
		sanitizeInt,
		signatureProfileLabel,
		warpErrorMessage,
	} from './warpscoutUi';

	interface Props {
		awg: WarpAWGParams;
		obfuscationSources?: WarpObfuscationSource[];
		dirty: boolean;
		saving: boolean;
		findJunk?: WarpFindJunkStatus | null;
		engineAvailable?: boolean;
		hasAccount?: boolean;
		scanRunning?: boolean;
		scanInterface?: string;
		/** Сохранить параметры AWG; true — сохранено, поповер можно закрыть. */
		onsave: () => Promise<boolean>;
		onreset: () => void;
		onfindjunkapplied?: (awg: WarpAWGParams) => void | Promise<void>;
		onfindjunkchanged?: () => void | Promise<void>;
	}

	let {
		awg = $bindable(),
		obfuscationSources = [],
		dirty,
		saving,
		findJunk = null,
		engineAvailable = false,
		hasAccount = false,
		scanRunning = false,
		scanInterface = '',
		onsave,
		onreset,
		onfindjunkapplied,
		onfindjunkchanged,
	}: Props = $props();

	let junkProfile = $state<string>('random');
	let junkMaxAttempts = $state<number>(LIMITS.findJunkAttempts.def);
	let junkThresholdPct = $state<number>(LIMITS.findJunkThreshold.def);
	let startingJunk = $state(false);
	let stoppingJunk = $state(false);
	let applyingJunk = $state(false);

	const junkRunning = $derived(findJunk?.running ?? false);
	const junkAttempts = $derived(findJunk?.attempts ?? []);
	const junkUsableBest = $derived(hasUsableFindJunkBest(findJunk));
	const junkCanApply = $derived(canApplyFindJunk(findJunk));

	const signatureOptions = $derived.by<DropdownOption[]>(() => {
		const opts: DropdownOption[] = SIGNATURE_PROFILES.map((s) => ({
			value: s,
			label: s === 'none' ? m.warpscout_sig_none() : s,
		}));
		if (awg.signatureProfile === 'custom') {
			opts.unshift({ value: 'custom', label: m.warpscout_sig_custom() });
		}
		return opts;
	});

	const findJunkProfileOptions = $derived<DropdownOption[]>(
		FIND_JUNK_PROFILES.map((s) => ({
			value: s,
			label:
				s === 'random'
					? m.warpscout_findjunk_sig_random()
					: s === 'none'
						? m.warpscout_sig_none()
						: s,
		})),
	);

	const copySourceOptions = $derived<DropdownOption[]>([
		{ value: '', label: m.warpscout_awg_copy_placeholder() },
		...obfuscationSources.map((s) => ({
			value: `${s.mode}:${s.id}`,
			label: `${s.name}${s.iface ? ` (${s.iface})` : ''} · Jc ${s.awg.jc}`,
		})),
	]);

	// Список — действие «скопировать», а не выбор: после копирования он
	// возвращается к заглушке, иначе тот же туннель нельзя выбрать повторно
	// (Dropdown зовёт onchange только при смене значения).
	let copySource = $state('');

	function applyObfSource(key: string): void {
		if (!key) return;
		const src = obfuscationSources.find((s) => `${s.mode}:${s.id}` === key);
		if (src) awg = { ...src.awg };
		copySource = '';
	}

	function changeSignatureProfile(v: string): void {
		const next = { ...awg, signatureProfile: v };
		if (v !== 'custom') {
			// Ключи убираются, а не обнуляются: сервер пустые I1..I5 не
			// присылает, и с пустыми строками черновик не сравнялся бы с
			// сохранённой конфигурацией («не сохранено» без изменений).
			delete next.i1;
			delete next.i2;
			delete next.i3;
			delete next.i4;
			delete next.i5;
		}
		awg = next;
	}

	async function startFindJunk(): Promise<void> {
		junkMaxAttempts = sanitizeInt(junkMaxAttempts, LIMITS.findJunkAttempts);
		junkThresholdPct = sanitizeInt(junkThresholdPct, LIMITS.findJunkThreshold);
		startingJunk = true;
		try {
			await api.warpscoutFindJunkStart({
				i1Profile: junkProfile,
				maxAttempts: junkMaxAttempts,
				thresholdPct: junkThresholdPct,
				ifname: scanInterface.trim() || undefined,
			});
			await onfindjunkchanged?.();
		} catch (e) {
			notifications.error(warpErrorMessage(e));
		} finally {
			startingJunk = false;
		}
	}

	async function stopFindJunk(): Promise<void> {
		stoppingJunk = true;
		try {
			await api.warpscoutFindJunkStop();
			await onfindjunkchanged?.();
		} catch (e) {
			notifications.error(warpErrorMessage(e));
		} finally {
			stoppingJunk = false;
		}
	}

	async function applyBestJunk(): Promise<void> {
		applyingJunk = true;
		try {
			const nextAwg = await api.warpscoutFindJunkApply();
			await onfindjunkapplied?.(nextAwg);
		} catch (e) {
			notifications.error(warpErrorMessage(e));
		} finally {
			applyingJunk = false;
		}
	}
</script>

<WarpChipPopover
	title={m.warpscout_awg_pop_title()}
	ariaLabel={m.warpscout_awg_pop_aria()}
	chipTitle={m.warpscout_awg_chip_tip()}
	width="min(29rem, calc(100vw - 2rem))"
>
	{#snippet chip()}
		<Shield size={13} class="chip-ic" />
		<span class="chip-dim hide-sm">{m.warpscout_awg_chip_label()}</span>
		<span class="mono chip-strong">{signatureProfileLabel(awg.signatureProfile)}</span>
		<span class="mono chip-dim">· Jc {awg.jc} · {awg.jmin}–{awg.jmax}</span>
		{#if junkRunning}
			<span class="mono chip-dim">· {Math.max(1, findJunk?.attempt ?? 0)}/{findJunk?.maxAttempts ?? junkMaxAttempts}</span>
		{/if}
		{#if dirty}<span class="unsaved-dot" title={m.warpscout_unsaved_tip()}></span>{/if}
	{/snippet}

	{#snippet children(close)}
		<div class="awg-pop-body">
			{#if obfuscationSources.length > 0}
				<Dropdown
					label={m.warpscout_awg_copy_label()}
					bind:value={copySource}
					options={copySourceOptions}
					fullWidth
					onchange={applyObfSource}
				/>
			{/if}

			<Dropdown
				label={m.warpscout_awg_sig_label()}
				value={awg.signatureProfile}
				options={signatureOptions}
				fullWidth
				onchange={changeSignatureProfile}
			/>

			<div class="awg-nums">
				<label class="num-field">
					<span>Jc</span>
					<input class="field-input mono" type="number" min={LIMITS.jc.min} max={LIMITS.jc.max} bind:value={awg.jc} />
				</label>
				<label class="num-field">
					<span>Jmin</span>
					<input class="field-input mono" type="number" min={LIMITS.jmin.min} max={LIMITS.jmin.max} bind:value={awg.jmin} />
				</label>
				<label class="num-field">
					<span>Jmax</span>
					<input class="field-input mono" type="number" min={LIMITS.jmax.min} max={LIMITS.jmax.max} bind:value={awg.jmax} />
				</label>
			</div>

			<p class="pop-hint">
				{awg.signatureProfile === 'custom' ? m.warpscout_awg_custom_hint() : m.warpscout_awg_hint()}
			</p>

			<div class="findjunk-box">
				<div class="fj-head">
					<span class="fj-title">{m.warpscout_findjunk_title()}</span>
					{#if junkRunning}
						<Button size="sm" variant="danger" loading={stoppingJunk} onclick={stopFindJunk}>
							{#snippet iconBefore()}<Square size={12} />{/snippet}
							{m.common_stop()}
						</Button>
					{:else}
						<Button
							size="sm"
							variant="secondary"
							loading={startingJunk}
							disabled={!engineAvailable || !hasAccount || scanRunning}
							onclick={startFindJunk}
						>
							{#snippet iconBefore()}<Sparkles size={12} />{/snippet}
							{m.warpscout_findjunk_start_btn()}
						</Button>
					{/if}
				</div>

				<p class="pop-hint">{m.warpscout_findjunk_hint()}</p>

				<div class="fj-controls">
					<div class="fj-prof">
						<Dropdown
							label={m.warpscout_findjunk_profile_label()}
							value={junkProfile}
							options={findJunkProfileOptions}
							fullWidth
							disabled={junkRunning}
							onchange={(v) => (junkProfile = v)}
						/>
					</div>
					<label class="num-field">
						<span>{m.warpscout_findjunk_attempts_label()}</span>
						<input
							class="field-input mono"
							type="number"
							min={LIMITS.findJunkAttempts.min}
							max={LIMITS.findJunkAttempts.max}
							bind:value={junkMaxAttempts}
							disabled={junkRunning}
						/>
					</label>
					<label class="num-field">
						<span>{m.warpscout_findjunk_threshold_label()}</span>
						<input
							class="field-input mono"
							type="number"
							min={LIMITS.findJunkThreshold.min}
							max={LIMITS.findJunkThreshold.max}
							bind:value={junkThresholdPct}
							disabled={junkRunning}
						/>
					</label>
				</div>

				{#if junkRunning || junkAttempts.length > 0 || findJunk?.error}
					{#if junkRunning}
						<div class="fj-status mono">
							{m.warpscout_findjunk_running({
								cur: Math.max(1, findJunk?.attempt ?? 1),
								max: findJunk?.maxAttempts ?? junkMaxAttempts,
							})}
						</div>
					{/if}

					{#if junkAttempts.length > 0}
						<div class="junk-attempts">
							{#each junkAttempts as att (att.attempt)}
								<div
									class="junk-row"
									class:best={!att.incomplete && att.working > 0 && findJunk?.best?.attempt === att.attempt}
								>
									<span class="mono">#{att.attempt}</span>
									<span class="mono">{signatureProfileLabel(att.profile)}</span>
									<span class="mono fj-params">Jc {att.awg.jc} ({att.awg.jmin}–{att.awg.jmax})</span>
									<span class="mono fj-score">{att.working}/{att.total} ({att.pct}%)</span>
									{#if att.incomplete}
										<Badge variant="warning" size="xs">{m.warpscout_findjunk_incomplete()}</Badge>
									{/if}
								</div>
							{/each}
						</div>
					{/if}

					{#if !junkRunning}
						{#if junkUsableBest && findJunk?.best}
							<div class="fj-foot">
								<span class="fj-best mono">
									{m.warpscout_findjunk_best_label({
										attempt: findJunk.best.attempt,
										profile: signatureProfileLabel(findJunk.best.profile),
										jc: findJunk.best.awg.jc,
										jmin: findJunk.best.awg.jmin,
										jmax: findJunk.best.awg.jmax,
										working: findJunk.best.working,
										total: findJunk.best.total,
										pct: findJunk.best.pct,
									})}
								</span>
								{#if findJunk.applied}
									<Badge variant="success" size="xs">{m.warpscout_findjunk_applied_badge()}</Badge>
								{:else}
									<Button
										size="sm"
										variant="secondary"
										loading={applyingJunk}
										disabled={!junkCanApply}
										onclick={applyBestJunk}
									>
										{m.warpscout_findjunk_apply_btn()}
									</Button>
								{/if}
							</div>
						{:else if junkAttempts.length > 0 && !findJunk?.error}
							<p class="fj-empty">{m.warpscout_findjunk_no_working()}</p>
						{/if}
					{/if}

					{#if findJunk?.error}
						<p class="fj-err">{findJunk.error}</p>
					{/if}
				{/if}
			</div>
		</div>

		<div class="pop-foot end">
			{#if dirty}
				<Button size="sm" variant="ghost" onclick={onreset} disabled={saving}>
					{m.warpscout_reset_btn()}
				</Button>
			{/if}
			<Button
				size="sm"
				variant="primary"
				loading={saving}
				disabled={!dirty}
				onclick={async () => {
					if (await onsave()) close();
				}}
			>
				{m.common_save()}
			</Button>
		</div>
	{/snippet}
</WarpChipPopover>

<style>
	.awg-pop-body {
		display: flex;
		flex-direction: column;
		gap: 0.55rem;
	}
	.awg-nums {
		display: grid;
		grid-template-columns: repeat(3, minmax(0, 1fr));
		gap: 0.45rem;
	}
	.num-field {
		display: flex;
		flex-direction: column;
		gap: 0.25rem;
		font-size: 0.78rem;
		font-weight: 500;
		color: var(--color-text-secondary);
	}
	.findjunk-box {
		display: flex;
		flex-direction: column;
		gap: 0.6rem;
		padding: 0.75rem 0 0.1rem;
		border-top: 1px solid var(--color-border);
	}
	.fj-head {
		display: flex;
		align-items: center;
		justify-content: space-between;
		gap: 0.6rem;
	}
	.fj-title {
		font-size: 0.85rem;
		font-weight: 600;
		color: var(--color-text-primary);
	}
	.fj-controls {
		display: grid;
		grid-template-columns: minmax(0, 1.8fr) minmax(0, 0.75fr) minmax(0, 0.75fr);
		gap: 0.5rem;
		align-items: end;
	}
	.fj-prof {
		min-width: 0;
	}
	.fj-status {
		font-size: 0.78rem;
		color: var(--color-info);
	}
	.junk-attempts {
		display: flex;
		flex-direction: column;
		gap: 0.3rem;
		max-height: 12.5rem;
		overflow-y: auto;
		padding: 0.45rem;
		border-radius: var(--radius-sm);
		border: 1px solid var(--color-border);
		background: var(--color-bg-primary);
	}
	.junk-row {
		display: flex;
		align-items: center;
		gap: 0.45rem;
		padding: 0.32rem 0.5rem;
		border-radius: var(--radius-sm);
		font-size: 0.8125rem;
		line-height: 1.35;
		color: var(--color-text-secondary);
		flex-wrap: wrap;
	}
	.junk-row.best {
		background: var(--color-success-tint);
		color: var(--color-text-primary);
	}
	.fj-params {
		color: var(--color-text-muted);
	}
	.fj-score {
		margin-left: auto;
		font-weight: 600;
		white-space: nowrap;
		flex-shrink: 0;
	}
	.fj-foot {
		display: flex;
		align-items: center;
		justify-content: space-between;
		gap: 0.55rem;
		padding-top: 0.15rem;
		flex-wrap: wrap;
	}
	.fj-best {
		font-size: 0.8125rem;
		line-height: 1.35;
		color: var(--color-text-primary);
	}
	.fj-empty {
		margin: 0;
		font-size: 0.78rem;
		color: var(--color-text-muted);
	}
	.fj-err {
		margin: 0;
		font-size: 0.78rem;
		color: var(--color-error);
	}
	.mono {
		font-family: var(--font-mono);
	}
</style>
