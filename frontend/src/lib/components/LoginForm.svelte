<script lang="ts">
	import { auth } from '$lib/stores/auth';
	import { createPersistedStore } from '$lib/stores/persisted';
	import { Button, SegmentedControl } from '$lib/components/ui';
	import type { SegmentedOption } from '$lib/components/ui/segmentedControl';
	import BrandLogoMark from '$lib/components/layout/BrandLogoMark.svelte';
	import type { LoginMethod } from '$lib/types';
	import { m } from '$lib/i18n';

	let login = $state('');
	let password = $state('');
	let submitting = $state(false);

	// $derived, а не константа: подписи пересчитываются при смене языка.
	const methodOptions: SegmentedOption<LoginMethod>[] = $derived([
		{ value: 'router', label: m.login_method_router() },
		{ value: 'entware', label: 'Entware' }
	]);

	// Выбор способа входа помнит браузер; мусор в хранилище → роутер.
	const method = createPersistedStore<LoginMethod>('awgm-login-method', {
		defaultValue: 'router',
		deserialize: (raw) => (raw === 'entware' ? 'entware' : 'router'),
		serialize: (value) => value
	});
	const isEntware = $derived($method === 'entware');

	async function handleSubmit() {
		if (!login || !password) return;

		submitting = true;
		await auth.login(login, password, $method);
		submitting = false;
	}

	function handleKeydown(e: KeyboardEvent) {
		if (e.key === 'Enter') {
			handleSubmit();
		}
	}
</script>

<div class="login-container">
	<div class="login-card">
		<div class="login-header">
			<div class="login-brand">
				<BrandLogoMark dimension={52} />
			</div>
			<h1>AWG Manager</h1>
			<p class="login-subtitle">
				{isEntware ? m.login_subtitle_entware() : m.login_subtitle_router()}
			</p>
		</div>

		{#if $auth.error}
			<div class="login-error">
				{$auth.error}
			</div>
		{/if}

		<form onsubmit={(e) => { e.preventDefault(); handleSubmit(); }} class="login-form">
			<div class="login-method">
				<SegmentedControl
					value={$method}
					options={methodOptions}
					ariaLabel={m.login_method_aria()}
					disabled={submitting}
					fullWidth
					onchange={(v) => {
						method.set(v);
						auth.clearError();
					}}
				/>
			</div>

			<div class="form-group">
				<label for="login">{m.login_label_login()}</label>
				<input
					id="login"
					type="text"
					bind:value={login}
					oninput={() => auth.clearError()}
					onkeydown={handleKeydown}
					placeholder={isEntware ? m.login_placeholder_login_entware() : m.login_placeholder_login_router()}
					autocomplete="username"
					disabled={submitting}
				/>
			</div>

			<div class="form-group">
				<label for="password">{m.login_label_password()}</label>
				<input
					id="password"
					type="password"
					bind:value={password}
					oninput={() => auth.clearError()}
					onkeydown={handleKeydown}
					placeholder={isEntware ? m.login_placeholder_password_entware() : m.login_placeholder_password_router()}
					autocomplete="current-password"
					disabled={submitting}
				/>
			</div>

			<!-- TODO Phase 1: Button primitive has no `lg` size yet — using `md`; revisit when `lg` lands. -->
			<div class="login-button">
				<Button
					type="submit"
					variant="primary"
					size="md"
					fullWidth
					disabled={!login || !password}
					loading={submitting}
				>
					{submitting ? m.login_submitting() : m.login_submit()}
				</Button>
			</div>
		</form>

	<p class="login-hint">
		{isEntware ? m.login_hint_entware() : m.login_hint_router()}
	</p>

	<p class="login-hint" style="margin-top: 0.2rem;">
		{m.login_terms_prefix()} <a href="/terms">{m.login_terms_link()}</a>
	</p>
</div>
</div>

<style>
	.login-container {
		min-height: calc(100dvh - 56px);
		display: flex;
		align-items: center;
		justify-content: center;
		padding: 1rem;
		background: var(--bg-primary);
	}

	.login-card {
		width: 100%;
		max-width: 380px;
		padding: 2rem;
		background: var(--bg-secondary);
		border-radius: var(--radius);
		border: 1px solid var(--border);
		box-shadow: var(--shadow);
	}

	.login-header {
		text-align: center;
		margin-bottom: 1.5rem;
	}

	.login-brand {
		display: flex;
		justify-content: center;
		margin-bottom: 0.75rem;
	}

	.login-brand :global(.brand-logo-mark) {
		width: 52px;
		height: 52px;
	}

	@media (min-width: 641px) and (max-width: 1050px) {
		.login-brand :global(.brand-logo-mark) {
			width: 44px;
			height: 44px;
		}

		.login-header h1 {
			font-size: 1.375rem;
		}
	}

	.login-header h1 {
		font-size: 1.5rem;
		margin-bottom: 0.25rem;
	}

	.login-subtitle {
		color: var(--text-secondary);
		font-size: 0.875rem;
	}

	.login-error {
		background: color-mix(in srgb, var(--error) 15%, transparent);
		border: 1px solid var(--error);
		color: var(--error);
		padding: 0.75rem;
		border-radius: var(--radius-sm);
		margin-bottom: 1rem;
		font-size: 0.875rem;
		text-align: center;
	}

	.login-form {
		display: flex;
		flex-direction: column;
		gap: 0rem;
	}

	.login-method {
		margin-bottom: 1rem;
	}

	.form-group {
		display: flex;
		flex-direction: column;
		gap: 0.25rem;
	}

	.login-button {
		margin-top: 1rem;
	}

	.login-hint {
		margin-top: 1.5rem;
		text-align: center;
		font-size: 0.75rem;
		color: var(--text-muted);
	}

	.login-hint a {
		color: var(--color-accent, var(--text-muted));
		text-decoration: underline;
		text-underline-offset: 2px;
	}

	.login-hint a:hover {
		opacity: 0.8;
	}
</style>
