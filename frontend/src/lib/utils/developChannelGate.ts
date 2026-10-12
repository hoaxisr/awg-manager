import { m } from '$lib/i18n';

export const DEVELOP_CHANNEL_LOCKOUT_KEY = 'awg-manager-develop-channel-lockout-until';
export const DEVELOP_CHANNEL_QUIZ_PASSED_KEY = 'awg-manager-develop-channel-quiz-passed';
export const DEVELOP_CHANNEL_QUIZ_SIZE = 7;
export const DEVELOP_CHANNEL_QUIZ_MAX_WRONG = 2;
/** @deprecated Use {@link DEVELOP_CHANNEL_QUIZ_SIZE} and {@link DEVELOP_CHANNEL_QUIZ_MAX_WRONG}. */
export const DEVELOP_CHANNEL_QUIZ_PASS_MIN =
	DEVELOP_CHANNEL_QUIZ_SIZE - DEVELOP_CHANNEL_QUIZ_MAX_WRONG;
export const DEVELOP_CHANNEL_QUIZ_QUESTION_MS = 30 * 1000;
export const DEVELOP_CHANNEL_LOCKOUT_MS = 30 * 60 * 1000;
/** Shorter lockout in `yarn dev:mock` so the quiz flow can be exercised locally. */
export const DEVELOP_CHANNEL_LOCKOUT_MOCK_MS = 30 * 1000;
export const DEVELOP_CHANNEL_DOCS_URL = 'https://awgm.hoaxisr.ru/';

/** Shown as the only answer after copy/Ctrl+C during the quiz. */
export function getDevelopChannelCopyCheatOptions(): string[] {
	return [
		m.develop_gate_cheat_1(),
		m.develop_gate_cheat_2(),
		m.develop_gate_cheat_3(),
		m.develop_gate_cheat_4(),
		m.develop_gate_cheat_5(),
		m.develop_gate_cheat_6(),
		m.develop_gate_cheat_7(),
		m.develop_gate_cheat_8(),
		m.develop_gate_cheat_9(),
		m.develop_gate_cheat_10(),
	];
}

export function pickCopyCheatOption(
	pool: readonly string[] = getDevelopChannelCopyCheatOptions(),
): string {
	return pool[Math.floor(Math.random() * pool.length)] ?? pool[0];
}

export type DevelopQuizQuestion = {
	id: string;
	text: string;
	options: string[];
	correctIndex: number;
};

type Msg = () => string;

type QuizBankEntry = {
	id: string;
	text: Msg;
	options: Msg[];
	correctIndex: number;
};

function question(id: string, text: Msg, options: Msg[], correctIndex: number): QuizBankEntry {
	return { id, text, options, correctIndex };
}

/**
 * Банк вопросов квиза допуска к develop-каналу. Тексты — функции сообщений, чтобы
 * квиз следовал языку интерфейса; ответы проверяются по индексу, никогда по тексту.
 */
