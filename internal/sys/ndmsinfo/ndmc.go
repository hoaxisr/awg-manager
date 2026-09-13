package ndmsinfo

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/hoaxisr/awg-manager/internal/ndms"
	"github.com/hoaxisr/awg-manager/internal/sys/exec"
)

// Второй канал к той же правде. RCI — это HTTP на 127.0.0.1:79, и когда он
// не отвечает (замерено на стенде: вхолостую 20-25 мс, но под нагрузкой
// собственной же проводки 709 мс, а в поле видели 1.5-1.9 с), демон до сих
// пор оставался без версии, а значит и без правильного представления о
// роутере. `ndmc` ходит через unix-сокет /var/run/ndm.core.socket — другой
// транспорт, тот же ndm. Проверено на стенде при ПОЛНОСТЬЮ заблокированном
// :79: ndmc отдаёт и release, и список компонентов.
//
// Почему это не основной канал: каждый вызов оставляет в журнале роутера
// две строки (Core::Session connected/disconnected) — ровно та причина, по
// которой проект избегает ndmc в рантайме (internal/managed/rci.go). Здесь
// он звучит один раз за старт и только после отказа RCI.
// ndmcBinary — переменная, а не константа, ради шва для теста: иначе вся
// функция versionFromNdmc (включая стража формы релиза) не проверяется ничем.
// Бинарь есть на всех версиях прошивки и от версии не зависит.
var ndmcBinary = "/bin/ndmc"

// ndmcTimeout — запас к замеренным 50-70 мс.
const ndmcTimeout = 5 * time.Second

// versionFromNdmc спрашивает версию у ndm через unix-сокет.
func versionFromNdmc(ctx context.Context) (ndms.Version, error) {
	res, err := exec.RunWithOptions(ctx, ndmcBinary, []string{"-c", "show version"},
		exec.Options{Timeout: ndmcTimeout})
	if err != nil {
		return ndms.Version{}, fmt.Errorf("ndmc show version: %w", exec.FormatError(res, err))
	}
	v := parseNdmcVersion(res.Stdout)
	if !looksLikeRelease(v.Release) {
		// Пустая или неправдоподобная строка релиза — отказ, а не усыновление.
		// Усыновлённая неправда неисправима: по ней замораживается выбор
		// оператора, а отказ здесь означает «подождём», что безопасно.
		return ndms.Version{}, fmt.Errorf("ndmc show version: непригодный релиз %q", v.Release)
	}
	// Усыновляется вся структура, а не одно поле, поэтому и проверять надо не
	// одно поле. Обрезанный вывод — реальный путь: exec при ErrWaitDelay
	// обнуляет ошибку с пометкой «output may be truncated», и срез вполне
	// может прийтись после release и до components. Пустой список компонентов
	// усыновляется навсегда (у стора нет ни TTL, ни инвалидации) и означает
	// HasWireguardComponent()==false — то есть бэкенд nativewg объявлен
	// недоступным до перезапуска демона. На Keenetic пустого списка не бывает.
	if len(v.Components) == 0 {
		return ndms.Version{}, fmt.Errorf("ndmc show version: пустой список компонентов — вывод обрезан")
	}
	v.LastFetched = time.Now()
	return v, nil
}

// maxNdmcOutput — потолок разбираемого вывода. Вывод команды ничем не
// ограничен (exec копит его в bytes.Buffer), а склейка продолжений на
// мегабайтах даёт квадратичную работу: 400 КБ — 1.4 с, 4 МБ — 32 с на x86,
// на mipsel c десятками мегабайт ОЗУ это жор CPU прямо на старте. Реальный
// ответ — около 1.5 КБ.
const maxNdmcOutput = 64 << 10

// looksLikeRelease отбраковывает мусор, попавший в stdout вместо релиза:
// форма «цифры.цифры…», разумная длина. Без этого стражем была лишь
// непустота, и 400 КБ мусора усыновлялись как версия прошивки.
func looksLikeRelease(s string) bool {
	if len(s) == 0 || len(s) > 64 {
		return false
	}
	dot := strings.Index(s, ".")
	if dot <= 0 || dot == len(s)-1 {
		return false
	}
	for _, r := range s[:dot] {
		if r < '0' || r > '9' {
			return false
		}
	}
	return s[dot+1] >= '0' && s[dot+1] <= '9'
}

