package metrics

import (
	"context"
	"fmt"
	"strconv"
	"strings"
	"time"

	"github.com/hoaxisr/awg-manager/internal/ndms"
	"github.com/hoaxisr/awg-manager/internal/sys/exec"
)

// wgBin — wg из wireguard-tools (зависимость пакета). Системные WireguardN
// (ядро nwgN) понимает только он: наш awg на них отвечает «Not supported».
const wgBin = "/opt/bin/wg"

// wgShowDump возвращает вывод `wg show <kernel> dump`. Переопределяется в
// тестах.
var wgShowDump = func(ctx context.Context, kernel string) (string, error) {
	res, err := exec.Run(ctx, wgBin, "show", kernel, "dump")
	if err != nil {
		return "", fmt.Errorf("%s show %s dump: %w", wgBin, kernel, exec.FormatError(res, err))
	}
	return res.Stdout, nil
}

// parseWgDump разбирает `wg show <if> dump`: первая строка — сам интерфейс,
// дальше по строке на пира, 8 полей через TAB: public-key, preshared-key,
// endpoint, allowed-ips, latest-handshake (unix, 0 — не было), transfer-rx,
// transfer-tx, persistent-keepalive. Рукопожатие переводится в «секунды
// назад» от now — в той же форме, что у пиров NDMS; не было — -1.
func parseWgDump(out string, now time.Time) ([]ndms.Peer, error) {
	lines := strings.Split(strings.TrimRight(out, "\n"), "\n")
	if lines[0] == "" {
		return nil, fmt.Errorf("wg dump: пустой вывод")
	}
	peers := make([]ndms.Peer, 0, len(lines)-1)
	for _, line := range lines[1:] {
		f := strings.Split(line, "\t")
		if len(f) != 8 {
			return nil, fmt.Errorf("wg dump: строка пира из %d полей, ждали 8", len(f))
		}
		shake, err1 := strconv.ParseInt(f[4], 10, 64)
		rx, err2 := strconv.ParseInt(f[5], 10, 64)
		tx, err3 := strconv.ParseInt(f[6], 10, 64)
		if err1 != nil || err2 != nil || err3 != nil {
			return nil, fmt.Errorf("wg dump: нечисловое поле в строке пира %q", line)
		}
		ago := int64(-1)
		if shake > 0 {
			// Часы ядра чуть впереди наших — рукопожатие «сейчас», а не «не было».
			ago = max(now.Unix()-shake, 0)
		}
		peers = append(peers, ndms.Peer{PublicKey: f[0], RxBytes: rx, TxBytes: tx, LastHandshakeSecondsAgo: ago})
	}
	return peers, nil
}
