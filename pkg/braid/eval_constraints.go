package braid

import "fmt"

type EvalConstraints struct {
	MaxForbiddenHitRate    *float64 `json:"max_forbidden_hit_rate,omitempty" yaml:"max_forbidden_hit_rate,omitempty"`
	MaxMissingRequiredRate *float64 `json:"max_missing_required_rate,omitempty" yaml:"max_missing_required_rate,omitempty"`
	MaxBudgetOverrunRate   *float64 `json:"max_budget_overrun_rate,omitempty" yaml:"max_budget_overrun_rate,omitempty"`
}

func (c EvalConstraints) validate() error {
	for _, v := range []*float64{c.MaxForbiddenHitRate, c.MaxMissingRequiredRate, c.MaxBudgetOverrunRate} {
		if v != nil && (!finite(*v) || *v < 0 || *v > 1) {
			return fmt.Errorf("evaluation constraints must be rates in [0,1]")
		}
	}
	return nil
}
func (c *EvalConstraints) violations(m Metrics) []string {
	out := []string{}
	if c == nil {
		return out
	}
	for _, v := range []struct {
		name  string
		value float64
		limit *float64
	}{{"forbidden_hit_rate", m.ForbiddenHitRate, c.MaxForbiddenHitRate}, {"missing_required_rate", m.MissingRequiredRate, c.MaxMissingRequiredRate}, {"budget_overrun_rate", m.BudgetOverrunRate, c.MaxBudgetOverrunRate}} {
		if v.limit != nil && v.value > *v.limit {
			out = append(out, v.name)
		}
	}
	return out
}
func (s *evaluationStats) addBudget(r Result, l Label, defaults Budget) {
	s.m.BudgetCases++
	budget := defaults
	if l.Query.Budget != nil {
		budget = *l.Query.Budget
	}
	over := r.TotalCost > budget.Max
	if l.Output != nil {
		b, err := renderContext(r.Items, *l.Output)
		over = over || err != nil || len(b) > l.Output.MaxBytes
	}
	if over {
		s.m.BudgetOverrunRate++
	}
}

type EvaluationOutcome struct {
	QueryID          string                `json:"query_id"`
	Query            Query                 `json:"query"`
	Output           *Output               `json:"output,omitempty"`
	Accounting       CostAccounting        `json:"accounting"`
	Stages           map[string]StageStats `json:"stages,omitempty"`
	AbstentionReason string                `json:"abstention_reason,omitempty"`
}

func outcome(l Label, r Result) EvaluationOutcome {
	return EvaluationOutcome{QueryID: l.QueryID, Query: l.Query, Output: l.Output, Accounting: r.Accounting, Stages: r.Stages, AbstentionReason: r.AbstentionReason}
}
