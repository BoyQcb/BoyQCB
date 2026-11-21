// 片段标记 + 规则学习接口
//
// /api/test/marks  保存 / 读取 / 清空某条影片的人工标记
// /api/marks       按站点列出标记记录（学习素材）
// /api/learn/suggest  把标记归并成区间，生成候选时长指纹
// /api/learn/hash     抓取标记段采样内容，生成候选内容哈希组
// /api/learn/apply    把候选指纹 / 哈希写回站点规则（写前自动备份 rules.json）

package web

import (
	"encoding/json"
	"fmt"
	"math"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"time"

	"dytt_parser_go/internal/config"
	"dytt_parser_go/internal/core"
	"dytt_parser_go/internal/store"
)

// ---------- 标记存储 ----------

// handleTestMarks 保存 / 读取 / 清空某条影片的标记
func (s *Server) handleTestMarks(w http.ResponseWriter, r *http.Request) {
	switch r.Method {
	case http.MethodPost:
		var body struct {
			SiteID    string          `json:"site_id"`
			SiteName  string          `json:"site_name"`
			URL       string          `json:"url"`
			SubURL    string          `json:"sub_url"`
			TotalSegs int             `json:"total_segs"`
			Hits      [][2]int        `json:"hits"`
			Segs      []store.MarkSeg `json:"segs"`
			Note      string          `json:"note"`
		}
		_ = json.NewDecoder(r.Body).Decode(&body)
		if body.URL == "" {
			writeJSON(w, http.StatusOK, map[string]interface{}{"code": 403, "msg": "缺少 url"})
			return
		}
		rec := &store.MarkRecord{
			SiteID:    body.SiteID,
			SiteName:  body.SiteName,
			URL:       body.URL,
			SubURL:    body.SubURL,
			TotalSegs: body.TotalSegs,
			Hits:      body.Hits,
			Segs:      body.Segs,
			Note:      body.Note,
		}
		s.marks.Save(rec)
		s.logger.Write("INFO", "admin", fmt.Sprintf("保存片段标记 %s：%d 段（站点 %s）",
			body.URL, len(body.Segs), body.SiteName))
		writeJSON(w, http.StatusOK, map[string]interface{}{"code": 200, "msg": "已保存", "data": rec})

	case http.MethodGet:
		key := store.MarkKey(r.URL.Query().Get("site_id"), r.URL.Query().Get("url"))
		rec := s.marks.Get(key)
		if rec == nil {
			writeJSON(w, http.StatusOK, map[string]interface{}{"code": 200, "data": nil})
			return
		}
		writeJSON(w, http.StatusOK, map[string]interface{}{"code": 200, "data": rec})

	case http.MethodDelete:
		key := store.MarkKey(r.URL.Query().Get("site_id"), r.URL.Query().Get("url"))
		s.marks.Delete(key)
		writeJSON(w, http.StatusOK, map[string]interface{}{"code": 200, "msg": "已清空标记"})

	default:
		writeJSON(w, http.StatusOK, map[string]interface{}{"code": 405, "msg": "方法不允许"})
	}
}

// handleMarksList 按站点列出标记记录
func (s *Server) handleMarksList(w http.ResponseWriter, r *http.Request) {
	list := s.marks.List(r.URL.Query().Get("site_id"))
	if list == nil {
		list = []*store.MarkRecord{}
	}
	writeJSON(w, http.StatusOK, map[string]interface{}{"code": 200, "data": list})
}

// ---------- 学习 ----------

func learnSegs(in []store.MarkSeg) []core.LearnSeg {
	out := make([]core.LearnSeg, 0, len(in))
	for _, m := range in {
		out = append(out, core.LearnSeg{Index: m.Index, Dur: m.Dur, URL: m.URL, Kind: m.Kind, WasAd: m.WasAd})
	}
	return out
}

