package backup

import (
	"fmt"
	"strings"

	"github.com/hoaxisr/awg-manager/internal/proxyapp/wdttlink"
	"github.com/hoaxisr/awg-manager/internal/proxyrt/instancestore"
	"github.com/hoaxisr/awg-manager/internal/storage"
)

// RecordLister — срез хранилища прокси-инстансов: одно чтение записей.
// Объявлен здесь, у потребителя: пакету нужен ровно этот метод.
type RecordLister interface {
	Load() (instancestore.State, error)
}

// ReconcileLinkedEndpoints syncs AWG tunnel Peer.Endpoint to the listen port
// of linked FreeTurn/WDTT clients. Client listen is authoritative — fixes
// archives where listen-repair shuffled proxy ports but left tunnel endpoints stale.
// records и awgStore — уже сконфигурированные хранилища демона (у туннелей —
// свой lock-dir), а не новые экземпляры: иначе запись шла бы мимо общей
// блокировки.
func ReconcileLinkedEndpoints(records RecordLister, awgStore *storage.AWGTunnelStore) (int, error) {
	if records == nil {
		return 0, fmt.Errorf("хранилище прокси-инстансов не задано")
	}
	if awgStore == nil {
		return 0, fmt.Errorf("хранилище туннелей не задано")
	}

	st, err := records.Load()
	if err != nil {
		return 0, err
	}
	listens := clientListens(st.Records)
	if len(listens) == 0 {
		return 0, nil
	}

	tunnels, err := awgStore.List()
	if err != nil {
		return 0, err
	}

	updated := 0
	for i := range tunnels {
		tun := &tunnels[i]
		var listen string
		var linked bool
		// Отсутствие записи и пустой listen существующей записи — РАЗНЫЕ
		// случаи: у второго законный дефолт 9000, а у первого клиента нет
		// вовсе, и endpoint такого туннеля трогать нечем.
		switch {
		case strings.TrimSpace(tun.FreeTurnClientID) != "":
			listen, linked = listens[clientKey(instancestore.KindFreeTurnClient, tun.FreeTurnClientID)]
		case strings.TrimSpace(tun.WdttClientID) != "":
			listen, linked = listens[clientKey(instancestore.KindWdttClient, tun.WdttClientID)]
		}
		if !linked {
			continue
		}
		want := fmt.Sprintf("127.0.0.1:%d", wdttlink.ListenPortFromAddr(listen))
		if strings.TrimSpace(tun.Peer.Endpoint) == want {
			continue
		}
		tun.Peer.Endpoint = want
		if err := awgStore.Save(tun); err != nil {
			return updated, fmt.Errorf("tunnel %s: %w", tun.ID, err)
		}
		updated++
	}
	return updated, nil
}

// clientListens — адреса прослушивания КЛИЕНТСКИХ записей по ключу инстанса.
// Серверных здесь нет: endpoint связанного туннеля смотрит на клиента.
func clientListens(recs []instancestore.Record) map[string]string {
	out := map[string]string{}
	for _, rec := range recs {
		switch {
		case rec.WdttClient != nil:
			out[rec.Key()] = rec.WdttClient.Listen
		case rec.FreeTurnClient != nil:
			out[rec.Key()] = rec.FreeTurnClient.Listen
		}
	}
	return out
}

func clientKey(kind instancestore.Kind, id string) string {
	return instancestore.Record{Kind: kind, ID: strings.TrimSpace(id)}.Key()
}
