package main

import (
	"encoding/json"
	"fmt"
	"image"
	"image/color"
	"image/draw"
	"image/png"
	"math"
	"os"
	"path/filepath"
	"sort"
	"strconv"
)

type sheetSpec struct {
	name string
	file string
	rows int
	cols int
	out  string
}

type partInfo struct {
	ID        string `json:"id"`
	FullPath  string `json:"full_path"`
	CropPath  string `json:"crop_path"`
	Bounds    rect   `json:"bounds"`
	SheetCell cell   `json:"sheet_cell"`
}

type rect struct {
	X int `json:"x"`
	Y int `json:"y"`
	W int `json:"w"`
	H int `json:"h"`
}

type cell struct {
	Row int `json:"row"`
	Col int `json:"col"`
}

type generatedManifest struct {
	SourceDir   string              `json:"source_dir"`
	CanvasSize  rect                `json:"canvas_size"`
	Body        string              `json:"body"`
	Eyebrows    []partInfo          `json:"eyebrows"`
	Eyes        []partInfo          `json:"eyes"`
	Mouth       []partInfo          `json:"mouth"`
	Anchors     map[string]anchor   `json:"sample_anchors"`
	Expressions map[string][]string `json:"expressions"`
}

type anchor struct {
	X      int     `json:"x"`
	Y      int     `json:"y"`
	Method string  `json:"method"`
	Score  float64 `json:"score,omitempty"`
	Scale  float64 `json:"scale,omitempty"`
	Note   string  `json:"note,omitempty"`
}

func main() {
	root := filepath.FromSlash("internal/adapter/viewer/assets/images/mio/parts")
	source := filepath.Join(root, "new_parts")
	generated := filepath.Join(root, "generated")

	if err := os.RemoveAll(generated); err != nil {
		fatal(err)
	}
	for _, dir := range []string{
		filepath.Join(generated, "base"),
		filepath.Join(generated, "eyebrows", "full"),
		filepath.Join(generated, "eyebrows", "crop"),
		filepath.Join(generated, "eyes", "full"),
		filepath.Join(generated, "eyes", "crop"),
		filepath.Join(generated, "mouth", "full"),
		filepath.Join(generated, "mouth", "crop"),
		filepath.Join(generated, "preview"),
	} {
		if err := os.MkdirAll(dir, 0o755); err != nil {
			fatal(err)
		}
	}

	body, err := loadPNG(filepath.Join(source, "Mio_body.png"))
	if err != nil {
		fatal(err)
	}
	bodyLayer := transparentizeBackground(body)
	bodyPath := filepath.Join(generated, "base", "mio_body_base.png")
	if err := savePNG(bodyPath, bodyLayer); err != nil {
		fatal(err)
	}

	specs := []sheetSpec{
		{name: "eyebrow", file: "Mio_blow.png", rows: 7, cols: 2, out: "eyebrows"},
		{name: "eyes", file: "Mio_eyes.png", rows: 7, cols: 2, out: "eyes"},
		{name: "mouth", file: "Mio_mouse.png", rows: 3, cols: 3, out: "mouth"},
	}
	parts := map[string][]partInfo{}
	for _, spec := range specs {
		img, err := loadPNG(filepath.Join(source, spec.file))
		if err != nil {
			fatal(err)
		}
		items, err := extractSheet(generated, spec, img)
		if err != nil {
			fatal(err)
		}
		parts[spec.out] = items
	}

	exprs := map[string][]string{
		"normal":    {"eyebrow_01", "eyebrow_02", "eyes_01", "eyes_02", "mouth_01"},
		"smile":     {"eyebrow_13", "eyebrow_14", "eyes_01", "eyes_02", "mouth_02"},
		"talk_open": {"eyebrow_01", "eyebrow_02", "eyes_01", "eyes_02", "mouth_06"},
		"happy":     {"eyebrow_13", "eyebrow_14", "eyes_11", "eyes_12", "mouth_03"},
		"think":     {"eyebrow_05", "eyebrow_06", "eyes_05", "eyes_06", "mouth_08"},
		"surprise":  {"eyebrow_01", "eyebrow_02", "eyes_09", "eyes_10", "mouth_06"},
		"sad":       {"eyebrow_09", "eyebrow_10", "eyes_05", "eyes_06", "mouth_09"},
		"trouble":   {"eyebrow_11", "eyebrow_12", "eyes_07", "eyes_08", "mouth_09"},
		"wink":      {"eyebrow_13", "eyebrow_14", "eyes_13", "eyes_14", "mouth_02"},
	}
	anchors, err := estimateAnchors(source, generated)
	if err != nil {
		fatal(err)
	}
	if err := composeAnchorPreview(generated, bodyLayer, []string{"eyes_01", "eyes_02", "mouth_01"}, anchors); err != nil {
		fatal(err)
	}
	if err := composeExpressionMatrix(generated, bodyLayer, exprs, anchors); err != nil {
		fatal(err)
	}

	b := bodyLayer.Bounds()
	manifest := generatedManifest{
		SourceDir:   filepath.ToSlash(source),
		CanvasSize:  rect{X: 0, Y: 0, W: b.Dx(), H: b.Dy()},
		Body:        filepath.ToSlash(bodyPath),
		Eyebrows:    parts["eyebrows"],
		Eyes:        parts["eyes"],
		Mouth:       parts["mouth"],
		Anchors:     anchors,
		Expressions: exprs,
	}
	out, err := json.MarshalIndent(manifest, "", "  ")
	if err != nil {
		fatal(err)
	}
	if err := os.WriteFile(filepath.Join(generated, "manifest.json"), append(out, '\n'), 0o644); err != nil {
		fatal(err)
	}
	fmt.Printf("generated Mio parts under %s\n", generated)
}

