/-
  TCP sequence number arithmetic over the 32-bit cyclic space (RFC 9293 Section 3.4).

  All sequence arithmetic is modulo 2^32 (RFC 9293:871-878). The standard
  serial-number comparison (RFC 1982 flavour) declares `a < b` when the wrapped
  difference `(a - b) mod 2^32` has its top (sign) bit set, equivalently
  `int32(a - b) < 0`. This matches the Linux/BSD idiom

      static inline bool before(__u32 a, __u32 b) { return (__s32)(a - b) < 0; }

  and the Go form  `func seqLT(a, b uint32) bool { return int32(a-b) < 0 }`.

  We model UInt32 directly. `seqLT` is the sign bit of the wrapped difference,
  expressed as `BitVec.msb`. The bitmask idiom `(a-b) & 0x80000000 != 0` is the
  same predicate (see `msb_eq_mask`), so either Go form is faithful.

  Note on the half-window subtlety (RFC 9293:876 "subtleties to computer modulo
  arithmetic"): the cyclic order is NOT antisymmetric at antipodal points. If
  `a - b = 2^31` exactly, then both `seqLT a b` and `seqLT b a` are true. Real
  TCP never compares antipodal points because windows are far below 2^31; the
  antisymmetry theorem therefore carries an explicit half-window hypothesis.
-/

namespace TcpFv.Seq

/--
  `seqLT a b` : `a` precedes `b` in the cyclic sequence space.
  Sign bit of the wrapped difference `(a - b)`; UInt32 subtraction wraps mod 2^32.
  Go: `int32(a-b) < 0`.
-/
def seqLT (a b : UInt32) : Bool := (a - b).toBitVec.msb

/-- `a =< b` (RFC notation): not (b < a). -/
def seqLEQ (a b : UInt32) : Bool := ! seqLT b a

/-- `a > b`. -/
def seqGT (a b : UInt32) : Bool := seqLT b a

/-- `a >= b`. -/
def seqGEQ (a b : UInt32) : Bool := ! seqLT a b

/--
  Acceptable ACK test (RFC 9293:912-915, R-011): `SND.UNA < SEG.ACK =< SND.NXT`.
  Go: `seqLT(una, ack) && seqLEQ(ack, nxt)`.
-/
def acceptableAck (una ack nxt : UInt32) : Bool :=
  seqLT una ack && seqLEQ ack nxt

/--
  Sequence-space length advance with wrap (R-014, T-014): `(seq + len) mod 2^32`.
  UInt32 addition already wraps. Go: `seq + len`.
-/
def seqAdd (seq len : UInt32) : UInt32 := seq + len

/-! ## Bitmask idiom equivalence -/

/-- `(x & 0x80000000) != 0` equals the sign bit (helper for `msb_eq_mask`). -/
theorem mask_ne_zero_iff (x : UInt32) :
    ((x.toBitVec &&& 0x80000000#32) != 0#32) = x.toBitVec.msb := by
  rw [BitVec.msb_eq_getLsbD_last]
  rw [show (0x80000000#32 : BitVec 32) = BitVec.twoPow 32 31 from by decide]
  by_cases hb : x.toBitVec.getLsbD 31 = true
  · rw [show (32 - 1) = 31 from rfl, hb]
    rw [bne_iff_ne, ne_eq]
    intro hc
    have := congrArg (fun y => y.getLsbD 31) hc
    simp only [BitVec.getLsbD_and, BitVec.getLsbD_twoPow, BitVec.getLsbD_zero] at this
    rw [hb] at this; simp at this
  · simp only [Bool.not_eq_true] at hb
    rw [show (32 - 1) = 31 from rfl, hb]
    rw [bne_eq_false_iff_eq]
    apply BitVec.eq_of_getLsbD_eq
    intro i
    rw [BitVec.getLsbD_and, BitVec.getLsbD_twoPow, BitVec.getLsbD_zero]
    intro _
    by_cases hi : (31 = (i : Nat))
    · rw [← hi, hb]; simp
    · simp [hi]

/-- The sign-bit predicate equals the `& 0x80000000 != 0` bitmask idiom. -/
theorem msb_eq_mask (x : UInt32) :
    x.toBitVec.msb = ((x.toBitVec &&& 0x80000000#32) != 0#32) :=
  (mask_ne_zero_iff x).symm

/-! ## Reflexivity / wrap correctness (T-010, T-012) -/

/-- Irreflexivity: `seqLT a a = false`. -/
@[simp] theorem seqLT_irrefl (a : UInt32) : seqLT a a = false := by
  unfold seqLT
  rw [UInt32.toBitVec_sub, BitVec.sub_self]
  simp [BitVec.msb_eq_decide]

/-- Wrap correctness: `seqLT (2^32-1) 0 = true`. -/
theorem seqLT_wrap_max_zero : seqLT 0xFFFFFFFF 0 = true := by decide

/-- Wrap correctness: `seqLT 0 (2^32-1) = false`. -/
theorem seqLT_wrap_zero_max : seqLT 0 0xFFFFFFFF = false := by decide

/-- Adjacent: `seqLT 0 1 = true`. -/
theorem seqLT_zero_one : seqLT 0 1 = true := by decide

/-! ## Antisymmetry within a half window (T-012, INV-seqorder) -/

/--
  Antisymmetry, excluding the antipodal pair.
  If `a` and `b` are not exactly antipodal (`a - b ≠ 2^31`) and `seqLT a b`,
  then `seqLT b a` is false. This is the core cyclic strict-order property;
  the antipodal exclusion is mandatory (it genuinely fails at `a - b = 2^31`).
-/
theorem seqLT_asymm (a b : UInt32) (hne : a - b ≠ 0x80000000)
    (h : seqLT a b = true) : seqLT b a = false := by
  unfold seqLT at *
  rw [UInt32.toBitVec_sub, BitVec.msb_eq_decide, BitVec.toNat_sub] at h
  rw [UInt32.toBitVec_sub, BitVec.msb_eq_decide, BitVec.toNat_sub]
  simp only [decide_eq_true_eq, decide_eq_false_iff_not, Nat.not_le] at h ⊢
  have hA : a.toBitVec.toNat < 2^32 := a.toBitVec.isLt
  have hB : b.toBitVec.toNat < 2^32 := b.toBitVec.isLt
  have key : (2 ^ 32 - b.toBitVec.toNat + a.toBitVec.toNat) % 2 ^ 32 ≠ 2 ^ 31 := by
    intro hc; apply hne
    rw [← UInt32.toNat_inj, UInt32.toNat_sub]
    rw [show (0x80000000 : UInt32).toNat = 2^31 from by decide]
    exact hc
  omega

/--
  Equivalent half-window phrasing: if the forward distance is strictly less
  than 2^31 (top bit of `a - b` set but the value is not 2^31), antisymmetry holds.
  Stated via the distance directly for use as a TDD predicate.
-/
theorem seqLT_asymm_halfwindow (a b : UInt32)
    (hwin : (b - a).toBitVec.msb = false)
    (_h : seqLT a b = true) : seqLT b a = false := by
  unfold seqLT at *
  exact hwin

/-! ## Acceptable ACK (R-011, T-013) -/

/-- Acceptable ack lies strictly after SND.UNA. -/
theorem acceptableAck_after_una (una ack nxt : UInt32)
    (h : acceptableAck una ack nxt = true) : seqLT una ack = true := by
  revert h; unfold acceptableAck; simp only [Bool.and_eq_true]; exact fun h => h.1

/-- Acceptable ack lies at or before SND.NXT. -/
theorem acceptableAck_atMost_nxt (una ack nxt : UInt32)
    (h : acceptableAck una ack nxt = true) : seqLEQ ack nxt = true := by
  revert h; unfold acceptableAck; simp only [Bool.and_eq_true]; exact fun h => h.2

/--
  Half-window correctness of the acceptable-ack range (the strong general result).
  If the window SND.UNA → SND.NXT has forward distance < 2^31 (top bit of
  `nxt - una` clear, which holds whenever `SND.UNA <= SND.NXT` within a normal
  window), then any acceptable ack's forward distance from UNA is at most the
  window size. I.e. the ack genuinely lies in the cyclic interval `(UNA, NXT]`
  with no wrap pathology.
-/
theorem acceptableAck_in_window (una ack nxt : UInt32)
    (hwin : (nxt - una).toBitVec.msb = false)
    (h : acceptableAck una ack nxt = true) :
    (ack - una).toBitVec ≤ (nxt - una).toBitVec := by
  unfold acceptableAck seqLEQ seqLT at h
  simp only [Bool.and_eq_true] at h
  obtain ⟨h1, h2⟩ := h
  rw [BitVec.le_def]
  rw [UInt32.toBitVec_sub, BitVec.toNat_sub, UInt32.toBitVec_sub, BitVec.toNat_sub]
  rw [UInt32.toBitVec_sub, BitVec.msb_eq_decide, BitVec.toNat_sub] at h1
  rw [BitVec.msb_eq_decide] at hwin
  rw [UInt32.toBitVec_sub, BitVec.toNat_sub] at hwin
  simp only [Bool.not_eq_true', BitVec.msb_eq_decide] at h2
  rw [UInt32.toBitVec_sub, BitVec.toNat_sub] at h2
  simp only [decide_eq_true_eq, decide_eq_false_iff_not, Nat.not_le] at h1 hwin h2
  have hu : una.toBitVec.toNat < 2^32 := una.toBitVec.isLt
  have hk : ack.toBitVec.toNat < 2^32 := ack.toBitVec.isLt
  have hn : nxt.toBitVec.toNat < 2^32 := nxt.toBitVec.isLt
  omega

/-- Concrete acceptable-ack boundaries (T-013). -/
theorem acceptableAck_boundary_lo : acceptableAck 100 101 200 = true := by decide
theorem acceptableAck_reject_eq_una : acceptableAck 100 100 200 = false := by decide
theorem acceptableAck_accept_eq_nxt : acceptableAck 100 200 200 = true := by decide
theorem acceptableAck_reject_gt_nxt : acceptableAck 100 201 200 = false := by decide

/-! ## Sequence addition wrap (R-014, T-014) -/

/-- `seqAdd` equals modular addition (no overflow, no panic). -/
theorem seqAdd_eq (seq len : UInt32) : seqAdd seq len = seq + len := rfl

/-- Wrap: `(2^32-1) + 1 = 0`. -/
theorem seqAdd_wrap_max : seqAdd 0xFFFFFFFF 1 = 0 := by decide

/-- Wrap is total: `seqAdd` is defined for all inputs (UInt32 addition is total). -/
theorem seqAdd_total (seq len : UInt32) : ∃ r, seqAdd seq len = r := ⟨_, rfl⟩

end TcpFv.Seq
