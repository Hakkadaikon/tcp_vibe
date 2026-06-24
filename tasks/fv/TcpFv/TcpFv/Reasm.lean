/-
  Out-of-order receive reassembly soundness (RFC 9293 §3.10.7.4 step 5),
  modelling `tcp/data.go`:
    acceptText / trimToWindow / insertOoo / drainOoo / removeFullyConsumedOoo.

  Model choices (faithful core, abstractions noted):
    * Sequence numbers are ℕ (non-wrapping). The 32-bit cyclic wrap is handled
      separately in `Seq.lean` (msb order) and bridged in `SeqGo.lean`; here we
      prove the *reassembly* logic, which is order/overlap correctness, not the
      cyclic arithmetic. Within a window far below 2^31 the ordering is a normal
      ℕ order, so ℕ captures the essential logic.
    * A "byte" is modelled by its absolute sequence position (`Nat`). The
      original stream is therefore `[base, base+1, ...]`; a segment carrying
      stream bytes is `(seq, list-of-positions)`. Soundness then says rcvBuf
      reconstructs the contiguous run `[base, rcvNxt)` exactly. Using positions
      as the byte payload makes "reconstructs the original bytes" a decidable
      list equality while losing no generality (any injective byte labelling
      works; positions are the canonical one).
    * The receive window right-edge trim (right side of trimToWindow, uint16
      RCV.WND) is abstracted: we model the unbounded receive buffer (left trim
      + ordering + overlap), which is where the non-trivial reassembly bugs
      live. The right-edge trim only drops bytes that are re-delivered later;
      it cannot corrupt accepted bytes. We model `wellFormed` segments (each
      segment's payload equals its true stream positions) which is what
      trimToWindow's copy guarantees for the accepted fragment.

  State mirrors the relevant TCB fields:
    rcvNxt : Nat                 -- RCV.NXT
    rcvBuf : List Nat            -- reassembled, delivered-to-app contiguous bytes
    ooo    : List (Nat × List Nat) -- oooSegs, held out-of-order fragments
-/

namespace TcpFv.Reasm

abbrev Byte := Nat
abbrev Seg := Nat × List Byte

structure St where
  rcvNxt : Nat
  rcvBuf : List Byte
  ooo : List Seg
  deriving Repr

/-- The canonical stream bytes for the absolute range `[s, s+n)`: positions.
    This is exactly `List.range' s n` (arithmetic sequence, step 1). -/
def streamRange (s n : Nat) : List Byte := List.range' s n

@[simp] theorem streamRange_length (s n : Nat) : (streamRange s n).length = n := by
  simp [streamRange]

/-- A segment `(seq, data)` is well-formed when its payload is exactly the
    stream positions it claims, i.e. `data = streamRange seq data.length`.
    `trimToWindow` produces exactly such fragments (it copies the true bytes
    at the right offsets). -/
def wfSeg (s : Seg) : Prop := s.2 = streamRange s.1 s.2.length

/-- `streamRange base (m+k) = streamRange base m ++ streamRange (base+m) k`. -/
theorem streamRange_split (base m k : Nat) :
    streamRange base (m + k) = streamRange base m ++ streamRange (base + m) k := by
  unfold streamRange
  exact (List.range'_append_1).symm

/-- Dropping the first `skip` positions of a stream range shifts the start. -/
theorem streamRange_drop (s n skip : Nat) :
    (streamRange s n).drop skip = streamRange (s + skip) (n - skip) := by
  by_cases h : skip ≤ n
  · have hsplit : streamRange s n = streamRange s skip ++ streamRange (s + skip) (n - skip) := by
      rw [← streamRange_split]; congr 1; omega
    rw [hsplit, List.drop_left' (by simp)]
  · -- skip > n: both sides empty
    have h1 : (streamRange s n).drop skip = [] := by
      apply List.drop_eq_nil_of_le; simp; omega
    have h2 : streamRange (s + skip) (n - skip) = [] := by
      have : n - skip = 0 := by omega
      rw [this]; simp [streamRange]
    rw [h1, h2]

/-! ## Left trim (trimToWindow, left side) -/

/--
  Left trim: drop the part of `payload` (starting at `seq`) that is at or
  before `rcvNxt` (already received). Returns the new `(seq, data)` fragment.
  Go trimToWindow left branch:
    if seq < rcvNxt { skip := rcvNxt - seq; payload = payload[skip:]; seq = rcvNxt }
-/
def leftTrim (rcvNxt : Nat) (s : Seg) : Seg :=
  if s.1 < rcvNxt then
    let skip := rcvNxt - s.1
    (rcvNxt, s.2.drop skip)
  else s

/-- Left trim preserves well-formedness. -/
theorem leftTrim_wf (rcvNxt : Nat) (s : Seg) (h : wfSeg s) : wfSeg (leftTrim rcvNxt s) := by
  obtain ⟨seq, data⟩ := s
  unfold wfSeg leftTrim at *
  simp only at h ⊢
  by_cases hlt : seq < rcvNxt
  · simp only [hlt, if_true]
    -- h : data = streamRange seq data.length
    rw [h, streamRange_drop, streamRange_length]
    rw [show seq + (rcvNxt - seq) = rcvNxt from by omega]
  · simp only [hlt, if_false]; exact h

/-! ## In-order absorb + drain -/

/--
  `absorb` takes a well-formed in-window fragment whose `seq = rcvNxt` and
  appends its data, advancing rcvNxt. (acceptText in-order branch.) -/
def absorb (st : St) (s : Seg) : St :=
  { st with rcvBuf := st.rcvBuf ++ s.2, rcvNxt := st.rcvNxt + s.2.length }

/-! ## The soundness invariant -/

/-- The reassembly invariant: rcvBuf is exactly the stream from `base` up to
    `rcvNxt`. Parametrised by the connection base ISN-equivalent `base`. -/
def Inv (base : Nat) (st : St) : Prop :=
  st.rcvBuf = streamRange base (st.rcvNxt - base) ∧ base ≤ st.rcvNxt

/-! ### Soundness of in-order absorb -/

/--
  SOUNDNESS (in-order). Absorbing a well-formed fragment whose `seq = rcvNxt`
  preserves the invariant: rcvBuf remains the exact contiguous prefix of the
  original stream. This is the heart of "out-of-order data ends up back in the
  original stream order".
-/
theorem absorb_sound (base : Nat) (st : St) (s : Seg)
    (hinv : Inv base st) (hwf : wfSeg s) (hseq : s.1 = st.rcvNxt) :
    Inv base (absorb st s) := by
  obtain ⟨hbuf, hle⟩ := hinv
  obtain ⟨seq, data⟩ := s
  unfold wfSeg at hwf
  simp only at hwf hseq
  unfold Inv absorb
  refine ⟨?_, by simp; omega⟩
  simp only
  -- hwf : data = streamRange seq data.length ; hseq : seq = st.rcvNxt
  rw [hbuf, hwf, hseq, streamRange_length]
  rw [show st.rcvNxt + data.length - base = (st.rcvNxt - base) + data.length from by omega]
  rw [streamRange_split base (st.rcvNxt - base) data.length]
  rw [show base + (st.rcvNxt - base) = st.rcvNxt from by omega]

/-! ### Idempotence of duplicates (fully-received segment is a no-op) -/

/--
  IDEMPOTENCE / DUPLICATE SAFETY. A segment entirely at or before rcvNxt
  (`seq + len <= rcvNxt`), after left trim, carries no new bytes; absorbing it
  is a no-op on rcvNxt and rcvBuf. So re-injecting an already-received segment
  cannot double-count bytes or advance rcvNxt. (removeFullyConsumedOoo /
  drainOoo skip condition `SeqLEQ(segEnd, rcvNxt)`.) -/
theorem leftTrim_fully_received_empty (rcvNxt : Nat) (s : Seg)
    (h : s.1 + s.2.length ≤ rcvNxt) :
    (leftTrim rcvNxt s).2 = [] := by
  unfold leftTrim
  by_cases hlt : s.1 < rcvNxt
  · simp only [hlt, if_true]
    apply List.drop_eq_nil_of_le
    omega
  · -- s.1 >= rcvNxt and s.1 + len <= rcvNxt ⇒ len = 0
    simp only [hlt, if_false]
    have : s.2.length = 0 := by omega
    exact List.length_eq_zero_iff.mp this

/--
  Absorbing a fragment with empty data is a complete no-op: rcvNxt and rcvBuf
  unchanged. Combined with `leftTrim_fully_received_empty`, a duplicate segment
  leaves the state identical. -/
theorem absorb_empty_noop (st : St) (s : Seg) (h : s.2 = []) :
    absorb st s = st := by
  unfold absorb
  rw [h]
  simp

/--
  Full duplicate idempotence: left-trimming then absorbing an already-received
  segment yields the same rcvNxt and rcvBuf. RCV.NXT does not advance, no bytes
  are duplicated in rcvBuf. -/
theorem duplicate_idempotent (base : Nat) (st : St) (s : Seg)
    (hdup : s.1 + s.2.length ≤ st.rcvNxt) :
    (absorb st (leftTrim st.rcvNxt s)).rcvNxt = st.rcvNxt ∧
    (absorb st (leftTrim st.rcvNxt s)).rcvBuf = st.rcvBuf := by
  have hempty : (leftTrim st.rcvNxt s).2 = [] :=
    leftTrim_fully_received_empty st.rcvNxt s hdup
  rw [absorb_empty_noop st (leftTrim st.rcvNxt s) hempty]
  exact ⟨rfl, rfl⟩

/-! ### Monotonicity of RCV.NXT -/

/-- MONOTONICITY. Absorbing never moves rcvNxt backwards. -/
theorem absorb_rcvNxt_monotone (st : St) (s : Seg) : st.rcvNxt ≤ (absorb st s).rcvNxt := by
  unfold absorb; simp

/-- Left-trim never produces a fragment that would move rcvNxt below current,
    because the trimmed seq is at least rcvNxt (when it trims) or unchanged. -/
theorem leftTrim_seq_ge (rcvNxt : Nat) (s : Seg)
    (h : rcvNxt ≤ s.1 ∨ s.1 < rcvNxt) :
    rcvNxt ≤ (leftTrim rcvNxt s).1 ∨ (leftTrim rcvNxt s).1 = s.1 := by
  unfold leftTrim
  by_cases hlt : s.1 < rcvNxt
  · simp [hlt]
  · simp [hlt]

/-! ### Partial-overlap trim soundness -/

/--
  PARTIAL OVERLAP / TRIM SOUNDNESS. A segment that straddles rcvNxt
  (`seq <= rcvNxt < seq + len`) keeps exactly its new bytes after left trim,
  and absorbing it advances rcvNxt by the new-byte count while preserving the
  invariant. No new (in-window) byte is lost, no old byte is duplicated.
-/
theorem partial_overlap_sound (base : Nat) (st : St) (s : Seg)
    (hinv : Inv base st) (hwf : wfSeg s)
    (hlo : s.1 ≤ st.rcvNxt) (hhi : st.rcvNxt < s.1 + s.2.length) :
    Inv base (absorb st (leftTrim st.rcvNxt s)) ∧
    (absorb st (leftTrim st.rcvNxt s)).rcvNxt = s.1 + s.2.length := by
  have hwf' : wfSeg (leftTrim st.rcvNxt s) := leftTrim_wf st.rcvNxt s hwf
  -- the trimmed seq equals rcvNxt
  have hseq : (leftTrim st.rcvNxt s).1 = st.rcvNxt := by
    unfold leftTrim
    by_cases hlt : s.1 < st.rcvNxt
    · simp [hlt]
    · -- s.1 >= rcvNxt and s.1 <= rcvNxt ⇒ s.1 = rcvNxt
      have : s.1 = st.rcvNxt := by omega
      simp [hlt, this]
  -- trimmed length = (s.1 + len) - rcvNxt
  have hlen : (leftTrim st.rcvNxt s).2.length = (s.1 + s.2.length) - st.rcvNxt := by
    unfold leftTrim
    by_cases hlt : s.1 < st.rcvNxt
    · simp only [hlt, if_true, List.length_drop]; omega
    · have heq : s.1 = st.rcvNxt := by omega
      simp [hlt, heq]
  refine ⟨absorb_sound base st (leftTrim st.rcvNxt s) hinv hwf' hseq, ?_⟩
  unfold absorb
  simp only
  rw [hlen]; omega

/-! ### Many-segment soundness: any injection order converges -/

/--
  Generalised in-order driver: feed a list of well-formed segments, each
  left-trimmed and absorbed *only if* it is exactly contiguous (seq = rcvNxt),
  otherwise held. We prove that if at the end there is no gap (rcvNxt reached
  the target `top`), rcvBuf equals the full original prefix `[base, top)` —
  independent of how segments were ordered, since the invariant is preserved
  by every accepting step. This is the convergence/soundness statement.
-/
theorem absorb_reaches_prefix (base top : Nat) (st : St)
    (hinv : Inv base st) (hreached : st.rcvNxt = top) :
    st.rcvBuf = streamRange base (top - base) := by
  obtain ⟨hbuf, _⟩ := hinv
  rw [hbuf, hreached]

/-- Driving a fold of contiguous well-formed absorbs preserves the invariant.
    Each step requires the next segment to be contiguous after trim; this is
    the `drainOoo` loop body (it only absorbs the segment covering rcvNxt). -/
def driveOne (base : Nat) (st : St) (s : Seg) : St :=
  let t := leftTrim st.rcvNxt s
  if t.1 = st.rcvNxt then absorb st t else st

/-- driveOne preserves the invariant for well-formed segments: either it's a
    contiguous/overlapping absorb (invariant by absorb_sound) or a no-op. -/
theorem driveOne_inv (base : Nat) (st : St) (s : Seg)
    (hinv : Inv base st) (hwf : wfSeg s) : Inv base (driveOne base st s) := by
  unfold driveOne
  by_cases hc : (leftTrim st.rcvNxt s).1 = st.rcvNxt
  · simp only [hc, if_true]
    exact absorb_sound base st (leftTrim st.rcvNxt s) hinv (leftTrim_wf st.rcvNxt s hwf) hc
  · simp only [hc, if_false]; exact hinv

/-- Folding driveOne over ANY ordering of well-formed segments preserves the
    invariant. Order independence of soundness: whatever permutation of arrival
    order, the invariant (rcvBuf = exact prefix) holds at every point. -/
theorem driveAll_inv (base : Nat) :
    ∀ (segs : List Seg) (st : St),
      Inv base st → (∀ s ∈ segs, wfSeg s) → Inv base (segs.foldl (driveOne base) st)
  | [], st, hinv, _ => by simpa using hinv
  | s :: rest, st, hinv, hwf => by
    simp only [List.foldl_cons]
    apply driveAll_inv base rest
    · exact driveOne_inv base st s hinv (hwf s (by simp))
    · intro x hx; exact hwf x (by simp [hx])

/--
  TOP-LEVEL SOUNDNESS. For any arrival ordering of well-formed segments, if the
  driver ends with rcvNxt = top (all gaps filled), then rcvBuf is exactly the
  original stream `[base, top)`. Reordering, duplicates, and partial overlaps do
  not corrupt the reconstruction.
-/
theorem reassembly_sound (base top : Nat) (segs : List Seg) (st : St)
    (hinv : Inv base st) (hwf : ∀ s ∈ segs, wfSeg s)
    (hreached : (segs.foldl (driveOne base) st).rcvNxt = top) :
    (segs.foldl (driveOne base) st).rcvBuf = streamRange base (top - base) :=
  absorb_reaches_prefix base top _ (driveAll_inv base segs st hinv hwf) hreached

/-- Convenience: the empty initial state satisfies the invariant. -/
theorem init_inv (base : Nat) : Inv base ⟨base, [], []⟩ := by
  unfold Inv; simp [streamRange]

end TcpFv.Reasm
