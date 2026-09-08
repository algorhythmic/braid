package braid

import (
	"context"
	"encoding/json"
	"fmt"
)

// Output bounds the UTF-8 JSON array in ExportResult.Context, including all
// projected metadata and explanations. The diagnostic envelope is separate.
type Output struct {
	Fields   []string `json:"fields"`
	Attrs    []string `json:"attrs"`
	MaxBytes int      `json:"max_bytes"`
}
type ExportRequest struct {
	Query  Query  `json:"query"`
	Output Output `json:"output"`
}
type ExportResult struct {
	LexicalStatus    string                `json:"lexical_status,omitempty"`
	Dataset          DatasetState          `json:"dataset"`
	RankingVersion   string                `json:"ranking_version"`
	ConfigHash       string                `json:"config_hash"`
	Context          json.RawMessage       `json:"context"`
	ContextBytes     int                   `json:"context_bytes"`
	Projection       Output                `json:"projection"`
	Accounting       CostAccounting        `json:"accounting"`
	TotalCost        float64               `json:"total_cost"`
	Abstained        bool                  `json:"abstained"`
	AbstentionReason string                `json:"abstention_reason,omitempty"`
	Rejections       []Rejection           `json:"rejections,omitempty"`
	Stages           map[string]StageStats `json:"stages,omitempty"`
}

func (o Output) validate() error {
	if o.MaxBytes < 2 {
		return fmt.Errorf("max_bytes must allow at least the empty JSON array (2 bytes)")
	}
	for _, f := range o.Fields {
		if !contains([]string{"id", "type", "text", "ts", "attrs", "score", "cost", "why"}, f) {
			return fmt.Errorf("unknown output field %q", f)
		}
	}
	return nil
}
func renderContext(items []Item, o Output) ([]byte, error) {
	fields := o.Fields
	if fields == nil {
		fields = []string{"id", "type", "text", "attrs"}
	}
	rows := make([]map[string]any, 0, len(items))
	for _, it := range items {
		row := map[string]any{"id": it.ID}
		for _, f := range fields {
			switch f {
			case "type":
				row[f] = it.Type
			case "text":
				row[f] = it.Text
			case "ts":
				row[f] = it.TS
			case "score":
				row[f] = it.Score
			case "cost":
				row[f] = it.CostValue
			case "why":
				row[f] = it.Why
			case "attrs":
				attrs := it.Attrs
				if o.Attrs != nil {
					attrs = map[string]any{}
					for _, key := range o.Attrs {
						if v, ok := it.Attrs[key]; ok {
							attrs[key] = v
						}
					}
				}
				row[f] = attrs
			}
		}
		rows = append(rows, row)
	}
	return json.Marshal(rows)
}
func (e *Engine) Export(ctx context.Context, req ExportRequest) (out ExportResult, err error) {
	defer func() { err = contextError(ctx, err) }()
	if err := req.Output.validate(); err != nil {
		return ExportResult{}, err
	}
	if err := e.mu.LockContext(ctx); err != nil {
		return ExportResult{}, err
	}
	defer e.mu.Unlock()
	result, err := e.queryOutput(ctx, req.Query, &req.Output)
	if err != nil {
		return ExportResult{}, err
	}
	b, err := renderContext(result.Items, req.Output)
	if err != nil {
		return ExportResult{}, err
	}
	return ExportResult{LexicalStatus: result.LexicalStatus, Dataset: result.Dataset, RankingVersion: result.RankingVersion, ConfigHash: result.ConfigHash, Context: b, ContextBytes: len(b), Projection: req.Output, Accounting: result.Accounting, TotalCost: result.TotalCost, Abstained: result.Abstained, AbstentionReason: result.AbstentionReason, Rejections: result.Rejections, Stages: result.Stages}, ctx.Err()
}
