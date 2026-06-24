/-
  TCP/IP checksum: ones' complement of the ones' complement 16-bit sum
  (RFC 9293 Section 3.1, R-100..R-103).

  Model. Ones'-complement addition with end-around carry is exactly addition in
  the residue system modulo 65535 (= 2^16 - 1), on representatives 0..65534
  (where 0 and 0xFFFF both denote zero). We model the running sum as a Nat (the
  Go uint32 accumulator) and reduce with `onesAdd x y = (x + y) % 65535`, which
  performs one end-around carry. `wordSum` folds a word list this way.

  Correspondence to the Go implementation (uint32 accumulator + 16-bit fold):
    acc := uint32(0)
    for _, w := range words { acc += uint32(w) }      // plain 32-bit sum
    for acc>>16 != 0 { acc = (acc & 0xFFFF) + (acc>>16) }  // fold carries
    sum16 := uint16(acc)            // == plainSum % 65535 (mod-65535 representative)
    checksum := ^sum16              // ones' complement
  `wordSum_eq_sum_mod` proves the model equals `(Σ wᵢ) mod 65535`, which is what
  the carry-fold computes; so model and Go agree.

  1-bit detection (T-005 second half) is intentionally NOT proved here. A pure
  ones'-complement checksum does NOT detect every multi-bit change (it has known
  collisions, e.g. a plus-one / minus-one word pair), and even single-bit detection requires
  arguing over all bit positions; that breadth is better covered by PBT
  (1-bit-flip detection over random data) than by a Lean theorem. We prove the
  roundtrip soundness and the order-independence (metamorphic) basis instead.
-/

namespace TcpFv.Checksum

/-- Ones'-complement addition with one end-around carry: addition modulo 65535. -/
def onesAdd (x y : Nat) : Nat := (x + y) % 65535

/-- Plain (uncarried) sum helper, used to bridge to the Go 32-bit accumulator. -/
def sumN (l : List Nat) : Nat := l.foldr (· + ·) 0

/--
  Ones'-complement sum of a list of 16-bit words, as a representative in 0..65534.
  Go: fold each word with `acc = (acc + w) mod 0xFFFF` (or accumulate then fold carries).
-/
def wordSum (l : List UInt16) : Nat := (l.map (·.toNat)).foldl onesAdd 0

/-- Checksum value: ones' complement of the folded sum, as a 16-bit word. Go: `^sum16`. -/
def checksum (l : List UInt16) : UInt16 := (0xFFFF : UInt16) - (wordSum l).toUInt16

/-- Verification predicate: the data extended with its checksum sums (mod 65535) to 0. -/
def verify (l : List UInt16) : Bool := wordSum l == 0

/-! ## Ones'-complement addition is a commutative monoid mod 65535 (T-005 basis) -/

@[simp] theorem onesAdd_comm (x y : Nat) : onesAdd x y = onesAdd y x := by
  unfold onesAdd; rw [Nat.add_comm]

theorem onesAdd_assoc (x y z : Nat) :
    onesAdd (onesAdd x y) z = onesAdd x (onesAdd y z) := by
  unfold onesAdd
  rw [Nat.add_mod ((x+y)%65535) z, Nat.mod_mod, ← Nat.add_mod,
      Nat.add_mod x ((y+z)%65535), Nat.mod_mod, ← Nat.add_mod, Nat.add_assoc]

theorem onesAdd_right_swap (z x y : Nat) :
    onesAdd (onesAdd z x) y = onesAdd (onesAdd z y) x := by
  rw [onesAdd_assoc, onesAdd_comm x y, ← onesAdd_assoc]

theorem onesAdd_lt (x y : Nat) : onesAdd x y < 65535 := Nat.mod_lt _ (by decide)

/-! ## wordSum basic facts -/

theorem foldl_onesAdd_lt : ∀ (xs : List Nat) (a x : Nat),
    (x :: xs).foldl onesAdd a < 65535 := by
  intro xs; induction xs with
  | nil => intro a x; exact onesAdd_lt a x
  | cons y ys ih => intro a x; simp only [List.foldl_cons]; exact ih (onesAdd a x) y

/-- The folded sum representative is always `< 65535`. -/
theorem wordSum_lt (l : List UInt16) : wordSum l < 65535 := by
  unfold wordSum
  cases h : (l.map (·.toNat)) with
  | nil => decide
  | cons a t => exact foldl_onesAdd_lt t 0 a

theorem foldl_append (l : List Nat) (a w : Nat) :
    (l ++ [w]).foldl onesAdd a = onesAdd (l.foldl onesAdd a) w := by
  rw [List.foldl_append]; rfl

