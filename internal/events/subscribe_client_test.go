package events_test

import (
	"fmt"
	"strings"
	"testing"

	"github.com/hoaxisr/awg-manager/internal/events"
)

// ClientCount отвечает на вопрос «смотрит ли кто-нибудь прямо сейчас», и по нему
// фоновые опросчики решают, нужна ли их работа. Ответ верен ровно до тех пор, пока
// клиентскую подписку берёт ТОЛЬКО обработчик SSE: любой внутренний потребитель,
// взявший её, живёт всё время работы процесса и сделает счётчик вечно
// положительным — ровно тот дефект (F340), из-за которого гейт поллера метрик
// молча не работал.
//
// Это сторож того же рода, что TestResourceKeys_NoLiteralPublishers: компилятор
// такую ошибку не ловит, а тест ловит.
func TestSubscribeClient_OnlySSEHandler(t *testing.T) {
	const allowed = "internal/api/events.go"

	var offenders []string
	walkGoFiles(t, func(rel string, data []byte) {
		if rel == allowed {
			return
		}
		for i, line := range strings.Split(string(data), "\n") {
			if strings.Contains(line, "SubscribeClient()") {
				offenders = append(offenders, fmt.Sprintf("%s:%d", rel, i+1))
			}
		}
	})
	for _, o := range offenders {
		t.Errorf("%s — SubscribeClient() берёт только %s; внутреннему потребителю нужен Subscribe(), иначе ClientCount перестанет означать «кто-то смотрит»", o, allowed)
	}
}

// ClientCount считает клиентские подписки и не считает внутренние.
func TestClientCount_IgnoresInternalSubscribers(t *testing.T) {
	b := events.NewBus()

	_, _, unsubInternal := b.Subscribe()
	if got := b.ClientCount(); got != 0 {
		t.Fatalf("ClientCount=%d после внутренней подписки, ожидался 0", got)
	}
	if got := b.SubscriberCount(); got != 1 {
		t.Fatalf("SubscriberCount=%d, ожидался 1", got)
	}

	_, _, unsubClient := b.SubscribeClient()
	if got := b.ClientCount(); got != 1 {
		t.Fatalf("ClientCount=%d после клиентской подписки, ожидался 1", got)
	}

	unsubClient()
	if got := b.ClientCount(); got != 0 {
		t.Fatalf("ClientCount=%d после отписки клиента, ожидался 0", got)
	}
	unsubInternal()
	if got := b.SubscriberCount(); got != 0 {
		t.Fatalf("SubscriberCount=%d после всех отписок, ожидался 0", got)
	}
}

// SubscriberCount считает ВСЕХ подписчиков, включая пятерых внутренних, живущих
// всё время работы процесса. Как ответ на вопрос «смотрит ли кто-нибудь» он
// всегда «да» — ровно так гейт поллера метрик молча не работал (F340), выглядя
// при чтении кода исправным.
//
// Шесть фоновых производителей теперь выводят этот ответ самостоятельно
// (F351). Сводить их в общий хелпер не стали: четверо гасят не публикацию, а
// саму работу, и в шину это не уносится. Вместо абстракции — сторож ровно на
// тот способ, которым ошибка уже случалась.
func TestSubscriberCount_NotUsedOutsideEvents(t *testing.T) {
	var offenders []string
	walkGoFiles(t, func(rel string, data []byte) {
		for i, line := range strings.Split(string(data), "\n") {
			// Комментарии пропускаем: сторож про ВЫЗОВЫ, а объяснение, почему
			// этот счётчик не годится, как раз и должно упоминать его по имени.
			if strings.HasPrefix(strings.TrimSpace(line), "//") {
				continue
			}
			if strings.Contains(line, "SubscriberCount()") {
				offenders = append(offenders, fmt.Sprintf("%s:%d", rel, i+1))
			}
		}
	})
	for _, o := range offenders {
		t.Errorf("%s — SubscriberCount() считает и внутренних подписчиков; "+
			"для вопроса «открыта ли панель» нужен ClientCount()", o)
	}
}
