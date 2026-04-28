package api

import (
	"context"
	"fmt"
	"io"
	"net/http"

	"github.com/coder/websocket"

	"github.com/hoaxisr/awg-manager/internal/logging"
	"github.com/hoaxisr/awg-manager/internal/response"
	"github.com/hoaxisr/awg-manager/internal/terminal"
)

// ── Response DTOs ────────────────────────────────────────────────

// TerminalStartData is the payload returned by POST /terminal/start.
type TerminalStartData struct {
	Port int `json:"port" example:"7681"`
}

// TerminalStartResponse is the envelope for POST /terminal/start.
type TerminalStartResponse struct {
	Success bool              `json:"success" example:"true"`
	Data    TerminalStartData `json:"data"`
}

// TerminalHandler handles terminal API endpoints.
type TerminalHandler struct {
	manager terminal.Manager
	log     logging.AppLogger
}

// NewTerminalHandler creates a new terminal handler.
func NewTerminalHandler(manager terminal.Manager, log logging.AppLogger) *TerminalHandler {
	return &TerminalHandler{manager: manager, log: log}
}

// TerminalStatusResponse represents terminal status.
type TerminalStatusResponse struct {
	Installed     bool `json:"installed" example:"true"`
	Running       bool `json:"running" example:"false"`
	SessionActive bool `json:"sessionActive" example:"false"`
}

// Status returns the current terminal state.
// GET /api/terminal/status
//
//	@Summary		Terminal status
//	@Tags			terminal
//	@Produce		json
//	@Security		CookieAuth
//	@Success		200	{object}	APIEnvelope{data=TerminalStatusResponse}
//	@Failure		400	{object}	APIErrorEnvelope
//	@Failure		500	{object}	APIErrorEnvelope
//	@Router			/terminal/status [get]
func (h *TerminalHandler) Status(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		response.MethodNotAllowed(w)
		return
	}
	response.Success(w, TerminalStatusResponse{
		Installed:     h.manager.IsInstalled(r.Context()),
		Running:       h.manager.IsRunning(),
		SessionActive: h.manager.HasActiveSession(),
	})
}

// Install installs ttyd via opkg.
// POST /api/terminal/install
//
//	@Summary		Install terminal (ttyd)
//	@Tags			terminal
//	@Produce		json
//	@Security		CookieAuth
//	@Success		200	{object}	TerminalStatusResponse
//	@Failure		400	{object}	APIErrorEnvelope
//	@Failure		500	{object}	APIErrorEnvelope
//	@Router			/terminal/install [post]
func (h *TerminalHandler) Install(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		response.MethodNotAllowed(w)
		return
	}
	if h.manager.IsInstalled(r.Context()) {
		response.Success(w, map[string]bool{"installed": true})
		return
	}
	h.log.AppLog(logging.LevelInfo, "terminal", "", "install", "ttyd", "install requested via API")
	if err := h.manager.Install(r.Context()); err != nil {
		h.log.AppLog(logging.LevelWarn, "terminal", "", "install", "ttyd", "failed: "+err.Error())
		response.InternalError(w, err.Error())
		return
	}
	h.log.AppLog(logging.LevelInfo, "terminal", "", "install", "ttyd", "installed successfully via API")
	response.Success(w, map[string]bool{"installed": true})
}

// Start launches the ttyd process.
// POST /api/terminal/start
//
//	@Summary		Start terminal
//	@Tags			terminal
//	@Produce		json
//	@Security		CookieAuth
//	@Success		200	{object}	TerminalStartResponse
//	@Failure		400	{object}	APIErrorEnvelope
//	@Failure		500	{object}	APIErrorEnvelope
//	@Router			/terminal/start [post]
func (h *TerminalHandler) Start(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		response.MethodNotAllowed(w)
		return
	}
	if !h.manager.IsInstalled(r.Context()) {
		response.Error(w, "ttyd is not installed", "NOT_INSTALLED")
		return
	}
	h.log.AppLog(logging.LevelInfo, "terminal", "", "start", "ttyd", "start requested via API")
	port, err := h.manager.Start(r.Context())
	if err != nil {
		h.log.AppLog(logging.LevelWarn, "terminal", "", "start", "ttyd", "failed via API: "+err.Error())
		response.InternalError(w, err.Error())
		return
	}
	h.log.AppLog(logging.LevelInfo, "terminal", "", "start", "ttyd", fmt.Sprintf("started on port %d via API", port))
	response.Success(w, map[string]int{"port": port})
}

