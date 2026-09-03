<!--
  Карточка «MCP-сервер»: тумблер включения эндпоинта /mcp и управление
  именованными ключами доступа. Компонент презентационный: состояние и
  вызовы API отдаёт родитель через props/колбэки (как IntegrationsCard),
  поэтому тестируется без моков api.
-->
<script lang="ts">
	import { Button, ConfirmModal, Modal, Toggle } from '$lib/components/ui';
	import SettingsSectionLabel from './SettingsSectionLabel.svelte';
	import { copyToClipboard } from '$lib/utils/clipboard';
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
		oncreate: (name: string) => Promise<McpKeyCreated>;
		onrevoke: (id: string) => Promise<void>;
	}

	let { enabled, saving = false, keys, keysLoading = false, origin, ontoggle, oncreate, onrevoke }: Props = $props();

	const endpoint = $derived(`${origin}/mcp`);

	let createOpen = $state(false);
	let nameDraft = $state('');
	let creating = $state(false);
	let createError = $state<string | null>(null);
	let created = $state<McpKeyCreated | null>(null);
	let revokeTarget = $state<McpKey | null>(null);
	let revoking = $state(false);

	function openCreate() {
		nameDraft = '';
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
		createError = null;
	}

	async function submitCreate() {
		const name = nameDraft.trim();
		if (!name) {
			createError = 'Укажите название ключа';
			return;
		}
		creating = true;
		createError = null;
		try {
			created = await oncreate(name);
		} catch (e) {
			createError = e instanceof Error ? e.message : 'Не удалось создать ключ';
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
			notifications.success('Ключ скопирован в буфер обмена');
		} else {
			notifications.error('Не удалось скопировать ключ');
		}
	}

	function formatDate(iso?: string): string {
		if (!iso) return '—';
		const d = new Date(iso);
		return Number.isNaN(d.getTime()) ? '—' : d.toLocaleString();
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
		<SettingsSectionLabel label="MCP-сервер" icon={Plug} tone="indigo" header />
		<div class="setting-row toggle-inline-row">
			<div class="flex flex-col gap-1">
				<span class="font-medium">Доступ для ИИ-агентов (MCP)</span>
				<span class="setting-description">
					Эндпоинт Model Context Protocol для Claude Code, Cursor и других агентов. Доступ только по ключу, даже если авторизация веб-интерфейса выключена.
				</span>
			</div>
			<Toggle checked={enabled} onchange={ontoggle} disabled={saving} ariaLabel="MCP-сервер" />
		</div>

		{#if enabled}
			<div class="setting-row">
				<div class="flex flex-col gap-1">
					<span class="font-medium">Адрес эндпоинта</span>
					<span class="setting-description">Через KeenDNS используйте https://&lt;ваш-домен&gt;/mcp.</span>
				</div>
				<button type="button" class="api-key-input text-left" onclick={() => copyToClipboard(endpoint)} title="Скопировать">
					{endpoint}
				</button>
			</div>

			<div class="setting-row toggle-inline-row">
				<span class="font-medium">Ключи доступа</span>
				<Button variant="secondary" size="md" onclick={openCreate} disabled={saving}>Создать ключ</Button>
			</div>
			<div class="setting-row">
				{#if keysLoading}
					<span class="setting-description">Загрузка…</span>
				{:else if keys.length === 0}
					<span class="setting-description">Ключей пока нет — без ключа подключиться нельзя.</span>
				{:else}
					<div class="mcp-keys-table-wrap">
						<table class="mcp-keys-table w-full text-sm">
							<thead>
								<tr class="setting-description text-left">
									<th class="font-normal">Название</th>
									<th class="font-normal">Создан</th>
									<th class="font-normal">Использован</th>
									<th></th>
								</tr>
							</thead>
							<tbody>
								{#each keys as k (k.id)}
									<tr>
										<td>{k.name}</td>
										<td>{formatDate(k.createdAt)}</td>
										<td>{formatDate(k.lastUsedAt)}</td>
										<td class="text-right">
											<Button variant="danger" size="sm" onclick={() => (revokeTarget = k)} disabled={saving}>Отозвать</Button>
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

<Modal open={createOpen} title={created ? 'Ключ создан' : 'Новый ключ MCP'} size="md" onclose={() => closeCreate()}>
	{#if !created}
		<div class="flex flex-col gap-2">
			<label class="setting-description" for="mcp-key-name">Название (для кого этот ключ)</label>
			<input
				id="mcp-key-name"
				class="api-key-input"
				placeholder="Например, laptop"
				bind:value={nameDraft}
				maxlength="64"
				onkeydown={(e) => e.key === 'Enter' && submitCreate()}
			/>
			{#if createError}<span class="text-error text-sm">{createError}</span>{/if}
		</div>
	{:else}
		<div class="flex flex-col gap-3">
			<p class="setting-description">Ключ показывается только сейчас. Скопируйте его в конфигурацию клиента.</p>
			<div class="mcp-key-row">
				<button type="button" class="api-key-input text-left break-all" onclick={() => created && copyKey(created.key)} title="Скопировать">
					{created.key}
				</button>
				<button
					type="button"
					class="mcp-copy-btn"
					onclick={() => created && copyKey(created.key)}
					aria-label="Скопировать ключ"
					title="Скопировать ключ"
				>
					<Copy size={16} />
				</button>
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
			<Button variant="secondary" size="md" onclick={() => closeCreate()}>Отмена</Button>
			<Button variant="primary" size="md" onclick={submitCreate} disabled={creating}>Создать</Button>
		{:else}
			<Button variant="primary" size="md" onclick={() => closeCreate()}>Готово</Button>
		{/if}
	{/snippet}
</Modal>

<ConfirmModal
	open={revokeTarget !== null}
	title="Отозвать ключ?"
	message={revokeTarget ? `Клиенты с ключом «${revokeTarget.name}» потеряют доступ.` : ''}
	confirmLabel="Отозвать"
	busy={revoking}
	onConfirm={confirmRevoke}
	onClose={() => (revokeTarget = null)}
/>

<style>
	/* .api-key-input is scoped per-component in Svelte — the visual treatment
	   from the settings page (monospace, bordered, full-width) is repeated
	   here rather than shared, since it's only otherwise defined in +page.svelte. */
	.api-key-input {
		display: block;
		width: 100%;
		padding: 0.5rem 0.625rem;
		font-family: var(--font-mono);
		font-size: 0.8125rem;
		line-height: 1.35;
		word-break: break-all;
		background: var(--color-settings-control-bg, var(--bg-secondary));
		border: 1px solid var(--border, var(--color-border));
		border-radius: var(--radius-sm, 6px);
		color: var(--text-primary, var(--color-text-primary));
		cursor: pointer;
	}

	.mcp-key-row {
		display: flex;
		align-items: stretch;
		gap: 0.375rem;
	}

	.mcp-key-row .api-key-input {
		flex: 1;
		min-width: 0;
	}

	.mcp-copy-btn {
		display: inline-flex;
		align-items: center;
		justify-content: center;
		flex-shrink: 0;
		width: 2.25rem;
		background: var(--color-settings-control-bg, var(--bg-secondary));
		border: 1px solid var(--border, var(--color-border));
		border-radius: var(--radius-sm, 6px);
		color: var(--text-secondary, var(--color-text-secondary));
		cursor: pointer;
		transition: color 0.15s ease, background 0.15s ease;
	}

	.mcp-copy-btn:hover {
		color: var(--text-primary, var(--color-text-primary));
		background: var(--bg-hover, var(--color-bg-hover));
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
