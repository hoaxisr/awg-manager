import { describe, it, expect, beforeEach, vi } from 'vitest';
import {
	getDevelopChannelQuizQuestions,
	DEVELOP_CHANNEL_QUIZ_SIZE,
	clearDevelopChannelLockout,
	getDevelopChannelLockoutRemainingMs,
	isDevelopQuizPassed,
	pickDevelopQuizQuestions,
	pickCopyCheatOption,
	getDevelopChannelCopyCheatOptions,
	prepareDevelopQuizSession,
	scoreDevelopQuiz,
	setDevelopChannelLockout,
	shuffleQuestionOptions,
} from './developChannelGate';
import {
	DEVELOP_CHANNEL_QUIZ_PASSED_KEY,
	clearDevelopChannelQuizPassed,
	hasDevelopChannelQuizPassed,
	markDevelopChannelQuizPassed,
} from './developChannelGate';

function createLocalStorageMock() {
	const store = new Map<string, string>();
	return {
		getItem: (key: string) => store.get(key) ?? null,
		setItem: (key: string, value: string) => store.set(key, value),
		removeItem: (key: string) => store.delete(key),
		clear: () => store.clear(),
	};
}

describe('developChannelGate', () => {
	beforeEach(() => {
		vi.stubGlobal('localStorage', createLocalStorageMock());
		clearDevelopChannelLockout();
		clearDevelopChannelQuizPassed();
	});

	it('picks the requested number of unique questions', () => {
		const picked = pickDevelopQuizQuestions(DEVELOP_CHANNEL_QUIZ_SIZE);
		expect(picked).toHaveLength(DEVELOP_CHANNEL_QUIZ_SIZE);
		const ids = new Set(picked.map((q) => q.id));
		expect(ids.size).toBe(DEVELOP_CHANNEL_QUIZ_SIZE);
		for (const q of picked) {
			expect(getDevelopChannelQuizQuestions().some((bank) => bank.id === q.id)).toBe(true);
		}
	});

	it('scores answers and allows up to 2 wrong of 7', () => {
		const questions = pickDevelopQuizQuestions(7);
		const answers: Record<string, number> = {};
		for (let i = 0; i < questions.length; i++) {
			const wrongIndex = (questions[i].correctIndex + 1) % questions[i].options.length;
			answers[questions[i].id] = i < 5 ? questions[i].correctIndex : wrongIndex;
		}
		expect(scoreDevelopQuiz(questions, answers)).toBe(5);
		expect(isDevelopQuizPassed(5, 7, 2)).toBe(true);
		expect(isDevelopQuizPassed(4, 7, 2)).toBe(false);
	});

	it('shuffles options but keeps the correct answer scorable', () => {
		const base = getDevelopChannelQuizQuestions()[0];
		const shuffled = shuffleQuestionOptions(base);
		expect(shuffled.options).toHaveLength(base.options.length);
		expect(new Set(shuffled.options)).toEqual(new Set(base.options));
		const picked = shuffled.options[shuffled.correctIndex];
		expect(picked).toBe(base.options[base.correctIndex]);
	});

	it('prepareDevelopQuizSession reshuffles questions and answer options', () => {
		const orders = new Set(
			Array.from({ length: 24 }, () =>
				prepareDevelopQuizSession(7)
					.map((q) => q.id)
					.join(','),
			),
		);
		expect(orders.size).toBeGreaterThan(1);

		const sessions = Array.from({ length: 24 }, () => prepareDevelopQuizSession(7));
		const anyShiftedCorrectIndex = sessions.some((session) =>
			session.some((q) => {
				const bank = getDevelopChannelQuizQuestions().find((b) => b.id === q.id);
				return bank && q.correctIndex !== bank.correctIndex;
			}),
		);
		expect(anyShiftedCorrectIndex).toBe(true);
	});

	it('has no duplicate answer text within or across questions', () => {
		const seen = new Map<string, string>();
		for (const q of getDevelopChannelQuizQuestions()) {
			const local = new Set<string>();
			for (const option of q.options) {
				expect(local.has(option)).toBe(false);
				local.add(option);
				const otherId = seen.get(option);
				expect(otherId, `duplicate in ${q.id} and ${otherId}: ${option}`).toBeUndefined();
				seen.set(option, q.id);
			}
		}
	});

	it('pickCopyCheatOption returns a phrase from the pool', () => {
		const phrase = pickCopyCheatOption();
		expect(getDevelopChannelCopyCheatOptions()).toContain(phrase);
	});

	it('treats copy-cheat questions as always wrong', () => {
		const questions = pickDevelopQuizQuestions(3);
		const answers: Record<string, number> = {};
		for (const q of questions) {
			answers[q.id] = q.correctIndex;
		}
		expect(scoreDevelopQuiz(questions, answers)).toBe(3);
		expect(
			scoreDevelopQuiz(questions, answers, { [questions[0].id]: 'oops' }),
		).toBe(2);
	});

	it('stores and clears lockout', () => {
		const now = 1_000_000;
		setDevelopChannelLockout(60_000, now);
		expect(getDevelopChannelLockoutRemainingMs(now + 10_000)).toBe(50_000);
		expect(getDevelopChannelLockoutRemainingMs(now + 70_000)).toBe(0);
	});

	it('stores and clears passed quiz state', () => {
		expect(hasDevelopChannelQuizPassed()).toBe(false);
		markDevelopChannelQuizPassed();
		expect(hasDevelopChannelQuizPassed()).toBe(true);
		expect(localStorage.getItem(DEVELOP_CHANNEL_QUIZ_PASSED_KEY)).toBe('true');
		clearDevelopChannelQuizPassed();
		expect(hasDevelopChannelQuizPassed()).toBe(false);
	});
});
