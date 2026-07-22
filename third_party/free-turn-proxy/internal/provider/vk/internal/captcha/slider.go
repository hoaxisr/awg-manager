package captcha

import (
	"bytes"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"image"
	"image/color"
	_ "image/jpeg" // регистрация JPEG-декодера для image.Decode
	"math"
	"runtime"
	"sort"
	"strconv"
	"strings"
	"sync"

	"github.com/samosvalishe/free-turn-proxy/internal/randx"
)

type sliderPuzzle struct {
	Image    image.Image
	Size     int
	Swaps    []int
	Attempts int
}

type sliderGuess struct {
	Index         int
	Swaps         []int
	Score         int64
	ScoreRGB      int64
	ScoreLuma     int64
	ScoreReverse  int64
	ScoreText     float64
	ConsensusRank int
}

func (s *captchaSession) solveSliderCaptcha(
	sessionToken string,
	browserFP string,
	hash string,
	content captchaContentRef,
	debugInfo string,
) (string, error) {
	if content.Value == "" {
		return "", errors.New("slider content settings missing")
	}
	s.logger().Debugf("[Captcha] slider getContent source=%s len=%d", content.Source, len(content.Value))
	values := [][2]string{
		{"session_token", sessionToken},
		{"domain", s.domain},
		{"adFp", ""},
		{"access_token", ""},
		{"captcha_settings", content.Value},
	}

	resp, err := s.captchaRequest("captchaNotRobot.getContent", values)
	if err != nil {
		return "", fmt.Errorf("slider getContent failed: %w", err)
	}
	puzzle, err := parseSliderPuzzle(resp)
	if err != nil {
		return "", err
	}
	s.logger().Debugf("[Captcha] slider puzzle decoded: grid=%d attempts=%d swaps=%d", puzzle.Size, puzzle.Attempts, len(puzzle.Swaps))

	guesses, err := rankSliderGuesses(puzzle.Image, puzzle.Size, puzzle.Swaps)
	if err != nil {
		return "", err
	}

	limit := puzzle.Attempts
	if limit > len(guesses) {
		limit = len(guesses)
	}
	if limit <= 0 {
		return "", errors.New("slider has no attempts available")
	}
	s.logger().Debugf("[Captcha] slider guesses ranked: total=%d limit=%d", len(guesses), limit)

	deviceJSON := s.profile.DeviceJSON
	s.logger().Debugf("[Captcha] slider componentDone device_bytes=%d", len(deviceJSON))
	if _, err := s.captchaRequest("captchaNotRobot.componentDone", [][2]string{
		{"session_token", sessionToken},
		{"domain", s.domain},
		{"adFp", ""},
		{"access_token", ""},
		{"browser_fp", browserFP},
		{"device", deviceJSON},
	}); err != nil {
		return "", fmt.Errorf("captcha componentDone failed: %w", err)
	}

	for i := 0; i < limit; i++ {
		s.logger().Debugf("[Captcha] slider attempt %d/%d (guess #%d)", i+1, limit, guesses[i].Index)
		answerData, err := json.Marshal(struct {
			Value []int `json:"value"`
		}{Value: guesses[i].Swaps})
		if err != nil {
			return "", err
		}
		check, err := s.performCaptchaCheck(
			sessionToken,
			browserFP,
			hash,
			string(answerData),
			buildSliderCursor(guesses[i].Index, len(guesses)),
			debugInfo,
		)
		if err != nil {
			return "", err
		}
		s.logger().Debugf("[Captcha] slider attempt %d status=%s show_type=%s token_len=%d", i+1, check.Status, check.ShowType, len(check.SuccessToken))
		if strings.EqualFold(check.Status, "ok") {
			if check.SuccessToken == "" {
				return "", errors.New("captcha success token not found")
			}
			s.logger().Infof("[Captcha] slider accepted on attempt %d", i+1)
			return check.SuccessToken, nil
		}
		if strings.EqualFold(check.Status, "error_limit") {
			return "", errCaptchaRateLimit
		}
	}
	return "", errors.New("slider guesses exhausted")
}

