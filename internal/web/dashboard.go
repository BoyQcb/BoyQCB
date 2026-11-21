package web

import (
	"net/http"
	"time"

	"dytt_parser_go/internal/config"
	"dytt_parser_go/internal/store"
)

type trendPoint struct {
	Date  string `json:"date"`
	Total int    `json:"total"`
	OK    int    `json:"ok"`
}

type pieItem struct {
	Name  string `json:"name"`
	Value int    `json:"value"`
}

// handleDashboard 控制面板数据（统计卡片 + 7 天趋势 + 站点占比 + 系统概况 + 最近记录）
func (s *Server) handleDashboard(w http.ResponseWriter, r *http.Request) {
	cfg := s.cfgMgr.Snapshot()
	all := s.recs.All()
	now := time.Now()
	today := now.Format("2006-01-02")

	// 最近 7 天骨架
	daily := map[string]*trendPoint{}
	trend := make([]trendPoint, 0, 7)
	for i := 6; i >= 0; i-- {
		d := now.AddDate(0, 0, -i).Format("2006-01-02")
		p := &trendPoint{Date: d}
		daily[d] = p
		trend = append(trend, *p)
	}

	todayTotal, todayOK := 0, 0
	active := map[string]bool{}
	pieMap := map[string]int{}
	for _, rec := range all {
		d := ""
		if len(rec.Time) >= 10 {
			d = rec.Time[:10]
		}
		if tp, ok := daily[d]; ok {
			tp.Total++
			if rec.Code == 200 {
				tp.OK++
			}
		}
		if d == today {
			todayTotal++
			if rec.Code == 200 {
				todayOK++
				if rec.Site != "" {
					active[rec.Site] = true
				}
				pieMap[rec.Site]++
			}
		}
	}
	// 回写趋势
	for i := range trend {
		if tp, ok := daily[trend[i].Date]; ok {
			trend[i] = *tp
		}
	}

	pie := make([]pieItem, 0, len(pieMap))
	for name, v := range pieMap {
		pie = append(pie, pieItem{Name: name, Value: v})
	}

	rate := 0.0
	if todayTotal > 0 {
		rate = float64(todayOK) / float64(todayTotal) * 100
	}

	writeJSON(w, http.StatusOK, map[string]interface{}{
		"code": 200,
		"data": map[string]interface{}{
			"today_total":  todayTotal,
			"today_ok":     todayOK,
			"success_rate": rate,
			"active_sites": len(active),
			"trend":        trend,
			"site_pie":     pie,
			"recent":       s.recs.Recent(15),
			"system":       sysInfo(s, &cfg),
			"settings":     cfg.Settings,
			"server_time":  now.Format("2006-01-02 15:04:05"),
		},
	})
}

// ensure store referenced (avoid unused import on refactor)
var _ = store.Record{}
var _ = config.File{}
