// Command mockup places a series of ad creatives into chroma-keyed reference
// scenes — deterministic compositing, no AI at render time.
//
//	mockup detect scene.png...              write scene.surfaces.json next to each scene
//	mockup formats series.yaml [--assign]   the delivery spec: which formats, what sizes, what is missing
//	mockup render series.yaml               render every series × scene into the output dir
package main

import (
	"fmt"
	"html"
	"image"
	_ "image/jpeg"
	_ "image/png"
	"io"
	"math"
	"os"
	"path/filepath"
	"runtime"
	"sort"
	"strings"
	"sync"

	"github.com/pksorensen/pks-agent-mockup/internal/compose"
)

var imageDecodeConfig = func(r io.Reader) (image.Config, string, error) { return image.DecodeConfig(r) }

func main() {
	if len(os.Args) < 3 {
		fmt.Fprintln(os.Stderr, "usage: mockup detect <scene.png>... | mockup formats <series.yaml> [--assign] | mockup render <series.yaml>")
		os.Exit(2)
	}
	var err error
	switch os.Args[1] {
	case "detect":
		err = detect(os.Args[2:])
	case "formats":
		err = formats(os.Args[2], len(os.Args) > 3 && os.Args[3] == "--assign")
	case "render":
		err = render(os.Args[2])
	default:
		err = fmt.Errorf("unknown command %q", os.Args[1])
	}
	if err != nil {
		fmt.Fprintln(os.Stderr, "mockup:", err)
		os.Exit(1)
	}
}

func detect(scenes []string) error {
	for _, p := range scenes {
		img, err := compose.Load(p)
		if err != nil {
			return err
		}
		sc := Sidecar{Surfaces: compose.DefaultKey.Detect(img)}
		if err := writeSidecar(p, sc); err != nil {
			return err
		}
		fmt.Printf("%s: %d surfaces\n", p, len(sc.Surfaces))
	}
	return nil
}

// problem is one thing standing between the series file and a render.
type problem struct{ msg string }

// check validates the project: every slot has a known format, and every series
// delivers a creative of the right aspect for every format the scenes use.
func (p *Project) check() (used map[string][]string, probs []problem) {
	used = map[string][]string{} // format → "scene#slot"
	for _, sc := range p.Scenes {
		if !sc.HasCar {
			probs = append(probs, problem{fmt.Sprintf("%s: no sidecar — run `mockup detect %s`", sc.Name, sc.Path)})
			continue
		}
		for i, s := range sc.Sidecar.Surfaces {
			switch {
			case s.Format == "off":
			case s.Format == "":
				probs = append(probs, problem{fmt.Sprintf("%s#%d: no format (closest by shape: %s) — `mockup formats --assign` fills in suggestions", sc.Name, i, p.nearestFormat(s))})
			case p.Formats[s.Format].W == 0:
				probs = append(probs, problem{fmt.Sprintf("%s#%d: format %q is not defined under formats:", sc.Name, i, s.Format)})
			default:
				used[s.Format] = append(used[s.Format], fmt.Sprintf("%s#%d", sc.Name, i))
			}
		}
	}
	for _, se := range p.Series {
		for _, name := range sortedKeys(used) {
			spec := p.Formats[name]
			path, ok := se.Ads[name]
			if !ok {
				probs = append(probs, problem{fmt.Sprintf("series %s: missing %s (%dx%d)", se.Name, name, spec.W, spec.H)})
				continue
			}
			w, h, err := imageSize(p.rel(path))
			if err != nil {
				probs = append(probs, problem{fmt.Sprintf("series %s: %v", se.Name, err)})
				continue
			}
			if math.Abs(math.Log(float64(w)/float64(h)/spec.Aspect())) > 0.01 && p.Fit == "" {
				probs = append(probs, problem{fmt.Sprintf("series %s: %s is %dx%d, format %s wants %dx%d (or set fit: contain|cover)", se.Name, filepath.Base(path), w, h, name, spec.W, spec.H)})
			} else if w < spec.W*3/4 {
				fmt.Fprintf(os.Stderr, "warning: series %s: %s is %dx%d, below the %dx%d spec\n", se.Name, filepath.Base(path), w, h, spec.W, spec.H)
			}
		}
	}
	return used, probs
}

