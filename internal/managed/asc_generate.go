package managed

import (
	"encoding/base64"
	"encoding/json"
	"fmt"
	"math/rand"

	"github.com/hoaxisr/awg-manager/internal/ndms"
)

func rndInt(r *rand.Rand, a, b int) int {
	if b <= a {
		return a
	}
	return r.Intn(b-a+1) + a
}

func generateHRanges(r *rand.Rand) [4]string {
	bases := [4]int64{100_000_000, 1_200_000_000, 2_400_000_000, 3_600_000_000}
	var out [4]string
	for i, base := range bases {
		offset := int64(rndInt(r, 0, 4_000_000))
		spread := int64(rndInt(r, 100_000, 500_000))
		start := base + offset
		out[i] = fmt.Sprintf("%d-%d", start, start+spread)
	}
	return out
}

func generateHSingle(r *rand.Rand) [4]string {
	bases := [4]int64{100_000_000, 1_200_000_000, 2_400_000_000, 3_600_000_000}
	var out [4]string
	for i, base := range bases {
		out[i] = fmt.Sprintf("%d", base+int64(rndInt(r, 0, 4_000_000)))
	}
	return out
}

func generateASCParamsRaw(extended bool, hRanges bool, src rand.Source) (json.RawMessage, error) {
	r := rand.New(src)
	jmin := rndInt(r, 64, 128)
	jmax := rndInt(r, 256, 512)
	if jmax <= jmin {
		jmax = jmin + 1
	}

	h := generateHSingle(r)
	if hRanges {
		h = generateHRanges(r)
	}

	s1 := rndInt(r, 15, 32)
	s2 := rndInt(r, 15, 32)
	for s1+56 == s2 {
		s2 = rndInt(r, 15, 32)
	}

	if !extended {
		base := ndms.ASCParams{
			Jc:   rndInt(r, 3, 8),
			Jmin: jmin,
			Jmax: jmax,
			S1:   s1,
			S2:   s2,
			H1:   h[0],
			H2:   h[1],
			H3:   h[2],
			H4:   h[3],
		}
		return marshalNoEscape(base)
	}

	ext := ndms.ASCParamsExtended{
		ASCParams: ndms.ASCParams{
			Jc:   rndInt(r, 3, 8),
			Jmin: jmin,
			Jmax: jmax,
			S1:   s1,
			S2:   s2,
			H1:   h[0],
			H2:   h[1],
			H3:   h[2],
			H4:   h[3],
		},
		S3: rndInt(r, 8, 24),
		S4: rndInt(r, 6, 18),
	}
	return marshalNoEscape(ext)
}

// generateASC31ParamsRaw — ASC сервера AWG 3.1. Набор 3.x прошивка принимает
// только целиком (неполный молча игнорирует), поэтому пишется весь
// ndms.ASCParamsAWG3. При защите заголовков nonce берётся из первых 12 байт
// S-паддинга: каждый S ≥ 12, иначе прошивка отказывает («S1-S4 must be at
// least 12 when HeaderProtectionKey is set», стенд 5.02.B.1). Заголовок
// шифруется, поэтому H — 1..4. Тайминги и content padding — 0 («не задано»).
func generateASC31ParamsRaw(src rand.Source, hpKey []byte) (json.RawMessage, error) {
	r := rand.New(src)
	jmin := rndInt(r, 64, 128)
	p := ndms.ASCParamsAWG3{
		ASCParamsExtended: ndms.ASCParamsExtended{
			ASCParams: ndms.ASCParams{
				Jc:   rndInt(r, 3, 8),
				Jmin: jmin,
				Jmax: rndInt(r, 256, 512),
				S1:   rndInt(r, 15, 32),
				S2:   rndInt(r, 15, 32),
				H1:   "1",
				H2:   "2",
				H3:   "3",
				H4:   "4",
			},
			S3: rndInt(r, 12, 24),
			S4: rndInt(r, 12, 18),
		},
		HeaderProtectionKey: base64.StdEncoding.EncodeToString(hpKey),
		RandomTrailers:      1,
	}
	return marshalNoEscape(p)
}
