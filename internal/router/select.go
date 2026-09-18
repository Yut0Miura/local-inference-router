package router

// selectLocked picks the next route for an alias. The caller must hold s.mu.
//
// The order is exact:
//  1. skip routes already attempted for this request;
//  2. keep routes whose backend is checked and healthy;
//  3. keep routes whose backend is below max_inflight;
//  4. prefer the lowest priority value;
//  5. within one priority, prefer the backend with the fewest inflight requests;
//  6. on an exact tie, keep configuration order.
func (s *State) selectLocked(alias *aliasState, attempted map[*Route]bool) *Route {
	var best *Route
	var bestInflight int

	for _, route := range alias.routes {
		if attempted[route] {
			continue
		}
		bs := s.backends[route.Backend.Name]
		if !bs.checked || !bs.healthy {
			continue
		}
		if bs.inflight >= bs.backend.MaxInflight {
			continue
		}

		if best == nil {
			best, bestInflight = route, bs.inflight
			continue
		}
		switch {
		case route.Priority < best.Priority:
			best, bestInflight = route, bs.inflight
		case route.Priority > best.Priority:
			// keep the current best
		case bs.inflight < bestInflight:
			best, bestInflight = route, bs.inflight
		}
		// Equal priority and equal inflight: the earlier route keeps its place,
		// which is configuration order.
	}
	return best
}
