<script lang="ts">
	import { m } from '$lib/i18n';
	import { api } from '$lib/api/client';
	import { stripFircEmojis, type FircTunnelSource } from '$lib/types/firc';
	import { Button, Modal } from '$lib/components/ui';
	import { notifications } from '$lib/stores/notifications';
	import { SUB_INTERVALS, parseHeadersTextToMap, randomId } from './fircTunnelEdit';

	// Форма живёт, пока модалка смонтирована: владелец монтирует её на каждое
	// открытие, поэтому поля всегда начинаются пустыми.
	interface Props {
		kind: 'subscription' | 'link';
		headerProfiles: Array<{ kind: string; label: string; headersText: string }>;
		onclose: () => void;
		/** Разобранные источники; владелец добавляет их в черновик туннеля. */
		onadd: (sources: FircTunnelSource[]) => Promise<void>;
	}

	let { kind, headerProfiles, onclose, onadd }: Props = $props();

	let name = $state('');
	let url = $state('');
	let interval = $state(21600);
	let headersText = $state('');
	let profileKind = $state('');
	let detecting = $state(false);
	let busy = $state(false);

	async function handleDetect() {
		if (!url.trim() || detecting) return;
		detecting = true;
		try {
			const customHeaders = Object.entries(parseHeadersTextToMap(headersText) ?? {}).map(
				([name, value]) => ({ name, value }),
			);
			const res = await api.detectSubscriptionHeaders(url.trim(), customHeaders);
			if (res.normalizedUrl || res.decryptedUrl) {
				url = res.normalizedUrl || res.decryptedUrl || url;
			}
			if (res.headersText) {
				headersText = res.headersText;
			}
			if (res.kind) {
				profileKind = res.kind;
			}
			notifications.success(
				m.firc_tunnel_edit_detect_ok({
					profile: res.label || res.kind || m.firc_tunnel_edit_profile_auto(),
					count: res.serverCount ?? 0,
				}),
			);
		} catch (e: unknown) {
			notifications.error((e instanceof Error ? e.message : undefined) ?? m.firc_tunnel_edit_detect_error());
		} finally {
			detecting = false;
		}
	}

	function handleSelectProfile(next: string) {
		profileKind = next;
		const found = headerProfiles.find((p) => p.kind === next);
		if (found) {
			headersText = found.headersText || '';
		}
	}

	async function submit() {
		if (!url.trim()) return;
		busy = true;
		try {
			const headers = kind === 'subscription' ? parseHeadersTextToMap(headersText) : undefined;
			const defaultName = stripFircEmojis(name.trim() || (kind === 'subscription' ? 'Sub' : 'Node'));
			let addedSources: FircTunnelSource[] = [];
			try {
				const normRes = await api.fircNormalizeSource({
					input: url.trim(),
					name: defaultName,
					interval,
					headers,
				});
				if (normRes?.sources?.length) {
					addedSources = normRes.sources.map((s) => ({
						...s,
						id: s.id || randomId(),
						name: stripFircEmojis(s.name || defaultName),
						interval: s.kind === 'subscription' ? (s.interval || interval) : undefined,
						headers: s.kind === 'subscription' ? (s.headers ?? headers) : undefined,
					}));
				}
			} catch (normErr: unknown) {
				// If explicit happ://crypt or FiniteProto v2 parsing failed, surface the error directly
				const trimmedLower = url.trim().toLowerCase();
				if (
					trimmedLower.startsWith('happ://crypt') ||
					trimmedLower.startsWith('fp://') ||
					trimmedLower.startsWith('finiteproto://') ||
					trimmedLower.includes('finiteproto')
				) {
					throw normErr;
				}
			}

			if (addedSources.length === 0) {
				const fallbackSource: FircTunnelSource =
					kind === 'subscription'
						? {
								id: randomId(),
								kind: 'subscription',
								name: defaultName,
								url: url.trim(),
								interval,
								headers,
							}
						: {
								id: randomId(),
								kind: 'link',
								name: defaultName,
								link: url.trim(),
							};
				addedSources = [fallbackSource];
			}

			await onadd(addedSources);
		} catch (e: unknown) {
			notifications.error((e instanceof Error ? e.message : undefined) ?? m.firc_tunnel_edit_source_parse_error());
		} finally {
			busy = false;
		}
	}
</script>

<Modal
	open
	title={kind === 'subscription'
		? m.firc_tunnel_edit_source_modal_title_sub()
		: m.firc_tunnel_edit_source_modal_title_link()}
	size="md"
	{onclose}
