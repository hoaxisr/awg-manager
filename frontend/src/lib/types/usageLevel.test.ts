import { describe, it, expect } from 'vitest';
import { isRoutingSubTabVisible } from './usageLevel';

describe('isRoutingSubTabVisible', () => {
	// Политики доступа — базовая функция роутера, а не экспертная настройка:
	// вкладка видна на всех уровнях.
	it('shows accessPolicies on the basic level', () => {
		expect(isRoutingSubTabVisible('basic', 'accessPolicies')).toBe(true);
	});
});
