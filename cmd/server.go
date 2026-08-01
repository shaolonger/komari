package cmd

import (
	"context"
	"fmt"
	"log"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/komari-monitor/komari/api"
	"github.com/komari-monitor/komari/api/admin"
	"github.com/komari-monitor/komari/api/admin/clipboard"
	log_api "github.com/komari-monitor/komari/api/admin/log"
	"github.com/komari-monitor/komari/api/admin/notification"
	"github.com/komari-monitor/komari/api/admin/test"
	"github.com/komari-monitor/komari/api/admin/update"
	"github.com/komari-monitor/komari/api/client"
	"github.com/komari-monitor/komari/api/jsonRpc"
	public_api "github.com/komari-monitor/komari/api/public"
	"github.com/komari-monitor/komari/api/terminal"
	"github.com/komari-monitor/komari/cmd/flags"

	"github.com/komari-monitor/komari/config"
	"github.com/komari-monitor/komari/database"
	"github.com/komari-monitor/komari/database/accounts"
	"github.com/komari-monitor/komari/database/auditlog"
	d_notification "github.com/komari-monitor/komari/database/notification"
	"github.com/komari-monitor/komari/database/records"
	"github.com/komari-monitor/komari/database/tasks"
	"github.com/komari-monitor/komari/internal/runtimeprofile"
	"github.com/komari-monitor/komari/internal/scheduler"
	"github.com/komari-monitor/komari/internal/storage"
	"github.com/komari-monitor/komari/public"
	"github.com/komari-monitor/komari/utils"
	"github.com/komari-monitor/komari/utils/cloudflared"
	"github.com/komari-monitor/komari/utils/geoip"
	logutil "github.com/komari-monitor/komari/utils/log"
	"github.com/komari-monitor/komari/utils/messageSender"
	"github.com/komari-monitor/komari/utils/notifier"
	"github.com/komari-monitor/komari/utils/oauth"
	"github.com/komari-monitor/komari/ws"
	"github.com/spf13/cobra"
)

var (
	DynamicCorsEnabled bool = false
)

var ServerCmd = &cobra.Command{
	Use:   "server",
	Short: "Start the server",
	Long:  `Start the server`,
	Run: func(cmd *cobra.Command, args []string) {
		RunServer()
	},
}

func init() {
	// 从环境变量获取监听地址
	listenAddr := GetEnv("KOMARI_LISTEN", "0.0.0.0:25774")
	ServerCmd.PersistentFlags().StringVarP(&flags.Listen, "listen", "l", listenAddr, "监听地址 [env: KOMARI_LISTEN]")
	ServerCmd.PersistentFlags().BoolVar(&flags.Diagnostics, "diagnostics", GetEnv("KOMARI_DIAGNOSTICS", "false") == "true", "启用受管理员认证保护的 pprof/trace [env: KOMARI_DIAGNOSTICS]")
	RootCmd.AddCommand(ServerCmd)
}

