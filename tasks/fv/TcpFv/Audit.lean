import TcpFv.Seq
import TcpFv.SeqGo
import TcpFv.Checksum
import TcpFv.Rto
import TcpFv.Paws
import TcpFv.WScale
import TcpFv.Mux
import TcpFv.Cwnd
import TcpFv.Reasm

open TcpFv.Seq TcpFv.Checksum TcpFv.Cwnd TcpFv.Reasm

-- Seq
#print axioms seqLT_irrefl
#print axioms seqLT_wrap_max_zero
#print axioms seqLT_wrap_zero_max
#print axioms seqLT_zero_one
#print axioms seqLT_asymm
#print axioms seqLT_asymm_halfwindow
#print axioms acceptableAck_after_una
#print axioms acceptableAck_atMost_nxt
#print axioms acceptableAck_in_window
#print axioms acceptableAck_boundary_lo
#print axioms acceptableAck_reject_eq_una
#print axioms acceptableAck_accept_eq_nxt
#print axioms acceptableAck_reject_gt_nxt
#print axioms seqAdd_eq
#print axioms seqAdd_wrap_max
#print axioms msb_eq_mask

-- SeqGo (Go-faithful SeqLT bridge, FV-3)
#print axioms seqLTGo_irrefl
#print axioms seqLT_seqLTGo_diverge_antipodal
#print axioms seqLTGo_eq_seqLT_of_halfwindow
#print axioms seqLTGo_eq_seqLT_of_msb_false
#print axioms seqLTGo_transports_irrefl

-- Checksum
#print axioms onesAdd_comm
#print axioms onesAdd_assoc
#print axioms wordSum_lt
#print axioms wordSum_append
#print axioms wordSum_eq_sum_mod
#print axioms checksum_toNat
#print axioms verify_checksum
#print axioms end_around_carry_zero
#print axioms fold_FFFF_to_zero
#print axioms wordSum_perm
#print axioms wordSum_append_comm
#print axioms checksum_append_comm

-- Rto
#print axioms TcpFv.Rto.rto_ge_min
#print axioms TcpFv.Rto.rto_ge_1000
#print axioms TcpFv.Rto.rto_ge_raw
#print axioms TcpFv.Rto.rtoRaw_rounds_to_g
#print axioms TcpFv.Rto.rtoRaw_g_branch_of_rttvar_zero
#print axioms TcpFv.Rto.srtt_bounded
#print axioms TcpFv.Rto.rttvar_bounded
#print axioms TcpFv.Rto.est_bounded
#print axioms TcpFv.Rto.initEst_bounded
#print axioms TcpFv.Rto.backoff_ge_cur
#print axioms TcpFv.Rto.backoff_le_cap
#print axioms TcpFv.Rto.backoff_chain_monotone
#print axioms TcpFv.Rto.backoffIter_le_cap
#print axioms TcpFv.Rto.backoffIter_step_monotone
#print axioms TcpFv.Rto.rto_init_concrete
#print axioms TcpFv.Rto.rto_init_large_concrete
#print axioms TcpFv.Rto.rto_floor_engages
#print axioms TcpFv.Rto.backoff_concrete
#print axioms TcpFv.Rto.backoff_caps
#print axioms TcpFv.Rto.rtoRaw_g_concrete
#print axioms TcpFv.Rto.update_in_hull_concrete

-- Paws
#print axioms TcpFv.Paws.pawsDrop_iff_not_fresh
#print axioms TcpFv.Paws.pawsDrop_false_of_fresh
#print axioms TcpFv.Paws.pawsDrop_implies_older
#print axioms TcpFv.Paws.tsRecent_monotone
#print axioms TcpFv.Paws.tsRecent_update_fires
#print axioms TcpFv.Paws.tsRecent_update_holds
#print axioms TcpFv.Paws.paws_equal_not_dropped
#print axioms TcpFv.Paws.paws_newer_not_dropped
#print axioms TcpFv.Paws.paws_older_dropped
#print axioms TcpFv.Paws.paws_wrap_not_dropped
#print axioms TcpFv.Paws.paws_wrap_max_dropped
#print axioms TcpFv.Paws.tsRecent_update_concrete
#print axioms TcpFv.Paws.tsRecent_hold_concrete
#print axioms TcpFv.Paws.tsRecent_hold_stale_concrete

-- WScale
#print axioms TcpFv.WScale.effShift_le_max
#print axioms TcpFv.WScale.effShift_id_below
#print axioms TcpFv.WScale.effShift_sat_above
#print axioms TcpFv.WScale.effShift_idem
#print axioms TcpFv.WScale.scaleWindow_toNat
#print axioms TcpFv.WScale.scaleWindow_lt_2pow30
#print axioms TcpFv.WScale.scaleWindow_no_overflow
#print axioms TcpFv.WScale.effShift_15_clamps
#print axioms TcpFv.WScale.effShift_255_clamps
#print axioms TcpFv.WScale.effShift_0_id
#print axioms TcpFv.WScale.effShift_14_id
#print axioms TcpFv.WScale.effShift_7_id

-- Mux
#print axioms TcpFv.Mux.demuxKey_injective
#print axioms TcpFv.Mux.demuxKey_matches_iff
#print axioms TcpFv.Mux.fourTuple_eq_decidable
#print axioms TcpFv.Mux.demux_direction_distinct
#print axioms TcpFv.Mux.demux_concrete

-- Cwnd (byte-counting increase laws, FV-2)
#print axioms ssStep_increase_le_smss
#print axioms ssStep_monotone
#print axioms ssStep_preserves_floor
#print axioms ssStep_full
#print axioms caStep_cwnd_monotone
#print axioms caStep_increase_le_smss
#print axioms caStep_acc_no_underflow
#print axioms caStep_no_fire
#print axioms caStep_preserves_floor
#print axioms caFold_cwnd_monotone
#print axioms caFold_increase_le_smss_if_small

-- Reasm (out-of-order reassembly soundness, FV-1)
#print axioms streamRange_split
#print axioms streamRange_drop
#print axioms leftTrim_wf
#print axioms absorb_sound
#print axioms leftTrim_fully_received_empty
#print axioms absorb_empty_noop
#print axioms duplicate_idempotent
#print axioms absorb_rcvNxt_monotone
#print axioms partial_overlap_sound
#print axioms driveOne_inv
#print axioms driveAll_inv
#print axioms reassembly_sound
#print axioms init_inv
