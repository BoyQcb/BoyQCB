package store

import (
	"path/filepath"
	"testing"
)

func TestMarksSaveGetDelete(t *testing.T) {
	dir := t.TempDir()
	m := NewMarks(filepath.Join(dir, "marks.json"))

	rec := &MarkRecord{
		SiteID: "dytt", SiteName: "dytt", URL: "http://a/index.m3u8",
		TotalSegs: 900, Segs: []MarkSeg{
			{Index: 101, Dur: 5.0, URL: "http://a/101.ts", Kind: MarkAd, WasAd: false},
			{Index: 102, Dur: 4.5, URL: "http://a/102.ts", Kind: MarkKeep, WasAd: true},
		},
	}
	m.Save(rec)

	got := m.Get(MarkKey("dytt", "http://a/index.m3u8"))
	if got == nil || len(got.Segs) != 2 || got.Segs[0].Kind != MarkAd || !got.Segs[1].WasAd {
		t.Fatalf("读取标记失败: %+v", got)
	}
	if len(m.List("dytt")) != 1 || len(m.List("other")) != 0 {
		t.Fatalf("按站点列出失败: %d / %d", len(m.List("dytt")), len(m.List("other")))
	}

	// 同一条影片重复保存应覆盖而非追加
	rec.Segs = rec.Segs[:1]
	m.Save(rec)
	if got := m.Get(MarkKey("dytt", "http://a/index.m3u8")); len(got.Segs) != 1 {
		t.Fatalf("覆盖保存失败: %+v", got)
	}

	m.Delete(MarkKey("dytt", "http://a/index.m3u8"))
	if m.Get(MarkKey("dytt", "http://a/index.m3u8")) != nil {
		t.Fatal("删除后仍可读到标记")
	}

	// 落盘后重新加载
	m2 := NewMarks(filepath.Join(dir, "marks.json"))
	rec2 := &MarkRecord{SiteID: "xg", URL: "http://b/index.m3u8", Segs: []MarkSeg{{Index: 1, Kind: MarkSuspect}}}
	m2.Save(rec2)
	m3 := NewMarks(filepath.Join(dir, "marks.json"))
	if m3.Get(MarkKey("xg", "http://b/index.m3u8")) == nil {
		t.Fatal("重新加载后标记丢失")
	}
}
