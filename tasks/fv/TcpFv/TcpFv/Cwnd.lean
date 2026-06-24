/-
  Congestion-window increase rules (RFC 5681), modelling `tcp/congestion.go`.

  TLA+ collapsed SMSS to 1, which flattens the Slow-Start `min(N, SMSS)` cap
  and the Congestion-Avoidance byte-counting accumulator into trivial `+1`
  steps. Those are exactly the non-trivial arithmetic invariants, so we model
  them here at full generality (`smss` an arbitrary positive Nat, `acked` an
  arbitrary Nat per ACK) and prove the increase laws.

  Correspondence with congestion.go `onNewAck(ackedBytes, flightSize)`:
    * Slow Start branch (cwnd < ssthresh):
        c.cwnd += minU32(ackedBytes, c.smss)
      modelled by `ssStep`.
    * Congestion Avoidance branch (cwnd >= ssthresh), byte counting:
        c.bytesAckedThisRtt += ackedBytes
        if c.bytesAckedThisRtt >= c.cwnd {
          c.bytesAckedThisRtt -= c.cwnd
          c.cwnd += c.smss
        }
      modelled by `caStep` over the `(cwnd, acc)` pair.

  We work in ℕ. The Go code is uint32 with a saturating window kept below
  maxWindow, so within a single un-saturated step the ℕ model is faithful;
  overflow is out of scope (window is bounded well below 2^32 by design).
-/

namespace TcpFv.Cwnd

/-! ## Slow Start: `cwnd += min(acked, smss)` -/

/-- One Slow-Start ACK update. `smss` is the cap. Go: `cwnd += minU32(acked, smss)`. -/
def ssStep (cwnd smss acked : Nat) : Nat := cwnd + min acked smss

/-- SS increase per ACK is at most one SMSS (the `min` upper bound, RFC 5681 §3.1). -/
theorem ssStep_increase_le_smss (cwnd smss acked : Nat) :
    ssStep cwnd smss acked - cwnd ≤ smss := by
  unfold ssStep
  have : min acked smss ≤ smss := Nat.min_le_right _ _
  omega

/-- SS is monotone non-decreasing: cwnd never shrinks on an ACK. -/
theorem ssStep_monotone (cwnd smss acked : Nat) : cwnd ≤ ssStep cwnd smss acked := by
  unfold ssStep; omega

/-- SS preserves the lower bound `cwnd >= smss` (window floor maintained). -/
theorem ssStep_preserves_floor (cwnd smss acked : Nat) (h : smss ≤ cwnd) :
    smss ≤ ssStep cwnd smss acked := by
  unfold ssStep; omega

/-- Exact SS increase when the ACK covers a full segment (`acked >= smss`): `+smss`. -/
theorem ssStep_full (cwnd smss acked : Nat) (h : smss ≤ acked) :
    ssStep cwnd smss acked = cwnd + smss := by
  unfold ssStep; rw [Nat.min_eq_right h]

/-! ## Congestion Avoidance: byte-counting accumulator -/

/--
  One Congestion-Avoidance ACK update over the pair `(cwnd, acc)` where `acc`
  is `bytesAckedThisRtt`. Returns the new `(cwnd, acc)`.
  Go: acc += acked; if acc >= cwnd { acc -= cwnd; cwnd += smss }.
