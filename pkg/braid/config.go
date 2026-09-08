package braid

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"math"
	"os"
	"strings"

	"gopkg.in/yaml.v3"
)

var retrieverNames = []string{"lexical", "dense", "graph", "temporal"}

type Config struct {
	DatasetID string `yaml:"dataset_id" json:"dataset_id"`
	Version   int    `yaml:"version" json:"version"`
	Store     struct {
		Backend string `yaml:"backend"`
		Path    string `yaml:"path"`
	} `yaml:"store"`
	Embed struct {
		Provider string `yaml:"provider"`
		Model    string `yaml:"model"`
		URL      string `yaml:"url"`
	} `yaml:"embed"`
	Index struct {
		Nodes     map[string][]string `yaml:"nodes"`
		CostField string              `yaml:"cost_field"`
	} `yaml:"index"`
	Projection []Projection `yaml:"projection"`
	Graph      struct {
		Undirected []string `yaml:"undirected"`
		Directed   []string `yaml:"directed"`
		Damping    float64  `yaml:"damping"`
		Seed       string   `yaml:"seed"`
		MaxHops    int      `yaml:"max_hops"`
	} `yaml:"graph"`
	Temporal struct {
		HalfLifeHours float64 `yaml:"half_life_hours"`
		Neighborhood  bool    `yaml:"neighborhood"`
	} `yaml:"temporal"`
	Defaults struct {
		Weights   map[string]float64 `yaml:"weights"`
		K         map[string]float64 `yaml:"k"`
		Boosts    []Boost            `yaml:"boosts"`
		MMRLambda float64            `yaml:"mmr_lambda"`
		Budget    Budget             `yaml:"budget"`
	} `yaml:"defaults"`
	CandidateLimit int `yaml:"candidate_limit"`
}

func DefaultConfig() Config {
	var c Config
	c.DatasetID = "default"
	c.Version = 1
	c.Store.Backend = "sqlite"
	c.Store.Path = "braid.db"
	c.Embed.Provider = "none"
	c.Embed.URL = "http://localhost:11434"
	c.Graph.Damping = .85
	c.Graph.Seed = "uniform"
	c.Graph.MaxHops = 3
	c.Temporal.HalfLifeHours = 72
	c.Defaults.Weights = map[string]float64{"lexical": 1, "dense": 1, "graph": 1, "temporal": 1}
	c.Defaults.K = map[string]float64{"lexical": 60, "dense": 60, "graph": 60, "temporal": 60}
	c.Defaults.MMRLambda = .7
	c.Defaults.Budget.Max = 2500
	c.CandidateLimit = 200
	return c
}
func LoadConfig(path string) (Config, error) {
	c := DefaultConfig()
	b, err := os.ReadFile(path)
	if err != nil {
		return c, err
	}
	d := yaml.NewDecoder(bytes.NewReader(b))
	d.KnownFields(true)
	if err = d.Decode(&c); err != nil {
		return c, err
	}
	var extra any
	if err = d.Decode(&extra); err != io.EOF {
		return c, fmt.Errorf("config must contain exactly one YAML document")
	}
	return c, c.Validate()
}
func finite(v float64) bool { return !math.IsNaN(v) && !math.IsInf(v, 0) }
func (c Config) Validate() error {
	if strings.TrimSpace(c.DatasetID) == "" {
		return fmt.Errorf("dataset_id is required")
	}
	if c.Version != 1 {
		return fmt.Errorf("unsupported config version %d", c.Version)
	}
	if c.Store.Backend != "sqlite" || c.Store.Path == "" {
		return fmt.Errorf("store requires backend sqlite and a path")
	}
	if c.Embed.Provider != "none" && c.Embed.Provider != "ollama" {
		return fmt.Errorf("unsupported embedding provider %q; use none, ollama, or a library Provider", c.Embed.Provider)
	}
	if c.Embed.Provider != "none" && c.Embed.Model == "" {
		return fmt.Errorf("embedding model is required")
	}
	if !finite(c.Graph.Damping) || c.Graph.Damping < 0 || c.Graph.Damping >= 1 {
		return fmt.Errorf("graph damping must be in [0,1)")
	}
	if c.Graph.Seed != "uniform" && c.Graph.Seed != "inverse_degree" {
		return fmt.Errorf("invalid graph seed")
	}
	if c.Graph.MaxHops < 0 || c.Graph.MaxHops > 100 {
		return fmt.Errorf("max_hops must be between 0 and 100")
	}
	for _, t := range c.Graph.Directed {
		if contains(c.Graph.Undirected, t) {
			return fmt.Errorf("edge type %q is both directed and undirected", t)
		}
	}
	if !finite(c.Temporal.HalfLifeHours) || c.Temporal.HalfLifeHours <= 0 {
		return fmt.Errorf("half_life_hours must be positive")
	}
	if !finite(c.Defaults.MMRLambda) || c.Defaults.MMRLambda < 0 || c.Defaults.MMRLambda > 1 {
		return fmt.Errorf("mmr_lambda must be in [0,1]")
	}
	if !finite(c.Defaults.Budget.Max) || c.Defaults.Budget.Max < 0 || !finite(c.Defaults.Budget.Fixed) || c.Defaults.Budget.Fixed < 0 || c.CandidateLimit <= 0 {
		return fmt.Errorf("invalid budget or candidate_limit")
	}
	for _, m := range []map[string]float64{c.Defaults.Weights, c.Defaults.K} {
		for k, v := range m {
			if !contains(retrieverNames, k) || !finite(v) || v < 0 {
				return fmt.Errorf("invalid retriever parameter %q", k)
			}
		}
	}
	for _, b := range c.Defaults.Boosts {
		if err := validateBoost(b); err != nil {
			return err
		}
	}
	return nil
}
func contains(xs []string, s string) bool {
	for _, x := range xs {
		if x == s {
			return true
		}
	}
	return false
}
func validateBoost(b Boost) error {
	if !finite(b.Factor) || b.Factor < 0 {
		return fmt.Errorf("boost factor must be finite and nonnegative")
	}
	if b.Where != "" {
		p := strings.Split(b.Where, "==")
		if len(p) != 2 || !strings.HasPrefix(strings.TrimSpace(p[0]), "attrs.") || strings.TrimSpace(strings.TrimPrefix(strings.TrimSpace(p[0]), "attrs.")) == "" {
			return fmt.Errorf("boost where must be attrs.FIELD == VALUE")
		}
	}
	return nil
}
func configHash(c Config) string {
	b, _ := json.Marshal(c)
	h := sha256.Sum256(b)
	return hex.EncodeToString(h[:])
}
