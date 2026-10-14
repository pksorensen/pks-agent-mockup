package compose

import (
	"math"
	"math/rand"
	"sort"
)

// Look holds the finishing knobs that make a pasted ad read as photographed.
type Look struct {
	Bloom float32 // light spill around emissive surfaces, 0..1
	Grain float32 // sensor noise amplitude on the ad, 0..1 (0.01 is subtle)
}

var DefaultLook = Look{Bloom: 1, Grain: 0.012}

// Composite places ad into every surface of scene and returns the result.
// The key colour itself carries the scene's light: its brightness across the
// panel becomes the ad's shading, and wherever the matte is partial (hair, an
// arm, a pole in front of the screen) the scene shows through.
func Composite(scene *Raster, k Key, surfaces []Surface, pickAd func(Surface) *Raster, look Look, seed int64) *Raster {
	out := scene.Clone()
	matte := k.Matte(scene)
	light := NewRaster(scene.W, scene.H) // emitted light, blurred into bloom at the end
	rng := rand.New(rand.NewSource(seed))
	anyBloom := false

	// Any key colour no surface claims — screens too small to fill, green
	// bounced onto wet asphalt — is neutralised, so the scene never shows green.
	for i, a := range matte {
		if a > 0 {
			r, g, b := despill(scene.Pix[i*3], scene.Pix[i*3+1], scene.Pix[i*3+2])
			out.Pix[i*3], out.Pix[i*3+1], out.Pix[i*3+2] = r, g, b
		}
	}

	for _, s := range surfaces {
		ad := pickAd(s)
		if ad == nil {
			continue
		}
		w, h := s.Quad.Size()
		aspect := s.Aspect
		if aspect == 0 {
			aspect = w / h
		}
		ad = fitAd(ad, aspect, s.Fit)
		if f := int(float64(ad.W) / (w * 1.5)); f > 1 {
			ad = ad.Shrink(f)
		}

		grown := s.Quad.Grow(1.5)
		toAd, ok := QuadToQuad(grown, RectQuad(float64(ad.W), float64(ad.H)))
		if !ok {
			continue
		}
		ref := panelReference(scene, matte, s.Quad)
		finish := s.Finish
		if finish == "" {
			finish = "backlit"
		}
		if finish != "print" {
			anyBloom = true
		}

		x0, y0, x1, y1 := grown.Bounds()
		x0, y0 = max(x0-4, 0), max(y0-4, 0)
		x1, y1 = min(x1+4, scene.W-1), min(y1+4, scene.H-1)
		for y := y0; y <= y1; y++ {
			for x := x0; x <= x1; x++ {
				i := y*scene.W + x
				sr, sg, sb := scene.At(x, y)
				a := matte[i]
				if a == 0 || !grown.Contains(float64(x)+0.5, float64(y)+0.5) {
					continue
				}
				u, v := toAd.Apply(float64(x)+0.5, float64(y)+0.5)
				ar, ag, ab := ad.Bilinear(u, v)

				shade := shading(finish, sg, ref)
				ar, ag, ab = ar*shade, ag*shade, ab*shade
				if look.Grain > 0 {
					n := float32(rng.NormFloat64()) * look.Grain
					ar, ag, ab = ar+n, ag+n, ab+n
				}
				dr, dg, db := despill(sr, sg, sb)
				out.Set(x, y, ar*a+dr*(1-a), ag*a+dg*(1-a), ab*a+db*(1-a))
				if finish != "print" {
					strength := float32(0.35)
					if finish == "led" {
						strength = 1
					}
					light.Set(x, y, ar*a*strength, ag*a*strength, ab*a*strength)
				}
			}
		}
	}
	if anyBloom && look.Bloom > 0 {
		addBloom(out, light, look.Bloom)
	}
	return out
}

// panelReference is the bright end of the key colour on this panel, so shading
// is relative to the panel's own best-lit part.
func panelReference(scene *Raster, matte []float32, q Quad) float32 {
	x0, y0, x1, y1 := q.Bounds()
	var gs []float32
	for y := max(y0, 0); y <= min(y1, scene.H-1); y += 2 {
		for x := max(x0, 0); x <= min(x1, scene.W-1); x += 2 {
			if matte[y*scene.W+x] > 0.95 && q.Contains(float64(x), float64(y)) {
				_, g, _ := scene.At(x, y)
				gs = append(gs, g)
			}
		}
	}
	if len(gs) == 0 {
		return 1
	}
	sort.Slice(gs, func(i, j int) bool { return gs[i] < gs[j] })
	return gs[len(gs)*95/100]
}

// shading turns the key colour's brightness into a multiplier on the ad.
// Print is lit by the scene, so it follows the panel's absolute brightness;
// light boxes and screens make their own light and only take a hint of it.
func shading(finish string, g, ref float32) float32 {
	switch finish {
	case "print":
		return clamp(g/0.85, 0.08, 1.1)
	case "led":
		return 0.92 + 0.08*clamp(g/ref, 0, 1.1)
	default:
		return 0.78 + 0.22*clamp(g/ref, 0, 1.1)
	}
}

