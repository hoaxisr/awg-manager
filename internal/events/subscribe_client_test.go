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
