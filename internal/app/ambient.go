// ambient.go — the office's SOCIAL LIFE: a SocialClock that makes employees
// talk to each other, invite each other for tea/coffee walks, and trade
// gossip — layered on top of the ambient tick, never touching backend state.
//
// EVENTS (weights): (a) DIRECTED BANTER 40% — a 2-3 beat pair-dialogue
// between two employees (phrase banks keyed by role pairing: dev-dev,
// scout-dev, reviewer-anyone, hr-anyone); (b) TEA REQUEST 25% — A asks
// "<B>: coffee?", both drift to the machine (EvIdleDrift A then B), and on
// arrival both bubble "good idea." once; (c) GOSSIP 20% — a 3-beat chain
// mentioning an absent third employee BY NAME; (d) WATER-COOLER 15% — an
// idle employee wanders for a solo coffee with a self-bubble.
//
// GATES: never while a permission/question modal is open, a boss think
// stream is live, or a dispatch went out <30 ticks ago (busy !== social);
// firings are spaced (small office ~120-180 ticks ≈ up to ~3 min; 6+
// employees ~90-150); brain.json ui.ambientChatter=false silences the whole
// generator; a solo office (boss+hr) gets water-cooler only, half as often.
//
// DELIVERY: socialStep is the pure decision tree (seeded by tick+seq — the
// same tick sequence always yields the same social sequence); it returns a
// plan of beats with tick delays. The model owns the pending-beats table
// and tick counters, and emits each beat as plain EvBubble/EvIdleDrift
// events through the normal reducer path (dedupe-safe: fired beats are
// popped, one firing window guard per decision).
package app

import (
	"fmt"
	"math/rand"
	"strings"

	"github.com/theboringhumane/grafeio/internal/office"
	"github.com/theboringhumane/grafeio/internal/state"
)

const (
	socialBeatGap  = 5  // ticks between dialogue beats (~2s at busy cadence)
	socialTeaWalkA = 2  // ticks after the ask: A starts walking (~1s)
	socialTeaWalkB = 6  // ticks after the ask: B follows (~3s)
	socialTeaCheck = 51 // ticks after the ask: arrival check (B.walk + ~45)

	socialSpacing    = 120 // base ticks between firings (small office, max ~3 min)
	socialSpacingBig = 90  // 6+ employees: tighter social grid (~90s)
	socialDispatchWA = 30  // a dispatch younger than this many ticks = busy

	socialRollBanter = 40 // [0,40)  banter
	socialRollTea    = 65 // [40,65) tea request
	socialRollGossip = 85 // [65,85) gossip            (>=85) water-cooler
)

// ambientOn — brain.json ui.ambientChatter, set in New. False silences the
// whole social generator (explicit EvBubble events still render).
var ambientOn = true

// SocialForceRoll, when non-nil, replaces the social-type roll in socialStep
// (0-39 banter, 40-64 tea request, 65-84 gossip, 85+ water-cooler).
// uishot --social only; nil in production.
var SocialForceRoll *int

// SocialTracef, when set, receives one line per fired social beat (the
// banter-chain / tea-pair proof in uishot --social). Nil in production —
// the decision path checks before formatting.
var SocialTracef func(format string, args ...any)

func socialTracef(format string, args ...any) {
	if SocialTracef != nil {
		SocialTracef(format, args...)
	}
}

// --- phrase banks (pure ASCII, office-funny, never mean, 3-14 words/line) --
// Even-indexed lines are spoken by A, odd-indexed by B.

// socialSoloBank — the water-cooler / solo bank. The legacy ambient list
// lives here now (the social clock's (d) covers every old case, plus
// "stretch time." for the wander itself).
var socialSoloBank = []string{
	"stretch time.",
	"big day. lots of meetings.",
	"shipping friday.",
	"who took the red mug?",
	"standup in 5.",
	"this diff is a crime scene.",
	"coffee machine is empty again.",
	"review queue is deep today.",
	"anyone seen the staging key?",
	"quiet floor today.",
}

var socialBanterDevDev = [][]string{
	{"that diff was a crime scene.", "boss made me read it twice."},
	{"it works on my machine, as tradition demands.", "then we ship your machine."},
	{"rebasing is a personality trait at this point.", "your branch has a branch.", "it needed a friend."},
	{"the cache can't be the problem.", "famous last words, every thursday."},
}

var socialBanterScoutDev = [][]string{
	{"i crawled the whole repo before standup.", "and what did the repo do to deserve that?"},
	{"found the bug. it was us.", "always is. good archaeology though."},
	{"the edge case has edge cases.", "cool. i filed them their own test suite."},
}

