package webdav

import (
	"bytes"
	"context"
	"encoding/binary"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/p1neappleXpress/OpenFlux/netbind"
	"github.com/p1neappleXpress/OpenFlux/transport"
	"github.com/p1neappleXpress/OpenFlux/utils"
)

const (
	DefaultSlotCount     = 64
	DefaultBatchMaxBytes = 256 * 1024 // 256 KB
	HardBatchMaxBytes    = 512 * 1024 // 512 KB
	AdaptiveFlushTimeout = 40 * time.Millisecond
	NumPutWorkers        = 4
	NumGetWorkers        = 4
	MaxFetchAhead        = 8
	MagicHeader          = 0x4F465744 // "OFWD"
)

var epochCounter atomic.Int64

type putJob struct {
	seq   uint64
	batch [][]byte
}

type initPayload struct {
	Epoch int64  `json:"epoch"`
	Seq   uint64 `json:"seq"`
}

// WebDAVTransport implements a high-speed, block-streaming transport over WebDAV
// (Mail.ru Cloud, Yandex.Disk, Nextcloud, etc.).
type WebDAVTransport struct {
	*transport.BaseTransport

	baseURL    string
	user       string
	pass       string
	isExit     bool
	sendPrefix string
	recvPrefix string

	httpClient *http.Client
	sendQueue  chan []byte
	putJobs    chan putJob
	deleteChan chan string

	nextSendSeq atomic.Uint64
	epoch       int64

	reorderMu          sync.Mutex
	reorderCond        *sync.Cond
	reorderBuf         map[uint64][][]byte
	nextDeliverSeq     uint64
	nextFetchSeq       uint64
	maxAllowedFetchSeq uint64
	peerEpoch          int64
	isInitPolling      bool
}

// NewWebDAVTransport creates an initialized WebDAV transport instance.
func NewWebDAVTransport(rawURL, user, pass string, config transport.TransportConfig, isExit bool) *WebDAVTransport {
	cleanURL := strings.TrimSpace(rawURL)
	if cleanURL == "" {
		cleanURL = "https://webdav.cloud.mail.ru/openflux/"
	}

	// Parse credentials if embedded in URL (https://user:pass@host/path)
	if parsed, err := url.Parse(cleanURL); err == nil {
		if parsed.User != nil {
			if user == "" {
				user = parsed.User.Username()
			}
			if pass == "" {
				pass, _ = parsed.User.Password()
			}
			parsed.User = nil
			cleanURL = parsed.String()
		}
	}
	if !strings.HasSuffix(cleanURL, "/") {
		cleanURL += "/"
	}

	sendPfx := "c2s_"
	recvPfx := "s2c_"
	if isExit {
		sendPfx = "s2c_"
		recvPfx = "c2s_"
	}

	t := &WebDAVTransport{
		BaseTransport:      transport.NewBaseTransport(config),
		baseURL:            cleanURL,
		user:               user,
		pass:               pass,
		isExit:             isExit,
		sendPrefix:         sendPfx,
		recvPrefix:         recvPfx,
		sendQueue:          make(chan []byte, config.MaxQueueSize),
		putJobs:            make(chan putJob, 32),
		deleteChan:         make(chan string, 256),
		reorderBuf:         make(map[uint64][][]byte),
		nextDeliverSeq:     1,
		nextFetchSeq:       1,
		maxAllowedFetchSeq: 1,
		epoch:              time.Now().UnixNano() + epochCounter.Add(1),
	}
	t.reorderCond = sync.NewCond(&t.reorderMu)

	t.httpClient = &http.Client{
		Timeout: 30 * time.Second,
		Transport: &http.Transport{
			Proxy: http.ProxyFromEnvironment,
			DialContext: netbind.Wrap(&net.Dialer{
				Timeout:   10 * time.Second,
				KeepAlive: 30 * time.Second,
			}).DialContext,
			MaxIdleConns:        32,
			MaxIdleConnsPerHost: 16,
			IdleConnTimeout:     90 * time.Second,
			TLSHandshakeTimeout: 10 * time.Second,
			ForceAttemptHTTP2:   true,
		},
	}

	return t
}