func parseSliderPuzzle(raw map[string]any) (*sliderPuzzle, error) {
	resp, ok := raw["response"].(map[string]any)
	if !ok {
		return nil, fmt.Errorf("invalid slider content response: %v", raw)
	}
	status := captchaStringifyAny(resp["status"])
	if strings.EqualFold(status, "error") {
		return nil, fmt.Errorf("%w: slider getContent status: %s", errCaptchaSessionDead, status)
	}
	if !strings.EqualFold(status, "ok") {
		return nil, fmt.Errorf("slider getContent status: %s", status)
	}
	rawImage := captchaStringifyAny(resp["image"])
	if rawImage == "" {
		return nil, errors.New("slider image missing")
	}
	rawSteps, ok := resp["steps"].([]any)
	if !ok {
		return nil, errors.New("slider steps missing")
	}
	steps := make([]int, 0, len(rawSteps))
	for _, item := range rawSteps {
		switch v := item.(type) {
		case float64:
			steps = append(steps, int(v))
		case int:
			steps = append(steps, v)
		case string:
			n, err := strconv.Atoi(strings.TrimSpace(v))
			if err != nil {
				return nil, fmt.Errorf("invalid numeric value: %v", item)
			}
			steps = append(steps, n)
		default:
			return nil, fmt.Errorf("invalid numeric value: %v", item)
		}
	}
	size, swaps, attempts, err := splitSliderSteps(steps)
	if err != nil {
		return nil, err
	}
	data, err := base64.StdEncoding.DecodeString(rawImage)
	if err != nil {
		return nil, fmt.Errorf("decode slider image: %w", err)
	}
	img, _, err := image.Decode(bytes.NewReader(data))
	if err != nil {
		return nil, fmt.Errorf("decode slider image: %w", err)
	}
	return &sliderPuzzle{Image: img, Size: size, Swaps: swaps, Attempts: attempts}, nil
}

func splitSliderSteps(steps []int) (int, []int, int, error) {
	if len(steps) < 3 {
		return 0, nil, 0, errors.New("slider steps payload too short")
	}
	size := steps[0]
	if size <= 0 {
		return 0, nil, 0, fmt.Errorf("invalid slider size: %d", size)
	}
	tail := append([]int(nil), steps[1:]...)
	attempts := 4
	if len(tail)%2 != 0 {
		attempts = tail[len(tail)-1]
		tail = tail[:len(tail)-1]
		Log.Warnf("[Captcha] slider payload had odd-length tail; fallback attempts=%d", attempts)
	}
	if attempts <= 0 {
		attempts = 4
	}
	if len(tail) == 0 || len(tail)%2 != 0 {
		return 0, nil, 0, errors.New("invalid slider swap payload")
	}
	return size, tail, attempts, nil
}

