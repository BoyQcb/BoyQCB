package core

import "testing"

func TestGroupSpansMerge(t *testing.T) {
	segs := []LearnSeg{
		{Index: 101, Dur: 5.0, Kind: MarkAd, WasAd: false},
		{Index: 102, Dur: 4.5, Kind: MarkAd, WasAd: false},
		{Index: 104, Dur: 3.2, Kind: MarkAd, WasAd: false}, // 段号断开，应为新区间
		{Index: 105, Dur: 6.0, Kind: "keep", WasAd: true},  // 类型不同，应为新区间
	}
	spans := GroupSpans(segs)
	if len(spans) != 3 {
		t.Fatalf("期望 3 个区间，实际 %d", len(spans))
	}
	if spans[0].From != 101 || spans[0].To != 102 || len(spans[0].Durs) != 2 {
		t.Fatalf("区间1 归并错误: %+v", spans[0])
	}
	if spans[1].From != 104 || spans[1].To != 104 {
		t.Fatalf("区间2 错误: %+v", spans[1])
	}
	if spans[2].Kind != "keep" || spans[2].Total != 6.0 {
		t.Fatalf("区间3 错误: %+v", spans[2])
	}
	if spans[0].Total != 9.5 {
		t.Fatalf("区间总时长错误: %v", spans[0].Total)
	}
}

func TestStatsFromMarks(t *testing.T) {
	segs := []LearnSeg{
		{Index: 1, Kind: "ad", WasAd: false},      // 漏判
		{Index: 2, Kind: "ad", WasAd: true},       // 已判广告，不计漏判
		{Index: 3, Kind: "keep", WasAd: true},     // 误判
		{Index: 4, Kind: "keep", WasAd: false},    // 已判正片，不计误判
		{Index: 5, Kind: "suspect", WasAd: false}, // 疑似
	}
	st := StatsFromMarks(segs)
	if st.Marked != 5 || st.Missed != 1 || st.Wrong != 1 || st.Suspect != 1 {
		t.Fatalf("统计错误: %+v", st)
	}
}

func TestFingerprintExists(t *testing.T) {
	exist := [][]float64{{5.0, 4.5, 3.2}}
	if !FingerprintExists(exist, []float64{5.02, 4.48, 3.2}, 0.05) {
		t.Fatal("容差内应判定为已存在")
	}
	if FingerprintExists(exist, []float64{5.5, 4.5, 3.2}, 0.05) {
		t.Fatal("超出容差应判定为不存在")
	}
	if FingerprintExists(exist, []float64{5.0, 4.5}, 0.05) {
		t.Fatal("长度不同应判定为不存在")
	}
	if FingerprintExists(nil, []float64{1.0}, 0.05) {
		t.Fatal("无现有指纹时不应命中")
	}
}
