package compose

import (
	"math"
	"testing"
)

func TestQuadToQuadMapsCorners(t *testing.T) {
	src := Quad{{10, 20}, {300, 40}, {280, 220}, {30, 200}}
	dst := RectQuad(1920, 1080)
	h, ok := QuadToQuad(src, dst)
	if !ok {
		t.Fatal("singular")
	}
	for i := range src {
		x, y := h.Apply(src[i].X, src[i].Y)
		if math.Abs(x-dst[i].X) > 1e-6 || math.Abs(y-dst[i].Y) > 1e-6 {
			t.Errorf("corner %d -> (%v,%v), want %v", i, x, y, dst[i])
		}
	}
}

func TestFitQuadFindsPerspectiveCorners(t *testing.T) {
	want := Quad{{100, 50}, {400, 90}, {390, 260}, {110, 300}}
	var xs, ys []int
	for y := 0; y < 400; y++ {
		for x := 0; x < 500; x++ {
			if want.Contains(float64(x), float64(y)) {
				xs, ys = append(xs, x), append(ys, y)
			}
		}
	}
	got := FitQuad(xs, ys)
	for i := range want {
		if dist(got[i], want[i]) > 3 {
			t.Errorf("corner %d = %v, want %v", i, got[i], want[i])
		}
	}
}
