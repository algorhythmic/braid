// Package braid assembles explainable, budgeted context from consumer-owned data.
package braid

import (
	"context"
	"time"
)

type Node struct {
	ID    string         `json:"id"`
	Type  string         `json:"type"`
	Text  string         `json:"text"`
	TS    time.Time      `json:"ts"`
	Cost  *float64       `json:"cost,omitempty"`
	Attrs map[string]any `json:"attrs,omitempty"`
}
type Edge struct {
	Src    string    `json:"src"`
	Dst    string    `json:"dst"`
	Type   string    `json:"type"`
	Weight float64   `json:"weight"`
	TS     time.Time `json:"ts"`
}
type Embedding struct {
	NodeID string    `json:"node_id"`
	Model  string    `json:"model"`
	Vec    []float32 `json:"vec"`
}
type Batch struct {
	Nodes      []Node      `json:"nodes"`
	Edges      []Edge      `json:"edges"`
	Embeddings []Embedding `json:"embeddings,omitempty"`
}
type Event struct {
	ID      int64          `json:"id"`
	TS      time.Time      `json:"ts"`
	Name    string         `json:"name"`
	Payload map[string]any `json:"payload"`
}
type Budget struct {
	Max float64 `json:"max" yaml:"max"`
}
type Boost struct {
	Type   string  `json:"type" yaml:"type"`
	Where  string  `json:"where" yaml:"where"`
	Factor float64 `json:"factor" yaml:"factor"`
}
type Filters struct {
	Types      []string  `json:"types,omitempty"`
	Since      time.Time `json:"since,omitempty"`
	ExcludeIDs []string  `json:"exclude_ids,omitempty"`
}
type Query struct {
	MinScore        *float64           `json:"min_score,omitempty"`
	RequireEvidence bool               `json:"require_evidence,omitempty"`
	DatasetID       string             `json:"dataset_id,omitempty"`
	Anchors         []string           `json:"anchors,omitempty"`
	Text            string             `json:"text,omitempty"`
	Weights         map[string]float64 `json:"weights,omitempty"`
	Filters         Filters            `json:"filters,omitempty"`
	Boosts          []Boost            `json:"boosts,omitempty"`
	Budget          *Budget            `json:"budget,omitempty"`
	Explain         bool               `json:"explain,omitempty"`
	// At pins the recency clock for reproducible replay and evaluation.
	At time.Time `json:"at,omitempty"`
}
type Hit struct {
	ID    string   `json:"id"`
	Rank  int      `json:"rank"`
	Score float64  `json:"score"`
	Why   []string `json:"why"`
}
type Item struct {
	Node
	Score     float64  `json:"score"`
	CostValue float64  `json:"cost"`
	Why       []string `json:"why"`
}
type Result struct {
	Abstained        bool             `json:"abstained"`
	AbstentionReason string           `json:"abstention_reason,omitempty"`
	Dataset          DatasetState     `json:"dataset"`
	Items            []Item           `json:"items"`
	TotalCost        float64          `json:"total_cost"`
	Considered       int              `json:"considered"`
	DroppedForBudget int              `json:"dropped_for_budget"`
	ConfigHash       string           `json:"config_hash"`
	Ranks            map[string][]Hit `json:"ranks,omitempty"`
	FusedOrder       []Hit            `json:"fused_order,omitempty"`
}
type Retriever interface {
	Retrieve(context.Context, Query, int) ([]Hit, error)
}

// Provider must honor context cancellation and support concurrent Embed calls:
// query embedding can overlap a background reindex build.
type Provider interface {
	Model() string
	Embed(context.Context, []string) ([][]float32, error)
}
type CostFunc func(Node) (float64, error)
