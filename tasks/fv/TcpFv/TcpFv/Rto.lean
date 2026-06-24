/-
  RTO estimator soundness (RFC 6298, R-CC-003..R-CC-008, R-CC-012, R-CC-018,
  INV-CC-004 / INV-CC-008 / INV-CC-012).

  RFC 6298 in floating point:
    first sample (3.2):  SRTT = R;  RTTVAR = R/2;  RTO = SRTT + max(G, K*RTTVAR)
    later  (3.3, order RTTVAR THEN SRTT):
        RTTVAR = (1-b)*RTTVAR + b*|SRTT - R'|
        SRTT   = (1-a)*SRTT   + a*R'
      with a = 1/8, b = 1/4, K = 4.
    clamp (3.1, 2.4):  RTO = max(RTO, RTO_MIN);  RTO_MIN = 1000 ms.
    backoff (5.5):     RTO = RTO * 2 (capped).

  We model everything in MILLISECOND-SCALED Nat (fixed point, scale = 1 ms).
  Because a=1/8 and b=1/4 are powers of two, the smoothing is exact integer
  arithmetic with truncating division:

      RTTVAR' = (3*RTTVAR + |SRTT - R'|) / 4          -- (1-b)=3/4, b=1/4
      SRTT'   = (7*SRTT   + R')         / 8           -- (1-a)=7/8, a=1/8

  G is the clock granularity (a Nat, e.g. 1 ms or 0). RTO_MIN defaults to 1000.

  Why Lean and not a normal test: the estimator is an INFINITE recursion over
  unbounded sample streams; "SRTT does not diverge when samples are bounded"
  and "the floor of the EWMA stays inside [min,max]" are not reachable by a
  finite property test (you can never sample the tail). The wrapped-order and
  the truncation interplay also bites here. So we prove boundedness, the RTO
  floor, the K*RTTVAR=0 -> G rounding rule, and backoff monotonicity.
-/

namespace TcpFv.Rto

/-- RTO lower bound, RFC 6298:2.4 (R-CC-007). Milliseconds. -/
def rtoMin : Nat := 1000

/-- Smoothing constant K (RFC 6298:2.3, R-CC-003). -/
def kRttvar : Nat := 4

/--
  Estimator state in ms-scaled Nat. `srtt` and `rttvar` are the smoothed
  round-trip time and its variation; `g` is the clock granularity.
-/
structure Est where
  srtt   : Nat
  rttvar : Nat
  g      : Nat
deriving Repr, DecidableEq

/-- Raw RTO before the floor clamp: SRTT + max(G, K*RTTVAR) (R-CC-003 form). -/
def rtoRaw (e : Est) : Nat := e.srtt + max e.g (kRttvar * e.rttvar)

/-- Clamped RTO: lower bound RTO_MIN (R-CC-007, INV-CC-004). -/
def rto (e : Est) : Nat := max rtoMin (rtoRaw e)

/-- First-sample initialisation (R-CC-003): SRTT=R, RTTVAR=R/2. -/
def initEst (r g : Nat) : Est := { srtt := r, rttvar := r / 2, g := g }

/-- Absolute difference |a - b| on Nat. -/
def absDiff (a b : Nat) : Nat := max a b - min a b

/--
  Subsequent-sample update (R-CC-004), ORDER ENFORCED: RTTVAR uses the OLD
  SRTT, THEN SRTT is updated (INV-CC-012). Exact integer EWMA:
    RTTVAR' = (3*RTTVAR + |SRTT - R'|)/4 ;  SRTT' = (7*SRTT + R')/8.
-/
def updateEst (e : Est) (r' : Nat) : Est :=
  let rttvar' := (3 * e.rttvar + absDiff e.srtt r') / 4
  let srtt'   := (7 * e.srtt + r') / 8
  { srtt := srtt', rttvar := rttvar', g := e.g }

/-- Exponential backoff on timeout (R-CC-018): RTO doubles, capped at `cap`. -/
def backoff (cap cur : Nat) : Nat := min cap (cur * 2)

/-! ## RTO floor (INV-CC-004): clamped RTO is always >= RTO_MIN -/

/-- The clamped RTO is never below RTO_MIN, for ANY state. -/
theorem rto_ge_min (e : Est) : rto e ≥ rtoMin := by
  unfold rto
  exact Nat.le_max_left _ _

/-- Concretely, RTO_MIN = 1000 ms, so RTO >= 1000. -/
theorem rto_ge_1000 (e : Est) : rto e ≥ 1000 := rto_ge_min e