func rankSliderGuesses(img image.Image, gridSize int, swaps []int) ([]sliderGuess, error) {
	candidateCount := len(swaps) / 2
	if candidateCount == 0 {
		return nil, errors.New("slider has no candidates")
	}

	guesses := make([]sliderGuess, candidateCount)
	for idx := 1; idx <= candidateCount; idx++ {
		active := activeSwapsForIndex(swaps, idx)
		mapping, err := applySliderSwaps(gridSize, active)
		if err != nil {
			return nil, err
		}
		guesses[idx-1] = sliderGuess{Index: idx, Swaps: active}
		guesses[idx-1].ScoreLuma = seamScoreLuma(img, gridSize, mapping)
		if len(active) > 2 {
			reverseMapping, err := applySliderSwaps(gridSize, reverseSwapPairs(active))
			if err != nil {
				return nil, err
			}
			guesses[idx-1].ScoreReverse = seamScoreLuma(img, gridSize, reverseMapping)
		} else {
			guesses[idx-1].ScoreReverse = guesses[idx-1].ScoreLuma
		}
	}

	lumaOrder := append([]sliderGuess(nil), guesses...)
	sort.SliceStable(lumaOrder, func(i, j int) bool {
		if lumaOrder[i].ScoreLuma == lumaOrder[j].ScoreLuma {
			return lumaOrder[i].Index < lumaOrder[j].Index
		}
		return lumaOrder[i].ScoreLuma < lumaOrder[j].ScoreLuma
	})
	lumaRank := make(map[int]int, candidateCount)
	for rank, g := range lumaOrder {
		lumaRank[g.Index] = rank
	}

	stage2Count := candidateCount
	stage2Set := make(map[int]struct{}, stage2Count)
	for i := 0; i < stage2Count; i++ {
		stage2Set[lumaOrder[i].Index] = struct{}{}
	}

	type stage2Result struct {
		index int
		rgb   int64
		text  float64
		err   error
	}
	jobs := make([]int, 0, stage2Count)
	for idx := range stage2Set {
		jobs = append(jobs, idx)
	}
	jobCh := make(chan int, len(jobs))
	resCh := make(chan stage2Result, len(jobs))

	workers := runtime.NumCPU()
	if workers < 1 {
		workers = 1
	}
	if workers > len(jobs) {
		workers = len(jobs)
	}
	var wg sync.WaitGroup
	for range workers {
		wg.Go(func() {
			for index := range jobCh {
				mapping, err := applySliderSwaps(gridSize, guesses[index-1].Swaps)
				if err != nil {
					resCh <- stage2Result{index: index, err: err}
					continue
				}
				rgb, text := seamScoreRGBText(img, gridSize, mapping)
				resCh <- stage2Result{index: index, rgb: rgb, text: text}
			}
		})
	}
	for _, idx := range jobs {
		jobCh <- idx
	}
	close(jobCh)
	wg.Wait()
	close(resCh)
	for r := range resCh {
		if r.err != nil {
			return nil, r.err
		}
		g := &guesses[r.index-1]
		g.ScoreRGB = r.rgb
		g.ScoreText = r.text
	}

	stage2 := make([]sliderGuess, 0, stage2Count)
	for _, g := range guesses {
		if _, ok := stage2Set[g.Index]; ok {
			stage2 = append(stage2, g)
		}
	}

	rgbOrder := append([]sliderGuess(nil), stage2...)
	sort.SliceStable(rgbOrder, func(i, j int) bool {
		if rgbOrder[i].ScoreRGB == rgbOrder[j].ScoreRGB {
			return rgbOrder[i].Index < rgbOrder[j].Index
		}
		return rgbOrder[i].ScoreRGB < rgbOrder[j].ScoreRGB
	})
	rgbRank := make(map[int]int, len(rgbOrder))
	for rank, g := range rgbOrder {
		rgbRank[g.Index] = rank
	}

	textOrder := append([]sliderGuess(nil), stage2...)
	sort.SliceStable(textOrder, func(i, j int) bool {
		if textOrder[i].ScoreText == textOrder[j].ScoreText {
			return textOrder[i].Index < textOrder[j].Index
		}
		return textOrder[i].ScoreText < textOrder[j].ScoreText
	})
	textRank := make(map[int]int, len(textOrder))
	for rank, g := range textOrder {
		textRank[g.Index] = rank
	}

	reverseOrder := append([]sliderGuess(nil), guesses...)
	sort.SliceStable(reverseOrder, func(i, j int) bool {
		if reverseOrder[i].ScoreReverse == reverseOrder[j].ScoreReverse {
			return reverseOrder[i].Index < reverseOrder[j].Index
		}
		return reverseOrder[i].ScoreReverse < reverseOrder[j].ScoreReverse
	})
	reverseRank := make(map[int]int, len(reverseOrder))
	for rank, g := range reverseOrder {
		reverseRank[g.Index] = rank
	}

	for i := range guesses {
		g := &guesses[i]
		g.ConsensusRank = lumaRank[g.Index] + reverseRank[g.Index]
		if _, ok := stage2Set[g.Index]; ok {
			g.ConsensusRank += rgbRank[g.Index] + textRank[g.Index]
		} else {
			g.ConsensusRank += candidateCount
		}
		g.Score = int64(g.ConsensusRank)
	}

	sort.SliceStable(guesses, func(i, j int) bool {
		if guesses[i].ConsensusRank == guesses[j].ConsensusRank {
			if guesses[i].ScoreLuma == guesses[j].ScoreLuma {
				return guesses[i].Index < guesses[j].Index
			}
			return guesses[i].ScoreLuma < guesses[j].ScoreLuma
		}
		return guesses[i].ConsensusRank < guesses[j].ConsensusRank
	})
	return guesses, nil
}

