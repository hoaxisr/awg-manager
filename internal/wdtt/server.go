package wdtt

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/hoaxisr/awg-manager/internal/childproc"
)

func (s *Service) UpdateServerConfig(cfg ServerConfig) error {
	_, err := s.UpdateServerInstance(DefaultInstanceID, cfg)
	return err
}

// UpdateServerInstance сохраняет конфиг и возвращает его фактическое состояние
// после нормализации и авто-починки listen — его и отдаёт API.
func (s *Service) UpdateServerInstance(id string, cfg ServerConfig) (ServerConfig, error) {
	s.mu.Lock()
	full, err := s.store.Load()
	if err != nil {
		s.mu.Unlock()
		return ServerConfig{}, err
	}
	idx := findServerIndex(full.Servers, id)
	if idx < 0 {
		s.mu.Unlock()
		return ServerConfig{}, fmt.Errorf("сервер %q не найден", id)
	}
	prevCfg := full.Servers[idx].Config
	listens := serverListenAddresses(full.Servers)
	reserved := s.reservedServerPortsExcept(id)
	cfg.Listen = ensureUniqueServerListenAddr(listens, idx, cfg.Listen, reserved, 56000, 56100)
	cfg = normalizeServerConfig(cfg)
	cfg.WgPort = ensureUniqueWgPort(cfg.WgPort, cfg.Listen, reserved, 56000, 56100)
	// Клиенты правятся только ручкой /users. Форма сервера присылает конфиг
	// целиком и держит список снапшотом времени загрузки страницы: иначе
	// любое сохранение воскрешало бы удалённых и теряло добавленных.
	cfg.Clients = full.Servers[idx].Config.Clients
	// Столкновение «главный пароль == пароль абонента» проверяем ТОЛЬКО когда
	// главный пароль меняют этим вызовом. Состав абонентов сюда приходит из
	// хранилища (строкой выше), и отказ по нему был отказом за то, чего
	// вызывающий не делал: унаследованный из ручной правки wdtt.json битый
	// состав валил ЛЮБОЙ Update, а через него — старт (StartServerInstance
	// зовёт нас ради нормализации listen), NAT, политику и LAN-сегменты.
	//
	// Сама запись никуда не доезжает и без отказа: UsableServerClients её
	// отбрасывает (ServerClientMainPassword), в passwords.json и в ссылку она
	// не попадает, в инварианте не считается. Правило остаётся в силе везде,
	// где состав или пароль меняет вызывающий: смена пароля здесь и
	// AddServerClient.
	if err := validateServerMainPassword(cfg.Password, cfg.Clients); err != nil {
		if strings.TrimSpace(cfg.Password) != strings.TrimSpace(prevCfg.Password) {
			s.mu.Unlock()
			return ServerConfig{}, err
		}
		if s.appLog != nil {
			s.appLog.Warn("clients", id, "унаследованный конфиг: пароль абонента совпадает с главным — сервер такого абонента не примет, выдайте доступ другому")
		}
	}
	// Абонента здесь НЕ заводим: сохранение настроек сервера — путь UI, а
	// «Абонент 1», рождённый на пути UI, — невидимый живой пароль, которого
	// пользователь не заказывал (решение владельца, Дополнение №5). Инвариант
	// «у сервера с заданным паролем есть абонент, которого он примет» держат
	// опора на пути старта (ensureUsableServerClient) — последняя линия для
	// путей мимо UI, апгрейда и ручного запуска — и страж удаления последнего
	// рабочего. Фронт до старта не пускает, пока рабочих абонентов нет.
	// Enabled — только Start/Stop; сохранение настроек не должно гасить автостарт.
	cfg.Enabled = prevCfg.Enabled
	// Здесь backoff НЕ сбрасываем, в отличие от клиентского Update:
	// StartServerInstance сам зовёт этот метод для нормализации listen, и сброс
	// стирал бы окно на каждой попытке супервизора — рост до 15 минут переставал
	// работать ровно там, где путь старта тянет NDMS/RCI.
	full.Servers[idx].Config = cfg
	saveErr := s.store.Save(full)
	running := s.serverProcs.get(id).Status().Running
	savedCfg := full.Servers[idx].Config
	s.mu.Unlock()
	if saveErr != nil {
		return ServerConfig{}, saveErr
	}
	if running {
		restart := serverProcessConfigChanged(prevCfg, savedCfg)
		go func(prev, saved ServerConfig, id string, restart bool) {
			ctx := context.Background()
			if restart {
				if err := s.StopServerInstance(id); err != nil && s.appLog != nil {
					s.appLog.Warn("restart", id, "stop: "+err.Error())
				}
				if err := s.StartServerInstance(id); err != nil && s.appLog != nil {
					s.appLog.Warn("restart", id, "start: "+err.Error())
				}
				return
			}
			if serverAccessConfigChanged(prev, saved) {
				removeEntwareNATForServer(ctx, prev)
				if err := s.applyServerAccess(ctx, id, saved); err != nil && s.appLog != nil {
					s.appLog.Warn("access", id, "фоновое применение NAT/LAN: "+err.Error())
				}
			}
			s.syncServerListenFirewall(ctx, id, prev, saved)
		}(prevCfg, savedCfg, id, restart)
	}
	return savedCfg, nil
}

