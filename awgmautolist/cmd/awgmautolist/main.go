// awgmautolist — сателлит-демон awg-manager для автосписка сайтов, которые
// не открываются напрямую. Основная логика вынесена из AWGM, взаимодействие
// выполняется через HTTP API.
//
// Запуск:
//
//	awgmautolist -router http://192.168.31.1:2222 -apikey <ключ> [-config /opt/etc/awgmautolist.json]
//
// Флаги:
//
//	-router    базовый URL awg-manager (обязателен)
//	-apikey    значение Settings.ApiKey awg-manager (Bearer); пусто — без авторизации
//	-config    файл конфигурации (autoAdd, targetInterface); создаётся при первом сохранении
//	-listen    адрес статуса сателлита (по умолчанию 127.0.0.1:2226)
//	-verbose   подробный журнал без изменения файла конфигурации
//
// HTTP API (только loopback):
//
//	GET  /status                     — снимок кандидатов и конфигурации
//	POST /add      {domains:[...]}   — добавить выбранные домены
//	POST /dismiss  {domain}          — скрыть кандидата на сутки
//	POST /forget   {domain}          — убрать кандидата без скрытия
//	POST /restore  {domain}          — отменить скрытие/исключение
//	POST /exclude  {domains:[...]}   — добавить постоянные исключения
//	POST /probe    {host}            — новая ручная проба
//	GET  /config                     — конфигурация
//	POST /config   JSON merge        — изменить только переданные поля
package main

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"log"
	"net"
	"net/http"
	"os"
	"os/exec"
	"os/signal"
	"syscall"
	"time"

	autolist "github.com/hoaxisr/awg-manager/awgmautolist/internal/autolist"
	"github.com/hoaxisr/awg-manager/awgmautolist/internal/probeq"
)

func validateLoopbackListen(addr string) error {
	host, _, err := net.SplitHostPort(addr)
	if err != nil {
		return fmt.Errorf("listen: нужен host:port: %w", err)
	}
	if host == "localhost" {
		return nil
	}
	ip := net.ParseIP(host)
	if ip == nil || !ip.IsLoopback() {
		return fmt.Errorf("listen: неавторизованный API разрешён только на loopback")
	}
	return nil
}

