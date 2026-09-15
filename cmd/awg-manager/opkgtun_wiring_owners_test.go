package main

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/hoaxisr/awg-manager/internal/opkgtun"
	"github.com/hoaxisr/awg-manager/internal/storage"
)

// F16: номер, который держит запись прокси-инстанса без живого интерфейса и без
// записи NDMS, обязан считаться занятым И у выдающих номера туннелей, И у
// роутерных режимов — на mips/mipsel пулы всех троих пересекаются.

// noNDMSPins — «записей NDMS нет»: половина занятости, зависящая от роутера.
func noNDMSPins(context.Context) (map[int]string, error) { return nil, nil }

func TestOwnersAll_SeesProxyRecordPin(t *testing.T) {
	e := newOccEnv(t)
	e.putRecord(t, rawClientRecord("de", "OpkgTun12", "opkgtun12"))

	// Проверяется исходом выдачи: номер, который держит запись прокси, не
	// достаётся ни пину, ни перебору.
	got, err := reserveAll(e.pool(t, nil, noNDMSPins),
		[]proxyReq{{key: "wdtt-client:другой", pinned: 12, havePin: true}})
	if err != nil {
		t.Fatalf("выдача: %v", err)
	}
	if got[0] == 12 {
		t.Error("выдан номер 12, который держит запись другого прокси-инстанса")
	}
}

// Состав — контракт: выпавший поставщик не ломает ни сборку, ни прогон, он
// просто отдаёт чужой занятый номер как свободный. Инвентарь ловит и выпадение,
// и молча добавленного лишнего.
func TestOwnersInventory(t *testing.T) {
	e := newOccEnv(t)
	owners := e.owners(nil, noNDMSPins)

	names := func(src []opkgtun.Source) []string {
		out := make([]string, 0, len(src))
		for _, s := range src {
			out = append(out, s.Name)
		}
		return out
	}
	wantAll := []string{"записи туннелей", "запись режима роутера", "записи прокси",
		"записи NDMS", "живые интерфейсы"}
	if got := names(owners.all()); !equalStrings(got, wantAll) {
		t.Errorf("состав all() = %v, want %v", got, wantAll)
	}
}

// Ключ владельца у ПОСТАВЩИКА занятости и у ПРОСИТЕЛЯ обязан вычисляться одной
// функцией: разойдясь, владелец не узнает собственный пин и уедет с номера,
// оборвав permit'ы пользователя. Здесь проверяется, что поставщик прокси
// ключует записи ровно тем, чем их ключует проситель.
func TestProxyHoldersKeyMatchesRequester(t *testing.T) {
	e := newOccEnv(t)
	e.putRecord(t, rawClientRecord("cl", "OpkgTun12", "opkgtun12"))
	e.putRecord(t, serverRecord("sv", "OpkgTun13", "opkgtun13", "OpkgTun14", "opkgtun14"))

	got := readHolders(t, opkgtun.Source{Name: "прокси", Read: proxyHolders(e.store)})

	// Проситель строит ключ теми же конструкторами (ensurePins, посев).
	want := map[int]opkgtun.Holder{
		12: opkgtun.ProxyHolder("wdtt-client:cl", "", ""),
		13: opkgtun.ProxyHolder("wdtt-server:sv", "wg", ""),
		14: opkgtun.ProxyHolder("wdtt-server:sv", "raw", ""),
	}
	for idx, w := range want {
		h, busy := got[idx]
		if !busy {
			t.Fatalf("номер %d свободен, ждали держателя", idx)
		}
		if !sameOwnerForTest(h, w) {
			t.Errorf("держатель %d = %q, ключ не совпал с тем, что построит проситель", idx, h)
		}
	}
}

// Ключ режима роутера ОДИН на оба режима: на смене режима новый обязан узнать
// номер старого своим, иначе handover уедет на другой номер и оборвёт permit'ы.
func TestRouterModeKeyIsSharedBetweenModes(t *testing.T) {
	if !sameOwnerForTest(opkgtun.RouterModeHolder("fakeip-tun"),
		opkgtun.RouterModeHolder("policy-tun")) {
		t.Fatal("ключи режимов роутера разошлись: handover уедет на другой номер")
	}
}

// relation — как пул ВИДИТ держателя относительно просителя. Три состояния, и
// различаются они наблюдаемым поведением, а не чтением полей: поля Holder
// закрыты намеренно.
type relation string

const (
	relSelf    relation = "свой"   // строгая претензия честится
	relAnon    relation = "аноним" // строгая отказывает, обычная перебивает
	relForeign relation = "чужой"  // отказывают обе
)

