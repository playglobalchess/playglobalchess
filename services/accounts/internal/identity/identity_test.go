package identity

import "testing"

// TestRatedEligible is the truth table for the load-bearing rated gate
// (spec §3.6), including the per-market minor-rated lever (Q-ACC-RATED-MINOR)
// both ways and the edges the review effort pinned.
func TestRatedEligible(t *testing.T) {
	marketAllows := JurisdictionPolicy{MinorRatedAllowed: true}
	marketBars := JurisdictionPolicy{MinorRatedAllowed: false}

	cases := []struct {
		name string
		subj Subject
		want bool
	}{
		{
			name: "guest is never rated-eligible (ACC-INV-1)",
			subj: Subject{Kind: SubjectGuest},
			want: false,
		},
		{
			name: "active adult account is eligible",
			subj: Subject{Kind: SubjectAccount, State: AccountActive, KidAccount: false, Jurisdiction: marketAllows},
			want: true,
		},
		{
			name: "restricted (unconsented child) is excluded",
			subj: Subject{Kind: SubjectAccount, State: AccountRestricted, KidAccount: true, Jurisdiction: marketAllows},
			want: false,
		},
		{
			name: "tombstoned is excluded",
			subj: Subject{Kind: SubjectAccount, State: AccountTombstoned, KidAccount: false, Jurisdiction: marketAllows},
			want: false,
		},
		{
			name: "consented minor eligible when market allows (default)",
			subj: Subject{Kind: SubjectAccount, State: AccountActive, KidAccount: true, Jurisdiction: marketAllows},
			want: true,
		},
		{
			name: "consented minor excluded when market bars minor rated play",
			subj: Subject{Kind: SubjectAccount, State: AccountActive, KidAccount: true, Jurisdiction: marketBars},
			want: false,
		},
		{
			name: "non-minor unaffected by a minor-barring market (lever gates only kid accounts)",
			subj: Subject{Kind: SubjectAccount, State: AccountActive, KidAccount: false, Jurisdiction: marketBars},
			want: true,
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := tc.subj.RatedEligible(); got != tc.want {
				t.Fatalf("RatedEligible() = %v, want %v", got, tc.want)
			}
		})
	}
}

// TestRatedEligibleRecomputedLive proves the signal is derived at read time from
// the subject's current fields, not a stamped copy (spec §3.1/§3.1.1, finding
// S3): mutating a single input flips the result on the very next call, with no
// re-issuance step. This is the regression lock for the live-recompute gap.
func TestRatedEligibleRecomputedLive(t *testing.T) {
	// A minor account in a market that allows minor rated play, still restricted
	// (pending consent): not yet eligible.
	s := Subject{
		Kind:         SubjectAccount,
		State:        AccountRestricted,
		KidAccount:   true,
		Jurisdiction: JurisdictionPolicy{MinorRatedAllowed: true},
	}
	if s.RatedEligible() {
		t.Fatal("restricted minor should not be rated-eligible before consent")
	}

	// Consent captured: restricted -> active. Next read must see eligible.
	s.State = AccountActive
	if !s.RatedEligible() {
		t.Fatal("after consent flip to active, minor should be rated-eligible (market allows) — live recompute, no re-issuance")
	}

	// Market later bars minor rated play: next read must see excluded.
	s.Jurisdiction.MinorRatedAllowed = false
	if s.RatedEligible() {
		t.Fatal("after market flag flips to false, consented minor must become ineligible on the next read")
	}

	// Market restores minor rated play: eligible again.
	s.Jurisdiction.MinorRatedAllowed = true
	if !s.RatedEligible() {
		t.Fatal("after market flag flips back to true, consented minor must be eligible again")
	}
}