func (s *Service) CreateServer(in CreateServerInput) (ServerInstance, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	full, err := s.store.Load()
	if err != nil {
		return ServerInstance{}, err
	}
	// Один инстанс: wdtt-server всегда создаёт интерфейс wdtt0 с адресом
	// 10.66.66.1 — второй параллельный сервер поднять физически нельзя.
	if len(full.Servers) > 0 {
		return ServerInstance{}, errors.New("wdtt-server поддерживает один инстанс: интерфейс wdtt0 общий")
	}
	cfg := DefaultServerConfig()
	if in.Config != nil {
		cfg = *in.Config
	}
	reserved := s.occupiedLocalListenPorts("")
	cfg = normalizeServerConfig(cfg)
	cfg.Listen = nextServerListen(full.Servers, reserved)
	cfg.WgPort = ensureUniqueWgPort(cfg.WgPort, cfg.Listen, reserved, 56000, 56100)
	name := in.Name
	if name == "" {
		name = fmt.Sprintf("Сервер %d", len(full.Servers)+1)
	}
	inst := ServerInstance{ID: childproc.NewInstanceID(), Name: name, Config: cfg}
	full.Servers = append(full.Servers, inst)
	if err := s.store.Save(full); err != nil {
		return ServerInstance{}, err
	}
	return inst, nil
}

func (s *Service) DeleteServer(id string) error {
	s.mu.Lock()
	full, err := s.store.Load()
	if err != nil {
		s.mu.Unlock()
		return err
	}
	idx := findServerIndex(full.Servers, id)
	if idx < 0 {
		s.mu.Unlock()
		return fmt.Errorf("сервер %q не найден", id)
	}
	inst := full.Servers[idx]
	full.Servers = append(full.Servers[:idx], full.Servers[idx+1:]...)
	saveErr := s.store.Save(full)
	s.startBackoff.Forget(serverKey(id))
	s.mu.Unlock()
	_ = s.serverProcs.get(id).Stop()
	kernelIface := inst.Config.kernelServerIface()
	removeEntwareNATForServer(context.Background(), inst.Config)
	removeEntwareLAN(context.Background(), kernelIface)
	removeRawServerPolicyMark(context.Background(), inst.Config.kernelRawIface())
	_ = s.teardownServerOpkgTun(context.Background(), inst.Config)
	removeServerListenFirewall(context.Background(), inst.Config)
	return saveErr
}

func (s *Service) RenameServer(id, name string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	full, err := s.store.Load()
	if err != nil {
		return err
	}
	idx := findServerIndex(full.Servers, id)
	if idx < 0 {
		return fmt.Errorf("сервер %q не найден", id)
	}
	full.Servers[idx].Name = strings.TrimSpace(name)
	return s.store.Save(full)
}

func (s *Service) StartServer() error {
	return s.StartServerInstance(DefaultInstanceID)
}