/-- Appending a word adds it (with carry) to the running sum. -/
theorem wordSum_append (l : List UInt16) (w : UInt16) :
    wordSum (l ++ [w]) = onesAdd (wordSum l) w.toNat := by
  unfold wordSum; rw [List.map_append]; simp only [List.map_cons, List.map_nil]
  rw [foldl_append]

/-! ## Correspondence to plain sum mod 65535 (model ↔ Go fold) -/

theorem foldl_onesAdd_eq (l : List Nat) (a : Nat) (ha : a < 65535) :
    l.foldl onesAdd a = (a + sumN l) % 65535 := by
  induction l generalizing a with
  | nil =>
    simp only [List.foldl_nil, sumN, List.foldr_nil, Nat.add_zero]
    exact (Nat.mod_eq_of_lt ha).symm
  | cons x xs ih =>
    simp only [List.foldl_cons, sumN, List.foldr_cons]
    rw [ih (onesAdd a x) (Nat.mod_lt _ (by decide))]
    unfold onesAdd
    rw [Nat.add_mod ((a+x)%65535), Nat.mod_mod, ← Nat.add_mod, Nat.add_assoc]
    rfl

/-- The folded ones'-complement sum equals the plain word sum modulo 65535. -/
theorem wordSum_eq_sum_mod (l : List UInt16) :
    wordSum l = sumN (l.map (·.toNat)) % 65535 := by
  unfold wordSum
  rw [foldl_onesAdd_eq _ 0 (by decide), Nat.zero_add]

/-! ## Checksum value and roundtrip soundness (T-004, T-007) -/

/-- The checksum word's numeric value is `0xFFFF - (folded sum)`. -/
theorem checksum_toNat (l : List UInt16) : (checksum l).toNat = 65535 - wordSum l := by
  unfold checksum
  have hlt := wordSum_lt l
  rw [UInt16.toNat_sub]
  have hv : ((wordSum l).toUInt16).toNat = wordSum l := by
    show (UInt16.ofNat (wordSum l)).toNat = wordSum l
    rw [UInt16.toNat_ofNat']; omega
  have hff : ((0xFFFF : UInt16)).toNat = 65535 := by decide
  rw [hv, hff]; omega

/--
  Roundtrip soundness (T-004): data extended with its own checksum verifies,
  i.e. the total ones'-complement sum is zero. This is the core correctness
  property the receiver relies on (RFC 9293 MUST-3).
-/
theorem verify_checksum (l : List UInt16) : verify (l ++ [checksum l]) = true := by
  unfold verify
  rw [wordSum_append, checksum_toNat]
  unfold onesAdd
  have hlt := wordSum_lt l
  have hsum : (wordSum l + (65535 - wordSum l)) = 65535 := by omega
  rw [hsum]; decide

/--
  End-around carry / zero representation (T-007): when the folded sum is the
  all-ones value, the result is zero. In our mod-65535 representatives this is
  the statement that `0xFFFF` and `0x0000` denote the same residue, so a sum
  whose plain total is `65535` reduces to `0`.
-/
theorem end_around_carry_zero (l : List UInt16)
    (h : sumN (l.map (·.toNat)) % 65535 = 0) : wordSum l = 0 := by
  rw [wordSum_eq_sum_mod]; exact h

/-- Concrete T-007: a plain total of `0xFFFF` folds to `0`. -/
theorem fold_FFFF_to_zero : onesAdd 0x8000 0x7FFF = 0 := by decide

/-! ## Order independence / metamorphic basis (T-005) -/

/-- The folded sum is invariant under permutation of the word list. -/
theorem wordSum_perm {l1 l2 : List UInt16} (h : l1.Perm l2) : wordSum l1 = wordSum l2 := by
  unfold wordSum
  have hm : (l1.map (·.toNat)).Perm (l2.map (·.toNat)) := h.map _
  exact List.Perm.foldl_eq' hm (fun x _ y _ z => onesAdd_right_swap z x y) 0

/-- Concatenation order does not change the checksum sum (metamorphic relation). -/
theorem wordSum_append_comm (l1 l2 : List UInt16) :
    wordSum (l1 ++ l2) = wordSum (l2 ++ l1) :=
  wordSum_perm List.perm_append_comm

/-- Therefore the checksum value itself is order-independent. -/
theorem checksum_append_comm (l1 l2 : List UInt16) :
    checksum (l1 ++ l2) = checksum (l2 ++ l1) := by
  unfold checksum; rw [wordSum_append_comm]

end TcpFv.Checksum