>
	<div class="modal-form">
		<div class="field">
			<label class="lbl" for="src-modal-name">{m.firc_tunnel_edit_source_name_label()}</label>
			<input
				id="src-modal-name"
				class="inp"
				type="text"
				placeholder={kind === 'subscription'
					? m.firc_tunnel_edit_source_name_example_sub()
					: m.firc_tunnel_edit_source_name_example_link()}
				bind:value={name}
			/>
		</div>
		{#if kind === 'subscription'}
			<div class="field">
				<label class="lbl" for="src-modal-url">
					{m.firc_tunnel_edit_sub_url_label()}
				</label>
				<div style="display: flex; gap: 6px; align-items: center;">
					<input
						id="src-modal-url"
						class="inp mono"
						style="flex: 1;"
						type="text"
						placeholder={m.firc_tunnel_edit_sub_url_placeholder()}
						bind:value={url}
					/>
					<Button
						variant="secondary"
						size="sm"
						loading={detecting}
						disabled={!url.trim()}
						onclick={handleDetect}
					>
						{m.firc_tunnel_edit_detect_btn()}
					</Button>
				</div>
				<span class="hint">
					{m.firc_tunnel_edit_sub_url_hint_encrypted()} <code>happ://crypt..crypt4</code>, {m.firc_tunnel_edit_sub_url_hint_wrappers()} <code>happ://</code>, <code>clash://</code>, <code>sub://</code> {m.firc_tunnel_edit_sub_url_hint_files()}
				</span>
			</div>
			<div class="field-grid-2">
				<div class="field">
					<label class="lbl" for="src-modal-int">{m.firc_tunnel_edit_auto_refresh_label()}</label>
					<select id="src-modal-int" class="inp" bind:value={interval}>
						{#each SUB_INTERVALS as opt}
							<option value={opt.value}>{opt.label()}</option>
						{/each}
					</select>
				</div>
				<div class="field">
					<label class="lbl" for="src-modal-prof">{m.firc_tunnel_edit_profile_label()}</label>
					<select
						id="src-modal-prof"
						class="inp"
						value={profileKind}
						onchange={(e) => handleSelectProfile((e.currentTarget as HTMLSelectElement).value)}
					>
						<option value="">{m.firc_tunnel_edit_profile_default()}</option>
						{#each headerProfiles as prof (prof.kind)}
							<option value={prof.kind}>{prof.label}</option>
						{/each}
					</select>
				</div>
			</div>
			<div class="field">
				<label class="lbl" for="src-modal-hdr">{m.firc_tunnel_edit_headers_label()}</label>
				<textarea
					id="src-modal-hdr"
					class="inp mono"
					rows="3"
					placeholder="User-Agent: Happ/3.6.1&#10;X-HWID: ..."
					bind:value={headersText}
				></textarea>
			</div>
		{:else}
			<div class="field">
				<label class="lbl" for="src-modal-url">
					{m.firc_tunnel_edit_links_label()}
				</label>
				<textarea
					id="src-modal-url"
					class="inp mono"
					rows="6"
					placeholder={m.firc_tunnel_edit_links_placeholder()}
					bind:value={url}
				></textarea>
				<span class="hint">
					{m.firc_tunnel_edit_links_hint()}
				</span>
			</div>
		{/if}
	</div>
	{#snippet actions()}
		<Button variant="secondary" size="sm" onclick={onclose}>
			{m.common_cancel()}
		</Button>
		<Button
			variant="primary"
			size="sm"
			loading={busy}
			disabled={!url.trim()}
			onclick={submit}
		>
			{m.common_add()}
		</Button>
	{/snippet}
</Modal>

<style>
	.modal-form {
		display: flex;
		flex-direction: column;
		gap: 10px;
	}
	.field-grid-2 {
		display: grid;
		grid-template-columns: 1fr 1fr;
		gap: 8px;
	}
	.field {
		display: flex;
		flex-direction: column;
		gap: 4px;
	}
	.lbl {
		font-size: 11.5px;
		color: var(--text-secondary);
		font-weight: 500;
	}
	.hint {
		font-size: 11px;
		color: var(--text-muted);
	}
	.inp {
		padding: 5px 8px;
		border-radius: 6px;
		border: 1px solid var(--border);
		background: var(--bg-primary);
		color: var(--text-primary);
		font-size: 12px;
		width: 100%;
		box-sizing: border-box;
	}
	select.inp {
		padding-right: 1.75rem;
		text-overflow: ellipsis;
	}
	.mono {
		font-family: var(--font-mono);
	}
</style>
