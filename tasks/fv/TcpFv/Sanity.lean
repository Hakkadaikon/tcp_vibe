import TcpFv.Seq
import TcpFv.Checksum
open TcpFv.Seq TcpFv.Checksum

-- seq comparisons
#eval seqLT 0 1                 -- true
#eval seqLT 0xFFFFFFFF 0        -- true (wrap)
#eval seqLT 0 0xFFFFFFFF        -- false
#eval seqLT 100 100             -- false
#eval acceptableAck 100 101 200 -- true
#eval acceptableAck 100 100 200 -- false (== una)
#eval acceptableAck 100 200 200 -- true  (== nxt)
#eval acceptableAck 100 201 200 -- false (> nxt)
#eval seqAdd 0xFFFFFFFF 1       -- 0 (wrap)

-- checksum: RFC 1071 worked example words
-- 0x0001 0xf203 0xf4f5 0xf6f7  -> ones'-comp sum 0xddf2, checksum 0x220d
#eval wordSum [0x0001, 0xf203, 0xf4f5, 0xf6f7]   -- 0xddf2 = 56818
#eval checksum [0x0001, 0xf203, 0xf4f5, 0xf6f7]  -- 0x220d = 8717
#eval verify ([0x0001, 0xf203, 0xf4f5, 0xf6f7] ++ [checksum [0x0001, 0xf203, 0xf4f5, 0xf6f7]]) -- true

-- end-around carry / zero rep
#eval wordSum [0xFFFF, 0xFFFF]   -- 0 (0xFFFF ~ 0)
#eval checksum []                -- 0xFFFF (empty -> sum 0 -> complement 0xFFFF)
