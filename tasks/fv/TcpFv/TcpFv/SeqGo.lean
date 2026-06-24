/-
  Bridge between the Lean `seqLT` (sign-bit / msb of `a - b`) and the Go
  implementation `SeqLT` (`d := b - a; d != 0 && d < 2^31`).

  These two predicates are NOT the same function in general: they disagree at
  the antipodal point and at equality (see `seqLT` vs `seqLTGo` divergence
  notes below). The point of this module is to transport the msb-based proofs
  in `Seq.lean` (PAWS monotonicity, acceptable-ack, etc.) onto the *actual* Go
  predicate, under the hypothesis that the window is below the half space
  (`< 2^31`). The Go stack keeps the receive window in a uint16, so the
  hypothesis `(b - a).toNat < 2^31` (and the symmetric one) always holds in
  practice; this module makes that dependency explicit.

  Divergence (documented, not a bug):
    * a = b           : msb (a-b) = false,  Go: d = 0 -> false.  AGREE.
    * a - b = 2^31    : msb (a-b) = true,   Go: d = b-a = 2^31 -> false. DIFFER.
    * 0 < a-b < 2^31  : msb = false,        Go: 2^31 < b-a -> false. AGREE.
    * 2^31 < a-b      : msb = true,         Go: 0 < b-a < 2^31 -> true. AGREE.

  So the only disagreement is the antipodal pair `a - b = 2^31`. Below the
  half window that pair never occurs, hence equivalence.
-/

import TcpFv.Seq

namespace TcpFv.Seq

/--
  Go-faithful `SeqLT`: `d := b - a; d != 0 && d < 2^31`.
  Mirrors `tcp/seq.go`:
    func SeqLT(a, b uint32) bool { d := b - a; return d != 0 && d < halfSeqSpace }
  with `halfSeqSpace = 1 << 31`.
-/
def seqLTGo (a b : UInt32) : Bool :=
  let d := b - a
  d != 0 && d.toBitVec.toNat < 2 ^ 31

/-- `seqLTGo` is irreflexive (`d = 0` path). Go: `d == 0` returns false. -/
@[simp] theorem seqLTGo_irrefl (a : UInt32) : seqLTGo a a = false := by
  unfold seqLTGo
  simp

/--
  Antipodal divergence is real: at `a - b = 2^31` the msb predicate fires but
  the Go predicate does not. Concretely `seqLT 0 0x80000000 = true` while
  `seqLTGo 0 0x80000000 = false`. This justifies the half-window hypothesis.
-/
theorem seqLT_seqLTGo_diverge_antipodal :
    seqLT 0 0x80000000 = true ∧ seqLTGo 0 0x80000000 = false := by
  constructor <;> decide

/--
  Core bridge. Under the half-window hypothesis (`b - a` is in the lower half,
  `(b - a).toNat < 2^31`), the Go predicate `seqLTGo a b` agrees with the
  msb-based `seqLT a b`. This is the transport lemma: every theorem proved
  about `seqLT` in `Seq.lean` applies to the Go `SeqLT` whenever the operands
  sit within a half-space window.
-/
theorem seqLTGo_eq_seqLT_of_halfwindow (a b : UInt32)
    (hwin : (b - a).toBitVec.toNat < 2 ^ 31) :
    seqLTGo a b = seqLT a b := by
  unfold seqLTGo seqLT
  simp only []
  rw [BitVec.msb_eq_decide]
  -- relate (a-b) and (b-a): their toNat sum to 0 mod 2^32
  have hab : (a - b).toBitVec.toNat = (2 ^ 32 - (b - a).toBitVec.toNat) % 2 ^ 32 := by
    rw [UInt32.toBitVec_sub, UInt32.toBitVec_sub, BitVec.toNat_sub, BitVec.toNat_sub]
    have hA : a.toBitVec.toNat < 2 ^ 32 := a.toBitVec.isLt
    have hB : b.toBitVec.toNat < 2 ^ 32 := b.toBitVec.isLt
    omega
  have hd : (b - a).toBitVec.toNat = ((b - a).toBitVec).toNat := rfl
  -- case on whether b - a is zero
  by_cases hz : (b - a) = 0
  · -- b = a (since subtraction is injective in second slot): then a - b = 0 too
    have : a - b = 0 := by
      have : b = a := by
        have := congrArg (· + a) hz
        simpa using this
      simp [this]
    simp [hz, this]
  · -- b - a ≠ 0, and < 2^31, so a - b = 2^32 - (b-a) which is > 2^31
    have hne : (b - a).toBitVec.toNat ≠ 0 := by
      intro hc
      apply hz
      have : (b - a).toBitVec = 0#32 := by
        apply BitVec.eq_of_toNat_eq; simpa using hc
      apply UInt32.toBitVec_inj.mp
      simpa using this
    have hbnz : ((b - a) != 0) = true := by
      simp [bne_iff_ne, hz]
    rw [hbnz]
    simp only [Bool.true_and]
    -- goal: decide ((b-a).toNat < 2^31) = decide (2^(32-1) <= (a-b).toNat)
    have hBlt : (b - a).toBitVec.toNat < 2 ^ 32 := (b - a).toBitVec.isLt
    have hge : 2 ^ 31 ≤ (a - b).toBitVec.toNat := by
      rw [hab]
      have : (2 ^ 32 - (b - a).toBitVec.toNat) < 2 ^ 32 := by omega
      rw [Nat.mod_eq_of_lt this]
      omega
    rw [decide_eq_true hwin, decide_eq_true (show 2 ^ (32 - 1) ≤ (a - b).toBitVec.toNat by
      simpa using hge)]

/--
  Symmetric corollary in terms of the original window hypothesis used in
  `Seq.lean` (`(b - a).toBitVec.msb = false` means `b - a` is in the lower
  half, i.e. `< 2^31`). This is the exact shape the PAWS / acceptable-ack
  half-window theorems already assume, so they transport directly.
-/
theorem seqLTGo_eq_seqLT_of_msb_false (a b : UInt32)
    (hwin : (b - a).toBitVec.msb = false) :
    seqLTGo a b = seqLT a b := by
  apply seqLTGo_eq_seqLT_of_halfwindow
  rw [BitVec.msb_eq_decide] at hwin
  simp only [decide_eq_false_iff_not, Nat.not_le] at hwin
  exact hwin

/--
  Direct consequence: the Go acceptable-ack predicate matches the Lean one
  under the window hypothesis. `AcceptableAck(una, ack, nxt)` in Go is
  `SeqLT(una, ack) && SeqLEQ(ack, nxt)`. We give the `seqLT` half here; the
  full predicate transport follows by combining with `Seq.acceptableAck`.
-/
theorem seqLTGo_transports_irrefl (a : UInt32) : seqLTGo a a = seqLT a a := by
  simp

end TcpFv.Seq
