// Command mockup places a series of ad creatives into chroma-keyed reference
// scenes — deterministic compositing, no AI at render time.
//
//	mockup detect scene.png...      write scene.surfaces.json next to each scene
//	mockup render series.yaml       render every series × scene into the output dir
package main

import (
	"encoding/json"
	"fmt"
	"html"
	"math"
	"os"
	"path/filepath"
	"runtime"
	"sort"
	"strings"
	"sync"

	"github.com/pksorensen/pks-agent-mockup/internal/compose"
	"gopkg.in/yaml.v3"
)

// Config is the series file.
type Config struct {
	// Scenes are scene images or globs. Each may carry a sidecar
	// <scene>.surfaces.json (from `mockup detect`, hand-editable); without one
	// the surfaces are detected on the fly.
	Scenes []SceneRef `yaml:"scenes"`
	// Series are named ad sets, e.g. one per campaign week. Each maps a format
	// name to a creative; a surface gets the format whose aspect fits it best.
	Series  []Series `yaml:"series"`
	Output  string   `yaml:"output"`
	Format  string   `yaml:"format"` // jpg (default) or png
	Quality int      `yaml:"quality"`
	Key     *struct {
		Low, High float32
		MinArea   float64 `yaml:"minArea"`
	} `yaml:"key"`
	Look *struct{ Bloom, Grain float32 } `yaml:"look"`
}

type SceneRef struct {
	Path   string `yaml:"path"`
	Finish string `yaml:"finish"` // default finish for this scene's surfaces
}

// UnmarshalYAML accepts a bare path string as well as the mapping form.
func (s *SceneRef) UnmarshalYAML(n *yaml.Node) error {
	if n.Kind == yaml.ScalarNode {
		s.Path = n.Value
		return nil
	}
	type plain SceneRef
	return n.Decode((*plain)(s))
}

type Series struct {
	Name string            `yaml:"name"`
	Ads  map[string]string `yaml:"ads"`
}

// Sidecar is the hand-editable surface list for one scene.
type Sidecar struct {
	Finish   string            `json:"finish,omitempty"`
	Surfaces []compose.Surface `json:"surfaces"`
}

