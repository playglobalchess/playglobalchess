// Package identity holds the core account/identity domain types for the X-3
// accounts service. It mirrors the ratified spec SPEC-S3-ACCOUNTS (§3.1 session
// record, §3.6 rated-eligibility invariant, §3.9 account state machine) — the
// types here are the single source of truth the transport/store layers build on.
//
// This package is deliberately pure (no I/O, no DB, no transport) so the
// load-bearing invariants — above all the rated_eligibility derivation — are
// unit-testable in isolation.
package identity

// SubjectKind distinguishes an ephemeral guest from a persistent account.
// A guest and an account are indistinguishable to the client at the session
// token shape (§3.1); the distinction is server-side only.
type SubjectKind string

const (
	// SubjectGuest is an ephemeral GuestSession identity (unrated-only, ACC-INV-1).
	SubjectGuest SubjectKind = "guest"
	// SubjectAccount is a persistent User identity.
	SubjectAccount SubjectKind = "account"
)

// AccountState is the lifecycle state of a User.
//
// Canonical casing is LOWERCASE (spec §11.2): the stored/compared token is
// always lowercase, so AccountActive matches a stored "active". Prose elsewhere
// may name a state in caps as English ("a RESTRICTED account"), but the enum
// value never is (closes review finding S-1: a case mismatch would make an
// account_state==active comparison never match a stored "ACTIVE").
type AccountState string

const (
	// AccountActive: a fully enabled account.
	AccountActive AccountState = "active"
	// AccountRestricted: created but comms-muted and social/rated disabled,
	// pending verifiable parental consent (§3.9.2) or another hold.
	AccountRestricted AccountState = "restricted"
	// AccountTombstoned: an erased-person record (§3.9.3). Never appears on a
	// LIVE session — a tombstoned user has all login paths dead, so no session
	// is ever minted for it.
	AccountTombstoned AccountState = "tombstoned"
)

// JurisdictionPolicy carries the per-market config levers the age/rated gates
// read (spec §3.9.1 jurisdiction config table). Only the fields the identity
// layer needs are modelled here.
type JurisdictionPolicy struct {
	// MinorRatedAllowed is the per-market lever for whether a CONSENTED minor
	// may play rated/ranked chess (spec §3.1 rated_eligible, Q-ACC-RATED-MINOR).
	// DEFAULT TRUE (consent enables rated); a market that bars minors from rated
	// play regardless of parental consent sets it false — a config value, not a
	// change to the rated_eligible formula or its S2/X-4 consumers.
	MinorRatedAllowed bool
}

// DefaultJurisdictionPolicy is the policy applied when a market has no explicit
// override: a consented minor MAY play rated (MinorRatedAllowed = true).
func DefaultJurisdictionPolicy() JurisdictionPolicy {
	return JurisdictionPolicy{MinorRatedAllowed: true}
}

// Subject is the live enforcement view of whoever holds a session. It is the
// set of inputs the rated gate reads; see RatedEligible.
//
// IMPORTANT (spec §3.1 / §3.1.1, review finding S3): these fields are the
// AUTHORITATIVE live values — account_state is re-read from the live User row on
// each enforcement check, not served from a stamped-at-issuance copy, and
// RatedEligible is RECOMPUTED from them at read time. The caller is responsible
// for populating this Subject from live state before each gate check; this type
// deliberately holds no cached rated_eligible bool, so there is no stale copy to
// accidentally read.
type Subject struct {
	Kind         SubjectKind
	State        AccountState // meaningful only when Kind == SubjectAccount
	KidAccount   bool         // true for a minor account (consented or not)
	Jurisdiction JurisdictionPolicy
}

// RatedEligible is the single load-bearing fairness signal S2 reads for the
// rated-seek gate (spec §3.6). It folds three rules into one derivation:
//   - ACC-INV-1: guests are unrated-only.
//   - the RESTRICTED-child gate: an unconsented child (restricted) is excluded.
//   - the per-market minor-rated policy: a CONSENTED minor (active + kid) is
//     eligible iff the market's MinorRatedAllowed lever is true.
//
// It is computed here, at read time, from the Subject's current fields — never
// returned from a stamped copy (spec §3.1, review finding S3). A consent flip
// (restricted->active) or a MinorRatedAllowed market-flag change is therefore
// honoured on the next call with no session re-issuance.
func (s Subject) RatedEligible() bool {
	if s.Kind != SubjectAccount {
		return false // guests: unrated-only (ACC-INV-1)
	}
	if s.State != AccountActive {
		return false // restricted/tombstoned: excluded
	}
	if s.KidAccount && !s.Jurisdiction.MinorRatedAllowed {
		return false // consented minor, but this market bars minor rated play
	}
	return true
}