func reverseSwapPairs(swaps []int) []int {
	out := make([]int, 0, len(swaps))
	for i := len(swaps) - 2; i >= 0; i -= 2 {
		out = append(out, swaps[i], swaps[i+1])
	}
	return out
}

func activeSwapsForIndex(swaps []int, index int) []int {
	if index <= 0 {
		return []int{}
	}
	end := index * 2
	if end > len(swaps) {
		end = len(swaps)
	}
	return append([]int(nil), swaps[:end]...)
}

func applySliderSwaps(gridSize int, swaps []int) ([]int, error) {
	tileCount := gridSize * gridSize
	if tileCount <= 0 {
		return nil, fmt.Errorf("invalid slider tile count: %d", tileCount)
	}
	if len(swaps)%2 != 0 {
		return nil, fmt.Errorf("invalid slider swaps length: %d", len(swaps))
	}
	mapping := make([]int, tileCount)
	for i := range mapping {
		mapping[i] = i
	}
	for i := 0; i < len(swaps); i += 2 {
		left := swaps[i]
		right := swaps[i+1]
		if left < 0 || right < 0 || left >= tileCount || right >= tileCount {
			return nil, fmt.Errorf("slider step out of range: %d,%d", left, right)
		}
		mapping[left], mapping[right] = mapping[right], mapping[left]
	}
	return mapping, nil
}

func seamScoreLuma(img image.Image, gridSize int, mapping []int) int64 {
	bounds := img.Bounds()
	var score int64
	for row := 0; row < gridSize; row++ {
		for col := 0; col < gridSize-1; col++ {
			leftIdx := row*gridSize + col
			rightIdx := leftIdx + 1
			leftDst := sliderTileRect(bounds, gridSize, leftIdx)
			rightDst := sliderTileRect(bounds, gridSize, rightIdx)
			leftSrc := sliderTileRect(bounds, gridSize, mapping[leftIdx])
			rightSrc := sliderTileRect(bounds, gridSize, mapping[rightIdx])
			h := leftDst.Dy()
			if rightDst.Dy() < h {
				h = rightDst.Dy()
			}
			for y := 0; y < h; y++ {
				yy := leftDst.Min.Y + y
				a := sampleLumaMapped(img, leftDst, leftSrc, leftDst.Max.X-1, yy)
				b := sampleLumaMapped(img, rightDst, rightSrc, rightDst.Min.X, yy)
				score += int64(absInt(int(a) - int(b)))
			}
		}
	}
	for row := 0; row < gridSize-1; row++ {
		for col := 0; col < gridSize; col++ {
			topIdx := row*gridSize + col
			bottomIdx := (row+1)*gridSize + col
			topDst := sliderTileRect(bounds, gridSize, topIdx)
			bottomDst := sliderTileRect(bounds, gridSize, bottomIdx)
			topSrc := sliderTileRect(bounds, gridSize, mapping[topIdx])
			bottomSrc := sliderTileRect(bounds, gridSize, mapping[bottomIdx])
			w := topDst.Dx()
			if bottomDst.Dx() < w {
				w = bottomDst.Dx()
			}
			for x := 0; x < w; x++ {
				xx := topDst.Min.X + x
				a := sampleLumaMapped(img, topDst, topSrc, xx, topDst.Max.Y-1)
				b := sampleLumaMapped(img, bottomDst, bottomSrc, xx, bottomDst.Min.Y)
				score += int64(absInt(int(a) - int(b)))
			}
		}
	}
	return score
}

