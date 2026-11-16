package server

import (
	"fmt"
	"time"
)

// ControlLeaseTTL is how long an operator keeps exclusive control without
// sending input. It is renewed on every accepted event, so an operator who is
// actually driving never loses it, while an abandoned tab stops blocking the
// beacon after a minute.
const ControlLeaseTTL = 60 * time.Second

// controlHolder is one operator's exclusive-control grant for an agent.
type controlHolder struct {
	OperatorID uint
	Username   string
	Since      time.Time
	Renewed    time.Time
}

// expired reports whether the lease has lapsed at now.
func (h controlHolder) expired(now time.Time) bool {
	return now.Sub(h.Renewed) > ControlLeaseTTL
}

// claimControlAt is the testable core: take or renew the per-agent control
// lease.
//
// A free/expired lease is claimed silently (the first operator to act owns the
// beacon). A live lease held by someone else refuses unless force is set; force
// is the explicit, audited take-over a supervisor uses instead of waiting for
// the holder's session to lapse.
func (s *Server) claimControlAt(agentID string, operatorID uint, username string, force bool, now time.Time) (controlHolder, bool) {
	s.controlMu.Lock()
	defer s.controlMu.Unlock()
	if s.controlLeases == nil {
		s.controlLeases = make(map[string]controlHolder)
	}
	cur, ok := s.controlLeases[agentID]
	if ok && !cur.expired(now) && cur.OperatorID != operatorID && !force {
		return cur, false
	}
	if !ok || cur.expired(now) || cur.OperatorID != operatorID {
		cur = controlHolder{OperatorID: operatorID, Username: username, Since: now}
	}
	cur.Renewed = now
	s.controlLeases[agentID] = cur
	return cur, true
}

// claimControl is claimControlAt against the wall clock.
func (s *Server) claimControl(agentID string, operatorID uint, username string, force bool) (controlHolder, bool) {
	return s.claimControlAt(agentID, operatorID, username, force, time.Now())
}

// releaseControlAt drops the lease only when operatorID still holds it, so a
// stale tab cannot release control it lost.
func (s *Server) releaseControlAt(agentID string, operatorID uint, now time.Time) bool {
	s.controlMu.Lock()
	defer s.controlMu.Unlock()
	cur, ok := s.controlLeases[agentID]
	if !ok || cur.OperatorID != operatorID {
		return false
	}
	delete(s.controlLeases, agentID)
	return true
}

// releaseControl is releaseControlAt against the wall clock.
func (s *Server) releaseControl(agentID string, operatorID uint) bool {
	return s.releaseControlAt(agentID, operatorID, time.Now())
}

// controlHolderAt returns the live holder of an agent's lease, if any. Expired
// entries are treated as free (and pruned).
func (s *Server) controlHolderAt(agentID string, now time.Time) (controlHolder, bool) {
	s.controlMu.Lock()
	defer s.controlMu.Unlock()
	cur, ok := s.controlLeases[agentID]
	if !ok {
		return controlHolder{}, false
	}
	if cur.expired(now) {
		delete(s.controlLeases, agentID)
		return controlHolder{}, false
	}
	return cur, true
}

// controlHolderFor is controlHolderAt against the wall clock.
func (s *Server) controlHolderFor(agentID string) (controlHolder, bool) {
	return s.controlHolderAt(agentID, time.Now())
}

// controlConflictMessage is the operator-facing refusal: it names who holds
// control, because "another operator has control" without a name is not
// actionable.
func controlConflictMessage(holder controlHolder) string {
	if holder.Username == "" {
		return "another operator has control of this beacon"
	}
	return fmt.Sprintf("another operator has control of this beacon (%s)", holder.Username)
}
