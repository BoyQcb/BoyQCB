package core

import (
	"fmt"
	"os"
	"strings"
	"testing"
)

// TestZZSiteCheckDetail 打印 DISC 包裹区间与疑似广告块上下文
func TestZZSiteCheckDetail(t *testing.T) {
	dir := `C:\Users\00429\AppData\Roaming\Tencent\Marvis\User\oAN1i2SK4ZPINuDQR_mXpaFUGrT4\workspace\conv_8b99402c55f34d1da889375ab6dab354\temp\sitecheck`
	cases := []struct {
		key  string
		name string
	}{
		{key: "site1", name: "dytt-tvs"},
		{key: "site2", name: "phimgood-1"},
		{key: "site5", name: "xgplay-1"},
		{key: "site6", name: "xgplay-2"},
	}
	for _, c := range cases {
		path := fmt.Sprintf(`%s\%s-level2.m3u8`, dir, c.key)
		raw, err := os.ReadFile(path)
		if err != nil {
			t.Fatal(err)
		}
		segs, _ := ParseSegments(string(raw))
		fmt.Printf("\n########## %s (%s) 段数=%d ##########\n", c.key, c.name, len(segs))
		// 找出 DISC 位置
		var discPos []int
		for _, s := range segs {
			if s.Disc {
				discPos = append(discPos, s.Idx)
			}
		}
		fmt.Printf("DISC位置(%d个): %v\n", len(discPos), discPos)
		// 打印 DISC 相邻块上下文（每个 DISC 前后 3 段）
		for _, dp := range discPos {
			lo := dp - 3
			if lo < 1 {
				lo = 1
			}
			hi := dp + 4
			if hi > len(segs) {
				hi = len(segs)
			}
			var sb strings.Builder
			for i := lo; i <= hi; i++ {
				s := segs[i-1]
				d := "  "
				if s.Disc {
					d = "D "
				}
				sb.WriteString(fmt.Sprintf("    %s#%d dur=%.3g\n", d, s.Idx, s.Dur))
			}
			fmt.Printf("  DISC@#%d:\n%s", dp, sb.String())
		}
	}
}
