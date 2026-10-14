package compose

import (
	"math"
	"sort"
)

// Pt is a point in image pixel coordinates.
type Pt struct{ X, Y float64 }

// Quad is an ad surface in a scene, corners clockwise from top-left.
type Quad [4]Pt

// Homography is a 3x3 projective transform, row-major.
type Homography [9]float64

// Apply maps p through h.
func (h Homography) Apply(x, y float64) (float64, float64) {
	w := h[6]*x + h[7]*y + h[8]
	return (h[0]*x + h[1]*y + h[2]) / w, (h[3]*x + h[4]*y + h[5]) / w
}

// QuadToQuad solves the homography taking src corners onto dst corners.
func QuadToQuad(src, dst Quad) (Homography, bool) {
	// Eight equations in the eight unknowns h0..h7 (h8 = 1).
	var a [8][9]float64
	for i := 0; i < 4; i++ {
		x, y, u, v := src[i].X, src[i].Y, dst[i].X, dst[i].Y
		a[2*i] = [9]float64{x, y, 1, 0, 0, 0, -u * x, -u * y, u}
		a[2*i+1] = [9]float64{0, 0, 0, x, y, 1, -v * x, -v * y, v}
	}
	for col := 0; col < 8; col++ {
		pivot := col
		for r := col + 1; r < 8; r++ {
			if math.Abs(a[r][col]) > math.Abs(a[pivot][col]) {
				pivot = r
			}
		}
		if math.Abs(a[pivot][col]) < 1e-12 {
			return Homography{}, false
		}
		a[col], a[pivot] = a[pivot], a[col]
		for r := 0; r < 8; r++ {
			if r == col {
				continue
			}
			f := a[r][col] / a[col][col]
			for c := col; c < 9; c++ {
				a[r][c] -= f * a[col][c]
			}
		}
	}
	var h Homography
	for i := 0; i < 8; i++ {
		h[i] = a[i][8] / a[i][i]
	}
	h[8] = 1
	return h, true
}

// RectQuad is the axis-aligned rectangle [0,w]x[0,h] as a Quad.
func RectQuad(w, h float64) Quad {
	return Quad{{0, 0}, {w, 0}, {w, h}, {0, h}}
}

func dist(a, b Pt) float64 { return math.Hypot(a.X-b.X, a.Y-b.Y) }

// Size estimates the surface's rectified width and height from its edge lengths.
// Perspective makes this approximate; a sidecar can pin the real aspect instead.
func (q Quad) Size() (w, h float64) {
	w = (dist(q[0], q[1]) + dist(q[3], q[2])) / 2
	h = (dist(q[0], q[3]) + dist(q[1], q[2])) / 2
	return
}

// Bounds is the integer bounding box of the quad.
func (q Quad) Bounds() (x0, y0, x1, y1 int) {
	minX, minY, maxX, maxY := q[0].X, q[0].Y, q[0].X, q[0].Y
	for _, p := range q[1:] {
		minX, maxX = math.Min(minX, p.X), math.Max(maxX, p.X)
		minY, maxY = math.Min(minY, p.Y), math.Max(maxY, p.Y)
	}
	return int(math.Floor(minX)), int(math.Floor(minY)), int(math.Ceil(maxX)), int(math.Ceil(maxY))
}

// Contains reports whether p lies inside the (convex) quad.
func (q Quad) Contains(x, y float64) bool {
	sign := 0.0
	for i := 0; i < 4; i++ {
		a, b := q[i], q[(i+1)%4]
		c := (b.X-a.X)*(y-a.Y) - (b.Y-a.Y)*(x-a.X)
		if c != 0 {
			if sign == 0 {
				sign = c
			} else if (c > 0) != (sign > 0) {
				return false
			}
		}
	}
	return true
}

// Grow pushes each corner away from the centroid by px pixels, so the warp
// covers the anti-aliased rim of the key colour.
func (q Quad) Grow(px float64) Quad {
	cx := (q[0].X + q[1].X + q[2].X + q[3].X) / 4
	cy := (q[0].Y + q[1].Y + q[2].Y + q[3].Y) / 4
	var out Quad
	for i, p := range q {
		d := math.Hypot(p.X-cx, p.Y-cy)
		if d == 0 {
			out[i] = p
			continue
		}
		out[i] = Pt{p.X + (p.X-cx)/d*px, p.Y + (p.Y-cy)/d*px}
	}
	return out
}