// parseNdmcVersion разбирает текстовый ответ `ndmc -c "show version"`.
//
// Форма ответа (стенд 5.01.C.3.0-1): строки «<отступ>ключ: значение», плюс
// секции с пустым значением (ndm:, bsp:, ndw:). Длинные значения
// ПЕРЕНОСЯТСЯ по ширине и рвутся посреди токена:
//
//	components: acl,base,cloudcontrol,corewireless,dhcpd,dns-
//	            https,dns-tls,...
//
// поэтому продолжения клеятся БЕЗ разделителя. Продолжением считается только
// строка БЕЗ двоеточия: строка с двоеточием — это ключ, пусть и незнакомого
// нам вида, и приклеивать её к предыдущему значению нельзя.
//
// Вложенные ключи (exact/cdate под ndm и bsp, version под ndw4) разбор видит
// плоско, поэтому побеждает ПЕРВОЕ вхождение: иначе вложенный `release` под
// ndw4 перебил бы верхнеуровневый и объявил четвёрку пятёркой.
func parseNdmcVersion(out string) ndms.Version {
	if len(out) > maxNdmcOutput {
		out = out[:maxNdmcOutput]
	}
	fields := map[string]string{}
	var curKey string
	var curVal strings.Builder

	flush := func() {
		// ПЕРВЫЙ выигрывает. Ответ содержит вложенные ключи (exact/cdate под
		// ndm и bsp, version под ndw4), а разбор плоский: при last-wins
		// вложенный `release` под ndw4 объявил бы четвёрку пятёркой — и с
		// новым умолчанием osdetect это ничем не страхуется.
		if curKey != "" {
			if _, seen := fields[curKey]; !seen {
				fields[curKey] = strings.TrimSpace(curVal.String())
			}
		}
		curKey = ""
		curVal.Reset()
	}

	for _, raw := range strings.Split(out, "\n") {
		trimmed := strings.TrimSpace(stripANSI(raw))
		if trimmed == "" {
			continue
		}
		if key, val, ok := splitNdmcField(trimmed); ok {
			flush()
			curKey = key
			curVal.WriteString(val)
			continue
		}
		// Продолжением считаем ТОЛЬКО строку без двоеточия. Строка с
		// двоеточием — это ключ, просто незнакомого нам вида (дефис, точка,
		// заглавная на другой прошивке); приклеив её к предыдущему значению,
		// мы бы молча испортили соседнее поле, например release.
		if strings.Contains(trimmed, ":") {
			flush()
			continue
		}
		if curKey != "" {
			curVal.WriteString(trimmed) // перенос рвёт токен — клеим встык
		}
	}
	flush()

	v := ndms.Version{
		Release:      fields["release"],
		Title:        fields["title"],
		HardwareID:   fields["hw_id"],
		Description:  fields["description"],
		Manufacturer: fields["manufacturer"],
		Vendor:       fields["vendor"],
		Series:       fields["series"],
		Model:        fields["model"],
		Device:       fields["device"],
		Region:       fields["region"],
	}
	if c := fields["components"]; c != "" {
		for _, part := range strings.Split(c, ",") {
			if part = strings.TrimSpace(part); part != "" {
				v.Components = append(v.Components, part)
			}
		}
	}
	return v
}

// splitNdmcField опознаёт строку вида «ключ: значение». Ключ — только
// [a-z0-9_]: всё прочее (дефис, точка, заглавная) — незнакомый ключ другой
// прошивки, и вызывающий обязан такую строку пропустить, а не приклеить.
func splitNdmcField(s string) (key, val string, ok bool) {
	i := strings.Index(s, ":")
	if i <= 0 {
		return "", "", false
	}
	key = s[:i]
	for _, r := range key {
		if !(r >= 'a' && r <= 'z') && !(r >= '0' && r <= '9') && r != '_' {
			return "", "", false
		}
	}
	return key, strings.TrimSpace(s[i+1:]), true
}

// stripANSI убирает управляющие последовательности: ndmc печатает ^[[K.
func stripANSI(s string) string {
	for {
		i := strings.Index(s, "\x1b[")
		if i < 0 {
			return s
		}
		j := i + 2
		for j < len(s) && (s[j] < '@' || s[j] > '~') {
			j++
		}
		if j < len(s) {
			j++
		}
		s = s[:i] + s[j:]
	}
}