const QUIZ_BANK: QuizBankEntry[] = [
	question(
		'tun-interface',
		m.develop_gate_q_tun_interface_text,
		[m.develop_gate_q_tun_interface_o1, m.develop_gate_q_tun_interface_o2, m.develop_gate_q_tun_interface_o3, m.develop_gate_q_tun_interface_o4],
		0,
	),
	question(
		'wg-handshake',
		m.develop_gate_q_wg_handshake_text,
		[m.develop_gate_q_wg_handshake_o1, m.develop_gate_q_wg_handshake_o2, m.develop_gate_q_wg_handshake_o3, m.develop_gate_q_wg_handshake_o4],
		0,
	),
	question(
		'allowed-ips-full-tunnel',
		m.develop_gate_q_allowed_ips_full_tunnel_text,
		[m.develop_gate_q_allowed_ips_full_tunnel_o1, m.develop_gate_q_allowed_ips_full_tunnel_o2, m.develop_gate_q_allowed_ips_full_tunnel_o3, m.develop_gate_q_allowed_ips_full_tunnel_o4, m.develop_gate_q_allowed_ips_full_tunnel_o5],
		0,
	),
	question(
		'cidr-24',
		m.develop_gate_q_cidr_24_text,
		[m.develop_gate_q_cidr_24_o1, m.develop_gate_q_cidr_24_o2, m.develop_gate_q_cidr_24_o3, m.develop_gate_q_cidr_24_o4, m.develop_gate_q_cidr_24_o5],
		0,
	),
	question(
		'dns-leak',
		m.develop_gate_q_dns_leak_text,
		[m.develop_gate_q_dns_leak_o1, m.develop_gate_q_dns_leak_o2, m.develop_gate_q_dns_leak_o3, m.develop_gate_q_dns_leak_o4, m.develop_gate_q_dns_leak_o5],
		0,
	),
	question(
		'udp-vs-tcp',
		m.develop_gate_q_udp_vs_tcp_text,
		[m.develop_gate_q_udp_vs_tcp_o1, m.develop_gate_q_udp_vs_tcp_o2, m.develop_gate_q_udp_vs_tcp_o3, m.develop_gate_q_udp_vs_tcp_o4],
		0,
	),
	question(
		'mtu-tunnel',
		m.develop_gate_q_mtu_tunnel_text,
		[m.develop_gate_q_mtu_tunnel_o1, m.develop_gate_q_mtu_tunnel_o2, m.develop_gate_q_mtu_tunnel_o3, m.develop_gate_q_mtu_tunnel_o4],
		0,
	),
	question(
		'persistent-keepalive',
		m.develop_gate_q_persistent_keepalive_text,
		[m.develop_gate_q_persistent_keepalive_o1, m.develop_gate_q_persistent_keepalive_o2, m.develop_gate_q_persistent_keepalive_o3, m.develop_gate_q_persistent_keepalive_o4],
		0,
	),
	question(
		'split-tunnel',
		m.develop_gate_q_split_tunnel_text,
		[m.develop_gate_q_split_tunnel_o1, m.develop_gate_q_split_tunnel_o2, m.develop_gate_q_split_tunnel_o3, m.develop_gate_q_split_tunnel_o4],
		0,
	),
	question(
		'wg-peer',
		m.develop_gate_q_wg_peer_text,
		[m.develop_gate_q_wg_peer_o1, m.develop_gate_q_wg_peer_o2, m.develop_gate_q_wg_peer_o3, m.develop_gate_q_wg_peer_o4],
		0,
	),
	question(
		'keepalive-behind-nat',
		m.develop_gate_q_keepalive_behind_nat_text,
		[m.develop_gate_q_keepalive_behind_nat_o1, m.develop_gate_q_keepalive_behind_nat_o2, m.develop_gate_q_keepalive_behind_nat_o3, m.develop_gate_q_keepalive_behind_nat_o4, m.develop_gate_q_keepalive_behind_nat_o5],
		0,
	),
	question(
		'kill-switch',
		m.develop_gate_q_kill_switch_text,
		[m.develop_gate_q_kill_switch_o1, m.develop_gate_q_kill_switch_o2, m.develop_gate_q_kill_switch_o3, m.develop_gate_q_kill_switch_o4, m.develop_gate_q_kill_switch_o5],
		0,
	),
	question(
		'public-key',
		m.develop_gate_q_public_key_text,
		[m.develop_gate_q_public_key_o1, m.develop_gate_q_public_key_o2, m.develop_gate_q_public_key_o3, m.develop_gate_q_public_key_o4, m.develop_gate_q_public_key_o5],
		0,
	),
	question(
		'endpoint',
		m.develop_gate_q_endpoint_text,
		[m.develop_gate_q_endpoint_o1, m.develop_gate_q_endpoint_o2, m.develop_gate_q_endpoint_o3, m.develop_gate_q_endpoint_o4, m.develop_gate_q_endpoint_o5],
		0,
	),
	question(
		'preshared-key',
		m.develop_gate_q_preshared_key_text,
		[m.develop_gate_q_preshared_key_o1, m.develop_gate_q_preshared_key_o2, m.develop_gate_q_preshared_key_o3, m.develop_gate_q_preshared_key_o4, m.develop_gate_q_preshared_key_o5],
		0,
	),
	question(
		'handshake-rtt',
		m.develop_gate_q_handshake_rtt_text,
		[m.develop_gate_q_handshake_rtt_o1, m.develop_gate_q_handshake_rtt_o2, m.develop_gate_q_handshake_rtt_o3, m.develop_gate_q_handshake_rtt_o4, m.develop_gate_q_handshake_rtt_o5],
		0,
	),
	question(
		'awg-obfuscation',
		m.develop_gate_q_awg_obfuscation_text,
		[m.develop_gate_q_awg_obfuscation_o1, m.develop_gate_q_awg_obfuscation_o2, m.develop_gate_q_awg_obfuscation_o3, m.develop_gate_q_awg_obfuscation_o4],
		0,
	),
	question(
		'issue-attachments',
		m.develop_gate_q_issue_attachments_text,
		[m.develop_gate_q_issue_attachments_o1, m.develop_gate_q_issue_attachments_o2, m.develop_gate_q_issue_attachments_o3, m.develop_gate_q_issue_attachments_o4],
		0,
	),
	question(
		'bad-situation-report',
		m.develop_gate_q_bad_situation_report_text,
		[m.develop_gate_q_bad_situation_report_o1, m.develop_gate_q_bad_situation_report_o2, m.develop_gate_q_bad_situation_report_o3, m.develop_gate_q_bad_situation_report_o4],
		0,
	),
	question(
		'devtools-network',
		m.develop_gate_q_devtools_network_text,
		[m.develop_gate_q_devtools_network_o1, m.develop_gate_q_devtools_network_o2, m.develop_gate_q_devtools_network_o3, m.develop_gate_q_devtools_network_o4],
		0,
	),
	question(
		'issue-channel',
		m.develop_gate_q_issue_channel_text,
		[m.develop_gate_q_issue_channel_o1, m.develop_gate_q_issue_channel_o2, m.develop_gate_q_issue_channel_o3, m.develop_gate_q_issue_channel_o4],
		0,
	),
	question(
		'issue-minimum',
		m.develop_gate_q_issue_minimum_text,
		[m.develop_gate_q_issue_minimum_o1, m.develop_gate_q_issue_minimum_o2, m.develop_gate_q_issue_minimum_o3, m.develop_gate_q_issue_minimum_o4],
		0,
	),
	question(
		'awgm-usage-level-advanced',
		m.develop_gate_q_awgm_usage_level_advanced_text,
		[m.develop_gate_q_awgm_usage_level_advanced_o1, m.develop_gate_q_awgm_usage_level_advanced_o2, m.develop_gate_q_awgm_usage_level_advanced_o3, m.develop_gate_q_awgm_usage_level_advanced_o4],
		0,
	),
	question(
		'awgm-download-route',
		m.develop_gate_q_awgm_download_route_text,
		[m.develop_gate_q_awgm_download_route_o1, m.develop_gate_q_awgm_download_route_o2, m.develop_gate_q_awgm_download_route_o3, m.develop_gate_q_awgm_download_route_o4],
		0,
	),
	question(
		'awgm-tunnels-home',
		m.develop_gate_q_awgm_tunnels_home_text,
		[m.develop_gate_q_awgm_tunnels_home_o1, m.develop_gate_q_awgm_tunnels_home_o2, m.develop_gate_q_awgm_tunnels_home_o3, m.develop_gate_q_awgm_tunnels_home_o4, m.develop_gate_q_awgm_tunnels_home_o5],
		0,
	),
	question(
		'awgm-ndms-proxy',
		m.develop_gate_q_awgm_ndms_proxy_text,
		[m.develop_gate_q_awgm_ndms_proxy_o1, m.develop_gate_q_awgm_ndms_proxy_o2, m.develop_gate_q_awgm_ndms_proxy_o3, m.develop_gate_q_awgm_ndms_proxy_o4, m.develop_gate_q_awgm_ndms_proxy_o5],
		0,
	),
	question(
		'awgm-monitoring-pingcheck',
		m.develop_gate_q_awgm_monitoring_pingcheck_text,
		[m.develop_gate_q_awgm_monitoring_pingcheck_o1, m.develop_gate_q_awgm_monitoring_pingcheck_o2, m.develop_gate_q_awgm_monitoring_pingcheck_o3, m.develop_gate_q_awgm_monitoring_pingcheck_o4],
		0,
	),
	question(
		'awgm-ndms-dns-ipset',
		m.develop_gate_q_awgm_ndms_dns_ipset_text,
		[m.develop_gate_q_awgm_ndms_dns_ipset_o1, m.develop_gate_q_awgm_ndms_dns_ipset_o2, m.develop_gate_q_awgm_ndms_dns_ipset_o3, m.develop_gate_q_awgm_ndms_dns_ipset_o4],
		0,
	),
	question(
		'awgm-restart-init',
		m.develop_gate_q_awgm_restart_init_text,
		[m.develop_gate_q_awgm_restart_init_o1, m.develop_gate_q_awgm_restart_init_o2, m.develop_gate_q_awgm_restart_init_o3, m.develop_gate_q_awgm_restart_init_o4],
		0,
	),
];