func RunServer() {
	// #region 初始化
	if err := os.MkdirAll("./data/theme", os.ModePerm); err != nil {
		log.Fatalf("Failed to create theme directory: %v", err)
	}
	InitDatabase()
	if utils.VersionHash != "unknown" {
		gin.SetMode(gin.ReleaseMode)
	}
	conf, err := config.GetManyAs[config.Legacy]()
	if err != nil {
		log.Fatal(err)
	}
	go geoip.InitGeoIp()
	scheduledCtx, scheduledCancel := context.WithCancel(context.Background())
	go DoScheduledWorkContext(scheduledCtx)
	go messageSender.Initialize()
	// oidcInit
	go oauth.Initialize()

	if conf.NezhaCompatEnabled {
		go func() {
			if err := StartNezhaCompat(conf.NezhaCompatListen); err != nil {
				log.Printf("Nezha compat server error: %v", err)
				auditlog.EventLog("error", fmt.Sprintf("Nezha compat server error: %v", err))
			}
		}()
	}

	config.Subscribe(func(event config.ConfigEvent) {
		if ok, t := config.IsChangedT[string](event, config.OAuthProviderKey); ok {
			if t == "" || t == "none" {
				t = "github"
			}
			oidcProvider, err := database.GetOidcConfigByName(t)
			if err != nil {
				log.Printf("Failed to get OIDC provider config: %v", err)
			} else {
				log.Printf("Using %s as OIDC provider", oidcProvider.Name)
			}
			err = oauth.LoadProvider(oidcProvider.Name, oidcProvider.Addition)
			if err != nil {
				auditlog.EventLog("error", fmt.Sprintf("Failed to load OIDC provider: %v", err))
			}
		}

		if ok, t := config.IsChangedT[bool](event, config.NezhaCompatEnabledKey); ok {
			if t {
				l, _ := config.GetAs[string](config.NezhaCompatListenKey)
				if err := StartNezhaCompat(l); err != nil {
					log.Printf("start Nezha compat server error: %v", err)
					auditlog.EventLog("error", fmt.Sprintf("start Nezha compat server error: %v", err))
				}
			} else {
				if err := StopNezhaCompat(); err != nil {
					log.Printf("stop Nezha compat server error: %v", err)
					auditlog.EventLog("error", fmt.Sprintf("stop Nezha compat server error: %v", err))
				}
			}
		}

	})
	// 初始化 cloudflared
	if err := cloudflared.AutoStart(GetEnv("KOMARI_CLOUDFLARED_TOKEN", "")); err != nil {
		log.Printf("failed to auto start cloudflared: %v", err)
	}

	r := gin.New()
	r.Use(logutil.GinLogger())
	r.Use(logutil.GinRecovery())
	r.Use(utils.EnforceHTTPSMiddleware())

	// 动态 CORS 中间件

	DynamicCorsEnabled = conf.AllowCors
	config.Subscribe(func(event config.ConfigEvent) {
		if ok, t := config.IsChangedT[bool](event, config.AllowCorsKey); ok {
			DynamicCorsEnabled = t
		}
		if event.IsChanged(config.GeoIpProviderKey) || event.IsChanged(config.GeoIpEnabledKey) {
			go geoip.InitGeoIp()
		}

		if event.IsChanged(config.NotificationMethodKey) {
			go messageSender.Initialize()
		}

	})
	r.Use(func(c *gin.Context) {
		if DynamicCorsEnabled {
			c.Header("Access-Control-Allow-Origin", "*")
			c.Header("Access-Control-Allow-Methods", "GET, POST, PUT, PATCH, DELETE, HEAD, OPTIONS")
			c.Header("Access-Control-Allow-Headers", "Origin, Content-Length, Content-Type, Authorization, Accept, X-CSRF-Token, X-Requested-With, Set-Cookie")
			c.Header("Access-Control-Expose-Headers", "Content-Length, Authorization, Set-Cookie")
			c.Header("Access-Control-Allow-Credentials", "true")
			c.Header("Access-Control-Max-Age", "43200") // 12 hours
			if c.Request.Method == "OPTIONS" {
				c.AbortWithStatus(204)
				return
			}
		}
		c.Next()
	})

	r.Use(api.IdentityMiddleware())
	r.Use(api.PrivateSiteMiddleware())

	r.Use(func(c *gin.Context) {
		if len(c.Request.URL.Path) >= 4 && c.Request.URL.Path[:4] == "/api" {
			c.Header("Cache-Control", "no-store")
		}
		c.Next()
	})

	r.Any("/ping", func(c *gin.Context) {
		c.String(200, "pong")
	})
	r.GET("/admin/notification/fleet-report-settings", api.RequireRole(api.RoleAdmin), notification.FleetReportSettingsPage)
	// #region 公开路由
	r.POST("/api/login", public_api.Login)
	r.GET("/api/me", public_api.GetMe)
	r.GET("/api/clients", api.GetClients)
	r.GET("/api/nodes", public_api.GetNodesInformation)
	r.GET("/api/public", public_api.GetPublicSettings)
	r.GET("/api/public/asset-fx", public_api.GetAssetFxSnapshot)
	r.GET("/api/oauth", public_api.OAuth)
	r.GET("/api/oauth_callback", public_api.OAuthCallback)
	r.GET("/api/logout", public_api.Logout)
	r.GET("/api/version", public_api.GetVersion)
	r.GET("/api/recent/:uuid", public_api.GetClientRecentRecords)

	r.GET("/api/records/load", public_api.GetRecordsByUUID)
	r.GET("/api/records/ping", public_api.GetPingRecords)
	r.GET("/api/traffic/range", public_api.GetTrafficRange)
	r.GET("/api/task/ping", public_api.GetPublicPingTasks)
	r.GET("/api/rpc2", jsonRpc.OnRpcRequest)
	r.POST("/api/rpc2", jsonRpc.OnRpcRequest)
	r.GET("/api/mjpeg_live", public_api.MjpegLiveHandler)
	// #region Agent
	r.POST("/api/clients/register", client.RegisterClient)
	tokenAuthrized := r.Group("/api/clients", api.RequireRole(api.RoleAdmin, api.RoleClient))
	{
		tokenAuthrized.GET("/report", client.WebSocketReport) // websocket
		tokenAuthrized.POST("/uploadBasicInfo", client.UploadBasicInfo)
		tokenAuthrized.POST("/report", client.UploadReport)
		tokenAuthrized.GET("/terminal", terminal.EstablishConnection)
		tokenAuthrized.POST("/task/result", client.TaskResult)
		tokenAuthrized.GET("/ping/tasks", client.GetPingTasks)
		tokenAuthrized.POST("/ping/result", client.UploadPingResult)
	}
	// #region 管理员
	adminAuthrized := r.Group("/api/admin", api.RequireRole(api.RoleAdmin))
	{
		admin.RegisterDiagnostics(adminAuthrized, flags.Diagnostics)
		adminAuthrized.GET("/download/backup", admin.DownloadBackup)
		adminAuthrized.POST("/upload/backup", admin.UploadBackup)
		// test
		testGroup := adminAuthrized.Group("/test")
		{
			testGroup.GET("/geoip", test.TestGeoIp)
			testGroup.POST("/sendMessage", test.TestSendMessage)
		}
		// update
		updateGroup := adminAuthrized.Group("/update")
		{
			updateGroup.POST("/mmdb", update.UpdateMmdbGeoIP)
			updateGroup.POST("/user", update.UpdateUser)
			updateGroup.PUT("/favicon", update.UploadFavicon)
			updateGroup.POST("/favicon", update.DeleteFavicon)
		}
		// tasks
		taskGroup := adminAuthrized.Group("/task")
		{
			taskGroup.GET("/all", admin.GetTasks)
			taskGroup.POST("/exec", admin.Exec)
			taskGroup.GET("/:task_id", admin.GetTaskById)
			taskGroup.GET("/:task_id/result", admin.GetTaskResultsByTaskId)
			taskGroup.GET("/:task_id/result/:uuid", admin.GetSpecificTaskResult)
			taskGroup.GET("/client/:uuid", admin.GetTasksByClientId)
		}
		// settings
		settingsGroup := adminAuthrized.Group("/settings")
		{
			settingsGroup.GET("/", admin.GetSettings)
			settingsGroup.POST("/", admin.EditSettings)
			settingsGroup.POST("/oidc", admin.SetOidcProvider)
			settingsGroup.GET("/oidc", admin.GetOidcProvider)
			settingsGroup.POST("/message-sender", admin.SetMessageSenderProvider)
			settingsGroup.GET("/message-sender", admin.GetMessageSenderProvider)
			settingsGroup.GET("/cloudflared", admin.GetCloudflaredStatus)
			settingsGroup.POST("/cloudflared/start", admin.StartCloudflared)
			settingsGroup.POST("/cloudflared/stop", admin.StopCloudflared)
			settingsGroup.POST("/cloudflared/remove-token", admin.RemoveCloudflaredToken)
		}
		// themes
		themeGroup := adminAuthrized.Group("/theme")
		{
			themeGroup.PUT("/upload", admin.UploadTheme)
			themeGroup.GET("/list", admin.ListThemes)
			themeGroup.POST("/delete", admin.DeleteTheme)
			themeGroup.GET("/set", admin.SetTheme)
			themeGroup.POST("/update", admin.UpdateTheme)
			themeGroup.POST("/import", admin.ImportTheme)
			themeGroup.POST("/settings", admin.UpdateThemeSettings)
		}
		// clients
		clientGroup := adminAuthrized.Group("/client")
		{
			clientGroup.POST("/add", admin.AddClient)
			clientGroup.GET("/list", admin.ListClients)
			clientGroup.GET("/assets", admin.GetClientAssetInventory)
			clientGroup.GET("/asset-summary", admin.GetClientAssetSummary)
			clientGroup.GET("/asset-issues", admin.GetClientAssetIssues)
			clientGroup.POST("/asset-fx/refresh", admin.RefreshAssetFxSnapshot)
			clientGroup.POST("/batch-edit", admin.BatchEditClientAssets)
			clientGroup.GET("/facets", admin.ListClientHomeFacets)
			clientGroup.POST("/facets", admin.BatchUpdateClientHomeFacets)
			clientGroup.GET("/:uuid", admin.GetClient)
			clientGroup.POST("/:uuid/edit", admin.EditClient)
			clientGroup.GET("/:uuid/facets", admin.GetClientHomeFacets)
			clientGroup.POST("/:uuid/facets", admin.UpdateClientHomeFacets)
			clientGroup.POST("/:uuid/remove", admin.RemoveClient)
			clientGroup.GET("/:uuid/token", admin.GetClientToken)
			clientGroup.POST(":uuid/token/rotate", admin.RotateClientToken)
			clientGroup.POST(":uuid/token/reissue", admin.ReissueClientToken)
			clientGroup.POST(":uuid/token/revoke", admin.RevokeClientToken)
			clientGroup.POST("/order", admin.OrderWeight)
			// client terminal
			clientGroup.GET("/:uuid/terminal", terminal.RequestTerminal)
		}

		// records
		recordGroup := adminAuthrized.Group("/record")
		{
			recordGroup.POST("/clear", admin.ClearRecord)
			recordGroup.POST("/clear/all", admin.ClearAllRecords)
		}
		// oauth2
		oauth2Group := adminAuthrized.Group("/oauth2")
		{
			oauth2Group.GET("/bind", admin.BindingExternalAccount)
			oauth2Group.POST("/unbind", admin.UnbindExternalAccount)
		}
		sessionGroup := adminAuthrized.Group("/session")
		{
			sessionGroup.GET("/get", admin.GetSessions)
			sessionGroup.POST("/remove", admin.DeleteSession)
			sessionGroup.POST("/remove/all", admin.DeleteAllSession)
		}
		two_factorGroup := adminAuthrized.Group("/2fa")
		{
			two_factorGroup.GET("/generate", admin.Generate2FA)
			two_factorGroup.POST("/enable", admin.Enable2FA)
			two_factorGroup.POST("/disable", admin.Disable2FA)
		}
		adminAuthrized.GET("/logs", log_api.GetLogs)

		// clipboard
		clipboardGroup := adminAuthrized.Group("/clipboard")
		{
			clipboardGroup.GET("/:id", clipboard.GetClipboard)
			clipboardGroup.GET("", clipboard.ListClipboard)
			clipboardGroup.POST("", clipboard.CreateClipboard)
			clipboardGroup.POST("/:id", clipboard.UpdateClipboard)
			clipboardGroup.POST("/remove", clipboard.BatchDeleteClipboard)
			clipboardGroup.POST("/:id/remove", clipboard.DeleteClipboard)
		}

		notificationGroup := adminAuthrized.Group("/notification")
		{
			// offline notifications
			notificationGroup.GET("/offline", notification.ListOfflineNotifications)
			notificationGroup.POST("/offline/edit", notification.EditOfflineNotification)
			notificationGroup.POST("/offline/enable", notification.EnableOfflineNotification)
			notificationGroup.POST("/offline/disable", notification.DisableOfflineNotification)
			loadAlertGroup := notificationGroup.Group("/load")
			{
				loadAlertGroup.GET("/", notification.GetAllLoadNotifications)
				loadAlertGroup.POST("/add", notification.AddLoadNotification)
				loadAlertGroup.POST("/delete", notification.DeleteLoadNotification)
				loadAlertGroup.POST("/edit", notification.EditLoadNotification)
			}
			trafficReportGroup := notificationGroup.Group("/traffic-report")
			{
				trafficReportGroup.GET("", notification.ListTrafficReportNotifications)
				trafficReportGroup.GET("/", notification.ListTrafficReportNotifications)
				trafficReportGroup.POST("/edit", notification.EditTrafficReportNotifications)
			}
			fleetReportGroup := notificationGroup.Group("/fleet-report")
			{
				fleetReportGroup.GET("", notification.GetFleetReportNotification)
				fleetReportGroup.GET("/", notification.GetFleetReportNotification)
				fleetReportGroup.POST("/edit", notification.EditFleetReportNotification)
				fleetReportGroup.POST("/test", notification.TestFleetReportNotification)
			}
		}

		pingTaskGroup := adminAuthrized.Group("/ping")
		{
			pingTaskGroup.GET("/", admin.GetAllPingTasks)
			pingTaskGroup.POST("/add", admin.AddPingTask)
			pingTaskGroup.POST("/delete", admin.DeletePingTask)
			pingTaskGroup.POST("/edit", admin.EditPingTask)
			pingTaskGroup.POST("/order", admin.OrderPingTask)

		}

	}

	public.Static(r.Group("/"), func(handlers ...gin.HandlerFunc) {
		r.NoRoute(handlers...)
	})

	srv := newHTTPServer(flags.Listen, r, productionHTTPServerLimits())
	log.Printf("Starting server on %s ...", flags.Listen)
	go func() {
		if err := srv.ListenAndServe(); err != nil && err != http.ErrServerClosed {
			OnFatal(err)
			log.Fatalf("listen: %s\n", err)
		}
	}()
	quit := make(chan os.Signal, 1)
	signal.Notify(quit, os.Interrupt, syscall.SIGTERM)
	<-quit
	scheduledCancel()
	schedulerStopCtx, schedulerStopCancel := context.WithTimeout(context.Background(), 2*time.Second)
	if err := utils.StopPingSchedule(schedulerStopCtx); err != nil {
		log.Printf("Ping scheduler shutdown failed: %v", err)
	}
	if err := notifier.StopLoadNotificationSchedule(schedulerStopCtx); err != nil {
		log.Printf("Notification scheduler shutdown failed: %v", err)
	}
	schedulerStopCancel()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if err := srv.Shutdown(ctx); err != nil {
		log.Printf("HTTP server graceful shutdown failed: %v", err)
	}
	if err := StopNezhaCompat(); err != nil {
		log.Printf("Nezha compatibility server shutdown failed: %v", err)
	}
	ws.CloseAllAgentConnections()
	if err := api.FlushClientReports(); err != nil {
		log.Printf("Final telemetry flush failed: %v", err)
	}
	activityCtx, activityCancel := context.WithTimeout(context.Background(), 5*time.Second)
	if err := accounts.CloseSessionActivity(activityCtx); err != nil {
		log.Printf("Session activity drain failed: %v", err)
	}
	activityCancel()
	drainCtx, drainCancel := context.WithTimeout(context.Background(), 5*time.Second)
	if err := storage.Close(drainCtx); err != nil {
		log.Printf("Storage drain failed: %v", err)
	}
	drainCancel()
	OnShutdown()

}

