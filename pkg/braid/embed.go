package braid

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"math"
	"net/http"
	"strings"
	"time"
)

type Ollama struct {
	URL       string
	ModelName string
	Client    *http.Client
}

func (o *Ollama) Model() string { return o.ModelName }
func (o *Ollama) Embed(ctx context.Context, texts []string) ([][]float32, error) {
	b, err := json.Marshal(map[string]any{"model": o.ModelName, "input": texts, "truncate": false})
	if err != nil {
		return nil, err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, strings.TrimRight(o.URL, "/")+"/api/embed", bytes.NewReader(b))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Content-Type", "application/json")
	client := o.Client
	if client == nil {
		client = &http.Client{Timeout: 60 * time.Second}
	}
	resp, err := client.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		b, _ := io.ReadAll(io.LimitReader(resp.Body, 4096))
		return nil, fmt.Errorf("ollama: HTTP %d: %s", resp.StatusCode, b)
	}
	var out struct {
		Embeddings [][]float32 `json:"embeddings"`
	}
	if err = json.NewDecoder(io.LimitReader(resp.Body, 64<<20)).Decode(&out); err != nil {
		return nil, err
	}
	if err = validateVectors(out.Embeddings, len(texts)); err != nil {
		return nil, err
	}
	return out.Embeddings, nil
}
func validateVectors(vs [][]float32, n int) error {
	if len(vs) != n {
		return fmt.Errorf("provider returned %d vectors for %d inputs", len(vs), n)
	}
	dim := 0
	for _, v := range vs {
		if len(v) == 0 || (dim > 0 && len(v) != dim) {
			return fmt.Errorf("provider returned inconsistent dimensions")
		}
		dim = len(v)
		norm := 0.
		for _, x := range v {
			if !finite(float64(x)) {
				return fmt.Errorf("provider returned non-finite vector")
			}
			norm += float64(x) * float64(x)
		}
		if norm == 0 {
			return fmt.Errorf("provider returned zero vector")
		}
	}
	return nil
}
func cosine(a, b []float32) float64 {
	if len(a) == 0 || len(a) != len(b) {
		return 0
	}
	dot, aa, bb := 0., 0., 0.
	for i, x := range a {
		y := float64(b[i])
		z := float64(x)
		dot += z * y
		aa += z * z
		bb += y * y
	}
	if aa == 0 || bb == 0 {
		return 0
	}
	return math.Max(-1, math.Min(1, dot/math.Sqrt(aa*bb)))
}
