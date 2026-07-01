package cockpit

import (
	"os"
	"strings"
)

// SessionPhase is ECAM-style flight phase for a coding session.
type SessionPhase string

const (
	PhaseEmergency SessionPhase = "emergency"
	PhasePreflight SessionPhase = "preflight"
	PhaseApproach  SessionPhase = "approach"
	PhaseLanding   SessionPhase = "landing"
	PhaseCruise    SessionPhase = "cruise"
)

func detectPhase(s Signals, prReview string) SessionPhase {
	if s.ContextUsedPct >= 90 || s.Rate5hPct >= 90 || s.Rate7dPct >= 90 {
		return PhaseEmergency
	}
	if s.Turns <= 8 || (s.Searches >= 5 && s.Turns <= 15) {
		return PhasePreflight
	}
	if prReview != "" {
		switch prReview {
		case "APPROVED":
			return PhaseLanding
		case "REVIEW_REQUIRED", "CHANGES_REQUESTED", "COMMENTED":
			return PhaseApproach
		}
	}
	if s.Turns >= 40 && s.ContextUsedPct >= 70 {
		return PhaseLanding
	}
	return PhaseCruise
}

func (p SessionPhase) label() string {
	return sessionPhaseDisplay(string(p))
}

// sessionPhaseDisplay maps internal phase ids to short status-bar labels.
func sessionPhaseDisplay(phase string) string {
	switch strings.ToLower(strings.TrimSpace(phase)) {
	case "emergency", "emer":
		return "hot"
	case "preflight":
		return "warmup"
	case "approach":
		return "review"
	case "landing":
		return "ship"
	default:
		return "steady"
	}
}

func costIndex() string {
	switch strings.ToLower(strings.TrimSpace(os.Getenv("COCKPIT_COST_INDEX"))) {
	case "eco", "economy", "save":
		return "eco"
	case "perf", "performance", "fast":
		return "perf"
	default:
		return "normal"
	}
}

func displayMode() string {
	switch strings.ToLower(strings.TrimSpace(os.Getenv("COCKPIT_DISPLAY"))) {
	case "minimal", "min":
		return "minimal"
	case "debug":
		return "debug"
	default:
		return "full"
	}
}