func InitDatabase() {
	installStorageAdapters()
	// // 打印数据库类型和连接信息
	// if flags.DatabaseType == "mysql" {
	// 	log.Printf("使用 MySQL 数据库连接: %s@%s:%s/%s",
	// 		flags.DatabaseUser, flags.DatabaseHost, flags.DatabasePort, flags.DatabaseName)
	// 	log.Printf("环境变量配置: [KOMARI_DB_TYPE=%s] [KOMARI_DB_HOST=%s] [KOMARI_DB_PORT=%s] [KOMARI_DB_USER=%s] [KOMARI_DB_NAME=%s]",
	// 		os.Getenv("KOMARI_DB_TYPE"), os.Getenv("KOMARI_DB_HOST"), os.Getenv("KOMARI_DB_PORT"),
	// 		os.Getenv("KOMARI_DB_USER"), os.Getenv("KOMARI_DB_NAME"))
	// } else {
	// 	log.Printf("使用 SQLite 数据库文件: %s", flags.DatabaseFile)
	// 	log.Printf("环境变量配置: [KOMARI_DB_TYPE=%s] [KOMARI_DB_FILE=%s]",
	// 		os.Getenv("KOMARI_DB_TYPE"), os.Getenv("KOMARI_DB_FILE"))
	// }
	control, err := storage.RequireControl()
	if err != nil {
		panic(err)
	}
	hasUsers, err := control.HasUsers(context.Background())
	if err != nil {
		panic(fmt.Errorf("check control store users: %w", err))
	}
	if !hasUsers {
		user, passwd, err := accounts.CreateDefaultAdminAccount()
		if err != nil {
			panic(err)
		}
		if os.Getenv("ADMIN_PASSWORD") != "" {
			log.Printf("Default admin account created. Username: %s. Password was configured via ADMIN_PASSWORD environment variable.\n", user)
		} else {
			// P0-03 安全整改：防止初始管理员密码在控制台/启动日志/容器日志中明文暴露
			// 仅将其安全写入本地 0600 权限文件 ./data/init_password.txt，供操作者读取后自动销毁
			_ = os.MkdirAll("./data", 0700)
			filePath := "./data/init_password.txt"
			err = os.WriteFile(filePath, []byte(passwd), 0600)
			if err != nil {
				_ = accounts.DeleteAccountByUsername(user)
				log.Fatalf("Failed to persist the initial admin password to %s securely: %v. The default admin account has been rolled back; fix the path permissions or set ADMIN_PASSWORD before restarting.", filePath, err)
			} else {
				log.Printf("Default admin account created. Username: %s. Password has been securely written to local file %s to prevent log exposure. Retrieve it and delete the file.\n", user, filePath)
			}
		}
	}
}