/** Полный банк вопросов на текущем языке интерфейса. */
export function getDevelopChannelQuizQuestions(): DevelopQuizQuestion[] {
	return QUIZ_BANK.map((entry) => ({
		id: entry.id,
		text: entry.text(),
		options: entry.options.map((option) => option()),
		correctIndex: entry.correctIndex,
	}));
}

function shuffleInPlace<T>(items: T[]): T[] {
	for (let i = items.length - 1; i > 0; i--) {
		const j = Math.floor(Math.random() * (i + 1));
		[items[i], items[j]] = [items[j], items[i]];
	}
	return items;
}

export function pickDevelopQuizQuestions(
	count = DEVELOP_CHANNEL_QUIZ_SIZE,
	pool: DevelopQuizQuestion[] = getDevelopChannelQuizQuestions(),
): DevelopQuizQuestion[] {
	const copy = [...pool];
	shuffleInPlace(copy);
	return copy.slice(0, Math.min(count, copy.length));
}

/** Shuffles answer options and updates {@link DevelopQuizQuestion.correctIndex}. */
export function shuffleQuestionOptions(question: DevelopQuizQuestion): DevelopQuizQuestion {
	const indexed = question.options.map((text, index) => ({ text, index }));
	shuffleInPlace(indexed);
	return {
		...question,
		options: indexed.map((entry) => entry.text),
		correctIndex: indexed.findIndex((entry) => entry.index === question.correctIndex),
	};
}