func (s *Service) StartServerInstance(id string) error {
	unlock, ok := s.tryLockServerStart(id)
	if !ok {
		return ErrServerStartInFlight
	}
	defer unlock()
	inst, err := s.serverInstance(id)
	if err != nil {
		return err
	}
	saved, err := s.UpdateServerInstance(id, inst.Config)
	if err != nil {
		return err
	}
	if strings.TrimSpace(saved.Password) == "" {
		return errors.New("укажите пароль подключения (-password)")
	}
	ctx := context.Background()
	s.beginOpkgStart()
	defer s.endOpkgStart()
	cfg, err := s.ensureServerOpkgIndex(ctx, id, saved)
	if err != nil {
		return err
	}
	if cfg.usesNDMSOpkgTun() {
		removeEntwareNAT(ctx, DefaultWdttIface)
		removeEntwareLAN(ctx, DefaultWdttIface)
		if cfg.usesNDMSRawOpkgTun() {
			// Симметрично сносу wdtt0 выше: raw-половина переехала в OpkgTun, а
			// наши правила остались на прежнем имени. Инертны (интерфейса нет),
			// но это ровно тот класс утечки, который чинили в 2.17.0.
			sweepLegacyRawServerRules(ctx)
		}
		if err := s.prepareNDMSOpkgTun(ctx, cfg); err != nil {
			_ = s.teardownServerOpkgTun(ctx, cfg)
			return err
		}
	}
	cfgDir, err := s.serverConfigDir(id, cfg)
	if err != nil {
		if cfg.usesNDMSOpkgTun() {
			_ = s.teardownServerOpkgTun(ctx, cfg)
		}
		return err
	}
	if err := os.MkdirAll(cfgDir, 0755); err != nil {
		if cfg.usesNDMSOpkgTun() {
			_ = s.teardownServerOpkgTun(ctx, cfg)
		}
		return fmt.Errorf("config-dir: %w", err)
	}
	// passwords.json собираем из wdtt.json до старта: сервер читает его в
	// память на старте. Усыновление идёт первым — иначе абоненты, заведённые
	// мимо менеджера (телеграм-бот), вычёркивались бы из файла.
	// Отказ здесь фатален: процесс поднялся бы на пустом или протухшем
	// passwords.json и умер бы log.Fatalf'ом — мёртвая инстанция без объяснимой
	// причины. Отказ старта с внятным текстом виден и в UI, и в журнале.
	if err := s.syncServerClientsOnStart(id, cfgDir, cfg); err != nil {
		if cfg.usesNDMSOpkgTun() {
			_ = s.teardownServerOpkgTun(ctx, cfg)
		}
		return fmt.Errorf("passwords.json не записан: %w", err)
	}
	if err := redirectServerStatsLog(cfgDir, id, cfg.StatsLog); err != nil && s.appLog != nil {
		s.appLog.Warn("stats-log", id, "server.log redirect: "+err.Error())
	}
	cfg.ConfigDir = cfgDir
	_ = s.serverProcs.get(id).Stop()
	freeStaleServerListenPorts(s.serverBin, cfg)
	if err := s.serverProcs.get(id).Start(buildServerArgs(cfg)); err != nil {
		if cfg.usesNDMSOpkgTun() {
			_ = s.teardownServerOpkgTun(ctx, cfg)
		}
		return err
	}
	// Тумблер «в политиках» применяется только здесь, на старте: security-level
	// уже проставлен prepareNDMSOpkgTun, `ip global` доставит activate ниже.
	// Запоминаем ровно за тем процессом, который только что стартовал, — иначе
	// применённое значение наружу не узнать ни от кого.
	s.serverProcs.get(id).setAppliedExposeToPolicies(cfg.ExposeToPolicies)
	if !waitForInterface(s.ifaceChecker, cfg.kernelRawIface(), 8*time.Second) {
		_ = s.serverProcs.get(id).Stop()
		if cfg.usesNDMSOpkgTun() {
			_ = s.teardownServerOpkgTun(ctx, cfg)
		}
		return fmt.Errorf("интерфейс %s не появился после запуска wdtt-server (raw)", cfg.kernelRawIface())
	}
	if cfg.usesNDMSOpkgTun() {
		kernelIface := cfg.kernelWGIface()
		if !waitForInterface(s.ifaceChecker, kernelIface, 8*time.Second) {
			_ = s.serverProcs.get(id).Stop()
			_ = s.teardownServerOpkgTun(ctx, cfg)
			return fmt.Errorf("интерфейс %s не появился после запуска wdtt-server", kernelIface)
		}
		if err := s.activateNDMSOpkgTun(ctx, cfg); err != nil {
			_ = s.serverProcs.get(id).Stop()
			_ = s.teardownServerOpkgTun(ctx, cfg)
			return err
		}
	} else {
		kernelIface := cfg.kernelWGIface()
		if !waitForInterface(s.ifaceChecker, kernelIface, 8*time.Second) {
			_ = s.serverProcs.get(id).Stop()
			return fmt.Errorf("интерфейс %s не появился после запуска wdtt-server", kernelIface)
		}
	}
	if err := s.applyServerAccess(ctx, id, cfg); err != nil {
		_ = s.serverProcs.get(id).Stop()
		if cfg.usesNDMSOpkgTun() {
			_ = s.teardownServerOpkgTun(ctx, cfg)
		}
		return err
	}
	if err := applyServerListenFirewall(ctx, cfg); err != nil && s.appLog != nil {
		s.appLog.Warn("firewall", id, "INPUT для listen-порта: "+err.Error())
	}
	if err := s.setServerEnabled(id, true); err != nil && s.appLog != nil {
		s.appLog.Warn("start", id, "не удалось сохранить enabled: "+err.Error())
	}
	return nil
}