// FitQuad picks the four corners of a roughly rectangular blob from its pixels.
// It seeds with the extremes along the two diagonals, then lets each corner slide
// to the convex-hull vertex that maximises the quad's area. That holds up under
// strong perspective, where the diagonal extremes alone land mid-edge. A person
// standing across a corner still pulls it in; the sidecar can pin it.
func FitQuad(xs, ys []int) Quad {
	hull := convexHull(xs, ys)
	if len(hull) < 4 {
		return Quad{}
	}
	idx := [4]int{}
	for i, p := range hull {
		s, d := p.X+p.Y, p.X-p.Y
		if s < hull[idx[0]].X+hull[idx[0]].Y {
			idx[0] = i
		}
		if d > hull[idx[1]].X-hull[idx[1]].Y {
			idx[1] = i
		}
		if s > hull[idx[2]].X+hull[idx[2]].Y {
			idx[2] = i
		}
		if d < hull[idx[3]].X-hull[idx[3]].Y {
			idx[3] = i
		}
	}
	quad := func() Quad { return Quad{hull[idx[0]], hull[idx[1]], hull[idx[2]], hull[idx[3]]} }
	for iter := 0; iter < 20; iter++ {
		moved := false
		for c := 0; c < 4; c++ {
			prev := idx[c]
			best, bestArea := prev, quad().Area()
			for i := range hull {
				idx[c] = i
				if a := quad().Area(); a > bestArea+1e-9 && quad().isConvexClockwise() {
					best, bestArea = i, a
				}
			}
			idx[c] = best
			moved = moved || best != prev
		}
		if !moved {
			break
		}
	}
	return quad()
}

// Area is the quad's area (shoelace).
func (q Quad) Area() float64 {
	a := 0.0
	for i := 0; i < 4; i++ {
		j := (i + 1) % 4
		a += q[i].X*q[j].Y - q[j].X*q[i].Y
	}
	return math.Abs(a) / 2
}

func (q Quad) isConvexClockwise() bool {
	for i := 0; i < 4; i++ {
		a, b, c := q[i], q[(i+1)%4], q[(i+2)%4]
		if (b.X-a.X)*(c.Y-b.Y)-(b.Y-a.Y)*(c.X-b.X) <= 0 { // image y points down, so clockwise is positive
			return false
		}
	}
	return true
}

// convexHull is Andrew's monotone chain over the blob's per-row extremes,
// which carry the whole hull at a fraction of the points.
func convexHull(xs, ys []int) []Pt {
	rowMin, rowMax := map[int]int{}, map[int]int{}
	for i := range xs {
		if v, ok := rowMin[ys[i]]; !ok || xs[i] < v {
			rowMin[ys[i]] = xs[i]
		}
		if v, ok := rowMax[ys[i]]; !ok || xs[i] > v {
			rowMax[ys[i]] = xs[i]
		}
	}
	pts := make([]Pt, 0, 2*len(rowMin))
	for y, x := range rowMin {
		pts = append(pts, Pt{float64(x), float64(y)}, Pt{float64(rowMax[y]), float64(y)})
	}
	sortPts(pts)
	cross := func(o, a, b Pt) float64 { return (a.X-o.X)*(b.Y-o.Y) - (a.Y-o.Y)*(b.X-o.X) }
	var lower, upper []Pt
	for _, p := range pts {
		for len(lower) >= 2 && cross(lower[len(lower)-2], lower[len(lower)-1], p) <= 0 {
			lower = lower[:len(lower)-1]
		}
		lower = append(lower, p)
	}
	for i := len(pts) - 1; i >= 0; i-- {
		p := pts[i]
		for len(upper) >= 2 && cross(upper[len(upper)-2], upper[len(upper)-1], p) <= 0 {
			upper = upper[:len(upper)-1]
		}
		upper = append(upper, p)
	}
	return append(lower[:len(lower)-1], upper[:len(upper)-1]...)
}

func sortPts(p []Pt) {
	sort.Slice(p, func(i, j int) bool {
		if p[i].X != p[j].X {
			return p[i].X < p[j].X
		}
		return p[i].Y < p[j].Y
	})
}
