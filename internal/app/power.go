// power.go — the adaptive power governor: the office's tick cadence follows
// busyness per cycle. Busy = smooth (fast re-arm), idle = cheap (slow
// re-arm), auto drifts to a 3s "screensaver cadence" after 60s of quiet.
// The whole posture is brain.json-driven (cfg.UI.Power, cfg.UI.TickMs) and
// pure: TickDelay decides, the model re-arms, nothing else changes.
package app

import (
	"time"

	"github.com/theboringhumane/theboringoffice/internal/config"
	"github.com/theboringhumane/theboringoffice/internal/state"
)

const (
	tickBusyAuto     = 180 * time.Millisecond // auto busy floor (the old fixed cadence)
	tickIdleAuto     = 1 * time.Second        // auto idle floor
	tickDrift        = 3 * time.Second        // auto: idle for >60s — screensaver cadence
	tickPerf         = 150 * time.Millisecond // performance: always smooth
	tickBusySaver    = 400 * time.Millisecond // saver busy floor
	tickIdleSaver    = 2 * time.Second        // saver idle floor
	idleDriftTrigger = 60 * time.Second       // continuous idle before drift mode (auto only)
)

// PowerMode normalizes cfg.UI.Power; nil cfg, "" or unknown → auto.
func PowerMode(cfg *config.Config) config.PowerMode {
	if cfg == nil {
		return config.PowerAuto
	}
	switch cfg.UI.Power {
	case config.PowerPerformance:
		return config.PowerPerformance
	case config.PowerSaver:
		return config.PowerSaver
	default:
		return config.PowerAuto
	}
}

// officeBusy — the governor's BUSY signals (any one re-arms fast):
// streaming boss / pending bubble, an open think stream, a walking sprite,
// a live ambient bubble, or an open permission/question modal.
func officeBusy(st state.OfficeState, modalOpen, thinkActive bool) bool {
	if modalOpen || thinkActive || len(st.Bubbles) > 0 {
		return true
	}
	for _, c := range st.Chat {
		if c.From == "boss" && c.Pending {
			return true
		}
	}
	for _, e := range st.Employees {
		switch e.Sprite {
		case state.SpriteToManager, state.SpriteToDesk, state.SpriteToCoffee:
			return true
		}
	}
	return false
}

// TickDelay — the per-cycle re-arm delay. cfg.UI.TickMs > 0 overrides the
// busy delay of every power mode (the idle/drift floors stay per-mode);
// otherwise: auto busy 180ms / idle 1s / idle 60s+ → 3s drift, performance
// constant 150ms, saver busy 400ms / idle 2s. thinkActive = an open boss
// EvThought stream; modalOpen = a permission/question modal is up.
func TickDelay(st state.OfficeState, cfg *config.Config, thinkActive, modalOpen bool, idleFor time.Duration) time.Duration {
	base := time.Duration(0)
	if cfg != nil && cfg.UI.TickMs > 0 {
		base = time.Duration(cfg.UI.TickMs) * time.Millisecond
	}
	busyDelay := func(dflt time.Duration) time.Duration {
		if base > 0 {
			return base
		}
		return dflt
	}
	switch PowerMode(cfg) {
	case config.PowerPerformance:
		return busyDelay(tickPerf)
	case config.PowerSaver:
		if officeBusy(st, modalOpen, thinkActive) {
			return busyDelay(tickBusySaver)
		}
		return tickIdleSaver
	default: // auto
		if officeBusy(st, modalOpen, thinkActive) {
			return busyDelay(tickBusyAuto)
		}
		if idleFor >= idleDriftTrigger {
			return tickDrift
		}
		return tickIdleAuto
	}
}

// powerDescribe — one-line human summary of a mode's cadence (slash output).
func powerDescribe(mode config.PowerMode) string {
	switch mode {
	case config.PowerPerformance:
		return "constant 150ms — always smooth"
	case config.PowerSaver:
		return "busy 400ms · idle 2s — battery first"
	default:
		return "busy 180ms · idle 1s · 3s drift after 60s quiet"
	}
}

// currentTick — the delay the governor would arm this exact cycle
// (slash/readout display; the same signals tickCmd feeds).
func (m *Model) currentTick() time.Duration {
	return TickDelay(m.st, m.cfg,
		len(m.activeThink) > 0, m.permQ.front() != nil || m.question != nil,
		time.Since(m.gov.lastBusy))
}