func seamScoreRGBText(img image.Image, gridSize int, mapping []int) (int64, float64) {
	bounds := img.Bounds()
	height := float64(bounds.Dy())
	textCenters := []float64{
		float64(bounds.Min.Y) + 0.2*height,
		float64(bounds.Min.Y) + 0.5*height,
		float64(bounds.Min.Y) + 0.8*height,
	}
	sigma := height * 0.14
	if sigma < 1.0 {
		sigma = 1.0
	}
	weight := func(y int) float64 {
		yf := float64(y)
		best := absFloat(yf - textCenters[0])
		for i := 1; i < len(textCenters); i++ {
			d := absFloat(yf - textCenters[i])
			if d < best {
				best = d
			}
		}
		return 1 + 3*math.Exp(-(best*best)/(2*sigma*sigma))
	}

	var rgbScore int64
	var textScore float64
	for row := 0; row < gridSize; row++ {
		for col := 0; col < gridSize-1; col++ {
			leftIdx := row*gridSize + col
			rightIdx := leftIdx + 1
			leftDst := sliderTileRect(bounds, gridSize, leftIdx)
			rightDst := sliderTileRect(bounds, gridSize, rightIdx)
			leftSrc := sliderTileRect(bounds, gridSize, mapping[leftIdx])
			rightSrc := sliderTileRect(bounds, gridSize, mapping[rightIdx])
			h := leftDst.Dy()
			if rightDst.Dy() < h {
				h = rightDst.Dy()
			}
			for y := 0; y < h; y++ {
				yy := leftDst.Min.Y + y
				l := sampleColorMapped(img, leftDst, leftSrc, leftDst.Max.X-1, yy)
				r := sampleColorMapped(img, rightDst, rightSrc, rightDst.Min.X, yy)
				rgbScore += pixelDiff(l, r)
				_, _, lb, _ := l.RGBA()
				_, _, rb, _ := r.RGBA()
				textScore += weight(yy) * float64(absInt(int(lb>>8)-int(rb>>8)))
			}
		}
	}
	for row := 0; row < gridSize-1; row++ {
		for col := 0; col < gridSize; col++ {
			topIdx := row*gridSize + col
			bottomIdx := (row+1)*gridSize + col
			topDst := sliderTileRect(bounds, gridSize, topIdx)
			bottomDst := sliderTileRect(bounds, gridSize, bottomIdx)
			topSrc := sliderTileRect(bounds, gridSize, mapping[topIdx])
			bottomSrc := sliderTileRect(bounds, gridSize, mapping[bottomIdx])
			w := topDst.Dx()
			if bottomDst.Dx() < w {
				w = bottomDst.Dx()
			}
			for x := 0; x < w; x++ {
				xx := topDst.Min.X + x
				t := sampleColorMapped(img, topDst, topSrc, xx, topDst.Max.Y-1)
				b := sampleColorMapped(img, bottomDst, bottomSrc, xx, bottomDst.Min.Y)
				rgbScore += pixelDiff(t, b)
				_, _, tb, _ := t.RGBA()
				_, _, bb, _ := b.RGBA()
				textScore += 0.65 * float64(absInt(int(tb>>8)-int(bb>>8)))
			}
		}
	}
	return rgbScore, textScore
}

func sampleColorMapped(img image.Image, dstRect image.Rectangle, srcRect image.Rectangle, dstX int, dstY int) color.Color {
	dx := dstRect.Dx()
	if dx < 1 {
		dx = 1
	}
	dy := dstRect.Dy()
	if dy < 1 {
		dy = 1
	}
	sx := srcRect.Min.X + (dstX-dstRect.Min.X)*srcRect.Dx()/dx
	sy := srcRect.Min.Y + (dstY-dstRect.Min.Y)*srcRect.Dy()/dy
	return img.At(sx, sy)
}

func sampleLumaMapped(img image.Image, dstRect image.Rectangle, srcRect image.Rectangle, dstX int, dstY int) uint8 {
	c := sampleColorMapped(img, dstRect, srcRect, dstX, dstY)
	r, g, b, _ := c.RGBA()
	y := (299*(r>>8) + 587*(g>>8) + 114*(b>>8)) / 1000
	if y > 255 {
		y = 255
	}
	return uint8(y) //nolint:gosec // bounded above by 255
}

func absFloat(v float64) float64 {
	if v < 0 {
		return -v
	}
	return v
}

func absInt(v int) int {
	if v < 0 {
		return -v
	}
	return v
}