func (s *Service) StopServer() error {
	return s.StopServerInstance(DefaultInstanceID)
}

func (s *Service) StopServerInstance(id string) error {
	// Ждём идущий старт этого сервера: иначе он допишет Enabled=true после нас,
	// и супервизор поднимет сервер, который пользователь только что выключил.
	unlock := s.lockServerStart(id)
	defer unlock()
	inst, err := s.serverInstance(id)
	if err != nil {
		return err
	}
	cfg := inst.Config
	kernelIface := cfg.kernelServerIface()
	// Останавливаем ДО снятия правил: Stop блокирует до ~3с, и тик
	// NAT-ресинка, попав в это окно, увидел бы процесс живым и поставил
	// правила заново — снять их было бы уже некому.
	err = s.serverProcs.get(id).Stop()
	removeEntwareNATForServer(context.Background(), cfg)
	removeEntwareLAN(context.Background(), kernelIface)
	removeRawServerPolicyMark(context.Background(), cfg.kernelRawIface())
	removeServerListenFirewall(context.Background(), inst.Config)
	_ = s.teardownServerOpkgTun(context.Background(), cfg)
	if e := s.setServerEnabled(id, false); e != nil && s.appLog != nil {
		s.appLog.Warn("stop", id, "не удалось сбросить enabled: "+e.Error())
	}
	return err
}

func (s *Service) ServerConfigForLink(id string) (ServerConfig, error) {
	inst, err := s.serverInstance(id)
	if err != nil {
		return ServerConfig{}, err
	}
	return inst.Config, nil
}

func (s *Service) serverInstance(id string) (ServerInstance, error) {
	full, err := s.store.Load()
	if err != nil {
		return ServerInstance{}, err
	}
	idx := findServerIndex(full.Servers, id)
	if idx < 0 {
		return ServerInstance{}, fmt.Errorf("сервер %q не найден", id)
	}
	return full.Servers[idx], nil
}

func (s *Service) serverConfigDir(id string, cfg ServerConfig) (string, error) {
	if dir := strings.TrimSpace(cfg.ConfigDir); dir != "" {
		return dir, nil
	}
	return filepath.Join(s.dataDir, "wdtt", "server", id), nil
}

// updateServerClients — RMW-хелпер над списком клиентов сервера в wdtt.json.
// mutate возвращает новый список и признак «есть что сохранять».
func (s *Service) updateServerClients(id string, mutate func([]ServerClient) ([]ServerClient, bool)) ([]ServerClient, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	full, err := s.store.Load()
	if err != nil {
		return nil, err
	}
	idx := findServerIndex(full.Servers, id)
	if idx < 0 {
		return nil, fmt.Errorf("сервер %q не найден", id)
	}
	next, changed := mutate(full.Servers[idx].Config.Clients)
	if !changed {
		return full.Servers[idx].Config.Clients, nil
	}
	full.Servers[idx].Config.Clients = next
	if err := s.store.Save(full); err != nil {
		return nil, err
	}
	return next, nil
}

// putServerClient adds or updates one client identity in wdtt.json.
//
// Сравнение — по ПОДРЕЗАННОМУ паролю, как во всём остальном конвейере
// (UsableServerClients, mergeServerClients, hasServerClientPassword): пароль с
// пробелами мог попасть в wdtt.json ручной правкой или из старых конфигов, и
// сырое сравнение завело бы рядом второй экземпляр того же абонента.
func (s *Service) putServerClient(id string, client ServerClient) error {
	password := strings.TrimSpace(client.Password)
	_, err := s.updateServerClients(id, func(list []ServerClient) ([]ServerClient, bool) {
		for i, c := range list {
			if strings.TrimSpace(c.Password) == password {
				list[i] = client
				return list, true
			}
		}
		return append(list, client), true
	})
	return err
}

