package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"log"
	"net"
	"os"
	"os/signal"
	"sync"
	"syscall"
	"time"

	"github.com/pion/dtls/v3"
	"github.com/samosvalishe/free-turn-proxy/internal/clientsdb"
	"github.com/samosvalishe/free-turn-proxy/internal/config"
	"github.com/samosvalishe/free-turn-proxy/internal/logx"
	"github.com/samosvalishe/free-turn-proxy/internal/proxy/bondserver"
	"github.com/samosvalishe/free-turn-proxy/internal/proxy/tcpfwdserver"
	"github.com/samosvalishe/free-turn-proxy/internal/proxy/udpserver"
	"github.com/samosvalishe/free-turn-proxy/internal/transport/dtlsdial"
	"github.com/samosvalishe/free-turn-proxy/internal/wire"
	"github.com/samosvalishe/free-turn-proxy/internal/wire/rtpopus"
)

// version is populated at build time via -ldflags "-X main.version=...".
var version = "dev"

func main() {
	if len(os.Args) >= 2 && os.Args[1] == "clients" {
		handleClientsCommand(os.Args[2:])
		return
	}

	cfg, err := config.ParseServer(os.Args[1:], os.Stderr)
	if err != nil {
		// -help/-h: usage уже напечатан в ParseServer, выходим штатно.
		if errors.Is(err, flag.ErrHelp) {
			os.Exit(0)
		}
		// логгер ещё не создан - единственный fatal до его инициализации.
		log.Fatalf("%v", err)
	}
	logger := logx.New(cfg.Log.Debug)
	logger.Infof("Free Turn Proxy server version=%s", version)

	if cfg.Obf.GenKey {
		key, gerr := rtpopus.GenKeyHex()
		if gerr != nil {
			logger.Errorf("gen-obf-key: %v", gerr)
			os.Exit(1)
		}
		fmt.Println(key)
		return
	}

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	signalChan := make(chan os.Signal, 1)
	signal.Notify(signalChan, syscall.SIGTERM, syscall.SIGINT)
	go func() {
		<-signalChan
		logger.Infof("Terminating...")
		cancel()
		select {
		case <-signalChan:
		case <-time.After(5 * time.Second):
		}
		logger.Warnf("Forced exit after shutdown timeout")
		cancel()
		os.Exit(1)
	}()

	addr, err := net.ResolveUDPAddr("udp", cfg.Proxy.Listen)
	if err != nil {
		logger.Errorf("resolve listen addr: %v", err)
		os.Exit(1)
	}
	logger.Infof("Starting server listen=%s connect=%s mode=%s obf-profile=%s bond-autodetect=true",
		cfg.Proxy.Listen, cfg.Proxy.Connect, cfg.Proxy.Mode, cfg.Obf.Profile)
	if !cfg.Obf.Enabled() {
		logger.Warnf("running with -obf-profile=none: any client reaching %s can relay to %s (no shared-key auth)", cfg.Proxy.Listen, cfg.Proxy.Connect)
	}

	certificate, genErr := dtlsdial.GenerateSelfSignedCert()
	if genErr != nil {
		logger.Errorf("self-signed cert: %v", genErr)
		os.Exit(1)
	}

	dtlsOpts := []dtls.ServerOption{
		dtls.WithCertificates(certificate),
		dtls.WithExtendedMasterSecret(dtls.RequireExtendedMasterSecret),
		dtls.WithCipherSuites(dtls.TLS_ECDHE_ECDSA_WITH_AES_128_GCM_SHA256),
		dtls.WithConnectionIDGenerator(dtls.RandomCIDGenerator(8)),
	}
	var listener net.Listener
	if cfg.Obf.Enabled() {
		logger.Infof("OBF profile=%s: listener only accepts clients with matching -obf-profile and -obf-key", cfg.Obf.Profile)
		obfListener, oerr := wire.Listen(string(cfg.Obf.Profile), addr, cfg.Obf.Key, cfg.Obf.Timing)
		if oerr != nil {
			logger.Errorf("obf listen: %v", oerr)
			os.Exit(1)
		}
		listener, err = dtls.NewListenerWithOptions(obfListener, dtlsOpts...)
	} else {
		listener, err = dtls.ListenWithOptions("udp", addr, dtlsOpts...)
	}
	if err != nil {
		logger.Errorf("dtls listen: %v", err)
		os.Exit(1)
	}
	context.AfterFunc(ctx, func() {
		if err := listener.Close(); err != nil {
			logger.Errorf("listener close: %v", err)
		}
	})

	logger.Infof("Listening on %s", cfg.Proxy.Listen)

	registry := bondserver.NewRegistry(bondserver.Deps{Log: logger})

	var db *clientsdb.DB
	if cfg.ClientsFile != "" {
		d, err := clientsdb.New(cfg.ClientsFile)
		if err != nil {
			logger.Errorf("Failed to open clients-file: %v", err)
			os.Exit(1)
		}
		d.StartHotReload(10 * time.Second)
		db = d
		logger.Infof("Client ID authorization enabled via %s", cfg.ClientsFile)
	}

	var wg sync.WaitGroup
	for {
		select {
		case <-ctx.Done():
			wg.Wait()
			return
		default:
		}
		conn, err := listener.Accept()
		if err != nil {
			if ctx.Err() != nil {
				wg.Wait()
				return
			}
			logger.Warnf("accept: %v", err)
			continue
		}
		wg.Go(func() {
			handleAccepted(ctx, logger, registry, db, conn, cfg)
		})
	}
}

