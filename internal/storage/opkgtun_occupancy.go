package storage

import (
	"context"
	"fmt"
)

// OpkgTunIndexLister — живая половина занятости: номера OpkgTun, существующие
// прямо сейчас. Контракт объявлен здесь по consumer-owned паттерну (такая же
// копия живёт в singbox/router и wdtt); прод-реализация — адаптер в
// cmd/awg-manager поверх /sys и списка интерфейсов NDMS.
type OpkgTunIndexLister interface {
	LiveOpkgTunIndices(ctx context.Context) (map[int]bool, error)
}

// OpkgTunPins — пины ОДНОГО владельца: номера, которые он держит, даже если
// живого интерфейса нет. Таких владельцев трое: записи туннелей (номер занят
// с момента создания записи, а интерфейс появляется только при первом
// включении), удерживающая запись настроек (индекс держится ради permit'а
// пользователя) и записи инстансов прокси.
//
// Ошибка означает «не смогли посмотреть» и обязана дойти до вызывающего:
// пустая карта неотличима от «всё свободно».
type OpkgTunPins func(ctx context.Context) (map[int]bool, error)

// OpkgTunOccupancy собирает занятость: живая половина плюс пины всех
// владельцев. Fail-closed — сбой любого источника даёт отказ, а не неполную
// картину: недосчёт занятых номеров это единственное направление ошибки,
// приводящее к коллизии.
//
// Вычитание СОБСТВЕННЫХ пинов — на стороне вызывающего: только он знает, что
// считать своим. Без этого владелец, переиспользующий свой удержанный номер,
// получил бы другой — и permit'ы пользователя повисли бы.
func OpkgTunOccupancy(live OpkgTunIndexLister, pins ...OpkgTunPins) OpkgTunPins {
	return func(ctx context.Context) (map[int]bool, error) {
		occupied, err := live.LiveOpkgTunIndices(ctx)
		if err != nil {
			return nil, fmt.Errorf("живая занятость OpkgTun: %w", err)
		}
		out := make(map[int]bool, len(occupied))
		for idx := range occupied {
			out[idx] = true
		}
		for _, pin := range pins {
			held, err := pin(ctx)
			if err != nil {
				return nil, fmt.Errorf("пины OpkgTun: %w", err)
			}
			for idx := range held {
				out[idx] = true
			}
		}
		return out, nil
	}
}

// MergeOpkgTunPins склеивает нескольких поставщиков в одного. Fail-closed, как
// и OpkgTunOccupancy: ошибка любого — отказ, а не частичная карта.
func MergeOpkgTunPins(pins ...OpkgTunPins) OpkgTunPins {
	return func(ctx context.Context) (map[int]bool, error) {
		out := map[int]bool{}
		for _, pin := range pins {
			if pin == nil {
				continue
			}
			held, err := pin(ctx)
			if err != nil {
				return nil, err
			}
			for idx := range held {
				out[idx] = true
			}
		}
		return out, nil
	}
}

// OpkgTunPinsOf — поставщик пинов по записям туннелей. Перечисление строгое:
// прощающее унесло бы битую запись в карантин и молча освободило её номер.
func (s *AWGTunnelStore) OpkgTunPinsOf(context.Context) (map[int]bool, error) {
	tunnels, err := s.ListStrict()
	if err != nil {
		return nil, err
	}
	return OpkgTunIndicesOf(tunnels), nil
}

// OpkgTunPinsOf — поставщик пинов по удерживающей записи настроек.
func (s *SettingsStore) OpkgTunPinsOf(context.Context) (map[int]bool, error) {
	settings, err := s.Load()
	if err != nil {
		return nil, err
	}
	return opkgTunPinsOf(settings.OpkgTun), nil
}

// opkgTunPinsOf — чистое ядро: запись владения занимает свой номер, пока она
// существует. Гейт по наличию записи, а НЕ по Provisioned: удержание это как
// раз Provisioned=false при непустой записи. Нулевой номер валиден — это
// начало диапазона режимов роутера.
func opkgTunPinsOf(state *OpkgTunState) map[int]bool {
	if state == nil {
		return map[int]bool{}
	}
	return map[int]bool{state.Index: true}
}
