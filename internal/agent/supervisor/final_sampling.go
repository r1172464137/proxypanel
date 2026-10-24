package supervisor

// Final-sample loss cannot be repaired by a later healthy periodic sample of a
// different generation. Keep a cumulative, operator-visible metric, and bounded
// sanitized detail, without dropping or acknowledging any queued accounting.
func (s *Supervisor) noteFinalSamplingError(nodeID, reason string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.metrics["final_sampling_errors"]++
	s.metrics["health_degraded"] = 1
	s.lastSamplingErrors = append(s.lastSamplingErrors, nodeID+": "+reason)
	if len(s.lastSamplingErrors) > 32 {
		s.lastSamplingErrors = s.lastSamplingErrors[len(s.lastSamplingErrors)-32:]
	}
}
