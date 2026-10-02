<!--
  Карточка «MCP-сервер»: тумблер включения эндпоинта /mcp и управление
  именованными ключами доступа. Компонент презентационный: состояние и
  вызовы API отдаёт родитель через props/колбэки (как IntegrationsCard),
  поэтому тестируется без моков api.
-->
<script lang="ts">
	import { m } from '$lib/i18n';
	import { Badge, Button, ConfirmModal, IconButton, Modal, Toggle } from '$lib/components/ui';
	import SettingsSectionLabel from './SettingsSectionLabel.svelte';
	import { copyToClipboard } from '$lib/utils/clipboard';
	import { formatDate } from '$lib/utils/format';
	import { notifications } from '$lib/stores/notifications';
	import { Copy, Plug } from 'lucide-svelte';
	import type { McpKey, McpKeyCreated } from '$lib/types';

	interface Props {
		enabled: boolean;
		saving?: boolean;
		keys: McpKey[];
		keysLoading?: boolean;
		/** Origin веб-интерфейса, например http://192.168.1.1:2222. */
		origin: string;
		ontoggle: (enabled: boolean) => void;
		oncreate: (name: string, readOnly: boolean) => Promise<McpKeyCreated>;
		onrevoke: (id: string) => Promise<void>;
	}

	let { enabled, saving = false, keys, keysLoading = false, origin, ontoggle, oncreate, onrevoke }: Props = $props();

	const endpoint = $derived(`${origin}/mcp`);

	let createOpen = $state(false);
	let nameDraft = $state('');
	// Область выбирается только при выпуске: сменить её у существующего
	// ключа нельзя, иначе выданный агенту ключ менял бы права под ним.
	let readOnlyDraft = $state(false);
	let creating = $state(false);
	let createError = $state<string | null>(null);
	let created = $state<McpKeyCreated | null>(null);
	let revokeTarget = $state<McpKey | null>(null);
	let revoking = $state(false);

	function openCreate() {
		nameDraft = '';
		readOnlyDraft = false;
		createError = null;
		created = null;
		createOpen = true;
	}

	// Закрытие обязано стирать сам ключ: плейнтекст показывается один раз, и
	// держать его в состоянии компонента после закрытия окна незачем — до
	// следующего openCreate() он оставался бы в памяти вкладки.
	function closeCreate() {
		createOpen = false;
		created = null;
		nameDraft = '';
		readOnlyDraft = false;
		createError = null;
	}

	async function submitCreate() {
		const name = nameDraft.trim();
		if (!name) {
			createError = m.settings_mcp_name_required();
			return;
		}
		creating = true;
		createError = null;
		try {
			created = await oncreate(name, readOnlyDraft);
		} catch (e) {
			createError = e instanceof Error ? e.message : m.settings_mcp_create_failed();
		} finally {
			creating = false;
		}
	}

	async function confirmRevoke() {
		if (!revokeTarget) return;
		revoking = true;
		try {
			await onrevoke(revokeTarget.id);
		} catch {
			// The parent (settings page) surfaces the error via a notification.
			// The card must not leave the confirm dialog stuck open regardless
			// of whether a future/other caller rethrows — a hung dialog on a
			// destructive action is worse than a dialog that closes and lets
			// the toast explain what happened.
		} finally {
			revoking = false;
			revokeTarget = null;
		}
	}

	async function copyKey(text: string) {
		if (await copyToClipboard(text)) {
			notifications.success(m.settings_mcp_key_copied());
		} else {
			notifications.error(m.settings_mcp_key_copy_failed());
		}
	}

	async function copyEndpoint() {
		if (await copyToClipboard(endpoint)) {
			notifications.success(m.settings_mcp_address_copied());
		} else {
			notifications.error(m.settings_mcp_address_copy_failed());
		}
	}

	const claudeSnippet = $derived(
		created ? `claude mcp add --transport http awg-manager ${endpoint} --header "Authorization: Bearer ${created.key}"` : '',
	);
	const jsonSnippet = $derived(
		created
			? JSON.stringify({ mcpServers: { 'awg-manager': { type: 'http', url: endpoint, headers: { Authorization: `Bearer ${created.key}` } } } }, null, 2)
			: '',
	);
	// The header value goes through an env var, not straight into args:
	// Claude Desktop on Windows and Cursor split every args element on
	// spaces, so "Authorization:Bearer <key>" would arrive as two arguments
	// and the token would be lost (401 on every reconnect, with the user
	// told their freshly pasted key is invalid). This is the form
	// mcp-remote's own README prescribes for exactly that reason.
	const remoteSnippet = $derived(
		created
			? JSON.stringify(
					{
						mcpServers: {
							'awg-manager': {
								command: 'npx',
								args: ['-y', 'mcp-remote', endpoint, '--header', 'Authorization:${AUTH_HEADER}'],
								env: { AUTH_HEADER: `Bearer ${created.key}` },
							},
						},
					},
					null,
					2,
				)
			: '',
	);
