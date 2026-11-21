package main

import (
	"embed"
	"flag"
	"fmt"
	"io/fs"
	"log"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"time"

	"dytt_parser_go/internal/config"
	"dytt_parser_go/internal/core"
	"dytt_parser_go/internal/store"
	"dytt_parser_go/internal/web"
)

//go:embed all:static
var staticFS embed.FS

func main() {
	var (
		listen    = flag.String("addr", ":8091", "监听地址，如 :8080")
		staticDir = flag.String("static", "", "外部静态资源目录（默认使用内置资源）")
		noBrowser = flag.Bool("nobrowser", false, "启动后不自动打开浏览器")
		cmd       = flag.String("cmd", "", "命令行模式：version / clean / clear-cache")
		url       = flag.String("url", "", "-cmd=clean 时待解析的 m3u8 地址")
		clearMode = flag.String("clear", "expired", "-cmd=clear-cache 时清理模式：all / expired / days=N")
	)
	flag.Parse()

	rootDir := exeDir()
	dataDir := filepath.Join(rootDir, "data")
	cacheDir := filepath.Join(rootDir, "cache")
	logDir := filepath.Join(rootDir, "logs")

	_ = os.MkdirAll(dataDir, 0755)
	_ = os.MkdirAll(cacheDir, 0755)
	_ = os.MkdirAll(logDir, 0755)
	hardenDataDir(dataDir)

	// 片段采样哈希缓存持久化：重启后广告段哈希直接复用，省掉重复采样下载
	core.InitSegHashCache(dataDir)
	defer core.FlushSegHashCache()

	// 配置
	cfgMgr, err := config.New(filepath.Join(dataDir, "rules.json"))
	if err != nil {
		log.Fatalf("初始化配置失败: %v", err)
	}
	recs := store.NewRecords(filepath.Join(dataDir, "records.json"), 2000)

	// 命令行模式
	switch *cmd {
	case "version":
		fmt.Printf("dytt_parser_go v%s (go %s)\n", core.Version, runtime.Version())
		return
	case "clean":
		runClean(cfgMgr, cacheDir, *url)
		return
	case "clear-cache":
		runClearCache(cfgMgr, cacheDir, *clearMode)
		return
	}

	// Web 模式
	var sfs fs.FS
	if *staticDir != "" {
		sfs = os.DirFS(*staticDir)
		fmt.Printf("[静态资源] 使用外部目录: %s\n", *staticDir)
	} else {
		sub, err := fs.Sub(staticFS, "static")
		if err != nil {
			log.Fatalf("加载内置静态资源失败: %v", err)
		}
		sfs = sub
	}

	srv := web.NewServer(cfgMgr, recs, rootDir, cacheDir, logDir, *listen, "", sfs)

	httpSrv := &http.Server{
		Addr:              *listen,
		Handler:           srv.Handler(),
		ReadHeaderTimeout: 10 * time.Second,
	}

	adminEntry := strings.TrimRight(cfgMgr.Snapshot().Settings.AdminPath, "/")
	if adminEntry == "" {
		adminEntry = "/admin"
	}

	fmt.Println("==========================================")
	fmt.Printf("  M3U8 广告过滤后台  v%s\n", core.Version)
	fmt.Printf("  后台地址:     http://127.0.0.1%s%s/\n", *listen, adminEntry)
	fmt.Printf("  后台登录:     默认密码 admin123\n")
	fmt.Printf("  接口示例:     http://127.0.0.1%s/api/clean?url=<m3u8地址>\n", *listen)
	fmt.Printf("  数据目录:     %s\n", dataDir)
	fmt.Println("==========================================")

	if !*noBrowser {
		go openBrowser("http://127.0.0.1" + *listen + adminEntry + "/")
	}

	if err := httpSrv.ListenAndServe(); err != nil {
		log.Fatalf("服务启动失败: %v", err)
	}
}

// runClean CLI 单次解析
func runClean(cfgMgr *config.Manager, cacheDir, link string) {
	link = strings.TrimSpace(link)
	if link == "" {
		fmt.Println("缺少 -url 参数")
		return
	}
	cfg := cfgMgr.Snapshot()
	p := core.NewParser(&cfg.Settings, cfg.Sites, &cfg.AdsLib, cacheDir)
	base := "http://local/clean"
	resp, _ := p.Clean(link, base)
	fmt.Printf("%s %s origin=%s url=%s\n", resp.Msg, resp.Link, resp.Origin, resp.URL)
	if resp.Code != 200 {
		os.Exit(1)
	}
}

// runClearCache CLI 清理缓存
func runClearCache(cfgMgr *config.Manager, cacheDir, mode string) {
	cfg := cfgMgr.Snapshot()
	p := core.NewParser(&cfg.Settings, cfg.Sites, &cfg.AdsLib, cacheDir)
	var n int
	switch {
	case mode == "all":
		n = p.Cache.CleanByDays(0)
	case strings.HasPrefix(mode, "days="):
		var d int
		fmt.Sscanf(strings.TrimPrefix(mode, "days="), "%d", &d)
		n = p.Cache.CleanByDays(d)
	default:
		n = p.Cache.CleanExpired()
	}
	fmt.Printf("已清理 %d 个缓存文件 (cache 目录: %s)\n", n, cacheDir)
}

// hardenDataDir 加固数据目录：收紧目录权限（仅 Unix 生效）+ 每次启动写入 .htaccess，
// 避免程序被部署在 Apache / 宝塔 Apache 站点根下时 rules.json 等配置被直接下载。
const dataHtaccess = `# data 目录伪装成 404：不返回 403、不列目录，让探测者无法判断该目录是否存在
# 兼容 Apache 2.2 / 2.4；Windows 下写入无害（其他 Web 服务器忽略此文件）
<IfModule mod_alias.c>
  RedirectMatch 404 ^/data(/|$)
</IfModule>
<IfModule mod_rewrite.c>
  RewriteEngine On
  RewriteRule ^data(/|$) - [R=404,L]
</IfModule>
`

func hardenDataDir(dataDir string) {
	if runtime.GOOS != "windows" {
		if err := os.Chmod(dataDir, 0700); err != nil {
			fmt.Printf("[安全] 收紧 data 目录权限失败: %v\n", err)
		}
	}
	if err := os.WriteFile(filepath.Join(dataDir, ".htaccess"), []byte(dataHtaccess), 0644); err != nil {
		fmt.Printf("[安全] 写入 data/.htaccess 失败: %v\n", err)
	}
}

func exeDir() string {
	if p, err := os.Executable(); err == nil {
		if d, err2 := filepath.Abs(filepath.Dir(p)); err2 == nil {
			return d
		}
	}
	if d, err := os.Getwd(); err == nil {
		return d
	}
	return "."
}

func openBrowser(url string) {
	var cmd *exec.Cmd
	switch runtime.GOOS {
	case "windows":
		cmd = exec.Command("rundll32", "url.dll,FileProtocolHandler", url)
	case "darwin":
		cmd = exec.Command("open", url)
	default:
		cmd = exec.Command("xdg-open", url)
	}
	_ = cmd.Start()
}
