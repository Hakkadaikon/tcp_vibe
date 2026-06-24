/-
  Connection 4-tuple key soundness (RFC 9293, R-MUX-001 / INV-MUX-002).

  SCOPE / cost-benefit note:
  INV-MUX-001 ("at most one non-TIME-WAIT TCB per 4-tuple") is a STATE-SPACE
  invariant about the concurrent connection table under PassiveOpen /
  SegArriveListen / TimeWaitTimeout / ReopenFromTimeWait. That is an exhaustive
  reachability property and belongs to the TLA+ model checker (modeler), NOT
  Lean. Plain structural equality of a 4-tuple is decidable and trivially
  collision-free, so proving "distinct 4-tuples => distinct keys" for a record
  with DecidableEq is busy-work a property test (or the type system) already
  covers.

  The ONE place a real implementation bug hides is the DEMUX KEY NORMALISATION:
  from a received segment the local side is (dst_ip, dst_port) and the remote
  side is (src_ip, src_port), i.e. the wire's source/destination are SWAPPED
  relative to the connection's (local, remote) tuple. Getting that swap wrong
  (or making it non-injective) silently mis-routes segments. So we keep Lean to
  exactly that: the demux key built from a segment is INJECTIVE in the segment's
  four address fields, and it matches the stored connection tuple field-by-field.
  Everything else here is left to PBT / TLA+ by design.
-/

namespace TcpFv.Mux

/-- A connection's identifying 4-tuple (R-MUX-001). 16-bit ports, 32-bit IPv4. -/
structure FourTuple where
  localIp    : UInt32
  localPort  : UInt16
  remoteIp   : UInt32
  remotePort : UInt16
deriving DecidableEq, Repr

/-- The address fields carried by a received segment (wire view). -/
structure SegAddr where
  srcIp   : UInt32
  srcPort : UInt16
  dstIp   : UInt32
  dstPort : UInt16
deriving DecidableEq, Repr

/--
  Demux key built from a received segment (R-MUX, "key = (dst_ip,dst_port,
  src_ip,src_port)"): local <- destination, remote <- source. This is the
  source/destination SWAP that must be exactly right.
-/
def demuxKey (s : SegAddr) : FourTuple :=
  { localIp := s.dstIp, localPort := s.dstPort,
    remoteIp := s.srcIp, remotePort := s.srcPort }

/-! ## Key normalisation injectivity (the load-bearing property) -/

/--
  The demux key is injective: two segments yielding the same key must have
  identical four address fields. (No two distinct wire flows alias to one
  connection key.) This pins the swap down so a transposed-field bug fails.
-/
theorem demuxKey_injective (s t : SegAddr) (h : demuxKey s = demuxKey t) :
    s = t := by
  unfold demuxKey at h
  -- destructure the FourTuple equality into the four field equalities
  cases s; cases t
  simp only [FourTuple.mk.injEq] at h
  obtain ⟨h1, h2, h3, h4⟩ := h
  -- h1: dstIp, h2: dstPort, h3: srcIp, h4: srcPort
  subst h1; subst h2; subst h3; subst h4
  rfl

/--
  The demux key matches a stored connection tuple iff the wire source/dest map
  to the connection's remote/local exactly. This is the acceptance test demux
  must satisfy (INV-MUX-002: a segment reaches only the exact-match TCB).
-/
theorem demuxKey_matches_iff (s : SegAddr) (c : FourTuple) :
    demuxKey s = c ↔
      (c.localIp = s.dstIp ∧ c.localPort = s.dstPort ∧
       c.remoteIp = s.srcIp ∧ c.remotePort = s.srcPort) := by
  unfold demuxKey
  cases c
  simp only [FourTuple.mk.injEq]
  constructor
  · rintro ⟨h1, h2, h3, h4⟩; exact ⟨h1.symm, h2.symm, h3.symm, h4.symm⟩
  · rintro ⟨h1, h2, h3, h4⟩; exact ⟨h1.symm, h2.symm, h3.symm, h4.symm⟩

/-- Structural-equality collision-freedom is decidable (the trivial part, here
    only to document that it is covered by the type, not worth heavy proof). -/
theorem fourTuple_eq_decidable (a b : FourTuple) :
    a = b ∨ a ≠ b := by
  exact Decidable.em (a = b)

/-! ## Concrete demux cases -/

/-- Same flow reversed (src/dst swapped) yields a DIFFERENT key: the two
    directions of a connection are distinguished, as required for demux. -/
theorem demux_direction_distinct :
    demuxKey { srcIp := 10, srcPort := 1, dstIp := 20, dstPort := 2 }
      ≠ demuxKey { srcIp := 20, srcPort := 2, dstIp := 10, dstPort := 1 } := by
  decide

/-- Demux of a concrete segment yields the swapped tuple. -/
theorem demux_concrete :
    demuxKey { srcIp := 0xC0A80101, srcPort := 40000,
               dstIp := 0x0A000001, dstPort := 80 }
      = { localIp := 0x0A000001, localPort := 80,
          remoteIp := 0xC0A80101, remotePort := 40000 } := by decide

end TcpFv.Mux