// SetHTTPClient overrides the default HTTP client (primarily for unit tests).
func (t *WebDAVTransport) SetHTTPClient(client *http.Client) {
	if client != nil {
		t.httpClient = client
	}
}

func (t *WebDAVTransport) setAuth(req *http.Request) {
	if t.user != "" || t.pass != "" {
		req.SetBasicAuth(t.user, t.pass)
	}
}

func (t *WebDAVTransport) chunkURL(prefix string, epoch int64, seq uint64) string {
	return fmt.Sprintf("%s%s%x_%d.bin", t.baseURL, prefix, epoch, seq)
}

func (t *WebDAVTransport) initURL(prefix string) string {
	return fmt.Sprintf("%s%sinit.json", t.baseURL, prefix)
}

// NextDeliverSeq returns the current deliver sequence under lock (for tests and diagnostics).
func (t *WebDAVTransport) NextDeliverSeq() uint64 {
	t.reorderMu.Lock()
	defer t.reorderMu.Unlock()
	return t.nextDeliverSeq
}

// PeerEpoch returns the learned peer epoch under lock (for tests and diagnostics).
func (t *WebDAVTransport) PeerEpoch() int64 {
	t.reorderMu.Lock()
	defer t.reorderMu.Unlock()
	return t.peerEpoch
}

// EncodeChunk packs a sequence number and packet batch into a framed binary chunk.
func EncodeChunk(seq uint64, batch [][]byte) []byte {
	totalPayload := 0
	for _, pkt := range batch {
		totalPayload += len(pkt)
	}
	buf := make([]byte, 16+len(batch)*4+totalPayload)
	binary.BigEndian.PutUint32(buf[0:4], MagicHeader)
	binary.BigEndian.PutUint64(buf[4:12], seq)
	binary.BigEndian.PutUint32(buf[12:16], uint32(len(batch)))
	offset := 16
	for _, pkt := range batch {
		binary.BigEndian.PutUint32(buf[offset:offset+4], uint32(len(pkt)))
		offset += 4
		copy(buf[offset:], pkt)
		offset += len(pkt)
	}
	return buf
}

// DecodeChunk unpacks a binary chunk into sequence number and individual packets.
func DecodeChunk(data []byte) (uint64, [][]byte, error) {
	if len(data) < 16 {
		return 0, nil, fmt.Errorf("webdav: chunk too short (%d bytes)", len(data))
	}
	magic := binary.BigEndian.Uint32(data[0:4])
	if magic != MagicHeader {
		return 0, nil, fmt.Errorf("webdav: invalid magic 0x%x", magic)
	}
	seq := binary.BigEndian.Uint64(data[4:12])
	count := int(binary.BigEndian.Uint32(data[12:16]))
	offset := 16
	packets := make([][]byte, 0, count)
	for i := 0; i < count; i++ {
		if offset+4 > len(data) {
			return 0, nil, fmt.Errorf("webdav: truncated frame header at index %d", i)
		}
		pktLen := int(binary.BigEndian.Uint32(data[offset : offset+4]))
		offset += 4
		if offset+pktLen > len(data) {
			return 0, nil, fmt.Errorf("webdav: truncated frame data (needed %d, got %d)", pktLen, len(data)-offset)
		}
		pkt := make([]byte, pktLen)
		copy(pkt, data[offset:offset+pktLen])
		offset += pktLen
		packets = append(packets, pkt)
	}
	return seq, packets, nil
}

// PreferredBatchBytes caps batched framing chunks at DefaultBatchMaxBytes
// so each WebDAV PUT stays within typical provider limits.
func (t *WebDAVTransport) PreferredBatchBytes() int {
	return DefaultBatchMaxBytes
}

