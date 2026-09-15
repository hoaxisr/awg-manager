package router

import (
	"context"

	"github.com/hoaxisr/awg-manager/internal/opkgtun"
	"github.com/hoaxisr/awg-manager/internal/storage"
)

// reserveOpkgTun — номер для режима роутера и резервация, держащая его до
// записи владения.
//
// pin ≥ 0 — номер, на который проситель имеет право претендовать (opkgTunPin);
// отрицательный означает «любой свободный». Пин не гарантия: занятый чужой
// ЗАЯВЛЕННЫЙ номер пул не отдаст, и тогда режим уедет на другой — о чём
// вызывающий предупреждает пользователя, потому что permit'ы в политиках
// закреплены за именем.
//
// Собственная запись владения занятости больше не мешает: у обоих режимов и у
// поставщика один ключ владельца ("router-mode"), и пул отдаёт свой номер по
// совпадению ключа. Прежде это делалось вычитанием своей записи из состава
// поставщиков — приём, который ломался на handover: там номер принадлежит
// ДРУГОМУ режиму, и вычитать было нечего.
func (s *ServiceImpl) reserveOpkgTun(ctx context.Context, mode string, pin opkgTunPin) (int, *opkgtun.Reservation, error) {
	self := opkgtun.RouterModeHolder(mode)
	req := opkgtun.Want(self)
	switch {
	case pin.index >= 0 && pin.proven:
		req = opkgtun.WantPinned(self, pin.index)
	case pin.index >= 0:
		// Владение НЕ доказано: номер когда-то был записан за нами, но след на
		// нём мог остаться и чужой. Строгий пин не перебивает безключевого
		// держателя — чужую запись NDMS отбирать нельзя, создание интерфейса
		// поверх неё переписало бы чужие настройки.
		req = opkgtun.WantPinnedStrict(self, pin.index)
	}
	res, err := s.deps.OpkgTunPool.Reserve(ctx, req)
	if err != nil {
		return 0, nil, err
	}
	idx := res.Numbers()[0]
	if c := res.Conflicts(); len(c) > 0 {
		s.appLog.Warn(mode, tunIfaceName(idx), "спорные номера OpkgTun: "+c.String())
	}
	return idx, res, nil
}

// opkgTunPin — номер, на который режим вправе претендовать, и доказано ли
// владение следом на нём.
type opkgTunPin struct {
	index  int  // −1 — претензии нет
	proven bool // на номере наш интерфейс, доказано описанием
}

var noPin = opkgTunPin{index: -1}

// pinFor — номер, на который режим вправе претендовать.
//
// Ветка одна, и она про ДОКАЗАННОЕ владение своим прежним номером: запись наша
// и либо устройства на нём нет вовсе, либо оно наше по описанию. Молчаливый
// переезд рвёт permit'ы: пользователь закрепил их в политике за конкретным
// именем, а permit живёт ровно столько, сколько интерфейс, и пересозданием
// одноимённого не воскресает (стенд 2026-08-18).
//
// «Устройства нет» — не то же, что «номер свободен»: запись NDMS переживает
// удаление устройства, и она могла остаться от ЧУЖОГО интерфейса. Собственная
// запись настроек доказывает, что номер когда-то был наш, а не что след на нём
// наш, — поэтому такая претензия идёт СТРОГИМ пином, не перебивающим анонима.
// Доказанное описанием владение даёт обычный пин: там след точно наш.
//
// Транзиентный отказ скана при живом своём устройстве читается как «не знаем» и
// даёт −1: номер сменится. Это паритет с прежним поведением, и направление
// безопасное — чужой интерфейс не трогаем.
func (s *ServiceImpl) pinFor(ctx context.Context, prev *storage.OpkgTunState,
	live map[int]bool, description string,
) opkgTunPin {
	if prev == nil {
		return noPin
	}
	if s.ownsOpkgTun(ctx, tunNDMSName(prev.Index), description) {
		return opkgTunPin{index: prev.Index, proven: true}
	}
	if !live[prev.Index] {
		// Устройства нет, но запись NDMS его переживает, и она могла остаться
		// от ЧУЖОГО интерфейса. Претендуем строго.
		return opkgTunPin{index: prev.Index}
	}
	return noPin
}

// OpkgTunIndexLister перечисляет номера OpkgTun, устройства которых существуют
// в ядре ПРЯМО СЕЙЧАС. Записи NDMS сюда не входят — они приходят отдельным
// поставщиком занятости в пуле, потому что запись переживает удаление
// устройства.
//
// Живая половина нужна режимам роутера отдельно от занятости: та же карта
// отвечает ещё на два вопроса — «жив ли мой прежний интерфейс» и «доказуемо ли
// он чужой», — и подмешивание туда чужих пинов сломало бы оба.
//
// Узкий интерфейс намеренно объявлен в router, а не тянет конкретные типы
// internal/ndms: router декаплится от ndms через consumer-owned контракты (DIP),
// как и WANInterfaceLister/IngressResolver; здесь — только контракт.
type OpkgTunIndexLister interface {
	LiveOpkgTunIndices(ctx context.Context) (map[int]bool, error)
}
