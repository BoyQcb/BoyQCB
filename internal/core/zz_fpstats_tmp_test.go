package core

import (
	"fmt"
	"math"
	"os"
	"testing"
)

func TestZZVerifyFpStats(t *testing.T) {
	path := `C:\Users\00429\AppData\Roaming\Tencent\Marvis\User\oAN1i2SK4ZPINuDQR_mXpaFUGrT4\workspace\conv_8b99402c55f34d1da889375ab6dab354\temp\test_2000k.m3u8`
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	segs := ParseSegmentsRaw(string(raw))
	fp := []float64{4, 4, 4, 4, 4}
	tol := 0.15
	n := len(fp)
	total := len(segs)
	fmt.Printf("总段数=%d\n", total)
	cnt := 0
	for i := 0; i+n <= total; i++ {
		ok := true
		for k := 0; k < n; k++ {
			if math.Abs(segs[i+k].Dur-fp[k]) > tol {
				ok = false
				break
			}
		}
		if !ok {
			continue
		}
		cnt++
		ent, ext := false, false
		if i > 0 {
			ent = segs[i].Disc // 窗口首段前有 DISC（#(i+1) 前）
		}
		if i+n < total {
			ext = segs[i+n].Disc // 窗口尾段后（#(i+n+1) 前）有 DISC
		}
		// 窗口内是否有 DISC
		in := false
		for k := i + 1; k < i+n; k++ {
			if segs[k].Disc {
				in = true
			}
		}
		dual := ent && ext
		flag := ""
		if dual {
			flag = " <== 双侧DISC"
		}
		fmt.Printf("  窗口[%d-%d] 入口DISC=%v 出口DISC=%v 内部DISC=%v%s\n", segs[i].Idx, segs[i+n-1].Idx, ent, ext, in, flag)
	}
	fmt.Printf("普通匹配窗口总数=%d\n", cnt)
}