// handleLearnSuggest 把标记归并成区间，产出候选时长指纹，并与站点现有指纹比对
func (s *Server) handleLearnSuggest(w http.ResponseWriter, r *http.Request) {
	var body struct {
		SiteID string          `json:"site_id"`
		Segs   []store.MarkSeg `json:"segs"`
	}
	_ = json.NewDecoder(r.Body).Decode(&body)
	if len(body.Segs) == 0 {
		writeJSON(w, http.StatusOK, map[string]interface{}{"code": 403, "msg": "没有标记片段"})
		return
	}
	segs := learnSegs(body.Segs)
	spans := core.GroupSpans(segs)
	stats := core.StatsFromMarks(segs)

	// 站点现有指纹（含兼容字段 fingerprint 单组）与保护指纹
	exist := [][]float64{}
	keeps := [][]float64{}
	cfg := s.cfgMgr.Snapshot()
	siteName := ""
	rawMode := ""
	for _, st := range cfg.Sites {
		if st.ID != body.SiteID {
			continue
		}
		siteName = st.Name
		rawMode = strings.ToLower(strings.TrimSpace(st.DetectMode))
		exist = st.AdsFPList()
		keeps = st.KeepFPList()
	}

	tol := cfg.Settings.MatchTolerance
	if tol <= 0 {
		tol = 0.05
	}
	// 只有「用户判为广告」的区间才作为广告指纹候选
	add, dup := [][]float64{}, [][]float64{}
	for _, sp := range spans {
		if sp.Kind != core.MarkAd {
			continue
		}
		if core.FingerprintExists(exist, sp.Durs, tol) {
			dup = append(dup, sp.Durs)
			continue
		}
		add = append(add, sp.Durs)
	}

	// 广告指纹区分度评估：指纹各时长项若在正片（非广告标记）片段中高频出现，
	// 说明该指纹与正片时长同构，滑动匹配会把成片正片误删（如正片大量 4.0s 段而广告也是 4.0s 段）。
	// 此时提示用户改用内容哈希学习，或确认该指纹仅在广告边界处命中（检测端已加边界确认）。
	nonAdDurs := []float64{}
	for _, sg := range body.Segs {
		if sg.Kind != core.MarkAd {
			nonAdDurs = append(nonAdDurs, sg.Dur)
		}
	}
	lowDistinct := []map[string]interface{}{}
	for _, sp := range add {
		common, ratio := evalFPDistinct(sp, nonAdDurs)
		if ratio < 0.8 || len(common) == 0 {
			continue
		}
		lowDistinct = append(lowDistinct, map[string]interface{}{
			"fp":     sp,
			"common": common,
			"ratio":  ratio,
			"reason": fmt.Sprintf("该指纹 %d/%d 项时长与正片同构（正片中出现≥3次），仅按时长滑动匹配会把正片成片误删；建议改用「哈希采样」学习内容指纹，或勾选下方确认广告边界后采用", len(common), len(sp)),
		})
	}

	// 误判反查：keep（正片）区间内若含既有广告指纹的连续匹配，说明是该指纹把正片判成了广告
	remove := []map[string]interface{}{}
	for _, sp := range spans {
		if sp.Kind != core.MarkKeep {
			continue
		}
		for _, e := range exist {
			if !core.FingerprintInSpan(e, sp.Durs, tol) {
				continue
			}
			remove = append(remove, map[string]interface{}{
				"fp":     e,
				"span":   [2]int{sp.From, sp.To},
				"durs":   sp.Durs,
				"reason": fmt.Sprintf("正片区间 %d-%d 的时长序列包含该指纹，疑似误判来源", sp.From, sp.To),
			})
		}
	}
	// 保护指纹候选：keep 区间的时长序列，写入后该序列不再被剔除
	keepAdd := [][]float64{}
	for _, sp := range spans {
		if sp.Kind != core.MarkKeep {
			continue
		}
		fp := core.TrimFP(sp.Durs, 16)
		if len(fp) > 0 && !core.FingerprintExists(keeps, fp, tol) {
			keepAdd = append(keepAdd, fp)
		}
	}

	// 检测方式联动提示：结构型模式此前会完全忽略时长指纹，现已改为补充判据
	structural := rawMode == "sequence" || rawMode == "blockcut" || rawMode == "shortblock" || rawMode == "slicebatch"
	modeHint := ""
	if !structural {
		modeHint = "当前检测方式以时长指纹 / 内容哈希为主判据，学习的指纹会直接生效。"
	} else if len(exist) == 0 {
		modeHint = fmt.Sprintf("当前检测方式「%s」为主判据，站点暂无时长指纹；写入后指纹作为补充判据参与剔除（主检测漏判时兜底）。", rawMode)
	} else {
		modeHint = fmt.Sprintf("当前检测方式「%s」为主判据，已有 %d 组时长指纹现作为补充判据参与剔除（此前被完全忽略，这是指纹失效的根因）。", rawMode, len(exist))
	}

	writeJSON(w, http.StatusOK, map[string]interface{}{"code": 200, "data": map[string]interface{}{
		"site_id":     body.SiteID,
		"site_name":   siteName,
		"stats":       stats,
		"spans":       spans,
		"new_fps":     add,
		"dup_fps":     dup,
		"exist_fps":   len(exist),
		"exist_list":  exist,
		"remove_fps":  remove,
		"keep_fps":    keepAdd,
		"keep_exist":  keeps,
		"low_distinct": lowDistinct,
		"tolerance":   tol,
		"detect_mode": siteDetectMode(cfg, body.SiteID),
		"mode_raw":    rawMode,
		"structural":  structural,
		"mode_hint":   modeHint,
	}})
}