func main() {
	if len(os.Args) < 3 {
		fmt.Fprintln(os.Stderr, "usage: mockup detect <scene.png>... | mockup render <series.yaml>")
		os.Exit(2)
	}
	var err error
	switch os.Args[1] {
	case "detect":
		err = detect(os.Args[2:])
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

func sidecarPath(scene string) string {
	return strings.TrimSuffix(scene, filepath.Ext(scene)) + ".surfaces.json"
}

func detect(scenes []string) error {
	for _, p := range scenes {
		img, err := compose.Load(p)
		if err != nil {
			return err
		}
		sc := Sidecar{Surfaces: compose.DefaultKey.Detect(img)}
		for i := range sc.Surfaces {
			round(&sc.Surfaces[i].Quad)
		}
		b, _ := json.MarshalIndent(sc, "", "  ")
		if err := os.WriteFile(sidecarPath(p), append(b, '\n'), 0o644); err != nil {
			return err
		}
		fmt.Printf("%s: %d surfaces\n", p, len(sc.Surfaces))
	}
	return nil
}

func round(q *compose.Quad) {
	for i := range q {
		q[i].X, q[i].Y = math.Round(q[i].X), math.Round(q[i].Y)
	}
}

type job struct {
	scene  string
	finish string
	series Series
	out    string
}

func render(cfgPath string) error {
	raw, err := os.ReadFile(cfgPath)
	if err != nil {
		return err
	}
	var cfg Config
	if err := yaml.Unmarshal(raw, &cfg); err != nil {
		return fmt.Errorf("%s: %w", cfgPath, err)
	}
	base := filepath.Dir(cfgPath)
	rel := func(p string) string {
		if filepath.IsAbs(p) {
			return p
		}
		return filepath.Join(base, p)
	}
	key, look := compose.DefaultKey, compose.DefaultLook
	if cfg.Key != nil {
		key = compose.Key{Low: cfg.Key.Low, High: cfg.Key.High, MinArea: cfg.Key.MinArea}
	}
	if cfg.Look != nil {
		look = compose.Look{Bloom: cfg.Look.Bloom, Grain: cfg.Look.Grain}
	}
	if cfg.Format == "" {
		cfg.Format = "jpg"
	}
	if cfg.Quality == 0 {
		cfg.Quality = 90
	}
	out := rel(cfg.Output)
	if cfg.Output == "" {
		out = rel("out")
	}

	// Expand scene globs.
	var scenes []SceneRef
	seen := map[string]int{}
	for _, s := range cfg.Scenes {
		matches, err := filepath.Glob(rel(s.Path))
		if err != nil || len(matches) == 0 {
			return fmt.Errorf("scene %q matched nothing", s.Path)
		}
		sort.Strings(matches)
		for _, m := range matches {
			if i, ok := seen[m]; ok {
				scenes[i].Finish = s.Finish // a later, more specific entry wins
				continue
			}
			seen[m] = len(scenes)
			scenes = append(scenes, SceneRef{Path: m, Finish: s.Finish})
		}
	}

	// Load each creative once; series share files freely.
	ads := map[string]*compose.Raster{}
	for _, se := range cfg.Series {
		for _, p := range se.Ads {
			if _, ok := ads[rel(p)]; ok {
				continue
			}
			r, err := compose.Load(rel(p))
			if err != nil {
				return err
			}
			ads[rel(p)] = r
		}
	}

	var jobs []job
	for _, se := range cfg.Series {
		for _, sc := range scenes {
			stem := strings.TrimSuffix(filepath.Base(sc.Path), filepath.Ext(sc.Path))
			jobs = append(jobs, job{sc.Path, sc.Finish, se, filepath.Join(out, se.Name, stem+"."+cfg.Format)})
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
				err := renderOne(j, key, look, ads, rel, cfg.Quality)
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
	return writeIndex(out, cfg.Series, scenes, cfg.Format)
}

func renderOne(j job, key compose.Key, look compose.Look, ads map[string]*compose.Raster, rel func(string) string, quality int) error {
	scene, err := compose.Load(j.scene)
	if err != nil {
		return err
	}
	var sc Sidecar
	if b, err := os.ReadFile(sidecarPath(j.scene)); err == nil {
		if err := json.Unmarshal(b, &sc); err != nil {
			return fmt.Errorf("%s: %w", sidecarPath(j.scene), err)
		}
	} else {
		sc.Surfaces = key.Detect(scene)
	}
	finish := j.finish
	if sc.Finish != "" {
		finish = sc.Finish
	}
	for i := range sc.Surfaces {
		if sc.Surfaces[i].Finish == "" {
			sc.Surfaces[i].Finish = finish
		}
	}
	pick := func(s compose.Surface) *compose.Raster {
		if s.Ad != "" {
			if p, ok := j.series.Ads[s.Ad]; ok {
				return ads[rel(p)]
			}
		}
		w, h := s.Quad.Size()
		want := s.Aspect
		if want == 0 {
			want = w / h
		}
		var best *compose.Raster
		bestD := math.Inf(1)
		for _, p := range j.series.Ads {
			r := ads[rel(p)]
			if d := math.Abs(math.Log(float64(r.W) / float64(r.H) / want)); d < bestD {
				best, bestD = r, d
			}
		}
		return best
	}
	seed := int64(len(j.scene)*7919 + len(j.series.Name))
	res := compose.Composite(scene, key, sc.Surfaces, pick, look, seed)
	return compose.Save(res, j.out, quality)
}

// writeIndex drops a plain contact sheet next to the renders.
func writeIndex(out string, series []Series, scenes []SceneRef, format string) error {
	var b strings.Builder
	b.WriteString(`<!doctype html><meta charset="utf-8"><meta name="viewport" content="width=device-width,initial-scale=1"><title>Mockup series</title>
<style>body{margin:0;padding:16px;font:14px system-ui;background:#111;color:#eee}h2{font-weight:500;margin:24px 0 8px}
.g{display:grid;grid-template-columns:repeat(auto-fill,minmax(280px,1fr));gap:8px}img{width:100%;display:block;border-radius:4px}</style>`)
	for _, se := range series {
		fmt.Fprintf(&b, "<h2>%s</h2><div class=g>", html.EscapeString(se.Name))
		for _, sc := range scenes {
			stem := strings.TrimSuffix(filepath.Base(sc.Path), filepath.Ext(sc.Path))
			src := se.Name + "/" + stem + "." + format
			fmt.Fprintf(&b, `<a href="%s"><img loading=lazy src="%s" alt="%s"></a>`, src, src, html.EscapeString(stem))
		}
		b.WriteString("</div>")
	}
	return os.WriteFile(filepath.Join(out, "index.html"), []byte(b.String()), 0o644)
}
