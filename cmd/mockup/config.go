package main

import (
	"encoding/json"
	"fmt"
	"math"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"

	"github.com/pksorensen/pks-agent-mockup/internal/compose"
	"gopkg.in/yaml.v3"
)

// Config is the series file.
type Config struct {
	// Formats are the media formats the scenes contain, each with the pixel
	// size a creative for it must be delivered in — the delivery spec.
	Formats map[string]FormatSpec `yaml:"formats"`
	// Scenes are scene images or globs. Each carries a sidecar
	// <scene>.surfaces.json (from `mockup detect`) that names every slot's format.
	Scenes []SceneRef `yaml:"scenes"`
	// Series are named ad sets, e.g. one per campaign week: format → creative.
	Series []Series `yaml:"series"`
	// Fit lets render proceed when a creative's aspect does not match its
	// format: "contain" or "cover". Empty means such a delivery is an error.
	Fit       string `yaml:"fit"`
	Output    string `yaml:"output"`
	ImageType string `yaml:"imageType"` // jpg (default) or png
	Quality   int    `yaml:"quality"`
	Key       *struct {
		Low, High float32
		MinArea   float64 `yaml:"minArea"`
	} `yaml:"key"`
	Look *struct{ Bloom, Grain float32 } `yaml:"look"`
}

type FormatSpec struct {
	Size        string `yaml:"size"` // "1080x1920"
	Description string `yaml:"description"`
	W, H        int    `yaml:"-"`
}

func (f FormatSpec) Aspect() float64 { return float64(f.W) / float64(f.H) }

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

// Project is a loaded series file with its scenes resolved.
type Project struct {
	Config
	Dir    string
	Scenes []Scene
}

type Scene struct {
	Path    string
	Name    string
	Finish  string
	Sidecar Sidecar
	HasCar  bool
}

func load(cfgPath string) (*Project, error) {
	raw, err := os.ReadFile(cfgPath)
	if err != nil {
		return nil, err
	}
	p := &Project{Dir: filepath.Dir(cfgPath)}
	if err := yaml.Unmarshal(raw, &p.Config); err != nil {
		return nil, fmt.Errorf("%s: %w", cfgPath, err)
	}
	for name, f := range p.Formats {
		w, h, ok := parseSize(f.Size)
		if !ok {
			return nil, fmt.Errorf("format %q: size %q is not WxH", name, f.Size)
		}
		f.W, f.H = w, h
		p.Formats[name] = f
	}
	if p.ImageType == "" {
		p.ImageType = "jpg"
	}
	if p.Quality == 0 {
		p.Quality = 90
	}
	if p.Output == "" {
		p.Output = "out"
	}

	seen := map[string]int{}
	for _, s := range p.Config.Scenes {
		matches, err := filepath.Glob(p.rel(s.Path))
		if err != nil || len(matches) == 0 {
			return nil, fmt.Errorf("scene %q matched nothing", s.Path)
		}
		sort.Strings(matches)
		for _, m := range matches {
			if i, ok := seen[m]; ok {
				p.Scenes[i].Finish = s.Finish // a later, more specific entry wins
				continue
			}
			seen[m] = len(p.Scenes)
			p.Scenes = append(p.Scenes, Scene{
				Path:   m,
				Name:   strings.TrimSuffix(filepath.Base(m), filepath.Ext(m)),
				Finish: s.Finish,
			})
		}
	}
	for i := range p.Scenes {
		sc := &p.Scenes[i]
		b, err := os.ReadFile(sidecarPath(sc.Path))
		if err != nil {
			continue
		}
		if err := json.Unmarshal(b, &sc.Sidecar); err != nil {
			return nil, fmt.Errorf("%s: %w", sidecarPath(sc.Path), err)
		}
		sc.HasCar = true
	}
	return p, nil
}

func (p *Project) rel(path string) string {
	if filepath.IsAbs(path) {
		return path
	}
	return filepath.Join(p.Dir, path)
}

func sidecarPath(scene string) string {
	return strings.TrimSuffix(scene, filepath.Ext(scene)) + ".surfaces.json"
}

func writeSidecar(scene string, sc Sidecar) error {
	for i := range sc.Surfaces {
		q := &sc.Surfaces[i].Quad
		for j := range q {
			q[j].X, q[j].Y = math.Round(q[j].X), math.Round(q[j].Y)
		}
	}
	b, _ := json.MarshalIndent(sc, "", "  ")
	return os.WriteFile(sidecarPath(scene), append(b, '\n'), 0o644)
}

func parseSize(s string) (int, int, bool) {
	a, b, ok := strings.Cut(strings.ToLower(strings.TrimSpace(s)), "x")
	if !ok {
		return 0, 0, false
	}
	w, err1 := strconv.Atoi(a)
	h, err2 := strconv.Atoi(b)
	return w, h, err1 == nil && err2 == nil && w > 0 && h > 0
}

// nearestFormat is the format whose aspect is closest to the slot's estimated one.
// Only a suggestion: perspective squeezes the receding side, so a human confirms.
func (p *Project) nearestFormat(s compose.Surface) string {
	w, h := s.Quad.Size()
	best, bestD := "", math.Inf(1)
	for _, name := range p.formatNames() {
		if d := math.Abs(math.Log(w / h / p.Formats[name].Aspect())); d < bestD {
			best, bestD = name, d
		}
	}
	return best
}

func (p *Project) formatNames() []string {
	names := make([]string, 0, len(p.Formats))
	for n := range p.Formats {
		names = append(names, n)
	}
	sort.Strings(names)
	return names
}

// imageSize reads only the header of a PNG or JPEG.
func imageSize(path string) (int, int, error) {
	f, err := os.Open(path)
	if err != nil {
		return 0, 0, err
	}
	defer f.Close()
	cfg, _, err := imageDecodeConfig(f)
	if err != nil {
		return 0, 0, fmt.Errorf("%s: %w", path, err)
	}
	return cfg.Width, cfg.Height, nil
}