// evalFPDistinct 评估广告指纹与正片时长的同构程度：
// 统计指纹各时长项在「非广告标记」片段中出现 ≥3 次的项，返回这些公共时长项与占比。
// ratio 越高说明指纹与正片越同构，仅按时长滑动匹配的误删风险越大。
func evalFPDistinct(fp []float64, nonAdDurs []float64) (common []float64, ratio float64) {
	if len(fp) == 0 || len(nonAdDurs) == 0 {
		return nil, 0
	}
	count := map[int64]int{}
	for _, d := range nonAdDurs {
		count[int64(math.Round(d*100))]++
	}
	common = []float64{}
	for _, v := range fp {
		if c := count[int64(math.Round(v*100))]; c >= 3 {
			common = append(common, v)
		}
	}
	ratio = float64(len(common)) / float64(len(fp))
	return common, ratio
}

func siteDetectMode(cfg config.File, siteID string) string {
	for _, st := range cfg.Sites {
		if st.ID == siteID {
			if st.DetectMode == "" {
				return "fingerprint(默认)"
			}
			return st.DetectMode
		}
	}
	return ""
}

// handleLearnHash 抓取标记段的采样内容，生成候选内容哈希组
func (s *Server) handleLearnHash(w http.ResponseWriter, r *http.Request) {
	var body struct {
		Segs       []store.MarkSeg `json:"segs"`
		SampleSize int             `json:"sample_size"`
	}
	_ = json.NewDecoder(r.Body).Decode(&body)
	if len(body.Segs) == 0 {
		writeJSON(w, http.StatusOK, map[string]interface{}{"code": 403, "msg": "没有标记片段"})
		return
	}
	p := s.parser_()
	size := body.SampleSize
	if size <= 0 {
		size = p.Settings.AdsHashSampleSize
	}
	if size <= 0 {
		size = 262144
	}
	group, failed := core.BuildHashGroup(p.Client, learnSegs(body.Segs), p.UA, size, time.Duration(p.Settings.FetchTimeout)*time.Second)
	if len(group) == 0 {
		writeJSON(w, http.StatusOK, map[string]interface{}{"code": 403, "msg": "采样全部失败，源站可能不可达或限速"})
		return
	}
	writeJSON(w, http.StatusOK, map[string]interface{}{"code": 200, "data": map[string]interface{}{
		"sample_size": size,
		"group":       group,
		"failed":      failed,
		"count":       len(group),
	}})
}