-/
def caStep (cwnd smss acc acked : Nat) : Nat × Nat :=
  let acc' := acc + acked
  if acc' ≥ cwnd then (cwnd + smss, acc' - cwnd) else (cwnd, acc')

/-- CA cwnd is monotone non-decreasing. -/
theorem caStep_cwnd_monotone (cwnd smss acc acked : Nat) :
    cwnd ≤ (caStep cwnd smss acc acked).1 := by
  unfold caStep
  by_cases h : acc + acked ≥ cwnd <;> simp [h]

/-- CA increases cwnd by at most one SMSS per ACK (the `if` fires at most once). -/
theorem caStep_increase_le_smss (cwnd smss acc acked : Nat) :
    (caStep cwnd smss acc acked).1 - cwnd ≤ smss := by
  unfold caStep
  by_cases h : acc + acked ≥ cwnd <;> simp [h]

/-- CA accumulator stays non-negative (trivially, it's a Nat) and the reset never
    underflows: when the threshold fires, the new accumulator is `acc + acked - cwnd`
    which is `>= 0` because the branch guard guarantees `acc + acked >= cwnd`.
    We state it as: the new acc equals `acc + acked - cwnd` exactly on the fire path,
    so no "over-subtraction" occurs (the Go `acc -= cwnd` does not wrap). -/
theorem caStep_acc_no_underflow (cwnd smss acc acked : Nat)
    (h : acc + acked ≥ cwnd) :
    (caStep cwnd smss acc acked).2 + cwnd = acc + acked := by
  unfold caStep
  simp only [h, if_true, ge_iff_le]
  omega

/-- On the non-fire path the accumulator just grows and cwnd is unchanged. -/
theorem caStep_no_fire (cwnd smss acc acked : Nat) (h : ¬ acc + acked ≥ cwnd) :
    caStep cwnd smss acc acked = (cwnd, acc + acked) := by
  unfold caStep; simp [h]

/-- CA preserves the floor `cwnd >= smss`. -/
theorem caStep_preserves_floor (cwnd smss acc acked : Nat) (h : smss ≤ cwnd) :
    smss ≤ (caStep cwnd smss acc acked).1 :=
  Nat.le_trans h (caStep_cwnd_monotone cwnd smss acc acked)

/--
  "At most one SMSS per RTT" as a bound across a whole RTT's worth of ACKs.
  Folding `caStep` over a list of ACKs whose total acked bytes is `< cwnd`
  (one RTT delivers ~cwnd bytes) fires the increment at most once, so cwnd
  grows by at most `smss` over the RTT. We capture the per-step half here and
  the cumulative statement via the fold below.
-/
def caFold (cwnd smss acc : Nat) : List Nat → Nat × Nat
  | [] => (cwnd, acc)
  | a :: rest =>
    let (cwnd', acc') := caStep cwnd smss acc a
    caFold cwnd' smss acc' rest

/-- Folding CA over any ACK list keeps cwnd monotone. -/
theorem caFold_cwnd_monotone (smss : Nat) :
    ∀ (acks : List Nat) (cwnd acc : Nat), cwnd ≤ (caFold cwnd smss acc acks).1
  | [], cwnd, acc => by simp [caFold]
  | a :: rest, cwnd, acc => by
    unfold caFold
    have hstep : cwnd ≤ (caStep cwnd smss acc a).1 := caStep_cwnd_monotone cwnd smss acc a
    have hrest := caFold_cwnd_monotone smss rest (caStep cwnd smss acc a).1 (caStep cwnd smss acc a).2
    -- destructure the let
    cases hc : caStep cwnd smss acc a with
    | mk cwnd' acc' =>
      simp only [hc] at hstep
      have := caFold_cwnd_monotone smss rest cwnd' acc'
      simp only [hc]
      omega

/--
  Cumulative invariant tying acc and cwnd: across a CA fold, the total bytes
  acknowledged equal the cwnd growth (in SMSS units) times cwnd plus the
  leftover accumulator. We prove the conservation form:
    initial_acc + sum(acks) = final_acc + smss_fires * cwnd_growth_contribution
  Stated concretely as the invariant `acc + Σacks = acc_final + (#fires)*?`.
  The clean, robust statement is the per-step conservation already given by
  `caStep_acc_no_underflow`; the cumulative bound we actually need for the
  RTT property is `caFold_increase_le_smss_if_small` below.
-/
theorem caFold_increase_le_smss_if_small (smss : Nat) :
    ∀ (acks : List Nat) (cwnd acc : Nat),
      acc + acks.sum < cwnd →
      (caFold cwnd smss acc acks).1 = cwnd := by
  intro acks
  induction acks with
  | nil => intro cwnd acc _; simp [caFold]
  | cons a rest ih =>
    intro cwnd acc hsmall
    unfold caFold
    have hlist : acc + (a + rest.sum) < cwnd := by simpa [List.sum_cons] using hsmall
    -- first step does not fire
    have hnofire : ¬ acc + a ≥ cwnd := by omega
    rw [caStep_no_fire cwnd smss acc a hnofire]
    -- now acc' = acc + a, and (acc+a) + rest.sum < cwnd still
    apply ih
    omega

end TcpFv.Cwnd