func handleAccepted(ctx context.Context, logger logx.Logger, registry *bondserver.Registry, db *clientsdb.DB, conn net.Conn, cfg *config.Server) {
	defer func() {
		if closeErr := conn.Close(); closeErr != nil {
			logger.Warnf("failed to close incoming connection: %s", closeErr)
		}
	}()
	logger.Debugf("Connection from %s", conn.RemoteAddr())

	ctx1, cancel1 := context.WithTimeout(ctx, 30*time.Second)
	defer cancel1()

	dtlsConn, ok := conn.(*dtls.Conn)
	if !ok {
		logger.Errorf("Type error: expected *dtls.Conn")
		return
	}
	logger.Debugf("Start handshake")
	if err := dtlsConn.HandshakeContext(ctx1); err != nil {
		logger.Warnf("Handshake failed: %v", err)
		return
	}
	logger.Debugf("Handshake done")

	// Client ID читается всегда (клиент всегда шлёт его первой app-record) -
	// wire-контракт симметричен. -clients-file включает только enforce по allowlist.
	clientID, err := clientsdb.ReadClientID(dtlsConn)
	if err != nil {
		logger.Warnf("Read Client ID failed: %v", err)
		return
	}
	if db != nil {
		if !db.IsAuthorized(clientID) {
			logger.Warnf("Unauthorized Client ID: %s. Dropping connection.", clientID)
			return
		}
		logger.Debugf("Client %s authorized", clientID)
	} else {
		logger.Debugf("Client ID received (no allowlist): %s", clientID)
	}

	if cfg.Proxy.Mode == config.ProxyModeTCPFwd {
		tcpfwdserver.Handle(ctx, logger, registry, dtlsConn, cfg.Proxy.Connect, cfg.KCP.Profile, cfg.KCP.FEC)
	} else {
		udpserver.Handle(ctx, logger, conn, cfg.Proxy.Connect)
	}

	logger.Debugf("Connection closed: %s", conn.RemoteAddr())
}

func handleClientsCommand(args []string) {
	if len(args) == 0 {
		fmt.Println("Usage: server clients <add|remove|list> [args...]")
		os.Exit(1)
	}

	// Файл по умолчанию или из переменной окружения
	dbPath := "clients.json"
	if envPath := os.Getenv("CLIENTS_FILE"); envPath != "" {
		dbPath = envPath
	}

	db, err := clientsdb.New(dbPath)
	if err != nil {
		fmt.Printf("Failed to open %s: %v\n", dbPath, err)
		os.Exit(1)
	}

	cmd := args[0]
	switch cmd {
	case "add":
		if len(args) < 2 {
			fmt.Println("Usage: server clients add <client_id> [comment]")
			os.Exit(1)
		}
		clientID := args[1]
		comment := ""
		if len(args) > 2 {
			comment = args[2]
		}
		if err := db.Add(clientID, comment); err != nil {
			fmt.Printf("Failed to add client: %v\n", err)
			os.Exit(1)
		}
		fmt.Printf("Client %s added successfully to %s\n", clientID, dbPath)
	case "remove":
		if len(args) < 2 {
			fmt.Println("Usage: server clients remove <client_id>")
			os.Exit(1)
		}
		clientID := args[1]
		if err := db.Remove(clientID); err != nil {
			fmt.Printf("Failed to remove client: %v\n", err)
			os.Exit(1)
		}
		fmt.Printf("Client %s removed successfully from %s\n", clientID, dbPath)
	case "list":
		clients := db.List()
		fmt.Printf("Found %d clients in %s:\n", len(clients), dbPath)
		for id, info := range clients {
			fmt.Printf(" - %s (Comment: %s)\n", id, info.Comment)
		}
	default:
		fmt.Printf("Unknown command: %s\n", cmd)
		os.Exit(1)
	}
}