func extractSheet(root string, spec sheetSpec, src image.Image) ([]partInfo, error) {
	bounds := src.Bounds()
	cellW := bounds.Dx() / spec.cols
	cellH := bounds.Dy() / spec.rows
	var items []partInfo
	for row := 0; row < spec.rows; row++ {
		for col := 0; col < spec.cols; col++ {
			idx := row*spec.cols + col + 1
			id := fmt.Sprintf("%s_%02d", spec.name, idx)
			cellRect := image.Rect(col*cellW, row*cellH, (col+1)*cellW, (row+1)*cellH)
			if col == spec.cols-1 {
				cellRect.Max.X = bounds.Dx()
			}
			if row == spec.rows-1 {
				cellRect.Max.Y = bounds.Dy()
			}
			full := transparentizeCell(src, cellRect)
			partBounds := alphaBounds(full)
			if partBounds.Empty() {
				continue
			}
			crop := crop(full, partBounds)
			fullPath := filepath.Join(root, spec.out, "full", id+".png")
			cropPath := filepath.Join(root, spec.out, "crop", id+".png")
			if err := savePNG(fullPath, full); err != nil {
				return nil, err
			}
			if err := savePNG(cropPath, crop); err != nil {
				return nil, err
			}
			items = append(items, partInfo{
				ID:       id,
				FullPath: filepath.ToSlash(fullPath),
				CropPath: filepath.ToSlash(cropPath),
				Bounds:   rect{X: partBounds.Min.X, Y: partBounds.Min.Y, W: partBounds.Dx(), H: partBounds.Dy()},
				SheetCell: cell{
					Row: row + 1,
					Col: col + 1,
				},
			})
		}
	}
	return items, nil
}