</script>

<div class="settings-block">
	<div class="card">
		<SettingsSectionLabel label={m.settings_mcp_title()} icon={Plug} tone="indigo" header />
		<div class="setting-row toggle-inline-row">
			<div class="flex flex-col gap-1">
				<span class="flex items-center gap-2">
					<span class="font-medium">{m.settings_mcp_access_label()}</span>
					<Badge variant="accent" size="sm" uppercase>Beta</Badge>
				</span>
				<span class="setting-description">
					{m.settings_mcp_access_description()}
				</span>
			</div>
			<Toggle checked={enabled} onchange={ontoggle} disabled={saving} ariaLabel={m.settings_mcp_title()} />
		</div>

		{#if enabled}
			<div class="setting-row">
				<div class="flex flex-col gap-1">
					<span class="font-medium">{m.settings_mcp_endpoint_label()}</span>
					<span class="setting-description">{m.settings_mcp_endpoint_description()}</span>
				</div>
				<div class="mcp-key-row">
					<code class="mcp-code-value">{endpoint}</code>
					<IconButton size="md" ariaLabel={m.settings_mcp_copy_endpoint()} title={m.settings_mcp_copy_endpoint()} onclick={copyEndpoint}>
						<Copy size={16} />
					</IconButton>
				</div>
			</div>

			<div class="setting-row toggle-inline-row">
				<span class="font-medium">{m.settings_mcp_keys_label()}</span>
				<Button variant="secondary" size="md" onclick={openCreate} disabled={saving}>{m.settings_mcp_create_key()}</Button>
			</div>
			<div class="setting-row">
				{#if keysLoading}
					<span class="setting-description">{m.settings_mcp_loading()}</span>
				{:else if keys.length === 0}
					<span class="setting-description">{m.settings_mcp_no_keys()}</span>
				{:else}
					<div class="mcp-keys-table-wrap">
						<table class="mcp-keys-table w-full text-sm">
							<thead>
								<tr class="setting-description text-left">
									<th class="font-normal">{m.settings_mcp_col_name()}</th>
									<th class="font-normal">{m.settings_mcp_col_created()}</th>
									<th class="font-normal">{m.settings_mcp_col_used()}</th>
									<th></th>
								</tr>
							</thead>
							<tbody>
								{#each keys as k (k.id)}
									<tr>
										<td>
											{k.name}
											{#if k.readOnly}
												<Badge variant="info" size="sm">{m.settings_mcp_read_only_badge()}</Badge>
											{/if}
										</td>
										<td>{formatDate(k.createdAt)}</td>
										<td>{k.lastUsedAt ? formatDate(k.lastUsedAt) : '—'}</td>
										<td class="text-right">
											<Button variant="danger" size="sm" onclick={() => (revokeTarget = k)} disabled={saving}>{m.settings_mcp_revoke()}</Button>
										</td>
									</tr>
								{/each}
							</tbody>
						</table>
					</div>
				{/if}
			</div>
		{/if}
	</div>
</div>

<Modal open={createOpen} title={created ? m.settings_mcp_modal_created() : m.settings_mcp_modal_new()} size="md" onclose={() => closeCreate()}>
	{#if !created}
		<div class="flex flex-col gap-2">
			<label class="setting-description" for="mcp-key-name">{m.settings_mcp_name_label()}</label>
			<input
				id="mcp-key-name"
				class="api-key-input"
				placeholder={m.settings_mcp_name_placeholder()}
				bind:value={nameDraft}
				maxlength="64"
				onkeydown={(e) => e.key === 'Enter' && submitCreate()}
			/>
			<Toggle checked={readOnlyDraft} onchange={(v) => (readOnlyDraft = v)} label={m.settings_mcp_read_only_label()} size="sm" />
			<span class="setting-description text-xs">
				{m.settings_mcp_read_only_description()}
			</span>
			{#if createError}<span class="text-error text-sm">{createError}</span>{/if}
		</div>
	{:else}
		<div class="flex flex-col gap-3">
			<p class="setting-description">{m.settings_mcp_key_shown_once()}</p>
			<div class="mcp-key-row">
				<code class="mcp-code-value">{created.key}</code>
				<IconButton size="md" ariaLabel={m.settings_mcp_copy_key()} title={m.settings_mcp_copy_key()} onclick={() => created && copyKey(created.key)}>
					<Copy size={16} />
				</IconButton>
			</div>
			<details>
				<summary class="cursor-pointer">Claude Code (CLI)</summary>
				<pre class="text-xs whitespace-pre-wrap break-all">{claudeSnippet}</pre>
			</details>
			<details>
				<summary class="cursor-pointer">Cursor / .mcp.json</summary>
				<pre class="text-xs whitespace-pre-wrap break-all">{jsonSnippet}</pre>
			</details>
			<details>
				<summary class="cursor-pointer">Claude Desktop (mcp-remote)</summary>
				<pre class="text-xs whitespace-pre-wrap break-all">{remoteSnippet}</pre>
			</details>
		</div>
	{/if}
	{#snippet actions()}
		{#if !created}
			<Button variant="secondary" size="md" onclick={() => closeCreate()}>{m.common_cancel()}</Button>
			<Button variant="primary" size="md" onclick={submitCreate} disabled={creating}>{m.settings_mcp_create()}</Button>
		{:else}
			<Button variant="primary" size="md" onclick={() => closeCreate()}>{m.settings_mcp_done()}</Button>
		{/if}
	{/snippet}
</Modal>

<ConfirmModal
	open={revokeTarget !== null}
	title={m.settings_mcp_revoke_title()}
	message={revokeTarget ? m.settings_mcp_revoke_message({ name: revokeTarget.name }) : ''}
	confirmLabel={m.settings_mcp_revoke()}
	busy={revoking}
	onConfirm={confirmRevoke}
	onClose={() => (revokeTarget = null)}
/>

<style>
	/* The name input uses the shared .api-key-input from app.css. Read-only
	   values use .mcp-code-value instead, which deliberately looks nothing
	   like a field. */

	/* Read-only display for a value the user needs to copy manually (endpoint,
	   one-time key): no border/field affordance, so it doesn't look editable,
	   and user-select: all so a single click selects the whole value as a
	   fallback when the copy button's clipboard call fails — the important
	   case on a plain-HTTP router LAN address, which isn't a secure context. */
	.mcp-code-value {
		display: block;
		flex: 1;
		min-width: 0;
		padding: 0.5rem 0.625rem;
		font-family: var(--font-mono);
		font-size: 0.8125rem;
		line-height: 1.35;
		word-break: break-all;
		background: var(--color-settings-control-bg, var(--bg-secondary));
		border-radius: var(--radius-sm, 6px);
		color: var(--text-primary, var(--color-text-primary));
		user-select: all;
	}

	.mcp-key-row {
		display: flex;
		align-items: center;
		gap: 0.375rem;
	}

	/* Inside .setting-row (a space-between row shared with the label), the row
	   must claim the remaining width itself — .mcp-code-value's own flex:1 only
	   applies once it already has a sized flex container to grow inside. */
	.setting-row .mcp-key-row {
		flex: 1;
		min-width: 0;
	}

	/* The keys table's row spacing (py-1) was silently defeated: app.css has an
	   unlayered `*, *::before, *::after { margin: 0; padding: 0; }` reset, and
	   unlayered rules always beat @layer-utilities rules like Tailwind's `.py-1`
	   in the cascade regardless of specificity — so every td/th had 0 padding
	   and adjacent rows' "Отозвать" buttons touched edge to edge. Scoped
	   component <style> isn't layered either, so setting padding here (equal
	   footing, higher specificity than `*`) actually sticks.
	   min-width plus the wrapper's overflow-x keeps the four columns from being
	   squeezed onto tiny cards (~400px) — the row scrolls horizontally inside
	   the card instead of overlapping or bleeding past its edge. */
	.mcp-keys-table-wrap {
		overflow-x: auto;
		/* Without this, the wrap (a flex item of .setting-row) takes its
		   content's min-content width instead of shrinking to the row, so the
		   scrollable table bleeds past the card edge instead of scrolling. */
		min-width: 0;
		width: 100%;
	}

	.mcp-keys-table {
		min-width: 28rem;
		border-collapse: collapse;
	}

	.mcp-keys-table th,
	.mcp-keys-table td {
		padding: 0.25rem 0;
	}
</style>
