/-
  PAWS / TS.Recent monotonicity over the 32-bit wrapping timestamp space
  (RFC 7323 Section 5, R-OPT-052 / R-OPT-063 / INV-OPT-011 / INV-OPT-012).

  RFC 7323:5.2 reuses RFC 1982 serial-number arithmetic for TCP timestamps:
  "TS.Recent ... the timestamps are 32-bit unsigned integers in a modular
  32-bit space ... [SEG.TSval >= TS.Recent] uses the comparison defined for
  sequence numbers". So timestamp comparison is the SAME wrapped order as
  sequence numbers. We therefore reuse `TcpFv.Seq.seqLT` directly for the
  timestamp domain rather than re-deriving it.

  Definitions modelled (1:1 with the Go implementation):

  - `tsGEQ ts recent`  : SEG.TSval >= TS.Recent  (wrapped order, R-OPT-052).
  - `pawsDrop`         : PAWS verdict, SEG.TSval < TS.Recent => stale (R-OPT-063).
  - `tsRecentUpdate`   : TS.Recent update rule (R-OPT-052):
        update iff  SEG.TSval >= TS.Recent  AND  SEG.SEQ =< Last.ACK.sent.

  Properties proved:
  - PAWS drop is exactly the complement of the update's TSval condition
    (a segment is dropped iff its TSval is strictly older). [INV-OPT-011]
  - TS.Recent never moves backward under the update rule, in the wrapped
    order: the new value is always >= the old value (tsGEQ). [INV-OPT-012]
  - When PAWS accepts (not stale) the candidate is >= recent, so an update,
    if the SEQ gate also passes, can only advance or hold TS.Recent.
-/

import TcpFv.Seq

namespace TcpFv.Paws

open TcpFv.Seq

/-- SEG.TSval >= TS.Recent in the wrapped 32-bit order (RFC 7323, R-OPT-052).
    Go: `!seqLT(tsval, recent)`. -/
def tsGEQ (tsval recent : UInt32) : Bool := ! seqLT tsval recent

/-- PAWS staleness verdict (R-OPT-063): drop iff SEG.TSval strictly older
    than TS.Recent. Go: `seqLT(tsval, recent)`. -/
def pawsDrop (tsval recent : UInt32) : Bool := seqLT tsval recent

/--
  TS.Recent update rule (R-OPT-052). Returns the new TS.Recent.
  Update to `tsval` iff `tsval >= recent` (wrapped) AND `seq =< lastAckSent`.
  Otherwise keep `recent`.
  Go:
    if !seqLT(tsval, recent) && seqLEQ(seq, lastAckSent) { recent = tsval }
-/
def tsRecentUpdate (recent tsval seq lastAckSent : UInt32) : UInt32 :=
  if tsGEQ tsval recent && seqLEQ seq lastAckSent then tsval else recent

/-! ## PAWS verdict is exactly the complement of the freshness test (INV-OPT-011) -/

/-- A segment is PAWS-dropped iff it is NOT timestamp-fresh.
    The two predicates partition every (tsval, recent) pair. -/
theorem pawsDrop_iff_not_fresh (tsval recent : UInt32) :
    pawsDrop tsval recent = ! tsGEQ tsval recent := by
  unfold pawsDrop tsGEQ
  simp

/-- PAWS never drops a fresh-or-equal timestamp (no false positive on
    in-window data): if `tsval >= recent` then it is not dropped. -/
theorem pawsDrop_false_of_fresh (tsval recent : UInt32)
    (h : tsGEQ tsval recent = true) : pawsDrop tsval recent = false := by
  unfold tsGEQ at h
  unfold pawsDrop
  simpa using h

/-- PAWS drops exactly the strictly-older timestamps (no false negative):
    if dropped then it was strictly older. -/
theorem pawsDrop_implies_older (tsval recent : UInt32)
    (h : pawsDrop tsval recent = true) : seqLT tsval recent = true := h

/-! ## TS.Recent monotonicity (INV-OPT-012) -/

/--
  Core monotonicity: under the update rule, the new TS.Recent is always
  >= the old TS.Recent in the wrapped order. TS.Recent never moves backward.
  (`tsGEQ new old = true`.)
-/
theorem tsRecent_monotone (recent tsval seq lastAckSent : UInt32) :
    tsGEQ (tsRecentUpdate recent tsval seq lastAckSent) recent = true := by
  unfold tsRecentUpdate
  by_cases hc : (tsGEQ tsval recent && seqLEQ seq lastAckSent) = true
  · -- update branch: result = tsval, and the guard gives tsGEQ tsval recent
    rw [if_pos hc]
    rw [Bool.and_eq_true] at hc
    exact hc.1
  · -- keep branch: result = recent, and tsGEQ recent recent holds
    rw [if_neg hc]
    -- tsGEQ recent recent = !seqLT recent recent = !false = true
    unfold tsGEQ
    simp

/--
  If the update actually fires (both gates pass), the result equals `tsval`
  and `tsval` was fresh, so TS.Recent advances to a value that dominates
  the old one. (Spelled out for the test bridge.)
-/
theorem tsRecent_update_fires (recent tsval seq lastAckSent : UInt32)
    (hfresh : tsGEQ tsval recent = true)
    (hseq : seqLEQ seq lastAckSent = true) :
    tsRecentUpdate recent tsval seq lastAckSent = tsval := by
  unfold tsRecentUpdate
  rw [if_pos (by rw [Bool.and_eq_true]; exact ⟨hfresh, hseq⟩)]

/--
  If either gate fails, TS.Recent is held (no spurious change).
-/
theorem tsRecent_update_holds (recent tsval seq lastAckSent : UInt32)
    (h : ¬ (tsGEQ tsval recent = true ∧ seqLEQ seq lastAckSent = true)) :
    tsRecentUpdate recent tsval seq lastAckSent = recent := by
  unfold tsRecentUpdate
  rw [if_neg]
  rw [Bool.and_eq_true]
  exact h

/-! ## Concrete boundary cases (R-OPT-052 / R-OPT-063) -/

/-- Equal timestamps: not stale (>= is inclusive). -/
theorem paws_equal_not_dropped : pawsDrop 1000 1000 = false := by decide

/-- Strictly newer: not stale. -/
theorem paws_newer_not_dropped : pawsDrop 1001 1000 = false := by decide

/-- Strictly older: stale, dropped. -/
theorem paws_older_dropped : pawsDrop 999 1000 = true := by decide

/-- Wrap: a tsval just past the 32-bit boundary is newer than max, not stale. -/
theorem paws_wrap_not_dropped : pawsDrop 0 0xFFFFFFFF = false := by decide

/-- Wrap: max is older than the just-wrapped 0, so comparing max vs 0 is stale. -/
theorem paws_wrap_max_dropped : pawsDrop 0xFFFFFFFF 0 = true := by decide

/-- Update fires on a fresh tsval with passing SEQ gate. -/
theorem tsRecent_update_concrete :
    tsRecentUpdate 1000 1005 50 100 = 1005 := by decide

/-- Update held when SEQ gate fails (seq > lastAckSent). -/
theorem tsRecent_hold_concrete :
    tsRecentUpdate 1000 1005 200 100 = 1000 := by decide

/-- Update held when timestamp is stale. -/
theorem tsRecent_hold_stale_concrete :
    tsRecentUpdate 1000 999 50 100 = 1000 := by decide

end TcpFv.Paws