func estimateAnchors(source, generated string) (map[string]anchor, error) {
	sample, err := loadPNG(filepath.Join(source, "Mio_sample.png"))
	if err != nil {
		return nil, err
	}
	out := map[string]anchor{}
	searches := []struct {
		id     string
		path   string
		region image.Rectangle
	}{
		{id: "eyes_01", path: filepath.Join(generated, "eyes", "crop", "eyes_01.png"), region: image.Rect(390, 330, 610, 500)},
		{id: "eyes_02", path: filepath.Join(generated, "eyes", "crop", "eyes_02.png"), region: image.Rect(640, 330, 860, 500)},
		{id: "mouth_01", path: filepath.Join(generated, "mouth", "crop", "mouth_01.png"), region: image.Rect(520, 500, 720, 600)},
	}
	for _, s := range searches {
		tpl, err := loadPNG(s.path)
		if err != nil {
			return nil, err
		}
		p, score := matchTemplate(sample, tpl, s.region)
		scale := 1.0
		switch {
		case hasPrefix(s.id, "eyes_"):
			scale = 0.62
		case hasPrefix(s.id, "mouth_"):
			scale = 0.58
		}
		p = keepCenterAfterScale(p, tpl.Bounds().Dx(), tpl.Bounds().Dy(), scale)
		out[s.id] = anchor{X: p.X, Y: p.Y, Method: "template_match:Mio_sample+manual_sample_scale", Score: score, Scale: scale}
	}
	if left, ok := out["eyes_01"]; ok {
		out["eyebrow_01"] = anchor{X: left.X - 10, Y: left.Y - 72, Method: "estimated_from:eyes_01", Scale: left.Scale, Note: "Sampleでは眉が前髪に隠れるため目位置から推定"}
	}
	if right, ok := out["eyes_02"]; ok {
		out["eyebrow_02"] = anchor{X: right.X + 10, Y: right.Y - 72, Method: "estimated_from:eyes_02", Scale: right.Scale, Note: "Sampleでは眉が前髪に隠れるため目位置から推定"}
	}
	return out, nil
}

func keepCenterAfterScale(p image.Point, w, h int, scale float64) image.Point {
	sw := int(math.Round(float64(w) * scale))
	sh := int(math.Round(float64(h) * scale))
	return image.Point{
		X: p.X + (w-sw)/2,
		Y: p.Y + (h-sh)/2,
	}
}

func matchTemplate(sample image.Image, tpl image.Image, region image.Rectangle) (image.Point, float64) {
	sb := sample.Bounds()
	tb := tpl.Bounds()
	maxX := minInt(region.Max.X, sb.Max.X-tb.Dx())
	maxY := minInt(region.Max.Y, sb.Max.Y-tb.Dy())
	best := image.Point{X: region.Min.X, Y: region.Min.Y}
	bestScore := math.MaxFloat64
	for y := region.Min.Y; y <= maxY; y++ {
		for x := region.Min.X; x <= maxX; x++ {
			score, count := maskedSSD(sample, tpl, x, y)
			if count == 0 {
				continue
			}
			score = score / float64(count)
			if score < bestScore {
				bestScore = score
				best = image.Point{X: x, Y: y}
			}
		}
	}
	return best, bestScore
}

func maskedSSD(sample image.Image, tpl image.Image, offX, offY int) (float64, int) {
	tb := tpl.Bounds()
	var score float64
	count := 0
	for y := tb.Min.Y; y < tb.Max.Y; y += 2 {
		for x := tb.Min.X; x < tb.Max.X; x += 2 {
			tr, tg, tbv, ta := tpl.At(x, y).RGBA()
			if ta < 0x8000 {
				continue
			}
			sr, sg, sb, _ := sample.At(offX+x-tb.Min.X, offY+y-tb.Min.Y).RGBA()
			dr := float64(int(sr>>8) - int(tr>>8))
			dg := float64(int(sg>>8) - int(tg>>8))
			db := float64(int(sb>>8) - int(tbv>>8))
			score += dr*dr + dg*dg + db*db
			count++
		}
	}
	return score, count
}

func composeAnchorPreview(root string, body *image.NRGBA, ids []string, anchors map[string]anchor) error {
	dst, err := composeExpression(body, root, ids, anchors)
	if err != nil {
		return err
	}
	return savePNG(filepath.Join(root, "preview", "mio_normal_anchor_preview.png"), dst)
}