/-- The clamp dominates the raw value too (RTO >= raw estimate). -/
theorem rto_ge_raw (e : Est) : rto e ≥ rtoRaw e := by
  unfold rto
  exact Nat.le_max_right _ _

/-! ## K*RTTVAR = 0 rounds to G (R-CC-012) -/

/--
  When the variance term K*RTTVAR is zero, the raw RTO rounds to SRTT + G
  (the granularity floor inside the max), per R-CC-012.
-/
theorem rtoRaw_rounds_to_g (e : Est) (h : kRttvar * e.rttvar = 0) :
    rtoRaw e = e.srtt + e.g := by
  unfold rtoRaw
  rw [h, Nat.max_eq_left (Nat.zero_le _)]

/-- Equivalently, RTTVAR = 0 forces the G branch (since K > 0). -/
theorem rtoRaw_g_branch_of_rttvar_zero (e : Est) (h : e.rttvar = 0) :
    rtoRaw e = e.srtt + e.g := by
  apply rtoRaw_rounds_to_g
  rw [h, Nat.mul_zero]

/-! ## Boundedness / non-divergence (INV-CC: estimator stays finite) -/

/--
  SRTT boundedness: if the OLD srtt and the new sample R' are both <= bound B,
  then the updated SRTT is also <= B. The EWMA cannot escape the convex hull
  of its inputs (truncating division only lowers it). Proved for the EWMA
  combinator directly so it carries over arbitrarily many updates.