// holderRelationForTest кладёт держателя на номер и спрашивает у пула обе
// формы претензии. Обычная перебивает безключевого, строгая — нет, и эта
// разница и есть различитель.
func holderRelationForTest(h, self opkgtun.Holder) relation {
	granted := func(req opkgtun.Request) bool {
		pool := opkgtun.NewPool(16, opkgtun.Source{
			Name: "занятость",
			Read: func(context.Context) (opkgtun.Taken, error) { return opkgtun.Taken{5: h}, nil },
		})
		res, err := pool.Reserve(context.Background(), req)
		if err != nil {
			return false
		}
		defer res.Close()
		return res.Numbers()[0] == 5
	}
	switch {
	case granted(opkgtun.WantPinnedStrict(self, 5)):
		return relSelf
	case granted(opkgtun.WantPinned(self, 5)):
		return relAnon
	default:
		return relForeign
	}
}

// sameOwnerForTest — «тот же владелец» через наблюдаемое поведение пула.
//
// Претензия СТРОГАЯ, и это несущее: обычная перебивает безключевого держателя,
// то есть вернула бы «тот же владелец» на ЛЮБОМ безключевом a. Поставщик,
// потерявший ключ владельца, проходил бы тогда молча — ровно тот класс, против
// которого стражи ключей и заведены.
func sameOwnerForTest(a, b opkgtun.Holder) bool {
	return holderRelationForTest(a, b) == relSelf
}

func equalStrings(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}

// Нулевой номер удерживающей записи — законная занятость: 0 и 1 достаются
// только режимам роутера, но инстанс мог занять ноль на прошлых версиях, и
// выдать его повторно значит порвать permit'ы политики.
func TestRouterModeHoldAtZeroIsBusy(t *testing.T) {
	e := newOccEnv(t)
	if err := e.settings.SetOpkgTunState(&storage.OpkgTunState{
		Mode: storage.OpkgTunModePolicyTun, Index: 0}); err != nil {
		t.Fatal(err)
	}

	got := readHolders(t, opkgtun.Source{
		Name: "запись режима роутера", Read: routerModeHolders(e.settings)})
	if _, busy := got[0]; !busy {
		t.Fatalf("занятость = %v, нулевой номер обязан считаться занятым", got)
	}
}

// Пул собирается ПОЛНЫМ составом: подмена его на любой урезанный компилируется
// и проходит все прочие тесты, а kernel-туннель начинает отбирать номер у
// режима роутера.
func TestPoolIsBuiltFromFullOwnerSet(t *testing.T) {
	e := newOccEnv(t)
	if err := e.settings.SetOpkgTunState(&storage.OpkgTunState{
		Mode: storage.OpkgTunModePolicyTun, Index: 10}); err != nil {
		t.Fatal(err)
	}
	e.putRecord(t, rawClientRecord("de", "OpkgTun11", "opkgtun11"))
	if err := e.awg.Create(&storage.AWGTunnel{ID: "awg12", Name: "vpn"}); err != nil {
		t.Fatal(err)
	}

	// Проситель не из режимов роутера: окно 10..16, первые три номера держат
	// три РАЗНЫХ поставщика. Выпади любой — выдача сядет на его номер.
	got, err := reserveAll(e.pool(t, nil, noNDMSPins),
		[]proxyReq{{key: "wdtt-client:новый"}})
	if err != nil {
		t.Fatal(err)
	}
	if got[0] != 13 {
		t.Fatalf("выдан номер %d, ждали 13: из состава выпал поставщик", got[0])
	}
}

// Строгий пин обязан отказывать на БОЕВОМ составе, а не только на голом пуле.
//
// Композиция важнее примитива: собственная запись режима роутера лежит в
// занятости под тем же ключом, что и проситель, и при слиянии заявленный бьёт
// анонима — чужая запись NDMS на том же номере из занятости исчезает. Пул,
// собранный без поставщика записи режима, этого не показывает: там номер
// держит один аноним, и строгий пин честно отказывает. Боевой состав содержит
// обоих ВСЕГДА.
func TestStrictPinSparesForeignNDMSRecordOnFullOwnerSet(t *testing.T) {
	e := newOccEnv(t)
	if err := e.settings.SetOpkgTunState(&storage.OpkgTunState{
		Mode: storage.OpkgTunModeFakeIP, Index: 5}); err != nil {
		t.Fatal(err)
	}
	foreign := func(context.Context) (map[int]string, error) {
		return map[int]string{5: "запись NDMS OpkgTun5 («чужой»)"}, nil
	}

	self := opkgtun.RouterModeHolder(storage.OpkgTunModeFakeIP)
	res, err := e.pool(t, nil, foreign).Reserve(context.Background(),
		opkgtun.WantPinnedStrict(self, 5))
	if err != nil {
		t.Fatalf("выдача: %v", err)
	}
	defer res.Close()
	if got := res.Numbers()[0]; got == 5 {
		t.Fatal("строгий пин отдал номер, на котором стоит чужая запись NDMS: " +
			"интерфейс создался бы поверх неё (RCI создаёт upsert-ом)")
	}
}