var socialBanterReviewer = [][]string{
	{"did dikastes reject yours too?", "he called it 'a strong opinion'.", "that means yes. twice."},
	{"your pull request smells like friday afternoon.", "it IS friday afternoon.", "approved with feelings."},
}

var socialBanterHR = [][]string{
	{"hr gave the plant a performance review.", "it promised to grow next quarter."},
	{"wellness hour is at four today.", "does debugging count as cardio?", "hr said yes. exactly once."},
}

// socialGossipBank — 3-beat chains in A, B, A order; {C} is the absent
// third employee's name, substituted at plan time.
var socialGossipBank = [][]string{
	{"did {C} really reject that?", "without a single comment. brutal.", "classic {C}, honestly."},
	{"{C} refactored the builder before coffee.", "before COFFEE?", "before coffee."},
	{"heard {C} fixed prod with one line.", "one FONT? no, one LINE.", "save some bugs for the rest of us."},
}

// banterBank picks the role-pairing bank (dev-dev, scout-dev,
// reviewer-anyone, hr-anyone; anything else falls back to dev-dev).
func banterBank(ra, rb state.EmployeeRole) [][]string {
	switch {
	case ra == state.RoleHR || rb == state.RoleHR:
		return socialBanterHR
	case ra == state.RoleReviewer || rb == state.RoleReviewer:
		return socialBanterReviewer
	case (ra == state.RoleScout && rb == state.RoleDeveloper) ||
		(rb == state.RoleScout && ra == state.RoleDeveloper):
		return socialBanterScoutDev
	default:
		return socialBanterDevDev
	}
}

// socialBeat — one scheduled leg of a social event: plain events to emit at
// `due` ticks, or an arrival CHECK (checkTea): both walkers reached the
// machine → both bubble "good idea." exactly once.
type socialBeat struct {
	due          int
	events       []state.Event
	checkTea     bool
	teaA, teaB   string // walker ids for the arrival check
}

// SocialClock — the decision state. Model copies share ONE instance (pointer
// field) so the plan survives the value-copy update loop.
type SocialClock struct {
	lastFired int           // tick of the most recent firing decision
	seq       int           // firing sequence (part of the deterministic seed)
	pending   []socialBeat  // armed beats, tick-counter scheduled (disarm: pop)
	teaPairs  map[string]bool // walker ids with an in-flight tea walk (overlap guard)
}

func newSocialClock() *SocialClock {
	return &SocialClock{teaPairs: map[string]bool{}}
}

// seed — deterministic PRNG for one decision: same tick + same seq ⇒ same
// social outcome across runs (the uishot --social determinism proof).
func (c *SocialClock) rand(tick int) *rand.Rand {
	return rand.New(rand.NewSource(int64(tick)*73402369 + int64(c.seq)*2713 + 481))
}

// crewMembers — non-manager employees (the boss never banters; hr does).
func crewMembers(st state.OfficeState) []state.Employee {
	var crew []state.Employee
	for _, e := range st.Employees {
		if e.Role != state.RoleManager {
			crew = append(crew, e)
		}
	}
	return crew
}

// socialStep — the pure decision tree: given the current state and the
// busy/modal gates, either no plan (gate/spacing shut) or the full beat plan
// of one social event. Mutates only the clock's own fire bookkeeping
// (lastFired/seq) so a computed plan can't double-fire.
func (c *SocialClock) socialStep(st state.OfficeState, modalOpen, thinkActive bool, lastDispatchTick int) []socialBeat {
	if !ambientOn {
		return nil
	}
	// gate (1): busy office, no social.
	if modalOpen || thinkActive {
		return nil
	}
	if lastDispatchTick >= 0 && st.Tick-lastDispatchTick < socialDispatchWA {
		return nil
	}
	crew := crewMembers(st)
	if len(crew) == 0 {
		return nil
	}
	r := c.rand(st.Tick)
	solo := len(crew) <= 1 // boss+hr only (or less): water-cooler, half as often

	// gate (2): firing spacing.
	spacing := socialSpacing + r.Intn(61) // ~120-180 ticks (max ~3 min)
	if len(st.Employees) >= 6 {
		spacing = socialSpacingBig + r.Intn(31) // ~90-120 ticks (max ~90s)
	}
	if solo {
		spacing *= 2
	}
	if st.Tick-c.lastFired < spacing {
		return nil
	}
	c.lastFired = st.Tick
	c.seq++

	now := st.Tick
	if solo {
		return c.planWaterCooler(crew, r, now)
	}

	roll := r.Intn(100)
	if SocialForceRoll != nil {
		roll = *SocialForceRoll
	}
	switch {
	case roll < socialRollBanter:
		if beats := c.planBanter(crew, r, now); len(beats) > 0 {
			return beats
		}
	case roll < socialRollTea:
		if beats := c.planTea(crew, r, now); len(beats) > 0 {
			return beats
		}
	case roll < socialRollGossip:
		if beats := c.planGossip(crew, r, now); len(beats) > 0 {
			return beats
		}
	}
	// unavailable pairing / force roll water-cooler / roll >= 85: solo drift.
	return c.planWaterCooler(crew, r, now)
}