// handleLearnApply 把候选指纹 / 哈希写回站点规则
func (s *Server) handleLearnApply(w http.ResponseWriter, r *http.Request) {
	var body struct {
		SiteID       string           `json:"site_id"`
		Fingerprints [][]float64      `json:"fingerprints"`
		Hashes       [][]core.HashSeg `json:"hashes"`
		Note         string           `json:"note"`
		// 联动项：一并更新站点检测方式 / 移除造成误判的指纹 / 写入保护（正片）指纹
		DetectMode         string      `json:"detect_mode"`
		RemoveFingerprints [][]float64 `json:"remove_fingerprints"`
		KeepFingerprints   [][]float64 `json:"keep_fingerprints"`
	}
	_ = json.NewDecoder(r.Body).Decode(&body)
	if body.SiteID == "" {
		writeJSON(w, http.StatusOK, map[string]interface{}{"code": 403, "msg": "缺少 site_id（未匹配站点的解析结果无法回写规则）"})
		return
	}
	if len(body.Fingerprints) == 0 && len(body.Hashes) == 0 &&
		len(body.RemoveFingerprints) == 0 && len(body.KeepFingerprints) == 0 &&
		strings.TrimSpace(body.DetectMode) == "" {
		writeJSON(w, http.StatusOK, map[string]interface{}{"code": 403, "msg": "没有可写入的内容"})
		return
	}

	// 写前备份，写错可回滚
	bak := ""
	if b, err := os.ReadFile(filepath.Join(s.dataDir, "rules.json")); err == nil {
		bak = filepath.Join(s.dataDir, "rules.json.bak_"+time.Now().Format("20060102_150405"))
		_ = os.WriteFile(bak, b, 0644)
	}

	addedFP, addedHash, removedFP, addedKeep := 0, 0, 0, 0
	newMode := strings.ToLower(strings.TrimSpace(body.DetectMode))
	modeChanged := false
	err := s.cfgMgr.Update(func(f *config.File) error {
		for i := range f.Sites {
			if f.Sites[i].ID != body.SiteID {
				continue
			}
			st := &f.Sites[i]

			tol := f.Settings.MatchTolerance
			if tol <= 0 {
				tol = 0.05
			}

			// 1) 检测方式联动：写入规则时一并切换 detect_mode
			if newMode != "" && newMode != strings.ToLower(strings.TrimSpace(st.DetectMode)) {
				st.DetectMode = newMode
				modeChanged = true
			}

			// 2) 移除造成误判的既有广告指纹
			if len(body.RemoveFingerprints) > 0 {
				if len(st.Fingerprint) > 0 && core.FingerprintExists(body.RemoveFingerprints, st.Fingerprint, tol) {
					st.Fingerprint = nil
					removedFP++
				}
				kept := make([][]float64, 0, len(st.Fingerprints))
				for _, e := range st.Fingerprints {
					if core.FingerprintExists(body.RemoveFingerprints, e, tol) {
						removedFP++
						continue
					}
					kept = append(kept, e)
				}
				st.Fingerprints = kept
			}

			exist := st.AdsFPList()

			// 3) 新增广告指纹
			for _, fp := range body.Fingerprints {
				if len(fp) == 0 || core.FingerprintExists(exist, fp, tol) {
					continue
				}
				// 单组兼容字段留空时优先写入 fingerprint，否则追加到 fingerprints
				if len(st.Fingerprint) == 0 && len(st.Fingerprints) == 0 {
					st.Fingerprint = fp
				} else {
					st.Fingerprints = append(st.Fingerprints, fp)
				}
				exist = append(exist, fp)
				addedFP++
			}

			// 4) 新增保护（正片）指纹：命中即禁止剔除，用于修正正片被误删
			keeps := st.KeepFPList()
			for _, fp := range body.KeepFingerprints {
				if len(fp) == 0 || core.FingerprintExists(keeps, fp, tol) {
					continue
				}
				st.KeepFingerprints = append(st.KeepFingerprints, fp)
				keeps = append(keeps, fp)
				addedKeep++
			}

			for _, g := range body.Hashes {
				if len(g) == 0 {
					continue
				}
				st.AdsHashes = append(st.AdsHashes, g)
				addedHash++
			}

			if addedFP > 0 || addedHash > 0 || removedFP > 0 || addedKeep > 0 || modeChanged {
				st.Remark = trimRemark(st.Remark)
				if body.Note != "" {
					st.Remark = joinRemark(st.Remark, "标记学习:"+body.Note)
				}
			}
			return nil
		}
		return fmt.Errorf("站点不存在: %s", body.SiteID)
	})
	if err != nil {
		writeJSON(w, http.StatusOK, map[string]interface{}{"code": 403, "msg": err.Error()})
		return
	}
	if addedFP == 0 && addedHash == 0 && removedFP == 0 && addedKeep == 0 && !modeChanged {
		writeJSON(w, http.StatusOK, map[string]interface{}{"code": 200, "msg": "规则中已存在相同指纹，无需重复写入"})
		return
	}

	s.reload() // 重建解析器，使新规则立即生效
	s.logger.Write("INFO", "admin", fmt.Sprintf("标记学习写入规则 %s：指纹+%d/-%d 保护%d 哈希%d 检测方式=%s",
		body.SiteID, addedFP, removedFP, addedKeep, addedHash, newMode))

	msg := fmt.Sprintf("已写入：指纹 +%d/-%d 组、保护指纹 %d 组、哈希 %d 组", addedFP, removedFP, addedKeep, addedHash)
	if modeChanged {
		msg += "，检测方式已切换为 " + newMode
	}
	writeJSON(w, http.StatusOK, map[string]interface{}{"code": 200,
		"msg": msg + fmt.Sprintf("（规则已备份至 %s）", filepath.Base(bak)),
		"data": map[string]interface{}{
			"fingerprints": addedFP, "removed": removedFP, "keeps": addedKeep,
			"hashes": addedHash, "detect_mode": newMode, "backup": bak,
		}})
}

func trimRemark(s string) string {
	if len(s) > 200 {
		return s[:200]
	}
	return s
}

func joinRemark(old, add string) string {
	if old == "" {
		return add
	}
	if len(old)+len(add)+1 > 220 {
		return add
	}
	return old + "；" + add
}