// Start launches the WebDAV transport workers and initializes the cloud collection.
func (t *WebDAVTransport) Start() error {
	if err := t.BaseTransport.Start(); err != nil {
		return err
	}

	t.nextSendSeq.Store(0)
	t.reorderMu.Lock()
	t.nextDeliverSeq = 1
	t.nextFetchSeq = 1
	t.maxAllowedFetchSeq = 1
	t.peerEpoch = 0
	t.isInitPolling = false
	t.reorderBuf = make(map[uint64][][]byte)
	t.reorderMu.Unlock()

	// 1. Ensure remote collection exists (MKCOL)
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	_ = t.ensureCollection(ctx)
	cancel()

	// 2. Publish our session init marker
	t.publishInitMarker()

	// 3. Launch background routines
	utils.SafeGo("webdav.deleteWorker", t.deleteWorker)
	for i := 0; i < NumPutWorkers; i++ {
		utils.SafeGo(fmt.Sprintf("webdav.putWorker-%d", i), t.putWorker)
	}
	for i := 0; i < NumGetWorkers; i++ {
		utils.SafeGo(fmt.Sprintf("webdav.getWorker-%d", i), t.getWorker)
	}
	utils.SafeGo("webdav.delivererLoop", t.delivererLoop)
	utils.SafeGo("webdav.writerLoop", t.writerLoop)
	utils.SafeGo("webdav.reorderTicker", func() {
		ticker := time.NewTicker(500 * time.Millisecond)
		defer ticker.Stop()
		for t.IsRunning() {
			select {
			case <-ticker.C:
				t.reorderMu.Lock()
				t.reorderCond.Broadcast()
				t.reorderMu.Unlock()
			case <-t.Done():
				return
			}
		}
	})

	t.SetConnected(true)
	maskedUser := t.user
	if maskedUser == "" {
		maskedUser = "<none>"
	}
	utils.Debugf("[WEBDAV] Started %s transport on %s (user: %s, epoch: %x)", t.sendPrefix, t.baseURL, maskedUser, t.epoch)
	return nil
}

// Stop cleanly terminates all transport workers and closes connections.
func (t *WebDAVTransport) Stop() error {
	t.SetConnected(false)
	_ = t.BaseTransport.Stop()

	t.reorderMu.Lock()
	t.reorderCond.Broadcast()
	t.reorderMu.Unlock()

	if t.httpClient != nil {
		t.httpClient.CloseIdleConnections()
	}
	return nil
}

// Send queues data for adaptive batch transmission over WebDAV.
func (t *WebDAVTransport) Send(data []byte) error {
	if !t.IsRunning() {
		return fmt.Errorf("webdav: transport not running")
	}
	pkt := make([]byte, len(data))
	copy(pkt, data)

	select {
	case t.sendQueue <- pkt:
		return nil
	case <-t.Done():
		return fmt.Errorf("webdav: transport stopped")
	default:
		select {
		case t.sendQueue <- pkt:
			return nil
		case <-time.After(50 * time.Millisecond):
			return fmt.Errorf("webdav: send queue full")
		case <-t.Done():
			return fmt.Errorf("webdav: transport stopped")
		}
	}
}

func (t *WebDAVTransport) ensureCollection(ctx context.Context) error {
	req, err := http.NewRequestWithContext(ctx, "MKCOL", t.baseURL, nil)
	if err != nil {
		return err
	}
	t.setAuth(req)
	resp, err := t.httpClient.Do(req)
	if err != nil {
		return nil
	}
	_ = resp.Body.Close()
	return nil
}

func (t *WebDAVTransport) publishInitMarker() {
	payload, err := json.Marshal(initPayload{
		Epoch: t.epoch,
		Seq:   1,
	})
	if err != nil {
		return
	}
	for attempt := 0; attempt < 3; attempt++ {
		req, err := http.NewRequest("PUT", t.initURL(t.sendPrefix), bytes.NewReader(payload))
		if err != nil {
			return
		}
		req.Header.Set("Content-Type", "application/json")
		t.setAuth(req)
		resp, err := t.httpClient.Do(req)
		if err == nil {
			_ = resp.Body.Close()
			if resp.StatusCode >= 200 && resp.StatusCode <= 204 {
				return
			}
		}
		time.Sleep(50 * time.Millisecond)
	}
}

