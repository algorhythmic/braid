package braid

type evaluationStats struct {
	m                                                             Metrics
	total, answered, noAnswerFP, returned, relevant, stale, cross int
}

func (s *evaluationStats) add(ids []string, l Label) {
	s.total++
	if len(ids) > 0 {
		s.answered++
	}
	if l.NoAnswer {
		s.m.NoAnswerQueries++
		if len(ids) > 0 {
			s.noAnswerFP++
		}
	} else {
		s.m.AnswerableQueries++
		m := measure(ids, l.Expected)
		s.m.Recall5 += m.Recall5
		s.m.Recall20 += m.Recall20
		s.m.MRR += m.MRR
		s.m.NDCG10 += m.NDCG10
	}
	if len(l.StaleIDs) > 0 {
		s.m.StaleCases++
	}
	if len(l.CrossWorkstreamIDs) > 0 {
		s.m.CrossWorkstreamCases++
	}
	stale, cross := false, false
	seen := map[string]bool{}
	for i, id := range ids {
		if i >= 5 {
			break
		}
		if seen[id] {
			continue
		}
		seen[id] = true
		s.returned++
		if contains(l.Expected, id) {
			s.relevant++
		}
		stale = stale || contains(l.StaleIDs, id)
		cross = cross || contains(l.CrossWorkstreamIDs, id)
	}
	if stale {
		s.stale++
	}
	if cross {
		s.cross++
	}
}
func ratio(n, d int) float64 {
	if d == 0 {
		return 0
	}
	return float64(n) / float64(d)
}
func (s evaluationStats) metrics() Metrics {
	m := s.m
	if n := float64(m.AnswerableQueries); n > 0 {
		m.Recall5 /= n
		m.Recall20 /= n
		m.MRR /= n
		m.NDCG10 /= n
	}
	m.Coverage = ratio(s.answered, s.total)
	m.AbstentionRate = ratio(s.total-s.answered, s.total)
	m.NoAnswerFalsePositiveRate = ratio(s.noAnswerFP, m.NoAnswerQueries)
	m.Precision5 = ratio(s.relevant, s.returned)
	m.StaleHitRate5 = ratio(s.stale, m.StaleCases)
	m.CrossWorkstreamHitRate5 = ratio(s.cross, m.CrossWorkstreamCases)
	return m
}
