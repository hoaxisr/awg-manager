<script lang="ts">
	import { auth } from '$lib/stores/auth';

	let login = $state('');
	let password = $state('');
	let submitting = $state(false);

	async function handleSubmit() {
		if (!login || !password) return;

		submitting = true;
		await auth.login(login, password);
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
			<svg viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2" class="login-icon">
				<path d="M12 22s8-4 8-10V5l-8-3-8 3v7c0 6 8 10 8 10z"/>
			</svg>
			<h1>AWG Manager</h1>
			<p class="login-subtitle">Введите данные от роутера Keenetic</p>
		</div>

		{#if $auth.error}
			<div class="login-error">
				{$auth.error}
			</div>
		{/if}

		<form onsubmit={(e) => { e.preventDefault(); handleSubmit(); }} class="login-form">
			<div class="form-group">
				<label for="login">Логин</label>
				<input
					id="login"
					type="text"
					bind:value={login}
					onkeydown={handleKeydown}
					placeholder="admin"
					autocomplete="username"
					disabled={submitting}
				/>
			</div>

			<div class="form-group">
				<label for="password">Пароль</label>
				<input
					id="password"
					type="password"
					bind:value={password}
					onkeydown={handleKeydown}
					placeholder="Пароль от роутера"
					autocomplete="current-password"
					disabled={submitting}
				/>
			</div>

			<button
				type="submit"
				class="btn btn-primary btn-lg login-button"
				disabled={submitting || !login || !password}
			>
				{#if submitting}
					<span class="spinner"></span>
					Вход...
				{:else}
					Войти
				{/if}
			</button>
		</form>

		<p class="login-hint">
			Используйте логин и пароль администратора роутера
		</p>
	</div>
</div>

<style>
	.login-container {
		min-height: 100vh;
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

	.login-icon {
		width: 48px;
		height: 48px;
		color: var(--accent);
		margin-bottom: 0.75rem;
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
		gap: 1rem;
	}

	.form-group {
		display: flex;
		flex-direction: column;
		gap: 0.25rem;
	}

	.login-button {
		width: 100%;
		margin-top: 0.5rem;
	}

	.login-hint {
		margin-top: 1.5rem;
		text-align: center;
		font-size: 0.75rem;
		color: var(--text-muted);
	}

	.spinner {
		width: 16px;
		height: 16px;
		border: 2px solid transparent;
		border-top-color: currentColor;
		border-radius: 50%;
		animation: spin 0.6s linear infinite;
	}

	@keyframes spin {
		to { transform: rotate(360deg); }
	}
</style>