func formats(cfgPath string, assign bool) error {
	p, err := load(cfgPath)
	if err != nil {
		return err
	}
	if assign {
		for _, sc := range p.Scenes {
			if !sc.HasCar {
				continue
			}
			changed := false
			for i := range sc.Sidecar.Surfaces {
				if sc.Sidecar.Surfaces[i].Format == "" {
					sc.Sidecar.Surfaces[i].Format = p.nearestFormat(sc.Sidecar.Surfaces[i])
					changed = true
				}
			}
			if changed {
				if err := writeSidecar(sc.Path, sc.Sidecar); err != nil {
					return err
				}
				fmt.Printf("assigned formats in %s — check them, perspective fools the guess\n", sidecarPath(sc.Path))
			}
		}
	}
	used, probs := p.check()

	fmt.Println("Delivery spec — one creative per format, per series:")
	fmt.Println()
	fmt.Printf("  %-12s %-11s %-6s %s\n", "FORMAT", "SIZE", "RATIO", "SLOTS")
	for _, name := range p.formatNames() {
		spec := p.Formats[name]
		slots := used[name]
		if len(slots) == 0 {
			continue
		}
		fmt.Printf("  %-12s %-11s %-6s %d  %s\n", name, spec.Size, ratio(spec.W, spec.H), len(slots), summarize(slots))
		if spec.Description != "" {
			fmt.Printf("  %-12s %s\n", "", spec.Description)
		}
	}
	for _, name := range p.formatNames() {
		if len(used[name]) == 0 {
			fmt.Printf("  (%s is defined but no scene uses it)\n", name)
		}
	}
	fmt.Println()
	if len(probs) == 0 {
		fmt.Printf("All %d series deliver every format. Ready to render.\n", len(p.Series))
		return nil
	}
	fmt.Printf("%d problem(s):\n", len(probs))
	for _, pr := range probs {
		fmt.Println("  -", pr.msg)
	}
	return fmt.Errorf("not ready to render")
}

func ratio(w, h int) string {
	r := float64(w) / float64(h)
	if r >= 1 {
		return fmt.Sprintf("%.2g:1", r)
	}
	return fmt.Sprintf("1:%.2g", 1/r)
}

// summarize lists the scenes a format appears in, collapsing slot numbers.
func summarize(slots []string) string {
	count := map[string]int{}
	var order []string
	for _, s := range slots {
		name, _, _ := strings.Cut(s, "#")
		if count[name] == 0 {
			order = append(order, name)
		}
		count[name]++
	}
	parts := make([]string, len(order))
	for i, n := range order {
		parts[i] = n
		if count[n] > 1 {
			parts[i] = fmt.Sprintf("%s ×%d", n, count[n])
		}
	}
	return strings.Join(parts, ", ")
}

func sortedKeys[V any](m map[string]V) []string {
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	return keys
}

type job struct {
	scene  Scene
	series Series
	out    string
}

