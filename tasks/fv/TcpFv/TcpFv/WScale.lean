/-
  Window Scale option clamp and scaled-window soundness
  (RFC 7323 Section 2, R-OPT-029 / R-OPT-030 / INV-OPT-008).

  RFC 7323:2.3: "The maximum scale exponent is limited to 14 ... If a Window
  Scale option is received with a shift.cnt value larger than 14, the TCP
  MUST ... use the value 14 instead." So `effShift = min(rawShift, 14)`.

  RFC 7323:2.2: the shifted window must stay within the advertised window
  space. With effShift <= 14 and a 16-bit base window (SEG.WND < 2^16), the
  scaled window `win << effShift` is bounded by (2^16-1) << 14 < 2^30, well
  inside UInt32 (and inside the 2^30 receive-window ceiling). We prove the
  clamp and these bounds.

  The raw shift is modelled as a Nat (the wire value is a single byte, 0..255).
  The scaled window is modelled on UInt32 (the actual register width).
-/

namespace TcpFv.WScale

/-- The advertised window-scale ceiling (RFC 7323:2.3). -/
def maxShift : Nat := 14

/-- Effective shift after clamping the received raw shift (R-OPT-029/030).
    Go: `if raw > 14 { return 14 }; return raw`  ==  `min(raw, 14)`. -/
def effShift (rawShift : Nat) : Nat := min rawShift maxShift

/-- Scaled window: base window left-shifted by the (clamped) shift.
    Go: `uint32(win) << shift`. Base `win` is the 16-bit SEG.WND widened. -/
def scaleWindow (win : UInt32) (shift : Nat) : UInt32 :=
  win <<< (UInt32.ofNat shift)

/-! ## Clamp soundness (INV-OPT-008) -/

/-- The effective shift never exceeds 14, for ANY raw input. -/
theorem effShift_le_max (rawShift : Nat) : effShift rawShift ≤ maxShift :=
  Nat.min_le_right _ _

/-- The clamp is identity below the ceiling (faithful pass-through). -/
theorem effShift_id_below (rawShift : Nat) (h : rawShift ≤ maxShift) :
    effShift rawShift = rawShift :=
  Nat.min_eq_left h

/-- The clamp saturates above the ceiling. -/
theorem effShift_sat_above (rawShift : Nat) (h : maxShift ≤ rawShift) :
    effShift rawShift = maxShift :=
  Nat.min_eq_right h

/-- The clamp is idempotent: clamping an already-effective shift is a no-op. -/
theorem effShift_idem (rawShift : Nat) :
    effShift (effShift rawShift) = effShift rawShift :=
  effShift_id_below _ (effShift_le_max rawShift)

/-! ## Scaled-window bound: no overflow, stays under 2^30 (RFC 7323:2.3) -/

/--
  Helper: the UInt32 left-shift's underlying Nat value, for a small shift
  (<= 14) and a 16-bit base, equals the plain product `win.toNat * 2^shift`.
  This unfolds `UInt32.toNat_shiftLeft`'s `<<<` (Nat shiftLeft) and `% 2^32`
  no-op into ordinary multiplication, once and for all.
-/
theorem scaleWindow_toNat (win : UInt32) (shift : Nat)
    (hwin : win.toNat < 2 ^ 16) (hs : shift ≤ 14) :
    (scaleWindow win shift).toNat = win.toNat * 2 ^ shift := by
  unfold scaleWindow
  have hsmall : (UInt32.ofNat shift).toNat = shift := by
    rw [UInt32.toNat_ofNat']; exact Nat.mod_eq_of_lt (by omega)
  rw [UInt32.toNat_shiftLeft, hsmall]
  -- a.toNat <<< (shift % 32) % 2^32 ; shift < 32 so shift % 32 = shift
  rw [show shift % 32 = shift from Nat.mod_eq_of_lt (by omega), Nat.shiftLeft_eq]
  have hpow : 2 ^ shift ≤ 2 ^ 14 := Nat.pow_le_pow_right (by omega) hs
  have hprod : win.toNat * 2 ^ shift < 2 ^ 16 * 2 ^ 14 := by
    calc win.toNat * 2 ^ shift
        < 2 ^ 16 * 2 ^ shift := (Nat.mul_lt_mul_right (Nat.two_pow_pos _)).mpr hwin
      _ ≤ 2 ^ 16 * 2 ^ 14 := Nat.mul_le_mul_left _ hpow
  have hlt32 : win.toNat * 2 ^ shift < 2 ^ 32 := by
    have heq : (2:Nat) ^ 16 * 2 ^ 14 = 2 ^ 30 := by decide
    rw [heq] at hprod; exact Nat.lt_trans hprod (by decide)
  exact Nat.mod_eq_of_lt hlt32

theorem scaleWindow_lt_2pow30 (win : UInt32) (rawShift : Nat)
    (hwin : win.toNat < 2 ^ 16) :
    (scaleWindow win (effShift rawShift)).toNat < 2 ^ 30 := by
  have hs : effShift rawShift ≤ 14 := effShift_le_max rawShift
  rw [scaleWindow_toNat win _ hwin hs]
  have hpow : 2 ^ effShift rawShift ≤ 2 ^ 14 := Nat.pow_le_pow_right (by omega) hs
  have hprod : win.toNat * 2 ^ effShift rawShift < 2 ^ 16 * 2 ^ 14 := by
    calc win.toNat * 2 ^ effShift rawShift
        < 2 ^ 16 * 2 ^ effShift rawShift := (Nat.mul_lt_mul_right (Nat.two_pow_pos _)).mpr hwin
      _ ≤ 2 ^ 16 * 2 ^ 14 := Nat.mul_le_mul_left _ hpow
  have heq : (2:Nat) ^ 16 * 2 ^ 14 = 2 ^ 30 := by decide
  rw [heq] at hprod
  exact hprod

/--
  Stronger no-overflow statement: the shift product fits in UInt32, so the
  modular shift equals the true mathematical shift (no information lost).
-/
theorem scaleWindow_no_overflow (win : UInt32) (rawShift : Nat)
    (hwin : win.toNat < 2 ^ 16) :
    (scaleWindow win (effShift rawShift)).toNat
      = win.toNat * 2 ^ effShift rawShift :=
  scaleWindow_toNat win _ hwin (effShift_le_max rawShift)

/-! ## Concrete clamp cases (R-OPT-029/030) -/

theorem effShift_15_clamps : effShift 15 = 14 := by decide
theorem effShift_255_clamps : effShift 255 = 14 := by decide
theorem effShift_0_id : effShift 0 = 0 := by decide
theorem effShift_14_id : effShift 14 = 14 := by decide
theorem effShift_7_id : effShift 7 = 7 := by decide

end TcpFv.WScale
