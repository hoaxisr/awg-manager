package capture

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"time"
)

var errReadTimeout = errors.New("packet read timeout")

type packetSocket interface {
	Read([]byte) (int, error)
	Close() error
}

type socketOpener func(string) (packetSocket, error)

// Controller owns one generation of AF_PACKET sockets and one FlowTable.
// Operations are serialized so Resume cannot overlap a still-stopping capture.
type Controller struct {
	opMu sync.Mutex
	mu   sync.Mutex

	interfaces []string
	udpAll     bool
	emit       func(Suspect)
	onError    func(error)
	open       socketOpener

	running bool
	cancel  context.CancelFunc
	done    chan struct{}
	table   *FlowTable
}

type CaptureStatus struct {
	Running    bool      `json:"running"`
	Interfaces []string  `json:"interfaces"`
	FlowStats  FlowStats `json:"flowStats"`
}

func NewController(interfaces []string, udpAll bool, emit func(Suspect), onError func(error)) *Controller {
	return &Controller{
		interfaces: append([]string(nil), interfaces...),
		udpAll:     udpAll,
		emit:       emit,
		onError:    onError,
		open:       openPacketSocket,
		table:      NewFlowTable(udpAll),
	}
}

func (c *Controller) Start(parent context.Context) error {
	c.opMu.Lock()
	defer c.opMu.Unlock()
	c.mu.Lock()
	if c.running {
		c.mu.Unlock()
		return nil
	}
	if c.done != nil {
		select {
		case <-c.done:
			c.done = nil
		default:
			c.mu.Unlock()
			return errors.New("previous capture is still stopping")
		}
	}
	c.mu.Unlock()
	if err := parent.Err(); err != nil {
		return err
	}
	if len(c.interfaces) == 0 {
		return errors.New("no LAN bridge interfaces configured")
	}

	sockets := make([]packetSocket, 0, len(c.interfaces))
	for _, name := range c.interfaces {
		socket, err := c.open(name)
		if err != nil {
			for _, opened := range sockets {
				_ = opened.Close()
			}
			return fmt.Errorf("open AF_PACKET %s: %w", name, err)
		}
		sockets = append(sockets, socket)
	}
	ctx, cancel := context.WithCancel(parent)
	table := NewFlowTable(c.udpAll)
	done := make(chan struct{})
	c.mu.Lock()
	c.table = table
	c.cancel = cancel
	c.done = done
	c.running = true
	c.mu.Unlock()
	go c.run(ctx, sockets, table, done)
	return nil
}

func (c *Controller) run(ctx context.Context, sockets []packetSocket, table *FlowTable, done chan struct{}) {
	runCtx, cancel := context.WithCancel(ctx)
	defer cancel()
	var readers sync.WaitGroup
	for _, socket := range sockets {
		readers.Add(1)
		go func(socket packetSocket) {
			defer readers.Done()
			defer socket.Close()
			c.readLoop(runCtx, cancel, socket, table)
		}(socket)
	}
	ticker := time.NewTicker(500 * time.Millisecond)
	defer ticker.Stop()
	defer func() {
		cancel()
		readers.Wait()
		table.Reset()
		c.mu.Lock()
		if c.table == table {
			c.running = false
		}
		c.mu.Unlock()
		close(done)
	}()
	for {
		select {
		case <-runCtx.Done():
			return
		case now := <-ticker.C:
			for _, suspect := range table.Scan(now) {
				if c.emit != nil {
					c.emit(suspect)
				}
			}
		}
	}
}

func (c *Controller) readLoop(ctx context.Context, cancel context.CancelFunc, socket packetSocket, table *FlowTable) {
	buffer := make([]byte, 65535)
	for {
		n, err := socket.Read(buffer)
		if err == nil && n > 0 {
			if suspect := table.Handle(buffer[:n], time.Now()); suspect != nil && c.emit != nil {
				c.emit(*suspect)
			}
			continue
		}
		if errors.Is(err, errReadTimeout) {
			select {
			case <-ctx.Done():
				return
			default:
				continue
			}
		}
		if ctx.Err() != nil {
			return
		}
		if err != nil {
			cancel()
			if c.onError != nil {
				c.onError(err)
			}
			return
		}
	}
}

// Pause closes the current socket generation and clears all passive state.
func (c *Controller) Pause() error {
	c.opMu.Lock()
	defer c.opMu.Unlock()
	c.mu.Lock()
	done := c.done
	cancel := c.cancel
	c.mu.Unlock()
	if done == nil {
		return nil
	}
	if cancel != nil {
		cancel()
	}
	<-done
	c.mu.Lock()
	c.done = nil
	c.cancel = nil
	c.running = false
	c.mu.Unlock()
	return nil
}

func (c *Controller) Resume(ctx context.Context) error { return c.Start(ctx) }

func (c *Controller) Stop() error { return c.Pause() }

func (c *Controller) Status() CaptureStatus {
	c.mu.Lock()
	defer c.mu.Unlock()
	status := CaptureStatus{Running: c.running, Interfaces: append([]string(nil), c.interfaces...)}
	if c.table != nil {
		status.FlowStats = c.table.Stats()
	}
	return status
}
