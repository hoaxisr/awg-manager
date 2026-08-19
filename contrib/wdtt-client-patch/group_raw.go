package main

import (
	"context"
	"log"
	"math/rand"
	"net"
	"strings"
	"sync"
	"sync/atomic"
	"time"
)

// WorkerGroupRaw — как WorkerGroup, но RunRawSession вместо RunSession.
func WorkerGroupRaw(
	ctx context.Context,
	groupID int,
	hashIndex int,
	tp *TurnParams,
	peer *net.UDPAddr,
	d *Dispatcher,
	getConfig bool,
	configCh chan<- string,
	workerIDs []int,
	pauseFlag *int32,
	deviceID, password string,
	stats *Stats,
	waitReady <-chan struct{},
	signalReady chan<- struct{},
) {
	if waitReady != nil {
		log.Printf("[ГРУППА #%d] Ожидание сигнала от предыдущей группы...", groupID)
		select {
		case <-waitReady:
		case <-ctx.Done():
			return
		}
	}

	var configSent int32
	if !getConfig {
		configSent = 1
	}

	for atomic.LoadInt32(pauseFlag) != 0 {
		if ctx.Err() != nil {
			return
		}
		time.Sleep(1 * time.Second)
	}

	hash := tp.Hashes[hashIndex%len(tp.Hashes)]
	shortHash := hash
	if len(shortHash) > 8 {
		shortHash = shortHash[:8]
	}
	log.Printf("[ГРУППА #%d] Запрос кредов (хеш: %s...)", groupID, shortHash)

	credStreamID := groupID * 100
	user, pass, turnURLs, err := GetCreds(ctx, hash, credStreamID)
	var creds *Credentials
	if err == nil {
		creds = &Credentials{User: user, Pass: pass, TurnURLs: turnURLs, CacheStreamID: credStreamID}
	} else {
		log.Printf("[ГРУППА #%d] Ошибка кредов: %v", groupID, err)
		return
	}

	log.Printf("[ГРУППА #%d] Креды OK, TURN: %v, %d raw-воркеров", groupID, creds.TurnURLs, len(workerIDs))

	var wg sync.WaitGroup
	var credsMu sync.RWMutex
	var refreshMu sync.Mutex
	var lastCredRefresh atomic.Int64

	refreshCreds := func(reason string) bool {
		refreshMu.Lock()
		defer refreshMu.Unlock()

		now := time.Now().Unix()
		last := lastCredRefresh.Load()
		if last > 0 && now-last < 15 {
			return true
		}

		getStreamCache(credStreamID).invalidate(credStreamID)
		if getVkAuthMode() == "account" {
			invalidateInjectedTurnCreds(hash)
		}
		u, p, urls, refreshErr := GetCreds(ctx, hash, credStreamID)
		if refreshErr != nil {
			log.Printf("[TURN] Не удалось обновить креды после %s: %v", reason, refreshErr)
			return false
		}

		credsMu.Lock()
		creds = &Credentials{User: u, Pass: p, TurnURLs: urls, CacheStreamID: credStreamID}
		credsMu.Unlock()
		lastCredRefresh.Store(time.Now().Unix())
		log.Printf("[TURN] Креды обновлены после %s, TURN urls=%d", reason, len(urls))
		return true
	}

	if signalReady != nil {
		go func() {
			time.Sleep(2 * time.Second)
			close(signalReady)
			log.Printf("[ГРУППА #%d] Успешный старт! Передача эстафеты следующей группе...", groupID)
		}()
	}

	for i, wid := range workerIDs {
		wg.Add(1)
		workerDelay := time.Duration(i) * 500 * time.Millisecond

		go func(wid int, delay time.Duration) {
			defer wg.Done()

			if delay > 0 {
				select {
				case <-time.After(delay):
				case <-ctx.Done():
					return
				}
			}

			shouldGetConfig := getConfig
			attempt := 0

			for {
				if ctx.Err() != nil {
					return
				}

				getConf := shouldGetConfig
				var cc chan<- string
				if getConf && atomic.LoadInt32(&configSent) == 0 {
					cc = configCh
				}

				credsMu.RLock()
				credsSnapshot := *creds
				credsSnapshot.TurnURLs = cloneStringSlice(creds.TurnURLs)
				credsMu.RUnlock()

				configDelivered, sessErr := RunRawSession(ctx, tp, peer, d,
					getConf, cc, wid, &credsSnapshot, deviceID, password, stats)

				quotaRetry := false
				if getConf && configDelivered {
					atomic.StoreInt32(&configSent, 1)
				}

				if sessErr != nil {
					if ctx.Err() != nil {
						return
					}
					errStr := sessErr.Error()
					errStrLower := strings.ToLower(errStr)

					turnAllocAttrMissing := strings.Contains(errStrLower, "turn allocate") &&
						strings.Contains(errStrLower, "attribute not found")
					isTurnQuota := strings.Contains(errStrLower, "quota") || strings.Contains(errStr, "486")
					quotaRetry = isTurnQuota
					turnCredRefreshNeeded := !isTurnQuota && (turnAllocAttrMissing ||
						strings.Contains(errStrLower, "turn allocate auth") ||
						strings.Contains(errStrLower, "invalid credential") ||
						strings.Contains(errStrLower, "stale nonce") ||
						strings.Contains(errStrLower, "allocation mismatch") ||
						strings.Contains(errStrLower, "error 508"))

					if hint := workerErrorHint(sessErr); hint != "" {
						errStr += " | " + hint
					}

					if strings.Contains(errStr, "хеш мёртв") ||
						strings.Contains(errStr, "FATAL_AUTH") {
						log.Printf("[ВОРКЕР #%d] Фатальная ошибка: %s", wid, errStr)
						return
					}

					attempt++
					if isTurnQuota {
						log.Printf("[ВОРКЕР #%d] [TURN] Квота relay, ждём: %s", wid, errStr)
					} else if turnAllocAttrMissing || turnCredRefreshNeeded {
						refreshCreds("TURN allocation error")
					} else {
						log.Printf("[ВОРКЕР #%d] Ошибка (попытка %d): %s", wid, attempt, errStr)
					}

					isStunDeath := strings.Contains(errStrLower, "error 29") ||
						strings.Contains(errStrLower, "cannot create socket")
					if isStunDeath {
						log.Printf("[ВОРКЕР #%d] Невосстановимая TURN/STUN ошибка: %s", wid, errStr)
						return
					}
				}

				if ctx.Err() != nil {
					return
				}

				retryDelay := time.Duration(5+rand.Intn(11)) * time.Second
				if quotaRetry {
					retryDelay = time.Duration(30+rand.Intn(31)) * time.Second
				}
				select {
				case <-time.After(retryDelay):
				case <-ctx.Done():
					return
				}
			}
		}(wid, workerDelay)
	}

	wg.Wait()
	log.Printf("[ГРУППА #%d] Все raw-воркеры группы завершились.", groupID)
}
