package web

import (
	"encoding/json"
	"errors"
	"net/http"
	"sort"
	"strconv"
	"strings"
	"time"

	"dytt_parser_go/internal/config"
	"dytt_parser_go/internal/core"
)

// handleSources 资源站列表 CRUD（GET 列表 / POST 新增 / PUT 修改 / DELETE 删除）
//
// 资源站 = 一个苹果 CMS 采集接口；域名发现页以「资源站」为维度管理，
// 每个资源站可直接扫描其采集接口并把未配置域名批量写入规则。
func (s *Server) handleSources(w http.ResponseWriter, r *http.Request) {
	switch r.Method {
	case http.MethodGet:
		cfg := s.cfgMgr.Snapshot()
		list := append([]config.Source{}, cfg.Sources...)
		if len(list) == 0 {
			list = config.PresetSourcesCopy()
		}
		sortSources(list)
		// 附带站点列表，供前端选择「关联站点」
		writeJSON(w, http.StatusOK, map[string]interface{}{
			"code": 200, "data": list,
			"sites": cfg.Sites, "presets": config.PresetSourcesCopy(),
		})
		return

	case http.MethodPost:
		var src config.Source
		if err := json.NewDecoder(r.Body).Decode(&src); err != nil {
			writeJSON(w, http.StatusOK, map[string]interface{}{"code": 403, "msg": "参数错误: " + err.Error()})
			return
		}
		src.Name = strings.TrimSpace(src.Name)
		src.APIURL = strings.TrimSpace(src.APIURL)
		if src.Name == "" {
			writeJSON(w, http.StatusOK, map[string]interface{}{"code": 403, "msg": "请填写资源站名称"})
			return
		}
		if src.APIURL == "" {
			writeJSON(w, http.StatusOK, map[string]interface{}{"code": 403, "msg": "请填写采集接口地址"})
			return
		}
		if err := s.cfgMgr.Update(func(c *config.File) error {
			for _, x := range c.Sources {
				if x.ID == src.ID {
					return errors.New("资源站 ID 已存在: " + src.ID)
				}
			}
			src.ID = srcID(src.Name, c.Sources)
			src.Preset = false
			if src.Sort == 0 {
				src.Sort = len(c.Sources) + 1
			}
			c.Sources = append(c.Sources, src)
			return nil
		}); err != nil {
			writeJSON(w, http.StatusOK, map[string]interface{}{"code": 500, "msg": "保存失败: " + err.Error()})
			return
		}
		s.logger.Write("INFO", "admin", "新增资源站 "+src.Name+" "+src.APIURL)
		writeJSON(w, http.StatusOK, map[string]interface{}{"code": 200, "msg": "已添加资源站", "data": src})
		return

	case http.MethodPut:
		var src config.Source
		if err := json.NewDecoder(r.Body).Decode(&src); err != nil {
			writeJSON(w, http.StatusOK, map[string]interface{}{"code": 403, "msg": "参数错误: " + err.Error()})
			return
		}
		if strings.TrimSpace(src.ID) == "" {
			writeJSON(w, http.StatusOK, map[string]interface{}{"code": 403, "msg": "缺少资源站 ID"})
			return
		}
		var updated *config.Source
		if err := s.cfgMgr.Update(func(c *config.File) error {
			for i := range c.Sources {
				if c.Sources[i].ID == src.ID {
					if strings.TrimSpace(src.Name) != "" {
						c.Sources[i].Name = strings.TrimSpace(src.Name)
					}
					if strings.TrimSpace(src.APIURL) != "" {
						c.Sources[i].APIURL = strings.TrimSpace(src.APIURL)
					}
					c.Sources[i].Kind = src.Kind
					c.Sources[i].SiteID = src.SiteID
					c.Sources[i].Pages = src.Pages
					c.Sources[i].Sort = src.Sort
					u := c.Sources[i]
					updated = &u
					return nil
				}
			}
			return errors.New("资源站不存在: " + src.ID)
		}); err != nil {
			writeJSON(w, http.StatusOK, map[string]interface{}{"code": 500, "msg": "保存失败: " + err.Error()})
			return
		}
		s.logger.Write("INFO", "admin", "修改资源站 "+src.ID)
		writeJSON(w, http.StatusOK, map[string]interface{}{"code": 200, "msg": "已保存", "data": updated})
		return

	case http.MethodDelete:
		id := strings.TrimSpace(r.URL.Query().Get("id"))
		if id == "" {
			writeJSON(w, http.StatusOK, map[string]interface{}{"code": 403, "msg": "缺少资源站 ID"})
			return
		}
		if err := s.cfgMgr.Update(func(c *config.File) error {
			out := make([]config.Source, 0, len(c.Sources))
			found := false
			for _, x := range c.Sources {
				if x.ID == id {
					found = true
					continue
				}
				out = append(out, x)
			}
			if !found {
				return errors.New("资源站不存在: " + id)
			}
			c.Sources = out
			return nil
		}); err != nil {
			writeJSON(w, http.StatusOK, map[string]interface{}{"code": 500, "msg": "删除失败: " + err.Error()})
			return
		}
		s.logger.Write("INFO", "admin", "删除资源站 "+id)
		writeJSON(w, http.StatusOK, map[string]interface{}{"code": 200, "msg": "已删除资源站"})
		return

	default:
		writeJSON(w, http.StatusOK, map[string]interface{}{"code": 403, "msg": "不支持的方法"})
		return
	}
}

// srcID 生成资源站 ID：英文名取 slug，中文等无 ASCII 字符时退化为 src+时间戳
func srcID(name string, exist []config.Source) string {
	id := core.SlugID(name)
	if id == "site" || id == "" {
		id = "src" + strconv.FormatInt(time.Now().UnixNano()%1e9, 36)
	}
	used := map[string]bool{}
	for _, x := range exist {
		used[x.ID] = true
	}
	if !used[id] {
		return id
	}
	for n := 2; ; n++ {
		cand := id + "-" + itoa(n)
		if !used[cand] {
			return cand
		}
	}
}

func sortSources(list []config.Source) {
	sort.SliceStable(list, func(i, j int) bool {
		if list[i].Sort != list[j].Sort {
			if list[i].Sort == 0 {
				return false
			}
			if list[j].Sort == 0 {
				return true
			}
			return list[i].Sort < list[j].Sort
		}
		return list[i].Name < list[j].Name
	})
}
