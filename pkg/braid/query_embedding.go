package braid

import (
	"context"
	"database/sql"
	"fmt"
	"strings"
)

// Package A: https://github.com/algorhythmic/muninn/blob/8bdda8b/docs/braid-integration/A-EMBEDDING-COMPATIBILITY.md

// QueryEmbedding supplies an authoritative query vector in the configured space.
// Model identifies the complete embedding space, including preprocessing.
type QueryEmbedding struct {
	Model  string    `json:"model"`
	Vector []float32 `json:"vector"`
}

// EmbeddingIncompatibilityError reports a supplied query vector that cannot be
// used with the published embedding space. Code is also the protocol error code.
type EmbeddingIncompatibilityError struct {
	Code    string
	Message string
}

func (e *EmbeddingIncompatibilityError) Error() string { return e.Message }

func embeddingProblem(code, message string) error {
	return &EmbeddingIncompatibilityError{Code: code, Message: message}
}

// Validate against the unfiltered model space in the same read transaction as
// retrieval. In particular, empty eligibility and disabled dense retrieval must
// not hide incompatible vectors. The absent path performs no additional reads.
func (e *Engine) validateQueryEmbedding(ctx context.Context, q Query) error {
	v := q.QueryEmbedding
	if v == nil {
		return nil
	}
	if len(v.Vector) == 0 {
		return embeddingProblem("embedding_empty_vector", "query embedding vector must not be empty")
	}
	nonzero := false
	for _, x := range v.Vector {
		if !finite(float64(x)) {
			return embeddingProblem("embedding_nonfinite_vector", "query embedding vector must be finite")
		}
		nonzero = nonzero || x != 0
	}
	if !nonzero {
		return embeddingProblem("embedding_zero_vector", "query embedding vector must not be all zero")
	}
	if v.Model == "" || e.cfg.Embed.Model == "" || v.Model != e.cfg.Embed.Model {
		return embeddingProblem("embedding_model_mismatch", "query embedding model must exactly match configured embed.model")
	}
	if len(q.Anchors) > 0 && strings.TrimSpace(q.Text) == "" {
		return embeddingProblem("embedding_ambiguous_anchors", "query embedding with anchors requires nonblank text to disambiguate anchor averaging")
	}
	var dim int
	err := e.reader().QueryRowContext(ctx, `SELECT dim FROM embeddings WHERE model=? LIMIT 1`, v.Model).Scan(&dim)
	if err == sql.ErrNoRows {
		return embeddingProblem("embedding_dimension_unavailable", "no stored embedding dimension for configured model")
	}
	if err != nil {
		return err
	}
	if len(v.Vector) != dim {
		return embeddingProblem("embedding_dimension_mismatch", fmt.Sprintf("query embedding dimension %d differs from stored model dimension %d", len(v.Vector), dim))
	}
	return nil
}
