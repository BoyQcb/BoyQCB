package core

import (
	"fmt"
	"os"
	"testing"
)

// TestZZSiteCheckFPMiss 核对 phimgood/site1 中疑似漏检的指纹形态
func TestZZSiteCheckFPMiss(t *testing.T) {
	dir := `C:\Users\00429\AppData\Roaming\Tencent\Marvis\User\oAN1i2SK4ZPINuDQR_mXpaFUGrT4\workspace\conv_8b99402c55f34d1da889375ab6dab354\temp\sitecheck`
	cases := []struct {
		key string
	}{
		{key: "site1"},
		{key: "site2"},
		{key: "site4"},
	}
	// 指纹候选：dytt 主指纹 5.567/3.2/5.367/3.333/1.6；形态B 5.57/2.93/5.7/3.33/1.53
	fps := [][]float64{
		{5.567, 3.2, 5.367, 3.333, 1.6},
		{5.57, 2.93, 5.7, 3.33, 1.53},
	}
	for _, c := range cases {
		path := fmt.Sprintf(`%s\%s-level2.m3u8`, dir, c.key)
		raw, err := os.ReadFile(path)
		if err != nil {
			t.Fatal(err)
		}
		segs, _ := ParseSegments(string(raw))
		fmt.Printf("\n########## %s 段数=%d ##########\n", c.key, len(segs))
		for _, fp := range fps {
			fmt.Printf("指纹 %v 直接滑动匹配（tol=0.05相对）:\n", fp)
			for i := 0; i+len(fp) <= len(segs); i++ {
				ok := true
				for k := 0; k < len(fp); k++ {
					base := fp[k]
					tol := 0.05 * base
					if abs(segs[i+k].Dur-base) > tol {
						ok = false
						break
					}
				}
				if ok {
					pre := "-"
					if i > 0 {
						pre = fmt.Sprintf("%.3g", segs[i-1].Dur)
					}
					disc := ""
					if segs[i].Disc {
						disc = " 入口DISC"
					}
					fmt.Printf("  命中 @#%d 入口前=%s%s\n", segs[i].Idx, pre, disc)
				}
			}
		}
	}
}

func abs(f float64) float64 {
	if f < 0 {
		return -f
	}
	return f
}