// dropServerClient removes one client identity from wdtt.json.
//
// Сравнение подрезанное по той же причине, что и в putServerClient, но цена
// ошибки здесь выше: сырое сравнение не находило абонента с пробелами в пароле,
// и удаление отвечало УСПЕХОМ, ничего не удалив, — доступ оставался живым.
func (s *Service) dropServerClient(id, password string) error {
	password = strings.TrimSpace(password)
	_, err := s.updateServerClients(id, func(list []ServerClient) ([]ServerClient, bool) {
		for i, c := range list {
			if strings.TrimSpace(c.Password) == password {
				return append(list[:i:i], list[i+1:]...), true
			}
		}
		return list, false
	})
	return err
}

func (s *Service) setServerEnabled(id string, enabled bool) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	full, err := s.store.Load()
	if err != nil {
		return err
	}
	idx := findServerIndex(full.Servers, id)
	if idx < 0 {
		return fmt.Errorf("сервер %q не найден", id)
	}
	if full.Servers[idx].Config.Enabled == enabled {
		return nil
	}
	full.Servers[idx].Config.Enabled = enabled
	return s.store.Save(full)
}

// validateServerMainPassword rejects owner main password that equals a client password.
// wdtt-server uses main_password for WRAP; matching a client hash breaks DTLS/WG handshakes.
func validateServerMainPassword(main string, clients []ServerClient) error {
	main = strings.TrimSpace(main)
	if main == "" {
		return nil
	}
	for _, c := range clients {
		if strings.TrimSpace(c.Password) == main {
			return errors.New("пароль сервера не должен совпадать с паролем клиента")
		}
	}
	return nil
}

func normalizeServerConfig(cfg ServerConfig) ServerConfig {
	if cfg.Listen == "" {
		cfg.Listen = DefaultServerConfig().Listen
	}
	if cfg.WgPort <= 0 {
		cfg.WgPort = DefaultServerConfig().WgPort
	}
	cfg.Password = strings.TrimSpace(cfg.Password)
	cfg.AdminID = strings.TrimSpace(cfg.AdminID)
	cfg.BotToken = strings.TrimSpace(cfg.BotToken)
	cfg.ConfigDir = strings.TrimSpace(cfg.ConfigDir)
	if cfg.NatMode == "" {
		cfg.NatMode = DefaultServerConfig().NatMode
	}
	if cfg.Policy == "" {
		cfg.Policy = DefaultServerConfig().Policy
	}
	cfg.RelayMode = normalizeConnMode(cfg.RelayMode)
	cfg.RawListen = strings.TrimSpace(cfg.RawListen)
	cfg.DirectListen = strings.TrimSpace(cfg.DirectListen)
	return cfg
}

func buildServerArgs(c ServerConfig) []string {
	var args []string
	str := func(flag, val string) {
		if val != "" {
			args = append(args, flag, val)
		}
	}
	str("-listen", c.Listen)
	if c.WgPort > 0 {
		args = append(args, "-wg-port", strconv.Itoa(c.WgPort))
	}
	str("-config-dir", c.ConfigDir)
	str("-password", c.Password)
	str("-admin", c.AdminID)
	str("-bot-token", c.BotToken)
	args = append(args, "-no-nat")
	str("-nat-if", c.NatIface)
	if iface := strings.TrimSpace(c.WgIface); iface != "" {
		str("-wg-iface", iface)
	}
	if iface := strings.TrimSpace(c.RawIface); iface != "" {
		str("-raw-iface", iface)
	}
	str("-listen-raw", c.EffectiveRawListen())
	if direct := c.EffectiveDirectListen(); direct != "" && direct != c.Listen {
		str("-listen-direct", direct)
	}
	// -debug не передаём: wdtt-server его не знает (flag.Parse → exit 2),
	// подробного лога у него нет вовсе. Поле Debug оставлено ради совместимости
	// формата wdtt.json.
	// -dns: резолвер = шлюз, который видят клиенты. Дефолт монолита 8.8.8.8
	// уводит DNS мимо роутера — HR-ipset и доменные правила sing-box слепнут
	// к клиентским резолвам (PR #697, F1). DNAT :53 в netfilter.d-хуке
	// перехватывает и тех, кто раздачу игнорирует.
	if c.UsesWireGuardRelay() {
		str("-dns", c.serverAccessAddress())
	} else {
		str("-dns", DefaultRawServerAddr)
	}
	return args
}