/**
 * Picks a random question set and shuffles both question order and options.
 * Call once per quiz start so each attempt gets a fresh layout.
 */
export function prepareDevelopQuizSession(
	count = DEVELOP_CHANNEL_QUIZ_SIZE,
	pool: DevelopQuizQuestion[] = getDevelopChannelQuizQuestions(),
): DevelopQuizQuestion[] {
	const picked = pickDevelopQuizQuestions(count, pool);
	shuffleInPlace(picked);
	return picked.map(shuffleQuestionOptions);
}

export function scoreDevelopQuiz(
	questions: DevelopQuizQuestion[],
	answers: Record<string, number | undefined>,
	cheatCaughtByQuestionId: Record<string, string> = {},
): number {
	let correct = 0;
	for (const q of questions) {
		if (cheatCaughtByQuestionId[q.id]) continue;
		if (answers[q.id] === q.correctIndex) correct++;
	}
	return correct;
}

export function isDevelopQuizPassed(
	correctCount: number,
	total = DEVELOP_CHANNEL_QUIZ_SIZE,
	maxWrong = DEVELOP_CHANNEL_QUIZ_MAX_WRONG,
): boolean {
	return correctCount >= total - maxWrong;
}

export function readDevelopChannelLockoutUntil(): number | null {
	if (typeof localStorage === 'undefined') return null;
	const raw = localStorage.getItem(DEVELOP_CHANNEL_LOCKOUT_KEY);
	if (!raw) return null;
	const until = Number(raw);
	return Number.isFinite(until) && until > 0 ? until : null;
}

export function getDevelopChannelLockoutRemainingMs(now = Date.now()): number {
	const until = readDevelopChannelLockoutUntil();
	if (!until) return 0;
	const remaining = until - now;
	if (remaining <= 0) {
		clearDevelopChannelLockout();
		return 0;
	}
	return remaining;
}

export function resolveDevelopChannelLockoutMs(
	mockDevMode = false,
): number {
	return mockDevMode ? DEVELOP_CHANNEL_LOCKOUT_MOCK_MS : DEVELOP_CHANNEL_LOCKOUT_MS;
}

export function formatDevelopChannelLockoutDurationLabel(mockDevMode = false): string {
	return mockDevMode ? m.develop_gate_duration_seconds() : m.develop_gate_duration_minutes();
}

export function setDevelopChannelLockout(
	durationMs = DEVELOP_CHANNEL_LOCKOUT_MS,
	now = Date.now(),
): void {
	if (typeof localStorage === 'undefined') return;
	localStorage.setItem(DEVELOP_CHANNEL_LOCKOUT_KEY, String(now + durationMs));
}

export function clearDevelopChannelLockout(): void {
	if (typeof localStorage === 'undefined') return;
	localStorage.removeItem(DEVELOP_CHANNEL_LOCKOUT_KEY);
}

export function hasDevelopChannelQuizPassed(): boolean {
	if (typeof localStorage === 'undefined') return false;
	try {
		return localStorage.getItem(DEVELOP_CHANNEL_QUIZ_PASSED_KEY) === 'true';
	} catch {
		return false;
	}
}

export function markDevelopChannelQuizPassed(): void {
	if (typeof localStorage === 'undefined') return;
	try {
		localStorage.setItem(DEVELOP_CHANNEL_QUIZ_PASSED_KEY, 'true');
	} catch {
		/* ignore quota / private mode */
	}
}

export function clearDevelopChannelQuizPassed(): void {
	if (typeof localStorage === 'undefined') return;
	try {
		localStorage.removeItem(DEVELOP_CHANNEL_QUIZ_PASSED_KEY);
	} catch {
		/* ignore private mode */
	}
}

export function formatQuizQuestionCountdown(remainingMs: number): string {
	const totalSec = Math.max(0, Math.ceil(remainingMs / 1000));
	const min = Math.floor(totalSec / 60);
	const s = totalSec % 60;
	return `${min}:${String(s).padStart(2, '0')}`;
}

export function formatLockoutCountdown(remainingMs: number): string {
	const totalSec = Math.max(0, Math.ceil(remainingMs / 1000));
	const h = Math.floor(totalSec / 3600);
	const min = Math.floor((totalSec % 3600) / 60);
	const s = totalSec % 60;
	if (h > 0) {
		return `${h}:${String(min).padStart(2, '0')}:${String(s).padStart(2, '0')}`;
	}
	return `${min}:${String(s).padStart(2, '0')}`;
}