func composeExpression(body *image.NRGBA, root string, ids []string, anchors map[string]anchor) (*image.NRGBA, error) {
	layerPath := func(id string) (string, error) {
		switch {
		case hasPrefix(id, "eyebrow_"):
			return filepath.Join(root, "eyebrows", "crop", id+".png"), nil
		case hasPrefix(id, "eyes_"):
			return filepath.Join(root, "eyes", "crop", id+".png"), nil
		case hasPrefix(id, "mouth_"):
			return filepath.Join(root, "mouth", "crop", id+".png"), nil
		default:
			return "", fmt.Errorf("unknown layer id %q", id)
		}
	}
	dst := image.NewNRGBA(body.Bounds())
	draw.Draw(dst, dst.Bounds(), body, image.Point{}, draw.Src)
	for _, id := range ids {
		p, err := layerPath(id)
		if err != nil {
			return nil, err
		}
		layer, err := loadPNG(p)
		if err != nil {
			return nil, err
		}
		scale := 1.0
		if direct, ok := anchors[id]; ok && direct.Scale > 0 {
			scale = direct.Scale
		}
		if scale == 1.0 {
			if baseScale := categoryScale(id, anchors); baseScale > 0 {
				scale = baseScale
			}
		}
		layerNRGBA := toNRGBA(layer)
		if scale > 0 && math.Abs(scale-1.0) > 0.001 {
			layerNRGBA = scaleImage(layerNRGBA, scale)
		}
		a, ok := resolveLayerAnchor(root, id, anchors, layerNRGBA.Bounds().Dx(), layerNRGBA.Bounds().Dy())
		if !ok {
			continue
		}
		r := image.Rectangle{Min: image.Point{X: a.X, Y: a.Y}, Max: image.Point{X: a.X + layerNRGBA.Bounds().Dx(), Y: a.Y + layerNRGBA.Bounds().Dy()}}
		draw.Draw(dst, r, layerNRGBA, layerNRGBA.Bounds().Min, draw.Over)
	}
	return dst, nil
}

func categoryScale(id string, anchors map[string]anchor) float64 {
	switch {
	case hasPrefix(id, "eyes_"):
		if isOddPart(id) {
			return anchors["eyes_01"].Scale
		}
		return anchors["eyes_02"].Scale
	case hasPrefix(id, "eyebrow_"):
		if isOddPart(id) {
			return anchors["eyes_01"].Scale
		}
		return anchors["eyes_02"].Scale
	case hasPrefix(id, "mouth_"):
		return anchors["mouth_01"].Scale
	}
	return 1.0
}

func resolveLayerAnchor(root, id string, anchors map[string]anchor, w, h int) (anchor, bool) {
	if a, ok := anchors[id]; ok {
		return a, true
	}
	baseID := ""
	switch {
	case hasPrefix(id, "eyes_"):
		if isOddPart(id) {
			baseID = "eyes_01"
		} else {
			baseID = "eyes_02"
		}
	case hasPrefix(id, "eyebrow_"):
		if isOddPart(id) {
			baseID = "eyebrow_01"
		} else {
			baseID = "eyebrow_02"
		}
	case hasPrefix(id, "mouth_"):
		baseID = "mouth_01"
	}
	base, ok := anchors[baseID]
	if !ok || baseID == "" {
		return anchor{}, false
	}
	baseW, baseH, ok := layerSize(root, baseID)
	if !ok {
		return anchor{}, false
	}
	centerX := base.X + baseW/2
	centerY := base.Y + baseH/2
	return anchor{
		X:      centerX - w/2,
		Y:      centerY - h/2,
		Method: "derived_center:" + baseID,
		Scale:  base.Scale,
	}, true
}

func layerSize(root, id string) (int, int, bool) {
	dir := ""
	switch {
	case hasPrefix(id, "eyebrow_"):
		dir = filepath.Join(root, "eyebrows", "crop")
	case hasPrefix(id, "eyes_"):
		dir = filepath.Join(root, "eyes", "crop")
	case hasPrefix(id, "mouth_"):
		dir = filepath.Join(root, "mouth", "crop")
	default:
		return 0, 0, false
	}
	img, err := loadPNG(filepath.Join(dir, id+".png"))
	if err != nil {
		return 0, 0, false
	}
	return img.Bounds().Dx(), img.Bounds().Dy(), true
}

func isOddPart(id string) bool {
	idx := len(id) - 1
	for idx >= 0 && id[idx] >= '0' && id[idx] <= '9' {
		idx--
	}
	n, err := strconv.Atoi(id[idx+1:])
	if err != nil {
		return true
	}
	return n%2 == 1
}

