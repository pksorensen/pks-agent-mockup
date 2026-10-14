package compose

import "sort"

// Surface is one ad slot found in (or pinned for) a scene.
type Surface struct {
	Quad Quad `json:"quad" yaml:"quad"`
	// Aspect is the real width/height of the slot. Zero means: estimate it from the quad.
	Aspect float64 `json:"aspect,omitempty" yaml:"aspect,omitempty"`
	// Finish shapes how the ad sits in the scene: "print" (lit by the scene),
	// "backlit" (light box) or "led" (emissive screen with bloom). Empty means backlit.
	Finish string `json:"finish,omitempty" yaml:"finish,omitempty"`
	// Ad pins a format by name (the file stem, e.g. "portrait"); empty picks by aspect.
	Ad string `json:"ad,omitempty" yaml:"ad,omitempty"`
	// Fit is "contain" or "cover"; empty chooses by how far the aspects differ.
	Fit string `json:"fit,omitempty" yaml:"fit,omitempty"`
}

// Key holds the chroma-key parameters. Greenness is g - max(r, b) on 0..1.
type Key struct {
	Low, High float32 // greenness ramp: below Low is scene, above High is screen
	MinArea   float64 // smallest blob kept, as a fraction of the image area
}

var DefaultKey = Key{Low: 0.12, High: 0.35, MinArea: 0.0006}

// Greenness of one pixel.
func greenness(r, g, b float32) float32 {
	m := r
	if b > m {
		m = b
	}
	return g - m
}

// Matte returns the per-pixel screen coverage in 0..1.
func (k Key) Matte(s *Raster) []float32 {
	m := make([]float32, s.W*s.H)
	span := k.High - k.Low
	for i := range m {
		g := greenness(s.Pix[i*3], s.Pix[i*3+1], s.Pix[i*3+2])
		a := (g - k.Low) / span
		if a < 0 {
			a = 0
		} else if a > 1 {
			a = 1
		}
		m[i] = a
	}
	return m
}

// Detect finds the keyed surfaces: connected blobs of screen pixels, each fitted
// with a quad. Largest first.
func (k Key) Detect(s *Raster) []Surface {
	matte := k.Matte(s)
	n := s.W * s.H
	seen := make([]bool, n)
	minPix := int(k.MinArea * float64(n))
	type blob struct{ xs, ys []int }
	var blobs []blob
	stack := make([]int, 0, 1024)
	for start := 0; start < n; start++ {
		if seen[start] || matte[start] < 0.5 {
			continue
		}
		var b blob
		stack = append(stack[:0], start)
		seen[start] = true
		for len(stack) > 0 {
			p := stack[len(stack)-1]
			stack = stack[:len(stack)-1]
			x, y := p%s.W, p/s.W
			b.xs = append(b.xs, x)
			b.ys = append(b.ys, y)
			for dy := -1; dy <= 1; dy++ {
				for dx := -1; dx <= 1; dx++ {
					nx, ny := x+dx, y+dy
					if nx < 0 || ny < 0 || nx >= s.W || ny >= s.H {
						continue
					}
					q := ny*s.W + nx
					if !seen[q] && matte[q] >= 0.5 {
						seen[q] = true
						stack = append(stack, q)
					}
				}
			}
		}
		if len(b.xs) >= minPix {
			blobs = append(blobs, b)
		}
	}
	sort.Slice(blobs, func(i, j int) bool { return len(blobs[i].xs) > len(blobs[j].xs) })
	out := make([]Surface, 0, len(blobs))
	for _, b := range blobs {
		q := FitQuad(b.xs, b.ys)
		if q.Area() == 0 {
			continue
		}
		out = append(out, Surface{Quad: q})
	}
	return out
}