func (t *WebDAVTransport) checkPeerInit() *initPayload {
	req, err := http.NewRequest("GET", t.initURL(t.recvPrefix), nil)
	if err != nil {
		return nil
	}
	req.Header.Set("Cache-Control", "no-cache")
	t.setAuth(req)
	resp, err := t.httpClient.Do(req)
	if err != nil {
		return nil
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil
	}
	data, err := io.ReadAll(io.LimitReader(resp.Body, 1024))
	if err != nil {
		return nil
	}
	var init initPayload
	if err := json.Unmarshal(data, &init); err != nil {
		return nil
	}
	return &init
}

func (t *WebDAVTransport) writerLoop() {
	var batch [][]byte
	var batchBytes int
	var timer *time.Timer
	var timerC <-chan time.Time

	stopTimer := func() {
		if timer != nil {
			if !timer.Stop() {
				select {
				case <-timer.C:
				default:
				}
			}
			timer = nil
			timerC = nil
		}
	}

	defer stopTimer()

	for t.IsRunning() {
		select {
		case <-t.Done():
			return
		case pkt, ok := <-t.sendQueue:
			if !ok {
				return
			}
			batch = append(batch, pkt)
			batchBytes += len(pkt)

			if len(batch) == 1 {
				timer = time.NewTimer(AdaptiveFlushTimeout)
				timerC = timer.C
			}

			if batchBytes >= DefaultBatchMaxBytes {
				stopTimer()
				t.dispatchBatch(batch)
				batch = nil
				batchBytes = 0
			}
		case <-timerC:
			stopTimer()
			if len(batch) > 0 {
				t.dispatchBatch(batch)
				batch = nil
				batchBytes = 0
			}
		}
	}
}

func (t *WebDAVTransport) dispatchBatch(batch [][]byte) {
	seq := t.nextSendSeq.Add(1)
	job := putJob{
		seq:   seq,
		batch: batch,
	}
	select {
	case t.putJobs <- job:
	case <-t.Done():
	}
}

func (t *WebDAVTransport) putWorker() {
	for t.IsRunning() {
		select {
		case <-t.Done():
			return
		case job, ok := <-t.putJobs:
			if !ok {
				return
			}
			t.executePut(job)
		}
	}
}

func (t *WebDAVTransport) executePut(job putJob) {
	data := EncodeChunk(job.seq, job.batch)
	urlStr := t.chunkURL(t.sendPrefix, t.epoch, job.seq)

	for attempt := 0; attempt < 3 && t.IsRunning(); attempt++ {
		req, err := http.NewRequest("PUT", urlStr, bytes.NewReader(data))
		if err != nil {
			return
		}
		req.Header.Set("Content-Type", "application/octet-stream")
		req.ContentLength = int64(len(data))
		t.setAuth(req)

		resp, err := t.httpClient.Do(req)
		if err == nil {
			_ = resp.Body.Close()
			if resp.StatusCode >= 200 && resp.StatusCode <= 204 {
				t.RecordSend(len(data))
				if job.seq > DefaultSlotCount {
					t.queueDelete(t.chunkURL(t.sendPrefix, t.epoch, job.seq-DefaultSlotCount))
				}
				return
			}
		}
		select {
		case <-time.After(time.Duration(20*(attempt+1)) * time.Millisecond):
		case <-t.Done():
			return
		}
	}
}