func render(cfgPath string) error {
	p, err := load(cfgPath)
	if err != nil {
		return err
	}
	if _, probs := p.check(); len(probs) > 0 {
		for _, pr := range probs {
			fmt.Fprintln(os.Stderr, "  -", pr.msg)
		}
		return fmt.Errorf("%d problem(s); `mockup formats %s` shows the delivery spec", len(probs), cfgPath)
	}
	key, look := compose.DefaultKey, compose.DefaultLook
	if p.Key != nil {
		key = compose.Key{Low: p.Key.Low, High: p.Key.High, MinArea: p.Key.MinArea}
	}
	if p.Look != nil {
		look = compose.Look{Bloom: p.Look.Bloom, Grain: p.Look.Grain}
	}
	out := p.rel(p.Output)

	// Load each creative once; series share files freely.
	ads := map[string]*compose.Raster{}
	for _, se := range p.Series {
		for _, path := range se.Ads {
			if _, ok := ads[p.rel(path)]; ok {
				continue
			}
			r, err := compose.Load(p.rel(path))
			if err != nil {
				return err
			}
			ads[p.rel(path)] = r
		}
	}

	var jobs []job
	for _, se := range p.Series {
		for _, sc := range p.Scenes {
			jobs = append(jobs, job{sc, se, filepath.Join(out, se.Name, sc.Name+"."+p.ImageType)})
		}
	}

	// Scenes are ~30 MB each as float rasters; a handful of workers is plenty.
	workers := min(runtime.NumCPU(), 6)
	ch := make(chan job)
	var wg sync.WaitGroup
	var mu sync.Mutex
	var firstErr error
	for w := 0; w < workers; w++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for j := range ch {
				err := p.renderOne(j, key, look, ads)
				mu.Lock()
				if err != nil && firstErr == nil {
					firstErr = err
				}
				if err == nil {
					fmt.Println(j.out)
				}
				mu.Unlock()
			}
		}()
	}
	for _, j := range jobs {
		ch <- j
	}
	close(ch)
	wg.Wait()
	if firstErr != nil {
		return firstErr
	}
	return writeIndex(out, p.Series, p.Scenes, p.ImageType)
}

func (p *Project) renderOne(j job, key compose.Key, look compose.Look, ads map[string]*compose.Raster) error {
	scene, err := compose.Load(j.scene.Path)
	if err != nil {
		return err
	}
	finish := j.scene.Finish
	if j.scene.Sidecar.Finish != "" {
		finish = j.scene.Sidecar.Finish
	}
	surfaces := append([]compose.Surface(nil), j.scene.Sidecar.Surfaces...)
	for i := range surfaces {
		s := &surfaces[i]
		if s.Finish == "" {
			s.Finish = finish
		}
		if s.Aspect == 0 && s.Format != "off" {
			s.Aspect = p.Formats[s.Format].Aspect()
		}
		if s.Fit == "" {
			s.Fit = p.Fit
		}
	}
	pick := func(s compose.Surface) *compose.Raster {
		if s.Format == "off" {
			return nil
		}
		return ads[p.rel(j.series.Ads[s.Format])]
	}
	seed := int64(len(j.scene.Path)*7919 + len(j.series.Name))
	res := compose.Composite(scene, key, surfaces, pick, look, seed)
	return compose.Save(res, j.out, p.Quality)
}

// writeIndex drops a plain contact sheet next to the renders.
func writeIndex(out string, series []Series, scenes []Scene, imageType string) error {
	var b strings.Builder
	b.WriteString(`<!doctype html><meta charset="utf-8"><meta name="viewport" content="width=device-width,initial-scale=1"><title>Mockup series</title>
<style>body{margin:0;padding:16px;font:14px system-ui;background:#111;color:#eee}h2{font-weight:500;margin:24px 0 8px}
.g{display:grid;grid-template-columns:repeat(auto-fill,minmax(280px,1fr));gap:8px}img{width:100%;display:block;border-radius:4px}</style>`)
	for _, se := range series {
		fmt.Fprintf(&b, "<h2>%s</h2><div class=g>", html.EscapeString(se.Name))
		for _, sc := range scenes {
			src := se.Name + "/" + sc.Name + "." + imageType
			fmt.Fprintf(&b, `<a href="%s"><img loading=lazy src="%s" alt="%s"></a>`, src, src, html.EscapeString(sc.Name))
		}
		b.WriteString("</div>")
	}
	return os.WriteFile(filepath.Join(out, "index.html"), []byte(b.String()), 0o644)
}