func composeExpressionMatrix(root string, body *image.NRGBA, exprs map[string][]string, anchors map[string]anchor) error {
	names := []string{"normal", "smile", "talk_open", "happy", "think", "surprise", "sad", "trouble", "wink"}
	sort.Strings(names)
	// Stable order for scanning, not lexical.
	names = []string{"normal", "smile", "talk_open", "happy", "think", "surprise", "sad", "trouble", "wink"}
	tileW, tileH := 300, 330
	labelH := 30
	cols := 3
	rows := (len(names) + cols - 1) / cols
	canvas := image.NewNRGBA(image.Rect(0, 0, cols*tileW, rows*tileH))
	fill(canvas, color.NRGBA{R: 246, G: 247, B: 248, A: 255})
	for i, name := range names {
		img, err := composeExpression(body, root, exprs[name], anchors)
		if err != nil {
			return err
		}
		scaled := scaleNearest(img, 260, 260)
		x := (i % cols) * tileW
		y := (i / cols) * tileH
		fillRect(canvas, image.Rect(x+8, y+8, x+tileW-8, y+tileH-8), color.NRGBA{R: 255, G: 255, B: 255, A: 255})
		draw.Draw(canvas, image.Rect(x+20, y+labelH+18, x+20+scaled.Bounds().Dx(), y+labelH+18+scaled.Bounds().Dy()), scaled, image.Point{}, draw.Over)
		drawTinyText(canvas, x+18, y+12, name, color.NRGBA{R: 30, G: 36, B: 44, A: 255})
	}
	return savePNG(filepath.Join(root, "preview", "mio_expression_matrix.png"), canvas)
}

func transparentizeCell(src image.Image, cell image.Rectangle) *image.NRGBA {
	dst := image.NewNRGBA(src.Bounds())
	bg := backgroundMask(src, cell)
	for y := cell.Min.Y; y < cell.Max.Y; y++ {
		for x := cell.Min.X; x < cell.Max.X; x++ {
			r, g, b, _ := src.At(x, y).RGBA()
			c := color.NRGBA{R: uint8(r >> 8), G: uint8(g >> 8), B: uint8(b >> 8), A: 255}
			if bg[y-src.Bounds().Min.Y][x-src.Bounds().Min.X] {
				c.A = 0
			}
			dst.SetNRGBA(x, y, c)
		}
	}
	return dst
}

func transparentizeBackground(src image.Image) *image.NRGBA {
	bounds := src.Bounds()
	dst := image.NewNRGBA(bounds)
	bg := backgroundMask(src, bounds)
	for y := bounds.Min.Y; y < bounds.Max.Y; y++ {
		for x := bounds.Min.X; x < bounds.Max.X; x++ {
			r, g, b, _ := src.At(x, y).RGBA()
			c := color.NRGBA{R: uint8(r >> 8), G: uint8(g >> 8), B: uint8(b >> 8), A: 255}
			if bg[y-bounds.Min.Y][x-bounds.Min.X] {
				c.A = 0
			}
			dst.SetNRGBA(x, y, c)
		}
	}
	return dst
}

func backgroundMask(src image.Image, area image.Rectangle) [][]bool {
	b := src.Bounds()
	visited := make([][]bool, b.Dy())
	for i := range visited {
		visited[i] = make([]bool, b.Dx())
	}
	type pt struct{ x, y int }
	queue := make([]pt, 0, area.Dx()*2+area.Dy()*2)
	enqueue := func(x, y int) {
		if x < area.Min.X || x >= area.Max.X || y < area.Min.Y || y >= area.Max.Y {
			return
		}
		ix, iy := x-b.Min.X, y-b.Min.Y
		if visited[iy][ix] || !isPotentialCheckerBackground(nrgbaAt(src, x, y)) {
			return
		}
		visited[iy][ix] = true
		queue = append(queue, pt{x: x, y: y})
	}
	for x := area.Min.X; x < area.Max.X; x++ {
		enqueue(x, area.Min.Y)
		enqueue(x, area.Max.Y-1)
	}
	for y := area.Min.Y; y < area.Max.Y; y++ {
		enqueue(area.Min.X, y)
		enqueue(area.Max.X-1, y)
	}
	for head := 0; head < len(queue); head++ {
		p := queue[head]
		enqueue(p.x+1, p.y)
		enqueue(p.x-1, p.y)
		enqueue(p.x, p.y+1)
		enqueue(p.x, p.y-1)
	}
	return visited
}