// pickPartner — B for A, preferring the same side of the floor (seat-anchor
// x on the same half) when any same-side candidate exists.
func pickPartner(a state.Employee, crew []state.Employee, r *rand.Rand) (state.Employee, bool) {
	planW := office.CurrentPlan().Width
	ax := office.SeatAnchor(a.Seat).X < planW/2
	var same, other []state.Employee
	for _, e := range crew {
		if e.ID == a.ID {
			continue
		}
		if (office.SeatAnchor(e.Seat).X < planW/2) == ax {
			same = append(same, e)
		} else {
			other = append(other, e)
		}
	}
	if len(same) > 0 {
		return same[r.Intn(len(same))], true
	}
	if len(other) > 0 {
		return other[r.Intn(len(other))], true
	}
	return state.Employee{}, false
}

// planBanter (a) — a 2-3 beat pair dialog: A bubbles now, B answers
// socialBeatGap ticks later (optional third beat back to A).
func (c *SocialClock) planBanter(crew []state.Employee, r *rand.Rand, now int) []socialBeat {
	if len(crew) < 2 {
		return nil
	}
	a := crew[r.Intn(len(crew))]
	b, ok := pickPartner(a, crew, r)
	if !ok {
		return nil
	}
	bank := banterBank(a.Role, b.Role)
	chain := bank[r.Intn(len(bank))]
	speakers := [2]state.Employee{a, b}
	var beats []socialBeat
	for i, line := range chain {
		sp := speakers[i%2]
		beats = append(beats, socialBeat{due: now + i*socialBeatGap,
			events: []state.Event{{Kind: state.EvBubble, EmployeeID: sp.ID, Text: line}}})
		socialTracef("banter: %s › %q", sp.Name, line)
	}
	return beats
}

// planTea (b) — A asks "<B>: coffee?", A drifts at +2 ticks, B follows at
// +6, and an arrival check (their walk + ~45 ticks) bubbles "good idea."
// from both only if both actually reached the machine. Skew walkers (one
// already on a tea walk, or active-walking) disqualify the pair.
func (c *SocialClock) planTea(crew []state.Employee, r *rand.Rand, now int) []socialBeat {
	if len(crew) < 2 {
		return nil
	}
	// eligible: parked at a desk (idle or working), not mid-walk/mid-break,
	// not already invited on an in-flight tea walk.
	eligible := make([]state.Employee, 0, len(crew))
	for _, e := range crew {
		if c.teaPairs[e.ID] {
			continue
		}
		switch e.Sprite {
		case state.SpriteAtDesk, state.SpriteWorking:
			eligible = append(eligible, e)
		}
	}
	if len(eligible) < 2 {
		return nil
	}
	a := eligible[r.Intn(len(eligible))]
	b, ok := pickPartner(a, eligible, r)
	if !ok {
		return nil
	}
	c.teaPairs[a.ID], c.teaPairs[b.ID] = true, true
	socialTracef("tea: %s asks %s", a.Name, b.Name)
	return []socialBeat{
		{due: now, events: []state.Event{
			{Kind: state.EvBubble, EmployeeID: a.ID, Text: b.Name + ": coffee?"}}},
		{due: now + socialTeaWalkA, events: []state.Event{
			{Kind: state.EvIdleDrift, EmployeeID: a.ID}}},
		{due: now + socialTeaWalkB, events: []state.Event{
			{Kind: state.EvIdleDrift, EmployeeID: b.ID}}},
		{due: now + socialTeaCheck, checkTea: true, teaA: a.ID, teaB: b.ID},
	}
}

