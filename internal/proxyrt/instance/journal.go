package instance

import (
	"fmt"
	"strings"

	"github.com/hoaxisr/awg-manager/internal/proxyrt"
)

// Summarize — одна строка на реконсиляцию: фаза, счёт проходов и шагов,
// отказы поимённо с причинами. Больше одной строки журнал на прогон не
// получает (спека §8): подробности — в состоянии инстанса через API.
func Summarize(res proxyrt.Result, phase proxyrt.Phase) string {
	var b strings.Builder
	fmt.Fprintf(&b, "фаза %s, проходов %d, шагов %d", phase, res.Passes, len(res.Steps))
	var bad []string
	for _, st := range res.States {
		if st.Status == proxyrt.StatusFailed || st.Status == proxyrt.StatusUnknown {
			msg := string(st.ID) + ": " + string(st.Status)
			if st.Error != "" {
				msg += " (" + st.Error + ")"
			}
			bad = append(bad, msg)
		}
	}
	if len(bad) > 0 {
		b.WriteString("; ")
		b.WriteString(strings.Join(bad, "; "))
	}
	if res.Stop != proxyrt.StopNone {
		fmt.Fprintf(&b, "; стоп: %s", res.Stop)
	}
	return strings.ReplaceAll(b.String(), "\n", " ")
}
