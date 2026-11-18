package router

import (
	"context"
	"time"

	"github.com/hoaxisr/awg-manager/internal/storage"
)

const (
	schedulerInitialDelay = 2 * time.Minute
	schedulerTick         = 30 * time.Second
	// schedulerNudgeDelay — пауза между Nudge и внеочередным проходом. Хуки
	// смены WAN идут пачкой (down → pending → running за секунды), а адрес и
	// default-маршрут встают чуть позже события: проход «в ту же миллисекунду»
	// собрал бы исключения WAN-адреса без нового адреса. Промах не страшен —
	// его догонит обычный тик.
	schedulerNudgeDelay = 3 * time.Second
)

type Scheduler struct {
	svc      *ServiceImpl
	settings *storage.SettingsStore
	stop     chan struct{}
	done     chan struct{}
	nudge    chan struct{}

	// Поля, а не константы, — ради теста цикла без ServiceImpl и без ожидания
	// боевых интервалов.
	tick                           func(context.Context)
	initialDelay, every, nudgeWait time.Duration
}

func NewScheduler(svc *ServiceImpl, settings *storage.SettingsStore) *Scheduler {
	s := &Scheduler{
		svc:          svc,
		settings:     settings,
		stop:         make(chan struct{}),
		done:         make(chan struct{}),
		nudge:        make(chan struct{}, 1),
		initialDelay: schedulerInitialDelay,
		every:        schedulerTick,
		nudgeWait:    schedulerNudgeDelay,
	}
	s.tick = s.tickPolicySync
	return s
}

// Nudge просит внеочередной проход (смена состояния WAN, #1215): правила
// перехвата несут адрес роутера на WAN, и до тика они устаревшие. Не
// блокирует; повторные вызовы до прохода сливаются в один.
func (s *Scheduler) Nudge() {
	select {
	case s.nudge <- struct{}{}:
	default:
	}
}

func (s *Scheduler) Start() { go s.run() }

func (s *Scheduler) Stop() {
	close(s.stop)
	<-s.done
}

func (s *Scheduler) run() {
	defer close(s.done)

	select {
	case <-time.After(s.initialDelay):
	case <-s.stop:
		return
	}

	for {
		ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
		s.tick(ctx)
		cancel()

		select {
		case <-time.After(s.every):
		case <-s.nudge:
			select {
			case <-time.After(s.nudgeWait):
			case <-s.stop:
				return
			}
			// Nudge'и, пришедшие за время паузы, покрывает этот же проход.
			select {
			case <-s.nudge:
			default:
			}
		case <-s.stop:
			return
		}
	}
}

func (s *Scheduler) tickPolicySync(ctx context.Context) {
	// Get, а не Load: Load читает файл с флеша и берёт ПИШУЩИЙ лок стора, и
	// так — дважды за тик (здесь и внутри Reconcile). Тик самолечения ходит
	// раз в 30 секунд круглосуточно; читать ради него файл незачем, кэш стора
	// обновляется на каждой записи настроек.
	//
	// Живой объект тут безопасен: читаем поле-структуру SingboxRouter, а
	// оговорка стора (settings.go: «Get возвращает ЖИВОЙ объект») — про
	// параллельное чтение его map-полей, которых мы не касаемся.
	settings, err := s.settings.Get()
	if err != nil {
		return
	}
	if isFircRoutingEngine(settings.SingboxRouter) {
		return
	}
	if !settings.SingboxRouter.Enabled {
		// Движок выключен — Reconcile не гоняем, но reap fakeip-сирот обязан
		// работать и здесь: runtime-сирота (провал delete при disable)
		// возникает именно в выключенном состоянии. В steady-state дёшево —
		// скан читает кэш InterfaceStore. При включённом движке reap делает
		// сам Reconcile.
		if err := s.svc.ReapOrphanedFakeIPTun(ctx); err != nil {
			s.svc.appLog.Warn("fakeip-reap", "", err.Error())
		}
		return
	}
	if err := s.svc.Reconcile(ctx); err != nil {
		s.svc.appLog.Warn("scheduler-policy-sync", "", err.Error())
	}
}