func (t *WebDAVTransport) getWorker() {
	for t.IsRunning() {
		// 1. Wait until peerEpoch is discovered from init.json
		t.reorderMu.Lock()
		for t.IsRunning() && t.peerEpoch == 0 {
			if t.isInitPolling {
				t.reorderCond.Wait()
				continue
			}
			t.isInitPolling = true
			t.reorderMu.Unlock()

			init := t.checkPeerInit()

			t.reorderMu.Lock()
			t.isInitPolling = false
			if init != nil && init.Epoch != 0 {
				t.peerEpoch = init.Epoch
				t.nextDeliverSeq = init.Seq
				t.nextFetchSeq = init.Seq
				t.maxAllowedFetchSeq = init.Seq
				t.reorderCond.Broadcast()
			} else {
				t.reorderCond.Broadcast()
				t.reorderMu.Unlock()
				select {
				case <-time.After(150 * time.Millisecond):
				case <-t.Done():
					return
				}
				t.reorderMu.Lock()
			}
		}
		if !t.IsRunning() {
			t.reorderMu.Unlock()
			return
		}

		// 2. Fetch window gating:
		// In idle mode, maxAllowedFetchSeq == nextDeliverSeq, so only ONE worker polls.
		// Parallel prefetch (up to MaxFetchAhead) only opens during active bursts (chunk >= 64KB).
		for t.IsRunning() && t.peerEpoch != 0 && (t.nextFetchSeq > t.maxAllowedFetchSeq || t.nextFetchSeq >= t.nextDeliverSeq+MaxFetchAhead) {
			t.reorderCond.Wait()
		}
		if !t.IsRunning() || t.peerEpoch == 0 {
			t.reorderMu.Unlock()
			continue
		}

		seq := t.nextFetchSeq
		curEpoch := t.peerEpoch
		t.nextFetchSeq++
		t.reorderMu.Unlock()

		data, err := t.fetchChunkWithRetry(seq, curEpoch)
		if err != nil {
			continue
		}

		decodedSeq, packets, err := DecodeChunk(data)
		if err != nil {
			utils.Debugf("[WEBDAV] Corrupt chunk %d: %v", seq, err)
			t.reorderMu.Lock()
			if curEpoch == t.peerEpoch {
				if t.maxAllowedFetchSeq < seq+1 {
					t.maxAllowedFetchSeq = seq + 1
				}
				t.reorderBuf[seq] = [][]byte{} // advance deliverer past corrupt chunk
				t.reorderCond.Broadcast()
			}
			t.reorderMu.Unlock()
			continue
		}

		t.reorderMu.Lock()
		if curEpoch == t.peerEpoch {
			t.reorderBuf[decodedSeq] = packets
			if len(data) >= 64*1024 {
				// High speed burst: open parallel prefetch window up to MaxFetchAhead
				if t.maxAllowedFetchSeq < decodedSeq+MaxFetchAhead {
					t.maxAllowedFetchSeq = decodedSeq + MaxFetchAhead
				}
			} else {
				// Single packet / idle ping / ACK: advance by 1
				if t.maxAllowedFetchSeq < decodedSeq+1 {
					t.maxAllowedFetchSeq = decodedSeq + 1
				}
			}
			t.reorderCond.Broadcast()
		}
		t.reorderMu.Unlock()

		t.queueDelete(t.chunkURL(t.recvPrefix, curEpoch, decodedSeq))
	}
}