// despill pulls green back to the other channels, removing the key's cast from
// whatever stands in front of or around the screen.
func despill(r, g, b float32) (float32, float32, float32) {
	if m := (r + b) / 2; g > m {
		g = m + (g-m)*0.15
	}
	return r, g, b
}

// fitAd shapes the creative to the slot's aspect. "cover" crops around the
// centre; "contain" shows all of it and fills the rest with the creative's own
// edge colour. The default covers only when the mismatch is small enough that
// nothing meaningful is cut, and otherwise contains — headlines sit near the
// edges, and a cropped word gives a mockup away instantly.
func fitAd(ad *Raster, aspect float64, fit string) *Raster {
	mismatch := math.Abs(math.Log(float64(ad.W) / float64(ad.H) / aspect))
	if fit == "cover" || (fit == "" && mismatch < 0.1) {
		return coverCrop(ad, aspect)
	}
	return containPad(ad, aspect)
}

// coverCrop crops ad to the target aspect around its centre (CSS object-fit: cover).
func coverCrop(ad *Raster, aspect float64) *Raster {
	cur := float64(ad.W) / float64(ad.H)
	if math.Abs(cur-aspect)/aspect < 0.01 {
		return ad
	}
	if cur > aspect {
		w := int(float64(ad.H) * aspect)
		return ad.Crop((ad.W-w)/2, 0, w, ad.H)
	}
	h := int(float64(ad.W) / aspect)
	return ad.Crop(0, (ad.H-h)/2, ad.W, h)
}

// addBloom blurs the emitted light at low resolution and screens it onto out.
func addBloom(out, light *Raster, amount float32) {
	f := max(out.W/256, 1)
	small := light.Shrink(f)
	r := max(small.W/40, 2)
	for pass := 0; pass < 3; pass++ {
		boxBlur(small, r)
	}
	sx, sy := float64(small.W)/float64(out.W), float64(small.H)/float64(out.H)
	k := 0.45 * amount
	for y := 0; y < out.H; y++ {
		for x := 0; x < out.W; x++ {
			lr, lg, lb := small.Bilinear((float64(x)+0.5)*sx, (float64(y)+0.5)*sy)
			if lr+lg+lb < 0.002 {
				continue
			}
			or, og, ob := out.At(x, y)
			out.Set(x, y, screen(or, lr*k), screen(og, lg*k), screen(ob, lb*k))
		}
	}
}

func screen(a, b float32) float32 { return 1 - (1-a)*(1-b) }

func boxBlur(r *Raster, rad int) {
	tmp := make([]float32, len(r.Pix))
	run := func(src, dst []float32, n, stride, count, step int) {
		for line := 0; line < count; line++ {
			base := line * step
			for c := 0; c < 3; c++ {
				var acc float32
				for i := -rad; i <= rad; i++ {
					acc += src[base+clampInt(i, 0, n-1)*stride+c]
				}
				norm := float32(2*rad + 1)
				for i := 0; i < n; i++ {
					dst[base+i*stride+c] = acc / norm
					acc += src[base+clampInt(i+rad+1, 0, n-1)*stride+c] - src[base+clampInt(i-rad, 0, n-1)*stride+c]
				}
			}
		}
	}
	run(r.Pix, tmp, r.W, 3, r.H, r.W*3) // horizontal
	run(tmp, r.Pix, r.H, r.W*3, r.W, 3) // vertical
}

func clamp(v, lo, hi float32) float32 {
	if v < lo {
		return lo
	}
	if v > hi {
		return hi
	}
	return v
}

func clampInt(v, lo, hi int) int {
	if v < lo {
		return lo
	}
	if v > hi {
		return hi
	}
	return v
}

// containPad letterboxes ad to the target aspect using its mean border colour.
func containPad(ad *Raster, aspect float64) *Raster {
	w, h := ad.W, ad.H
	if float64(w)/float64(h) < aspect {
		w = int(math.Round(float64(h) * aspect))
	} else {
		h = int(math.Round(float64(w) / aspect))
	}
	if w == ad.W && h == ad.H {
		return ad
	}
	var sum [3]float32
	n := 0
	edge := func(x, y int) {
		r, g, b := ad.At(x, y)
		sum[0], sum[1], sum[2] = sum[0]+r, sum[1]+g, sum[2]+b
		n++
	}
	for x := 0; x < ad.W; x++ {
		edge(x, 0)
		edge(x, ad.H-1)
	}
	for y := 0; y < ad.H; y++ {
		edge(0, y)
		edge(ad.W-1, y)
	}
	o := NewRaster(w, h)
	fr, fg, fb := sum[0]/float32(n), sum[1]/float32(n), sum[2]/float32(n)
	for i := 0; i < w*h; i++ {
		o.Pix[i*3], o.Pix[i*3+1], o.Pix[i*3+2] = fr, fg, fb
	}
	ox, oy := (w-ad.W)/2, (h-ad.H)/2
	for y := 0; y < ad.H; y++ {
		copy(o.Pix[((oy+y)*w+ox)*3:((oy+y)*w+ox+ad.W)*3], ad.Pix[y*ad.W*3:(y+1)*ad.W*3])
	}
	return o
}
