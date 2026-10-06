package command

import (
	"context"

	"github.com/hoaxisr/awg-manager/internal/ndms/query"
)

// deleteInterface — единственный путь `no interface` в проекте (П24): любая
// запись любого типа сносится здесь и только по Confirmed. Кредит своего
// ifdestroyed выдаётся ДО POST (ExpectRemoval: хук может опередить разбор
// ответа), исход ответа закрывает жетон: снято — Removed (карта забывает
// запись сразу, не дожидаясь хука, F546), «unable to find» — Absent (цель
// достигнута, ошибки нет), отказ — Refused (запись в карте остаётся: иначе
// её индекс заняли бы поверх живой). Сохранение и invalidators — на обоих
// путях, как у любой мутации (postChecked).
func (m *ndmsMutator) deleteInterface(ctx context.Context, iface query.Confirmed, opDesc string, invalidators ...func()) error {
	payload := map[string]any{"interface": map[string]any{iface.Name(): map[string]any{"no": true}}}
	tok := m.queries.Interfaces.ExpectRemoval(iface.Name())
	resp, err := postChecked(ctx, m.poster, payload, opDesc, isMissingInterface,
		append([]func(){m.save.Request}, invalidators...)...)
	switch {
	case err != nil:
		tok.Refused()
	case len(ndmsStatusErrors(resp)) > 0: // только терпимое «unable to find»
		tok.Absent()
	default:
		tok.Removed()
	}
	return err
}