// #region 定时任务
func DoScheduledWork() {
	DoScheduledWorkContext(context.Background())
}

func DoScheduledWorkContext(ctx context.Context) {
	if err := tasks.MigrateAllClientsExpansion(); err != nil {
		log.Println("Failed to migrate ping task all_clients expansion:", err)
	}
	tasks.ReloadPingSchedule()
	d_notification.ReloadLoadNotificationSchedule()
	retentionTicker := time.NewTicker(time.Minute * 30)
	minute := time.NewTicker(60 * time.Second)
	profile, err := runtimeprofile.Current()
	if err != nil {
		log.Printf("Failed to resolve compaction profile: %v", err)
		profile.CompactionInterval = 5 * time.Minute
		profile.CompactionBudget = 15 * time.Second
	}
	compactionTicker := time.NewTicker(profile.CompactionInterval)
	defer retentionTicker.Stop()
	defer minute.Stop()
	defer compactionTicker.Stop()
	go notifier.CheckExpireScheduledWorkContext(ctx)
	notificationEngine, _ := scheduler.New(scheduler.Config{Workers: 2, QueueCapacity: 16})
	go func() {
		_ = notificationEngine.Run(ctx, []scheduler.Task{
			{Key: "notification:traffic-threshold", Interval: time.Minute, Run: func(context.Context) { notifier.CheckTraffic() }},
			{Key: "notification:traffic-report", Interval: time.Minute, Run: func(context.Context) { notifier.CheckTrafficReportOnce(time.Now()) }},
			{Key: "notification:fleet-report", Interval: time.Minute, Run: func(context.Context) { notifier.CheckFleetReportOnce(time.Now()) }},
		})
	}()
	for {
		select {
		case <-ctx.Done():
			return
		case <-compactionTicker.C:
			if err := records.CompactRecordContext(ctx, profile.CompactionBudget); err != nil && ctx.Err() == nil {
				log.Printf("Telemetry compaction quantum failed: %v", err)
			}
		case <-retentionTicker.C:
			cfg, _ := config.GetManyAs[config.Legacy]()
			records.DeleteRecordBefore(time.Now().Add(-time.Hour * time.Duration(cfg.RecordPreserveTime)))
			tasks.ClearTaskResultsByTimeBefore(time.Now().Add(-time.Hour * time.Duration(cfg.RecordPreserveTime)))
			tasks.DeletePingRecordsBefore(time.Now().Add(-time.Hour * time.Duration(cfg.PingRecordPreserveTime)))
			auditlog.RemoveOldLogs()
			accounts.RemoveExpiredSessions()
		case <-minute.C:
			cfg, _ := config.GetManyAs[config.Legacy]()
			api.SaveClientReportToDB()
			if !cfg.RecordEnabled {
				records.DeleteAll()
				tasks.DeleteAllPingRecords()
			}
		}
	}

}

func OnShutdown() {
	auditlog.Log("", "", "server is shutting down", "info")
	cloudflared.Shutdown()
}

func OnFatal(err error) {
	auditlog.Log("", "", "server encountered a fatal error: "+err.Error(), "error")
	cloudflared.Shutdown()
}