func main() {
	var (
		routerURL  = flag.String("router", "http://127.0.0.1:2222", "базовый URL awg-manager")
		apiKey     = flag.String("apikey", os.Getenv("AWGM_API_KEY"), "apiKey awg-manager (Bearer); иначе AWGM_API_KEY")
		configPath = flag.String("config", "/opt/etc/awgmautolist.json", "файл конфигурации")
		listen     = flag.String("listen", "127.0.0.1:2226", "адрес HTTP-статуса")
		verbose    = flag.Bool("verbose", false, "подробный журнал без изменения файла конфигурации")
		showVer    = flag.Bool("version", false, "печать версии и выход")
	)
	flag.Parse()

	if *showVer {
		fmt.Println("awgmautolist", autolist.Version)
		return
	}
	if *routerURL == "" {
		log.Fatal("нужен -router http://<router>:<port>")
	}
	if err := validateLoopbackListen(*listen); err != nil {
		log.Fatal(err)
	}
	if _, err := exec.LookPath("curl"); err != nil {
		log.Fatal("обязательный curl не найден: установите Entware-пакет curl")
	}
	log.SetFlags(log.LstdFlags)
	log.SetPrefix("[awgm-autolist] ")
	if output, err := openRotatingLog("/opt/var/log/awgmautolist.log"); err == nil {
		log.SetOutput(output)
		defer output.Close()
	} else {
		log.Printf("не удалось открыть log file: %v", err)
	}

	ctx, cancel := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer cancel()

	eng := autolist.NewEngine(*routerURL, *apiKey)
	eng.BaseURL = *routerURL
	eng.APIKey = *apiKey
	if err := eng.LoadConfig(*configPath); err != nil {
		log.Printf("конфиг не прочитан (%v), старт с пустым", err)
	}
	if *verbose {
		eng.SetVerboseRuntime(true)
	}

	mux := http.NewServeMux()
	writeJSON := func(w http.ResponseWriter, code int, v any) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(code)
		_ = json.NewEncoder(w).Encode(v)
	}

	mux.HandleFunc("GET /status", func(w http.ResponseWriter, r *http.Request) {
		writeJSON(w, http.StatusOK, eng.Snapshot())
	})

	mux.HandleFunc("POST /add", func(w http.ResponseWriter, r *http.Request) {
		var req struct {
			Domains []string `json:"domains"`
		}
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil || len(req.Domains) == 0 {
			writeJSON(w, http.StatusBadRequest, map[string]string{"error": "domains required"})
			return
		}
		res := eng.AddToTunnel(req.Domains)
		// error-интерфейс иначе сериализуется как {} и прячет причину от UI.
		results := make(map[string]any, len(res))
		for domain, err := range res {
			if err == nil {
				results[domain] = nil
			} else {
				results[domain] = err.Error()
			}
		}
		writeJSON(w, http.StatusOK, map[string]any{"results": results})
	})

	mux.HandleFunc("POST /dismiss", func(w http.ResponseWriter, r *http.Request) {
		var req struct {
			Domain string `json:"domain"`
		}
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil || req.Domain == "" {
			writeJSON(w, http.StatusBadRequest, map[string]string{"error": "domain required"})
			return
		}
		eng.Dismiss(req.Domain)
		w.WriteHeader(http.StatusNoContent)
	})

	mux.HandleFunc("POST /restore", func(w http.ResponseWriter, r *http.Request) {
		var req struct {
			Domain string `json:"domain"`
		}
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil || req.Domain == "" {
			writeJSON(w, http.StatusBadRequest, map[string]string{"error": "domain required"})
			return
		}
		if err := eng.RestoreDomain(req.Domain); err != nil {
			writeJSON(w, http.StatusBadRequest, map[string]string{"error": err.Error()})
			return
		}
		w.WriteHeader(http.StatusNoContent)
	})

	mux.HandleFunc("POST /forget", func(w http.ResponseWriter, r *http.Request) {
		var req struct {
			Domain string `json:"domain"`
		}
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil || req.Domain == "" {
			writeJSON(w, http.StatusBadRequest, map[string]string{"error": "domain required"})
			return
		}
		eng.Forget(req.Domain)
		w.WriteHeader(http.StatusNoContent)
	})

	mux.HandleFunc("POST /exclude", func(w http.ResponseWriter, r *http.Request) {
		var req struct {
			Domains []string `json:"domains"`
		}
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil || len(req.Domains) == 0 {
			writeJSON(w, http.StatusBadRequest, map[string]string{"error": "domains required"})
			return
		}
		if err := eng.ExcludeDomains(req.Domains); err != nil {
			writeJSON(w, http.StatusBadRequest, map[string]string{"error": err.Error()})
			return
		}
		writeJSON(w, http.StatusOK, map[string]bool{"success": true})
	})

	mux.HandleFunc("POST /probe", func(w http.ResponseWriter, r *http.Request) {
		var req struct {
			Host string `json:"host"`
		}
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil || req.Host == "" {
			writeJSON(w, http.StatusBadRequest, map[string]string{"error": "host required"})
			return
		}
		ask := eng.RequestProbe(req.Host)
		result := "full"
		switch ask {
		case probeq.AskQueued:
			result = "queued"
		case probeq.AskBusy:
			result = "busy"
		}
		writeJSON(w, http.StatusOK, map[string]string{"result": result})
	})

	mux.HandleFunc("GET /config", func(w http.ResponseWriter, r *http.Request) {
		writeJSON(w, http.StatusOK, eng.Config())
	})

	mux.HandleFunc("POST /config", func(w http.ResponseWriter, r *http.Request) {
		raw, err := io.ReadAll(r.Body)
		if err != nil {
			writeJSON(w, http.StatusBadRequest, map[string]string{"error": err.Error()})
			return
		}
		// Merge: отсутствующие поля не затирают текущие настройки/исключения.
		if err := eng.PatchConfig(raw); err != nil {
			writeJSON(w, http.StatusBadRequest, map[string]string{"error": err.Error()})
			return
		}
		cfg := eng.Config()
		log.Printf("конфиг применён: autoAdd=%v targetInterface=%q", cfg.AutoAdd, cfg.TargetInterface)
		writeJSON(w, http.StatusOK, eng.Config())
	})

	handler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodPost {
			r.Body = http.MaxBytesReader(w, r.Body, 1<<20)
		}
		mux.ServeHTTP(w, r)
	})
	srv := &http.Server{Addr: *listen, Handler: handler, ReadHeaderTimeout: 5 * time.Second}
	go func() {
		log.Printf("сервер статуса на http://%s/status", *listen)
		if err := srv.ListenAndServe(); err != nil && err != http.ErrServerClosed {
			log.Printf("http: %v", err)
		}
	}()

	cfg := eng.Config()
	log.Printf("старт: router=%s autoAdd=%v targetInterface=%q config=%s", *routerURL, cfg.AutoAdd, cfg.TargetInterface, *configPath)
	go eng.Run(ctx)

	<-ctx.Done()
	log.Printf("остановка")
	eng.Stop()
	_ = srv.Close()
}