// Stop kills the ttyd process.
// POST /api/terminal/stop
//
//	@Summary		Stop terminal
//	@Tags			terminal
//	@Produce		json
//	@Security		CookieAuth
//	@Success		200	{object}	APIEnvelope
//	@Failure		400	{object}	APIErrorEnvelope
//	@Failure		500	{object}	APIErrorEnvelope
//	@Router			/terminal/stop [post]
func (h *TerminalHandler) Stop(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		response.MethodNotAllowed(w)
		return
	}
	h.log.AppLog(logging.LevelInfo, "terminal", "", "stop", "ttyd", "stop requested via API")
	if err := h.manager.Stop(r.Context()); err != nil {
		h.log.AppLog(logging.LevelWarn, "terminal", "", "stop", "ttyd", "failed via API: "+err.Error())
		response.InternalError(w, err.Error())
		return
	}
	h.log.AppLog(logging.LevelInfo, "terminal", "", "stop", "ttyd", "stopped via API")
	response.Success(w, map[string]bool{"stopped": true})
}

// WebSocket proxies WebSocket connection to ttyd.
// GET /api/terminal/ws
//
//	@Summary		Terminal WebSocket
//	@Tags			terminal
//	@Produce		json
//	@Security		CookieAuth
//	@Success		200	{string}	string	"WebSocket upgrade"
//	@Failure		400	{object}	APIErrorEnvelope
//	@Failure		500	{object}	APIErrorEnvelope
//	@Router			/terminal/ws [get]
func (h *TerminalHandler) WebSocket(w http.ResponseWriter, r *http.Request) {
	if h.manager.HasActiveSession() {
		response.ErrorWithStatus(w, http.StatusConflict, "Terminal already open in another tab", "SESSION_ACTIVE")
		return
	}
	if !h.manager.IsRunning() {
		response.Error(w, "Terminal is not running", "NOT_RUNNING")
		return
	}

	// Accept client WebSocket. Disable compression for transparent binary passthrough.
	clientConn, err := websocket.Accept(w, r, &websocket.AcceptOptions{
		InsecureSkipVerify:  true, // same-origin, auth already checked by middleware
		CompressionMode:     websocket.CompressionDisabled,
	})
	if err != nil {
		return // Accept already wrote HTTP error
	}
	defer clientConn.CloseNow()

	// Connect to ttyd WebSocket with required "tty" subprotocol.
	ttydURL := fmt.Sprintf("ws://127.0.0.1:%d/ws", h.manager.Port())
	ctx := r.Context()
	ttydConn, _, err := websocket.Dial(ctx, ttydURL, &websocket.DialOptions{
		Subprotocols:    []string{"tty"},
		CompressionMode: websocket.CompressionDisabled,
	})
	if err != nil {
		clientConn.Close(websocket.StatusInternalError, "failed to connect to ttyd")
		return
	}
	defer ttydConn.CloseNow()

	h.manager.SetSessionActive(true)
	defer h.manager.SetSessionActive(false)

	// Detached context — HTTP request context may cancel prematurely.
	proxyCtx, proxyCancel := context.WithCancel(context.Background())
	defer proxyCancel()

	// Set read limits for both sides.
	clientConn.SetReadLimit(1024 * 1024)
	ttydConn.SetReadLimit(1024 * 1024)

	// Bidirectional proxy — transparent passthrough.
	errc := make(chan error, 2)

	// client -> ttyd
	go func() {
		errc <- wsCopy(proxyCtx, ttydConn, clientConn)
	}()

	// ttyd -> client
	go func() {
		errc <- wsCopy(proxyCtx, clientConn, ttydConn)
	}()

	// Wait for either direction to finish.
	<-errc
	proxyCancel()
}

// wsCopy streams complete WebSocket messages from src to dst.
//
// Uses the coder/websocket Reader/Writer stream API and io.CopyBuffer
// with a reused 32 KiB scratch buffer. The previous implementation
// called Read, which allocates a fresh []byte holding the whole
// message per call — for large output bursts (xterm log dumps, tail
// -f, etc.) that was a message-sized allocation on every hop.
func wsCopy(ctx context.Context, dst, src *websocket.Conn) error {
	buf := make([]byte, 32*1024)
	for {
		msgType, reader, err := src.Reader(ctx)
		if err != nil {
			return err
		}
		writer, err := dst.Writer(ctx, msgType)
		if err != nil {
			return err
		}
		if _, err := io.CopyBuffer(writer, reader, buf); err != nil {
			_ = writer.Close()
			return err
		}
		if err := writer.Close(); err != nil {
			return err
		}
	}
}