func nrgbaAt(img image.Image, x, y int) color.NRGBA {
	r, g, b, a := img.At(x, y).RGBA()
	return color.NRGBA{R: uint8(r >> 8), G: uint8(g >> 8), B: uint8(b >> 8), A: uint8(a >> 8)}
}

func isPotentialCheckerBackground(c color.NRGBA) bool {
	if max3(c.R, c.G, c.B)-min3(c.R, c.G, c.B) > 14 {
		return false
	}
	bg1 := color.NRGBA{R: 242, G: 242, B: 242, A: 255}
	bg2 := color.NRGBA{R: 255, G: 255, B: 255, A: 255}
	return colorDistance(c, bg1) <= 28 || colorDistance(c, bg2) <= 20
}

func min3(a, b, c uint8) uint8 {
	if b < a {
		a = b
	}
	if c < a {
		a = c
	}
	return a
}

func max3(a, b, c uint8) uint8 {
	if b > a {
		a = b
	}
	if c > a {
		a = c
	}
	return a
}

func fill(img *image.NRGBA, c color.NRGBA) {
	for y := img.Bounds().Min.Y; y < img.Bounds().Max.Y; y++ {
		for x := img.Bounds().Min.X; x < img.Bounds().Max.X; x++ {
			img.SetNRGBA(x, y, c)
		}
	}
}

func fillRect(img *image.NRGBA, r image.Rectangle, c color.NRGBA) {
	r = r.Intersect(img.Bounds())
	for y := r.Min.Y; y < r.Max.Y; y++ {
		for x := r.Min.X; x < r.Max.X; x++ {
			img.SetNRGBA(x, y, c)
		}
	}
}

func scaleNearest(src *image.NRGBA, maxW, maxH int) *image.NRGBA {
	sb := src.Bounds()
	scaleX := float64(maxW) / float64(sb.Dx())
	scaleY := float64(maxH) / float64(sb.Dy())
	scale := math.Min(scaleX, scaleY)
	if scale <= 0 {
		scale = 1
	}
	w := maxInt(1, int(float64(sb.Dx())*scale))
	h := maxInt(1, int(float64(sb.Dy())*scale))
	dst := image.NewNRGBA(image.Rect(0, 0, w, h))
	for y := 0; y < h; y++ {
		sy := sb.Min.Y + int(float64(y)/scale)
		if sy >= sb.Max.Y {
			sy = sb.Max.Y - 1
		}
		for x := 0; x < w; x++ {
			sx := sb.Min.X + int(float64(x)/scale)
			if sx >= sb.Max.X {
				sx = sb.Max.X - 1
			}
			dst.SetNRGBA(x, y, src.NRGBAAt(sx, sy))
		}
	}
	return dst
}

func drawTinyText(img *image.NRGBA, x, y int, text string, c color.NRGBA) {
	cursor := x
	for _, r := range text {
		if r == ' ' {
			cursor += 8
			continue
		}
		drawTinyRune(img, cursor, y, r, c)
		cursor += 8
	}
}

func drawTinyRune(img *image.NRGBA, x, y int, r rune, c color.NRGBA) {
	pattern, ok := tinyFont[r]
	if !ok {
		pattern = tinyFont['?']
	}
	for py, row := range pattern {
		for px, bit := range row {
			if bit != '1' {
				continue
			}
			fillRect(img, image.Rect(x+px*2, y+py*2, x+px*2+2, y+py*2+2), c)
		}
	}
}