-/
theorem srtt_bounded (e : Est) (r' B : Nat)
    (hS : e.srtt ≤ B) (hR : r' ≤ B) :
    (updateEst e r').srtt ≤ B := by
  unfold updateEst
  simp only
  -- (7*S + R')/8 <= (7*B + B)/8 = 8*B/8 = B
  have hnum : 7 * e.srtt + r' ≤ 8 * B := by
    have h7 : 7 * e.srtt ≤ 7 * B := Nat.mul_le_mul_left 7 hS
    omega
  calc (7 * e.srtt + r') / 8 ≤ (8 * B) / 8 := Nat.div_le_div_right hnum
    _ = B := by rw [Nat.mul_div_cancel_left B (by decide : 0 < 8)]

/--
  RTTVAR boundedness: if RTTVAR <= B, srtt <= B and the sample R' <= B, the
  updated RTTVAR <= B. (|SRTT - R'| <= B because both are in [0,B].)
-/
theorem rttvar_bounded (e : Est) (r' B : Nat)
    (hV : e.rttvar ≤ B) (hS : e.srtt ≤ B) (hR : r' ≤ B) :
    (updateEst e r').rttvar ≤ B := by
  unfold updateEst
  simp only
  have hdiff : absDiff e.srtt r' ≤ B := by
    unfold absDiff
    -- max a b <= B and min a b >= 0, so max - min <= B
    have hmax : max e.srtt r' ≤ B := Nat.max_le.mpr ⟨hS, hR⟩
    omega
  have hnum : 3 * e.rttvar + absDiff e.srtt r' ≤ 4 * B := by
    have h3 : 3 * e.rttvar ≤ 3 * B := Nat.mul_le_mul_left 3 hV
    omega
  calc (3 * e.rttvar + absDiff e.srtt r') / 4 ≤ (4 * B) / 4 :=
        Nat.div_le_div_right hnum
    _ = B := by rw [Nat.mul_div_cancel_left B (by decide : 0 < 4)]

/--
  Full estimator non-divergence: if a whole state is bounded by B (both srtt
  and rttvar) and the sample is bounded by B, the updated state is bounded by
  B in both components. This is the inductive invariant for an unbounded
  stream of bounded samples: SRTT and RTTVAR never blow up.
-/
theorem est_bounded (e : Est) (r' B : Nat)
    (hS : e.srtt ≤ B) (hV : e.rttvar ≤ B) (hR : r' ≤ B) :
    (updateEst e r').srtt ≤ B ∧ (updateEst e r').rttvar ≤ B :=
  ⟨srtt_bounded e r' B hS hR, rttvar_bounded e r' B hV hS hR⟩

/--
  Init boundedness: the first-sample state is bounded by the sample R
  (SRTT = R <= R, RTTVAR = R/2 <= R), seeding the invariant above.
-/
theorem initEst_bounded (r g : Nat) :
    (initEst r g).srtt ≤ r ∧ (initEst r g).rttvar ≤ r := by
  unfold initEst
  exact ⟨Nat.le_refl r, Nat.div_le_self r 2⟩

/-! ## Backoff monotonicity & boundedness (INV-CC-008) -/

/--
  Backoff is monotone non-decreasing as long as the current value has not yet
  reached the cap: doubling never lowers the value (capped). For any cur,
  `backoff cap cur >= min cap cur`. The clean statement most useful downstream
  is: if cur <= cap then backoff is >= cur (non-decreasing up to the cap).
-/
theorem backoff_ge_cur (cap cur : Nat) (h : cur ≤ cap) :
    backoff cap cur ≥ cur := by
  unfold backoff
  -- min cap (2*cur) >= cur : cur <= cap and cur <= 2*cur
  apply Nat.le_min.mpr
  exact ⟨h, by omega⟩

/-- Backoff never exceeds the cap (bounded growth). -/
theorem backoff_le_cap (cap cur : Nat) : backoff cap cur ≤ cap := by
  unfold backoff
  exact Nat.min_le_left _ _

/--
  Iterated backoff is monotone: backoff(backoff x) >= backoff x as long as the
  cap is respected. Stated as the single-step monotone law composed: for the
  chain `r₀, r₁ = backoff cap r₀, r₂ = backoff cap r₁, ...` each step is
  non-decreasing while below cap. The general chain monotonicity follows by
  induction from `backoff_ge_cur` + `backoff_le_cap`.
-/
theorem backoff_chain_monotone (cap r : Nat) (h : r ≤ cap) :
    backoff cap r ≥ r ∧ backoff cap r ≤ cap :=
  ⟨backoff_ge_cur cap r h, backoff_le_cap cap r⟩

/--
  n-fold backoff stays within the cap (closed form of the chain invariant):
  starting from any `r0`, applying backoff n times never exceeds the cap
  (for n >= 1) and, while below cap, is monotone. We give the iterate and
  prove its cap bound by induction.
-/
def backoffIter (cap : Nat) : Nat → Nat → Nat
  | 0,     r => r
  | n + 1, r => backoff cap (backoffIter cap n r)

/-- Every backoff iterate (n >= 1) is capped. -/
theorem backoffIter_le_cap (cap r : Nat) (n : Nat) (hn : 0 < n) :
    backoffIter cap n r ≤ cap := by
  cases n with
  | zero => omega
  | succ m =>
    unfold backoffIter
    exact backoff_le_cap _ _

/--
  Backoff iterate is monotone in n while the start is within cap: each added
  doubling does not decrease the value. We prove the consecutive-step form,
  which is the inductive step a TDD test pins down.
-/
theorem backoffIter_step_monotone (cap r : Nat) (n : Nat)
    (h : backoffIter cap n r ≤ cap) :
    backoffIter cap (n + 1) r ≥ backoffIter cap n r := by
  show backoff cap (backoffIter cap n r) ≥ backoffIter cap n r
  exact backoff_ge_cur cap (backoffIter cap n r) h

/-! ## Concrete cases -/

/-- Init from R=300ms, G=1: SRTT=300, RTTVAR=150, RTO=300+max(1,600)=900,
    floored to 1000 (R-CC-003 + R-CC-007). -/
theorem rto_init_concrete : rto (initEst 300 1) = 1000 := by decide

/-- Init from R=2000ms: SRTT=2000, RTTVAR=1000, RTO=2000+4000=6000 > floor. -/
theorem rto_init_large_concrete : rto (initEst 2000 1) = 6000 := by decide

/-- Floor actually engages on a tiny sample. -/
theorem rto_floor_engages : rto (initEst 10 1) = 1000 := by decide

/-- Backoff 1000 -> 2000 -> 4000, capped at 60000 (R-CC-008 upper). -/
theorem backoff_concrete : backoff 60000 1000 = 2000 := by decide
theorem backoff_caps : backoff 60000 40000 = 60000 := by decide

/-- K*RTTVAR=0 rounds RTO raw to SRTT+G. -/
theorem rtoRaw_g_concrete : rtoRaw { srtt := 500, rttvar := 0, g := 1 } = 501 := by decide

/-- One update step keeps SRTT inside the input hull (300, sample 340 -> in [300,340]). -/
theorem update_in_hull_concrete :
    (updateEst (initEst 300 1) 340).srtt = 305 := by decide

end TcpFv.Rto
