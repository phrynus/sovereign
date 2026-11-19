package server

import (
	"strconv"
	"time"
)

// linkQualitySnapshot is the latest self-reported link measurement from an
// implant. Values come from the beacon `info` map (agent-side link_quality.go),
// which the server sanitizes and allowlists. Only the operator-facing agent
// detail endpoint exposes these; they never leave the authenticated API.
type linkQualitySnapshot struct {
	RTTCms   int     `json:"rtt_ms"`
	LossEWMA float64 `json:"loss_ewma"`
	Samples  int     `json:"samples"`
	Total    int     `json:"total"`
	At       int64   `json:"at"` // unix seconds the snapshot was recorded
}

// parseLinkQualityInfo converts the raw info fields to a snapshot. Missing or
// malformed fields yield zero values — a beacon that hasn't reported yet is
// indistinguishable from one with no measurement, never an error to the UI.
func parseLinkQualityInfo(info map[string]string) linkQualitySnapshot {
	getInt := func(k string) int {
		if v, err := strconv.Atoi(info[k]); err == nil && v >= 0 {
			return v
		}
		return 0
	}
	getFloat := func(k string) float64 {
		if v, err := strconv.ParseFloat(info[k], 64); err == nil && v >= 0 && v <= 1 {
			return v
		}
		return 0
	}
	return linkQualitySnapshot{
		RTTCms:   getInt("link_rtt_ms"),
		LossEWMA: getFloat("link_loss"),
		Samples:  getInt("link_samples"),
		Total:    getInt("link_total"),
	}
}

// hasLinkQuality reports whether the agent has sent at least one measurement.
func (s linkQualitySnapshot) hasLinkQuality() bool {
	return s.Total > 0 || s.Samples > 0
}

func (s *Server) recordLinkQuality(agentID string, snap linkQualitySnapshot) {
	if agentID == "" {
		return
	}
	snap.At = time.Now().Unix()
	s.linkQualityMu.Lock()
	defer s.linkQualityMu.Unlock()
	if s.linkQuality == nil {
		s.linkQuality = make(map[string]linkQualitySnapshot)
	}
	s.linkQuality[agentID] = snap
}

func (s *Server) getLinkQuality(agentID string) (linkQualitySnapshot, bool) {
	s.linkQualityMu.RLock()
	defer s.linkQualityMu.RUnlock()
	snap, ok := s.linkQuality[agentID]
	return snap, ok
}

// linkQualitySweep drops snapshots older than maxAge. Called from the 24h
// cleanup; keyed by agent id and stamped on each beacon, so a dead agent's
// entry expires without a DB query and the map stays bounded by fleet size.
func (s *Server) linkQualitySweep(maxAge time.Duration) int {
	cutoff := time.Now().Add(-maxAge).Unix()
	s.linkQualityMu.Lock()
	defer s.linkQualityMu.Unlock()
	n := 0
	for id, snap := range s.linkQuality {
		if snap.At < cutoff {
			delete(s.linkQuality, id)
			n++
		}
	}
	return n
}