// planGossip (c) — a 3-beat chain between two talkers mentioning an absent
// third employee BY NAME.
func (c *SocialClock) planGossip(crew []state.Employee, r *rand.Rand, now int) []socialBeat {
	if len(crew) < 3 {
		return nil
	}
	a := crew[r.Intn(len(crew))]
	b, ok := pickPartner(a, crew, r)
	if !ok {
		return nil
	}
	var third *state.Employee
	for i := range crew {
		if crew[i].ID != a.ID && crew[i].ID != b.ID {
			third = &crew[i]
			break
		}
	}
	if third == nil {
		return nil
	}
	chain := socialGossipBank[r.Intn(len(socialGossipBank))]
	speakers := [2]state.Employee{a, b}
	var beats []socialBeat
	for i, raw := range chain {
		sp := speakers[i%2]
		line := strings.ReplaceAll(raw, "{C}", third.Name)
		beats = append(beats, socialBeat{due: now + i*socialBeatGap,
			events: []state.Event{{Kind: state.EvBubble, EmployeeID: sp.ID, Text: line}}})
		socialTracef("gossip: %s › %q", sp.Name, line)
	}
	socialTracef("gossip subject: %s", third.Name)
	return beats
}

// planWaterCooler (d) — an idle employee wanders off for a solo coffee with
// a self-bubble from the legacy bank; with nobody idling, a single solo
// line for a random crew member (the legacy ambient case).
func (c *SocialClock) planWaterCooler(crew []state.Employee, r *rand.Rand, now int) []socialBeat {
	line := socialSoloBank[r.Intn(len(socialSoloBank))]
	for _, e := range crew {
		if e.Sprite == state.SpriteAtDesk && !c.teaPairs[e.ID] {
			socialTracef("water-cooler: %s drifts for coffee (%q)", e.Name, line)
			return []socialBeat{{due: now, events: []state.Event{
				{Kind: state.EvBubble, EmployeeID: e.ID, Text: line},
				{Kind: state.EvIdleDrift, EmployeeID: e.ID}}}}
		}
	}
	e := crew[r.Intn(len(crew))]
	socialTracef("water-cooler: %s daydreams (%q)", e.Name, line)
	return []socialBeat{{due: now, events: []state.Event{
		{Kind: state.EvBubble, EmployeeID: e.ID, Text: line}}}}
}

// runSocial — the model-side tick hook: arm the clock's decision (if any),
// then fire every due beat through the normal reducer path. Dedupe-safe:
// pending beats are popped as they fire; a plan's first beat carries the
// decision's due=now and fires in the same pass.
func (m *Model) runSocial() {
	if m.social == nil {
		return
	}
	if beats := m.social.socialStep(m.st, m.perm != nil || m.question != nil,
		len(m.activeThink) > 0, m.lastDispatchTick); len(beats) > 0 {
		m.social.pending = append(m.social.pending, beats...)
	}
	var rest []socialBeat
	for _, b := range m.social.pending {
		if b.due > m.st.Tick {
			rest = append(rest, b)
			continue
		}
		if b.checkTea {
			m.social.arrivalTeaDone(m, b)
			continue
		}
		for _, ev := range b.events {
			m.applyEvent(ev)
		}
	}
	m.social.pending = rest
}

// arrivalTeaDone — the tea-walk arrival check: only if BOTH walkers are
// parked at the machine do they bubble "good idea." (one shot; the pair is
// released regardless so the next tea request can form).
func (c *SocialClock) arrivalTeaDone(m *Model, b socialBeat) {
	delete(c.teaPairs, b.teaA)
	delete(c.teaPairs, b.teaB)
	a, bb := findEmployee(m.st, b.teaA), findEmployee(m.st, b.teaB)
	if a == nil || bb == nil || a.Sprite != state.SpriteCoffee || bb.Sprite != state.SpriteCoffee {
		socialTracef("tea: %s + %s arrival check skipped (not both at coffee)", b.teaA, b.teaB)
		return
	}
	socialTracef("tea: %s + %s reached coffee — %q", a.Name, bb.Name, "good idea.")
	m.applyEvent(state.Event{Kind: state.EvBubble, EmployeeID: b.teaA, Text: "good idea."})
	m.applyEvent(state.Event{Kind: state.EvBubble, EmployeeID: b.teaB, Text: "good idea."})
}

// String — one-line social clock readout (debugging).
func (c *SocialClock) String() string {
	return fmt.Sprintf("social{lastFired=%d seq=%d pending=%d walkers=%d}",
		c.lastFired, c.seq, len(c.pending), len(c.teaPairs))
}