func (t *WebDAVTransport) fetchChunkWithRetry(seq uint64, epoch int64) ([]byte, error) {
	urlStr := t.chunkURL(t.recvPrefix, epoch, seq)
	attempts := 0

	for t.IsRunning() {
		req, err := http.NewRequest("GET", urlStr, nil)
		if err != nil {
			return nil, err
		}
		req.Header.Set("Cache-Control", "no-cache")
		t.setAuth(req)

		resp, err := t.httpClient.Do(req)
		rateLimited := false
		if err == nil {
			if resp.StatusCode == http.StatusOK {
				data, err := io.ReadAll(resp.Body)
				_ = resp.Body.Close()
				if err == nil {
					t.RecordReceive(len(data))
					return data, nil
				}
			} else {
				if resp.StatusCode == http.StatusTooManyRequests || resp.StatusCode == http.StatusServiceUnavailable {
					rateLimited = true
				}
				_ = resp.Body.Close()
			}
		}

		attempts++

		// Check if chunk has become obsolete because deliverer or epoch advanced
		if attempts%5 == 0 {
			t.reorderMu.Lock()
			stale := seq < t.nextDeliverSeq || epoch != t.peerEpoch
			t.reorderMu.Unlock()
			if stale {
				return nil, fmt.Errorf("webdav: chunk %d superseded", seq)
			}
		}

		if attempts%40 == 0 {
			// Periodic check after stalls: peer may have restarted with new epoch
			if init := t.checkPeerInit(); init != nil {
				t.reorderMu.Lock()
				if init.Epoch != t.peerEpoch && init.Epoch != 0 {
					t.peerEpoch = init.Epoch
					t.nextDeliverSeq = init.Seq
					t.nextFetchSeq = init.Seq
					t.maxAllowedFetchSeq = init.Seq
					t.reorderBuf = make(map[uint64][][]byte)
					t.reorderCond.Broadcast()
					t.reorderMu.Unlock()
					return nil, fmt.Errorf("webdav: peer session re-initialized")
				}
				t.reorderMu.Unlock()
			}
		}

		var sleepDuration time.Duration
		if rateLimited {
			sleepDuration = 500 * time.Millisecond
		} else if attempts <= 10 {
			sleepDuration = 35 * time.Millisecond
		} else if attempts <= 30 {
			sleepDuration = 80 * time.Millisecond
		} else {
			sleepDuration = 150 * time.Millisecond
		}

		select {
		case <-time.After(sleepDuration):
		case <-t.Done():
			return nil, fmt.Errorf("webdav: stopped")
		}
	}
	return nil, fmt.Errorf("webdav: stopped")
}

func (t *WebDAVTransport) delivererLoop() {
	var missingSeq uint64
	var missingSince time.Time

	for t.IsRunning() {
		t.reorderMu.Lock()
		for t.IsRunning() && t.reorderBuf[t.nextDeliverSeq] == nil {
			// If subsequent chunks have arrived while current is missing, prevent perpetual stall
			hasHigher := false
			for s := range t.reorderBuf {
				if s > t.nextDeliverSeq {
					hasHigher = true
					break
				}
			}
			if hasHigher {
				now := time.Now()
				if missingSeq != t.nextDeliverSeq {
					missingSeq = t.nextDeliverSeq
					missingSince = now
				} else if now.Sub(missingSince) > 3*time.Second {
					utils.Debugf("[WEBDAV] Chunk %d stalled in reorder buffer, advancing to unblock stream", t.nextDeliverSeq)
					t.nextDeliverSeq++
					if t.maxAllowedFetchSeq < t.nextDeliverSeq {
						t.maxAllowedFetchSeq = t.nextDeliverSeq
					}
					missingSeq = t.nextDeliverSeq
					missingSince = now
					t.reorderCond.Broadcast()
					continue
				}
			} else {
				missingSeq = 0
			}

			t.reorderCond.Wait()
		}
		if !t.IsRunning() {
			t.reorderMu.Unlock()
			return
		}
		packets := t.reorderBuf[t.nextDeliverSeq]
		delete(t.reorderBuf, t.nextDeliverSeq)
		t.nextDeliverSeq++
		if t.maxAllowedFetchSeq < t.nextDeliverSeq {
			t.maxAllowedFetchSeq = t.nextDeliverSeq
		}
		missingSeq = 0
		t.reorderCond.Broadcast()
		t.reorderMu.Unlock()

		for _, pkt := range packets {
			t.CallReceive(pkt)
		}
	}
}

func (t *WebDAVTransport) queueDelete(targetURL string) {
	select {
	case t.deleteChan <- targetURL:
	default:
	}
}

func (t *WebDAVTransport) deleteWorker() {
	for t.IsRunning() {
		select {
		case <-t.Done():
			return
		case targetURL, ok := <-t.deleteChan:
			if !ok {
				return
			}
			req, err := http.NewRequest("DELETE", targetURL, nil)
			if err == nil {
				t.setAuth(req)
				resp, err := t.httpClient.Do(req)
				if err == nil {
					_ = resp.Body.Close()
				}
			}
		}
	}
}
