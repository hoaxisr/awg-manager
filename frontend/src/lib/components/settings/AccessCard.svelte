<!--
  Карточка «Доступ»: вход по паролю роутера, время жизни сессии и HTTP-сервер
  панели. Сохранение общих настроек — у страницы (ontoggleAuth,
  onsaveSessionTtl); черновик времени сессии — здесь.
-->
<script lang="ts">
	import { m } from '$lib/i18n';
	import { Button, Toggle } from '$lib/components/ui';
	import { Lock } from 'lucide-svelte';
	import SettingsSectionLabel from './SettingsSectionLabel.svelte';
	import HttpServerCard from './HttpServerCard.svelte';
	import {
		clampSessionTtlHours,
		SESSION_TTL_DEFAULT_HOURS,
		SESSION_TTL_MIN_HOURS,
		SESSION_TTL_MAX_HOURS,
	} from './sessionTtl';

	interface Props {
		authEnabled: boolean;
		/** Сохранённое значение; старые бэкенды поле не отдают → 24 ч. */
		sessionTtlHours: number | undefined;
		saving?: boolean;
		ontoggleAuth: (enabled: boolean) => void;
		onsaveSessionTtl: (hours: number) => void;
	}

	let { authEnabled, sessionTtlHours, saving = false, ontoggleAuth, onsaveSessionTtl }: Props = $props();

	// «Время жизни сессии» — local state + changed-derived + save button
	// (same pattern as DnsRouteSettings).
	let sessionTtlLocal = $state<number | null>(SESSION_TTL_DEFAULT_HOURS);
	const savedSessionTtl = $derived(clampSessionTtlHours(sessionTtlHours));
	const sessionTtlChanged = $derived(sessionTtlLocal !== savedSessionTtl);

	$effect(() => {
		sessionTtlLocal = savedSessionTtl;
	});

	function saveSessionTtl() {
		const hours = clampSessionTtlHours(sessionTtlLocal);
		sessionTtlLocal = hours;
		onsaveSessionTtl(hours);
	}
</script>

<div class="settings-block">
	<div class="card">
	<SettingsSectionLabel label={m.settings_page_access_title()} icon={Lock} tone="blue" header />
	<div class="setting-row toggle-inline-row">
		<div class="flex flex-col gap-1">
			<span class="font-medium">{m.settings_page_auth_label()}</span>
			<span class="setting-description">
				{m.settings_page_auth_description()}
			</span>
		</div>
		<Toggle checked={authEnabled} controlled onchange={ontoggleAuth} disabled={saving} />
	</div>
	{#if authEnabled}
		<div class="setting-row session-ttl-row">
			<div class="flex flex-col gap-1">
				<span class="font-medium">{m.settings_page_session_ttl_label()}</span>
				<span class="setting-description">
					{m.settings_page_session_ttl_description()}
				</span>
			</div>
			<div class="session-ttl-form">
				<div class="input-with-suffix">
					<input
						type="number"
						id="sessionTtlHours"
						bind:value={sessionTtlLocal}
						min={SESSION_TTL_MIN_HOURS}
						max={SESSION_TTL_MAX_HOURS}
						disabled={saving}
					/>
					<span class="input-suffix">{m.settings_page_hours_suffix()}</span>
				</div>
				{#if sessionTtlChanged}
					<Button variant="primary" size="sm" onclick={saveSessionTtl} loading={saving}>
						{saving ? m.common_saving() : m.common_save()}
					</Button>
				{/if}
			</div>
		</div>
	{/if}
	<HttpServerCard />
	</div>
</div>

<style>
	/* «Время жизни сессии» — inline number input + save button (по образцу DnsRouteSettings) */
	.session-ttl-form {
		display: flex;
		align-items: center;
		justify-content: flex-end;
		gap: 0.5rem;
		flex-shrink: 0;
		min-width: 0;
	}

	.input-with-suffix {
		display: inline-flex;
		align-items: center;
		gap: 0.35rem;
		min-width: 0;
	}

	.input-suffix {
		font-size: 0.8125rem;
		color: var(--text-secondary);
	}

	.session-ttl-form input[type="number"] {
		width: 4.75rem;
	}

	@media (max-width: 640px) {
		.session-ttl-row {
			display: grid;
			grid-template-columns: minmax(0, 1fr) auto;
			align-items: center;
			gap: 0.75rem;
		}

		.session-ttl-form {
			flex-wrap: wrap;
			justify-content: flex-end;
		}
	}
</style>
