package compose

import (
	"fmt"
	"image"
	"image/draw"
	"image/jpeg"
	"image/png"
	"math"
	"os"
	"path/filepath"
	"strings"
)

// Raster is a linear float RGB buffer in 0..1, row-major.
type Raster struct {
	W, H int
	Pix  []float32 // len = W*H*3
}

func NewRaster(w, h int) *Raster { return &Raster{W: w, H: h, Pix: make([]float32, w*h*3)} }

func (r *Raster) At(x, y int) (float32, float32, float32) {
	i := (y*r.W + x) * 3
	return r.Pix[i], r.Pix[i+1], r.Pix[i+2]
}

func (r *Raster) Set(x, y int, cr, cg, cb float32) {
	i := (y*r.W + x) * 3
	r.Pix[i], r.Pix[i+1], r.Pix[i+2] = cr, cg, cb
}

func (r *Raster) Clone() *Raster {
	c := NewRaster(r.W, r.H)
	copy(c.Pix, r.Pix)
	return c
}

// Bilinear samples at continuous pixel coordinates, clamping at the border.
func (r *Raster) Bilinear(x, y float64) (float32, float32, float32) {
	x -= 0.5
	y -= 0.5
	x0, y0 := int(math.Floor(x)), int(math.Floor(y))
	fx, fy := float32(x-float64(x0)), float32(y-float64(y0))
	cl := func(v, hi int) int {
		if v < 0 {
			return 0
		}
		if v > hi {
			return hi
		}
		return v
	}
	xa, xb := cl(x0, r.W-1), cl(x0+1, r.W-1)
	ya, yb := cl(y0, r.H-1), cl(y0+1, r.H-1)
	var out [3]float32
	for c := 0; c < 3; c++ {
		p00 := r.Pix[(ya*r.W+xa)*3+c]
		p10 := r.Pix[(ya*r.W+xb)*3+c]
		p01 := r.Pix[(yb*r.W+xa)*3+c]
		p11 := r.Pix[(yb*r.W+xb)*3+c]
		out[c] = (p00*(1-fx)+p10*fx)*(1-fy) + (p01*(1-fx)+p11*fx)*fy
	}
	return out[0], out[1], out[2]
}

// Shrink box-filters the raster down by an integer factor, so a big ad warped
// onto a small surface does not alias.
func (r *Raster) Shrink(f int) *Raster {
	if f <= 1 {
		return r
	}
	w, h := r.W/f, r.H/f
	o := NewRaster(w, h)
	n := float32(f * f)
	for y := 0; y < h; y++ {
		for x := 0; x < w; x++ {
			var s [3]float32
			for dy := 0; dy < f; dy++ {
				for dx := 0; dx < f; dx++ {
					i := ((y*f+dy)*r.W + x*f + dx) * 3
					s[0] += r.Pix[i]
					s[1] += r.Pix[i+1]
					s[2] += r.Pix[i+2]
				}
			}
			o.Set(x, y, s[0]/n, s[1]/n, s[2]/n)
		}
	}
	return o
}

// Crop returns the sub-rectangle [x, x+w) x [y, y+h).
func (r *Raster) Crop(x, y, w, h int) *Raster {
	o := NewRaster(w, h)
	for row := 0; row < h; row++ {
		copy(o.Pix[row*w*3:(row+1)*w*3], r.Pix[((y+row)*r.W+x)*3:((y+row)*r.W+x+w)*3])
	}
	return o
}

// Load decodes a PNG or JPEG into a Raster. Alpha is flattened onto black.
func Load(path string) (*Raster, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	img, _, err := image.Decode(f)
	if err != nil {
		return nil, fmt.Errorf("%s: %w", path, err)
	}
	b := img.Bounds()
	rgba := image.NewRGBA(image.Rect(0, 0, b.Dx(), b.Dy()))
	draw.Draw(rgba, rgba.Bounds(), img, b.Min, draw.Src)
	r := NewRaster(b.Dx(), b.Dy())
	for i, j := 0, 0; i < len(rgba.Pix); i, j = i+4, j+3 {
		r.Pix[j] = float32(rgba.Pix[i]) / 255
		r.Pix[j+1] = float32(rgba.Pix[i+1]) / 255
		r.Pix[j+2] = float32(rgba.Pix[i+2]) / 255
	}
	return r, nil
}

// Save encodes by extension: .png, otherwise JPEG at the given quality.
func Save(r *Raster, path string, quality int) error {
	img := r.Image()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	f, err := os.Create(path)
	if err != nil {
		return err
	}
	defer f.Close()
	if strings.EqualFold(filepath.Ext(path), ".png") {
		return png.Encode(f, img)
	}
	return jpeg.Encode(f, img, &jpeg.Options{Quality: quality})
}

func (r *Raster) Image() *image.RGBA {
	img := image.NewRGBA(image.Rect(0, 0, r.W, r.H))
	for i, j := 0, 0; j < len(r.Pix); i, j = i+4, j+3 {
		img.Pix[i] = to8(r.Pix[j])
		img.Pix[i+1] = to8(r.Pix[j+1])
		img.Pix[i+2] = to8(r.Pix[j+2])
		img.Pix[i+3] = 255
	}
	return img
}

func to8(v float32) uint8 {
	v = v*255 + 0.5
	if v < 0 {
		return 0
	}
	if v > 255 {
		return 255
	}
	return uint8(v)
}
