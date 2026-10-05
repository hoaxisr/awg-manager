import { describe, it, expect, afterEach } from 'vitest';
import { locale } from '$lib/i18n';
import {
	getDevelopChannelQuizQuestions,
	getDevelopChannelCopyCheatOptions,
	prepareDevelopQuizSession,
	scoreDevelopQuiz,
	formatDevelopChannelLockoutDurationLabel,
} from './developChannelGate';
import { linkImportErrorText, groupLinkImportErrors } from './linkImportError';
import {
	buildRoutingTunnelDropdownOptions,
	groupPolicyGlobalInterfaces,
	routingGroupLabel,
	routingTunnelGroupId,
} from './routingTunnelOptions';
import { parsePortEntry } from './ports';
import { validateTunnelIP } from './peerForm';
import { chainOutboundLabel } from './singboxConnections';
import { ApiValidationError } from '$lib/api/validate';
import type { RoutingTunnel } from '$lib/types';

afterEach(() => {
	locale.set('ru');
	localStorage.clear();
});

function tunnel(over: Partial<RoutingTunnel>): RoutingTunnel {
	return { id: 'x', name: 'x', type: 'managed', available: true, ...over } as RoutingTunnel;
}

describe('developChannelGate i18n', () => {
	it('quiz question and options follow the language, indices stay stable', () => {
		const ru = getDevelopChannelQuizQuestions().find((q) => q.id === 'tun-interface')!;
		expect(ru.text).toBe('Что такое tun-интерфейс?');
		locale.set('en');
		const en = getDevelopChannelQuizQuestions().find((q) => q.id === 'tun-interface')!;
		expect(en.text).toBe('What is a tun interface?');
		expect(en.options).toHaveLength(ru.options.length);
		expect(en.options[en.correctIndex]).toBe(
			'A virtual network interface for tunneling traffic at L3',
		);
		expect(en.correctIndex).toBe(ru.correctIndex);
	});

	it('answer checking uses indices in English', () => {
		locale.set('en');
		const questions = prepareDevelopQuizSession(7);
		const answers: Record<string, number> = {};
		for (const q of questions) answers[q.id] = q.correctIndex;
		expect(scoreDevelopQuiz(questions, answers)).toBe(7);
		const wrong = { ...answers, [questions[0].id]: (questions[0].correctIndex + 1) % questions[0].options.length };
		expect(scoreDevelopQuiz(questions, wrong)).toBe(6);
		expect(scoreDevelopQuiz(questions, answers, { [questions[1].id]: 'caught' })).toBe(6);
	});

	it('copy-cheat phrases and lockout label are localized', () => {
		expect(getDevelopChannelCopyCheatOptions()[0]).toBe(
			'Я попытался скопировать вопрос, чтобы считерить, простите меня',
		);
		expect(formatDevelopChannelLockoutDurationLabel(false)).toBe('30 минут');
		locale.set('en');
		expect(getDevelopChannelCopyCheatOptions()[0]).toBe(
			'I tried to copy the question to cheat, forgive me',
		);
		expect(formatDevelopChannelLockoutDurationLabel(true)).toBe('30 seconds');
	});
});

describe('linkImportError i18n', () => {
	const raw = 'line 3 (clash:vless): vlink: vless: missing uuid';

	it('maps a backend error to the UI language, keeping the line number', () => {
		expect(linkImportErrorText(raw)).toBe('Строка 3: Не указан UUID');
		locale.set('en');
		expect(linkImportErrorText(raw)).toBe('Line 3: UUID is not set');
	});

	it('unknown text is wrapped, not lost, and groups keep counts', () => {
		locale.set('en');
		expect(linkImportErrorText('something odd')).toBe('The link could not be parsed: something odd');
		expect(groupLinkImportErrors(['vlink: vless: missing uuid', 'vlink: vless: missing uuid'])).toEqual([
			'UUID is not set (×2)',
		]);
	});
});

describe('routingTunnelOptions i18n', () => {
	it('groups by stable id, labels follow the language', () => {
		const wg = tunnel({ id: 'system:Wireguard0', name: 'WG', type: 'system' });
		expect(routingTunnelGroupId(wg)).toBe('systemWg');
		expect(routingGroupLabel('systemWg')).toBe('Системные WireGuard');
		expect(buildRoutingTunnelDropdownOptions([tunnel({}), wg]).map((o) => o.group)).toEqual([
			'AWG туннели',
			'Системные WireGuard',
		]);
		locale.set('en');
		const en = buildRoutingTunnelDropdownOptions([wg, tunnel({})]);
		// order is by group id, not by the translated label
		expect(en.map((o) => o.value)).toEqual(['x', 'system:Wireguard0']);
		expect(en.map((o) => o.group)).toEqual([
			routingGroupLabel('awg'),
			routingGroupLabel('systemWg'),
		]);
		expect(routingGroupLabel('provider')).toBe('Provider');
	});

	it('policy interface groups carry ids', () => {
		const groups = groupPolicyGlobalInterfaces([
			{ name: 'Proxy0', label: 'P', up: true },
			{ name: 'PPPoE0', label: 'ISP', up: true },
		]);
		expect(groups.map((g) => g.id)).toEqual(['provider', 'proxy']);
		locale.set('en');
		expect(groupPolicyGlobalInterfaces([{ name: 'Proxy0', label: 'P', up: true }])[0].group).toBe('Proxies');
	});
});

describe('client-side validation and API messages', () => {
	it('ports and peer form validators', () => {
		expect(parsePortEntry('443')).toEqual({ ok: false, error: 'укажите протокол: TCP или UDP' });
		expect(validateTunnelIP('')).toBe('укажите адрес');
		locale.set('en');
		expect(parsePortEntry('443')).toEqual({ ok: false, error: 'specify a protocol: TCP or UDP' });
		expect(validateTunnelIP('')).toBe('enter an address');
	});

	it('API validation error and sing-box outbound labels', () => {
		expect(new ApiValidationError('/x', ['a']).message).toBe('Некорректный ответ сервера (/x): a');
		expect(chainOutboundLabel(['DIRECT'])).toBe('Прямое');
		locale.set('en');
		expect(new ApiValidationError('/x', ['a']).message).toBe('Invalid server response (/x): a');
		expect(chainOutboundLabel(['DIRECT'])).toBe('Direct');
		expect(chainOutboundLabel(['my-proxy'])).toBe('my-proxy');
	});
});
