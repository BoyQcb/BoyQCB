package core

import (
	"fmt"
	"os"
	"testing"
)

// TestZZSiteCheckBoundary 用 FindAdsBoundary（入口锚点+bdTol>=1.05）核对各站点广告命中
func TestZZSiteCheckBoundary(t *testing.T) {
	dir := `C:\Users\00429\AppData\Roaming\Tencent\Marvis\User\oAN1i2SK4ZPINuDQR_mXpaFUGrT4\workspace\conv_8b99402c55f34d1da889375ab6dab354\temp\sitecheck`
	cases := []struct {
		key string
		fps [][]float64
	}{
		{key: "site1", fps: [][]float64{{5.57, 2.93, 5.7, 3.33, 1.53}}},
		{key: "site2", fps: [][]float64{{6.67, 2.13, 3.23, 3.73}, {5.57, 2.93, 5.7, 3.33, 1.53}}},
		{key: "site3", fps: [][]float64{{6.67, 2.13, 3.23, 3.73}}},
		{key: "site4", fps: [][]float64{{6.67, 2.13, 3.23, 3.73}, {5.57, 2.93, 5.7, 3.33, 1.53}}},
	}
	for _, c := range cases {
		path := fmt.Sprintf(`%s\%s-level2.m3u8`, dir, c.key)
		raw, err := os.ReadFile(path)
		if err != nil {
			t.Fatal(err)
		}
		segs, _ := ParseSegments(string(raw))
		fmt.Printf("\n########## %s 段数=%d ##########\n", c.key, len(segs))
		var all [][2]int
		for _, fp := range c.fps {
			h := FindAdsBoundary(segs, fp, 0.05)
			fmt.Printf("指纹 %v: %v\n", fp, h)
			all = append(all, h...)
		}
		fmt.Printf("合并: %v（%d 段）\n", MergeHits(all), countSegsH3(MergeHits(all)))
	}
}
