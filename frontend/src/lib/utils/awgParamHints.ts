import { m } from '$lib/i18n';

// Подсказки к параметрам AWG. Функция, а не константа: текст берётся из
// словаря при вызове, поэтому смена языка подхватывается без перезагрузки.
export function awgParamHints(): Record<string, string> {
	return {
		jc: m.awg_param_hint_jc(),
		jmin: m.awg_param_hint_jmin(),
		jmax: m.awg_param_hint_jmax(),
		s1: m.awg_param_hint_s1(),
		s2: m.awg_param_hint_s2(),
		s3: m.awg_param_hint_s3(),
		s4: m.awg_param_hint_s4(),
		h1: m.awg_param_hint_h1(),
		h2: m.awg_param_hint_h2(),
		h3: m.awg_param_hint_h3(),
		h4: m.awg_param_hint_h4(),
		i1: m.awg_param_hint_i1(),
		i2: m.awg_param_hint_i2(),
		i3: m.awg_param_hint_i3(),
		i4: m.awg_param_hint_i4(),
		i5: m.awg_param_hint_i5(),
	};
}