// Отказ чтения настроек — «не знаем», а не «номер свободен».
//
// Единственное направление ошибки, приводящее к коллизии, — недосчёт занятых,
// и контракт Source прямо запрещает молчать о нём. Повреждённый settings.json
// иначе освобождал бы номер режима роутера, и его забирал бы kernel-туннель.
func TestRouterModeHoldersFailClosedOnUnreadableSettings(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "settings.json"), []byte("{нет"), 0o600); err != nil {
		t.Fatal(err)
	}
	// Кэш НЕ прогрет: Load() здесь не зовётся, поэтому Get() пойдёт на диск.
	src := opkgtun.Source{Name: "запись режима роутера",
		Read: routerModeHolders(storage.NewSettingsStore(dir))}

	got, err := src.Read(context.Background())
	if err == nil {
		t.Fatalf("нечитаемые настройки отданы как «занятых нет»: %v", got)
	}
}

// Ключевость держателя — НЕСУЩЕЕ решение КАЖДОГО поставщика, и ошибиться в ней
// можно в обе стороны. Поставщик, потерявший ключ, отдаёт свой номер чужой
// обычной претензии и не узнаёт собственную строгую. Поставщик, ключ
// приписавший, наоборот: заявленного пул обычной претензией не перебивает, а
// двух разных заявленных на одном номере считает спором и не отдаёт номер
// никому.
//
// Поэтому проверяются все пятеро, а не только тот, где ошибка уже случалась.
func TestOwnerKeyIsPinnedForEverySource(t *testing.T) {
	adapter := newAdapterWith(t, ifaceListJSON, func() ([]int, error) { return []int{5}, nil })

	cases := []struct {
		name  string
		setup func(e *occEnv)
		src   func(e *occEnv) opkgtun.Source
		idx   int
		self  opkgtun.Holder
		want  relation
		why   string
	}{
		{
			name:  "записи туннелей",
			setup: func(e *occEnv) { mustCreateTunnel(t, e, "awg5", "дом") },
			src:   func(e *occEnv) opkgtun.Source { return opkgtun.Source{Name: "t", Read: tunnelHolders(e.awg)} },
			idx:   5,
			self:  opkgtun.TunnelHolder("awg5", "дом"),
			want:  relSelf,
			why:   "потеряв ключ, запись туннеля отдаёт свой номер обычной претензии прокси",
		},
		{
			name: "запись режима роутера",
			setup: func(e *occEnv) {
				if err := e.settings.SetOpkgTunState(&storage.OpkgTunState{
					Mode: storage.OpkgTunModeFakeIP, Index: 5}); err != nil {
					t.Fatal(err)
				}
			},
			src:  func(e *occEnv) opkgtun.Source { return opkgtun.Source{Name: "s", Read: routerModeHolders(e.settings)} },
			idx:  5,
			self: opkgtun.RouterModeHolder(storage.OpkgTunModeFakeIP),
			want: relSelf,
			why:  "потеряв ключ, режим роутера не узнаёт СВОЮ удерживающую запись и уезжает с номера, обрывая permit'ы",
		},
		{
			name:  "записи прокси",
			setup: func(e *occEnv) { e.putRecord(t, rawClientRecord("cl", "OpkgTun5", "opkgtun5")) },
			src:   func(e *occEnv) opkgtun.Source { return opkgtun.Source{Name: "p", Read: proxyHolders(e.store)} },
			idx:   5,
			self:  opkgtun.ProxyHolder("wdtt-client:cl", "", ""),
			want:  relSelf,
			why:   "потеряв ключ, инстанс уезжает со своего номера",
		},
		{
			name:  "записи NDMS",
			setup: func(*occEnv) {},
			src:   func(*occEnv) opkgtun.Source { return opkgtun.Source{Name: "n", Read: ndmsHolders(adapter)} },
			idx:   10,
			self:  opkgtun.RouterModeHolder(storage.OpkgTunModeFakeIP),
			want:  relAnon,
			why:   "запись NDMS — след владельца, а не владелец: приписав ей ключ, мы запретим режиму роутера поднять СВОЙ прежний номер",
		},
		{
			name:  "живые интерфейсы",
			setup: func(*occEnv) {},
			src:   func(*occEnv) opkgtun.Source { return opkgtun.Source{Name: "l", Read: liveHolders(adapter)} },
			idx:   5,
			self:  opkgtun.RouterModeHolder(storage.OpkgTunModeFakeIP),
			want:  relAnon,
			why:   "живое устройство знает только номер: приписав ему ключ, мы сделаем собственный интерфейс чужим",
		},
	}

	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			e := newOccEnv(t)
			c.setup(e)
			got := readHolders(t, c.src(e))
			h, busy := got[c.idx]
			if !busy {
				t.Fatalf("номер %d свободен, ждали держателя: %v", c.idx, got)
			}
			if rel := holderRelationForTest(h, c.self); rel != c.want {
				t.Errorf("держатель %q виден пулу как %q, ждали %q — %s", h, rel, c.want, c.why)
			}
		})
	}
}

func mustCreateTunnel(t *testing.T, e *occEnv, id, name string) {
	t.Helper()
	if err := e.awg.Create(&storage.AWGTunnel{ID: id, Name: name}); err != nil {
		t.Fatal(err)
	}
}