var tinyFont = map[rune][]string{
	'?': {"111", "001", "111", "100", "111"},
	'_': {"000", "000", "000", "000", "111"},
	'a': {"010", "101", "111", "101", "101"},
	'b': {"110", "101", "110", "101", "110"},
	'e': {"111", "100", "110", "100", "111"},
	'g': {"111", "100", "101", "101", "111"},
	'h': {"101", "101", "111", "101", "101"},
	'i': {"111", "010", "010", "010", "111"},
	'k': {"101", "101", "110", "101", "101"},
	'l': {"100", "100", "100", "100", "111"},
	'm': {"101", "111", "111", "101", "101"},
	'n': {"110", "101", "101", "101", "101"},
	'o': {"111", "101", "101", "101", "111"},
	'p': {"110", "101", "110", "100", "100"},
	'r': {"110", "101", "110", "101", "101"},
	's': {"111", "100", "111", "001", "111"},
	't': {"111", "010", "010", "010", "010"},
	'u': {"101", "101", "101", "101", "111"},
	'w': {"101", "101", "111", "111", "101"},
	'y': {"101", "101", "010", "010", "010"},
}

func minInt(a, b int) int {
	if a < b {
		return a
	}
	return b
}

func maxInt(a, b int) int {
	if a > b {
		return a
	}
	return b
}

func hasPrefix(s, prefix string) bool {
	return len(s) >= len(prefix) && s[:len(prefix)] == prefix
}

func colorDistance(a, b color.NRGBA) float64 {
	dr := float64(int(a.R) - int(b.R))
	dg := float64(int(a.G) - int(b.G))
	db := float64(int(a.B) - int(b.B))
	return math.Sqrt(dr*dr + dg*dg + db*db)
}

func alphaBounds(img *image.NRGBA) image.Rectangle {
	b := img.Bounds()
	minX, minY := b.Max.X, b.Max.Y
	maxX, maxY := b.Min.X, b.Min.Y
	for y := b.Min.Y; y < b.Max.Y; y++ {
		for x := b.Min.X; x < b.Max.X; x++ {
			if img.NRGBAAt(x, y).A == 0 {
				continue
			}
			if x < minX {
				minX = x
			}
			if y < minY {
				minY = y
			}
			if x+1 > maxX {
				maxX = x + 1
			}
			if y+1 > maxY {
				maxY = y + 1
			}
		}
	}
	return image.Rect(minX, minY, maxX, maxY)
}

func crop(src *image.NRGBA, r image.Rectangle) *image.NRGBA {
	dst := image.NewNRGBA(image.Rect(0, 0, r.Dx(), r.Dy()))
	draw.Draw(dst, dst.Bounds(), src, r.Min, draw.Src)
	return dst
}

func toNRGBA(src image.Image) *image.NRGBA {
	if img, ok := src.(*image.NRGBA); ok {
		return img
	}
	b := src.Bounds()
	dst := image.NewNRGBA(image.Rect(0, 0, b.Dx(), b.Dy()))
	draw.Draw(dst, dst.Bounds(), src, b.Min, draw.Src)
	return dst
}

func scaleImage(src *image.NRGBA, scale float64) *image.NRGBA {
	if scale <= 0 {
		scale = 1
	}
	sb := src.Bounds()
	w := maxInt(1, int(math.Round(float64(sb.Dx())*scale)))
	h := maxInt(1, int(math.Round(float64(sb.Dy())*scale)))
	dst := image.NewNRGBA(image.Rect(0, 0, w, h))
	for y := 0; y < h; y++ {
		sy := sb.Min.Y + minInt(sb.Dy()-1, int(float64(y)/scale))
		for x := 0; x < w; x++ {
			sx := sb.Min.X + minInt(sb.Dx()-1, int(float64(x)/scale))
			dst.SetNRGBA(x, y, src.NRGBAAt(sx, sy))
		}
	}
	return dst
}

func loadPNG(path string) (image.Image, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	return png.Decode(f)
}

func savePNG(path string, img image.Image) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	f, err := os.Create(path)
	if err != nil {
		return err
	}
	defer f.Close()
	enc := png.Encoder{CompressionLevel: png.DefaultCompression}
	return enc.Encode(f, img)
}

func fatal(err error) {
	fmt.Fprintln(os.Stderr, err)
	os.Exit(1)
}