func sliderTileRect(bounds image.Rectangle, gridSize int, index int) image.Rectangle {
	row := index / gridSize
	col := index % gridSize
	x0 := bounds.Min.X + col*bounds.Dx()/gridSize
	x1 := bounds.Min.X + (col+1)*bounds.Dx()/gridSize
	y0 := bounds.Min.Y + row*bounds.Dy()/gridSize
	y1 := bounds.Min.Y + (row+1)*bounds.Dy()/gridSize
	return image.Rect(x0, y0, x1, y1)
}

func pixelDiff(left color.Color, right color.Color) int64 {
	lr, lg, lb, _ := left.RGBA()
	rr, rg, rb, _ := right.RGBA()
	return absDiff(lr, rr) + absDiff(lg, rg) + absDiff(lb, rb)
}

func absDiff(left uint32, right uint32) int64 {
	if left > right {
		return int64(left - right)
	}
	return int64(right - left)
}

func buildSliderCursor(candidateIndex int, candidateCount int) string {
	if candidateCount <= 0 {
		return "[]"
	}
	if candidateIndex < 1 {
		candidateIndex = 1
	}
	if candidateIndex > candidateCount {
		candidateIndex = candidateCount
	}

	type cursorPoint struct {
		X int `json:"x"`
		Y int `json:"y"`
	}

	startX := 570 + randx.Intn(40)
	startY := 875 + randx.Intn(30)

	denom := candidateCount - 1
	if denom < 1 {
		denom = 1
	}
	baseTargetX := 734 + (937-734)*(candidateIndex-1)/denom
	targetX := baseTargetX + randx.Intn(10) - 5
	targetY := 655 + randx.Intn(14)

	points := make([]cursorPoint, 0, 128)

	for i := 0; i < 4+randx.Intn(8); i++ {
		points = append(points, cursorPoint{
			X: startX + randx.Intn(5) - 2,
			Y: startY + randx.Intn(5) - 2,
		})
	}

	transitSteps := 32 + randx.Intn(26)
	arcOffX := randx.Intn(60) - 30
	arcOffY := -(randx.Intn(30) + 10)
	for i := 1; i <= transitSteps; i++ {
		t := easeInOut(float64(i) / float64(transitSteps+1))
		cx := float64(startX+targetX)/2 + float64(arcOffX)
		cy := float64(startY+targetY)/2 + float64(arcOffY)
		bx := (1-t)*(1-t)*float64(startX) + 2*t*(1-t)*cx + t*t*float64(targetX)
		by := (1-t)*(1-t)*float64(startY) + 2*t*(1-t)*cy + t*t*float64(targetY)
		jitter := int((1-t)*5) + 1
		points = append(points, cursorPoint{
			X: int(math.Round(bx)) + randx.Intn(jitter*2+1) - jitter,
			Y: int(math.Round(by)) + randx.Intn(jitter*2+1) - jitter,
		})
		if i%9 == 0 && randx.Intn(3) == 0 {
			last := points[len(points)-1]
			points = append(points, last)
		}
	}

	approachSteps := 16 + randx.Intn(14)
	prev := points[len(points)-1]
	for i := 1; i <= approachSteps; i++ {
		t := easeOut(float64(i) / float64(approachSteps))
		ax := prev.X + int(math.Round(t*float64(targetX-prev.X))) + randx.Intn(5) - 2
		ay := prev.Y + int(math.Round(t*float64(targetY-prev.Y))) + randx.Intn(5) - 2
		points = append(points, cursorPoint{X: ax, Y: ay})
	}

	settleCount := 18 + randx.Intn(24)
	for i := 0; i < settleCount; i++ {
		points = append(points, cursorPoint{
			X: targetX + randx.Intn(7) - 3,
			Y: targetY + randx.Intn(7) - 3,
		})
	}

	data, err := json.Marshal(points)
	if err != nil {
		return "[]"
	}
	return string(data)
}

func easeInOut(t float64) float64 {
	if t < 0.5 {
		return 2 * t * t
	}
	p := -2*t + 2
	return 1 - p*p/2
}

func easeOut(t float64) float64 {
	p := 1 - t
	return 1 - p*p*p
}
