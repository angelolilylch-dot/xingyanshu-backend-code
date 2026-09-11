package app

import (
	"context"
	"fmt"
	"log"

	"github.com/gin-gonic/gin"

	"xingyanshu-backend/internal/bootstrap"
	"xingyanshu-backend/internal/config"
	"xingyanshu-backend/internal/database"
	"xingyanshu-backend/internal/modules/creators"
	"xingyanshu-backend/internal/modules/files"
	"xingyanshu-backend/internal/modules/foundation"
	"xingyanshu-backend/internal/modules/identity"
	"xingyanshu-backend/internal/modules/ips"
	"xingyanshu-backend/internal/modules/market"
	"xingyanshu-backend/internal/modules/plans"
	"xingyanshu-backend/internal/platform/cache"
	"xingyanshu-backend/internal/platform/contentmoderation"
	"xingyanshu-backend/internal/platform/evidence"
	"xingyanshu-backend/internal/platform/filestore"
	"xingyanshu-backend/internal/platform/httpx"
)

func RunServer(cfg config.Config, configPath string) error {
	gin.SetMode(gin.ReleaseMode)
	router := gin.New()
	// 所有路由先建立请求链路编号，再由统一恢复中间件返回标准错误结构。
	router.Use(httpx.CORSMiddleware(cfg.CORS), httpx.RequestIDMiddleware(), gin.Logger(), httpx.RecoveryMiddleware())
	redisClient, err := cache.OpenRedis(context.Background(), cfg.Redis)
	if err != nil {
		return fmt.Errorf("open redis: %w", err)
	}
	defer redisClient.Close()
	db, err := database.OpenMySQLConfig(cfg.Database)
	if err != nil {
		return fmt.Errorf("open database: %w", err)
	}
	storage, err := filestore.New(cfg.File.Storage)
	if err != nil {
		return fmt.Errorf("open COS file storage: %w", err)
	}
	filePolicy := config.NewFilePolicyProvider(cfg.File.Policy)
	watchContext, cancelWatch := context.WithCancel(context.Background())
	defer cancelWatch()
	config.WatchFilePolicy(watchContext, configPath, cfg.File.WatchInterval, filePolicy)
	captchaService := identity.NewCaptchaService(redisClient)
	loginService := identity.NewLoginService(db, captchaService, redisClient, cfg.Auth)
	accountService := identity.NewAccountService(db)
	organizationService := identity.NewOrganizationService(db)
	foundationService := foundation.NewService(db)
	fileService := files.NewService(db, storage, filePolicy, cfg.File)
	evidenceService := evidence.NewService(db, evidence.NewProvider(cfg.Evidence.Provider), cfg.Evidence.CallbackSecret)
	planService := plans.NewService(db)
	ipService := ips.NewService(db, contentmoderation.NewProvider(cfg.ContentSafety), cfg.Auth.JWTSecret, evidenceService)
	creatorService := creators.NewService(db, redisClient, cfg.Auth, cfg.App.Environment)
	apiGroup := router.Group("/api/v1")
	identity.RegisterPublicRoutes(apiGroup, captchaService, loginService)
	identity.RegisterUnifiedRoutes(apiGroup, db, cfg.Auth, loginService)
	creators.RegisterRoutes(apiGroup, creatorService, loginService, fileService, creators.RequireAuthentication(db, cfg.Auth), identity.RequireAdminAuthentication(db, cfg.Auth))
	foundation.RegisterPublicRoutes(apiGroup, foundationService)
	identity.RegisterProtectedRoutes(apiGroup, loginService, accountService, organizationService, identity.RequireAdminAuthentication(db, cfg.Auth))
	foundation.RegisterProtectedRoutes(apiGroup, foundationService, identity.RequireAdminAuthentication(db, cfg.Auth))
	files.RegisterProtectedRoutes(apiGroup, fileService, identity.RequireAdminAuthentication(db, cfg.Auth))
	plans.RegisterRoutes(apiGroup, planService, identity.RequireAdminAuthentication(db, cfg.Auth))
	ips.RegisterRoutes(apiGroup, ipService, identity.RequireAdminAuthentication(db, cfg.Auth))
	market.RegisterRoutes(apiGroup, db, creators.RequireAuthentication(db, cfg.Auth))
	evidence.RegisterCallbackRoute(apiGroup, evidenceService)

	log.Printf("server starting: environment=%s address=%s", cfg.App.Environment, cfg.App.Server.Address)
	return router.Run(cfg.App.Server.Address)
}

func RunWorker(cfg config.Config) error {
	log.Printf("worker initialized: environment=%s poll_interval=%s", cfg.App.Environment, cfg.App.Worker.PollInterval)
	return nil
}

// RunBootstrap 写入平台组织、预置角色权限和首个平台管理员。
func RunBootstrap(cfg config.Config) error {
	if cfg.Database.Driver != "mysql" {
		return fmt.Errorf("unsupported database driver: %s", cfg.Database.Driver)
	}

	db, err := database.OpenMySQLConfig(cfg.Database)
	if err != nil {
		return fmt.Errorf("open database: %w", err)
	}
	sqlDB, err := db.DB()
	if err != nil {
		return fmt.Errorf("get sql database: %w", err)
	}
	defer sqlDB.Close()
	if err := sqlDB.PingContext(context.Background()); err != nil {
		return fmt.Errorf("ping database: %w", err)
	}

	result, err := bootstrap.Run(context.Background(), db, cfg.Bootstrap)
	if err != nil {
		return err
	}
	log.Printf("bootstrap completed: platform_org_id=%d admin_user_id=%d created=%v", result.PlatformOrgID, result.AdminUserID, result.Created)
	return nil
}

// RunMigrate 连接配置数据库并执行所有未完成的 SQL 迁移。
func RunMigrate(cfg config.Config) error {
	if cfg.Database.Driver != "mysql" {
		return fmt.Errorf("unsupported database driver: %s", cfg.Database.Driver)
	}

	db, err := database.OpenMySQLConfig(cfg.Database)
	if err != nil {
		return fmt.Errorf("open database: %w", err)
	}
	sqlDB, err := db.DB()
	if err != nil {
		return fmt.Errorf("get sql database: %w", err)
	}
	defer sqlDB.Close()

	if err := sqlDB.PingContext(context.Background()); err != nil {
		return fmt.Errorf("ping database: %w", err)
	}
	// 连接成功后才执行迁移，避免把配置或网络错误误报为 SQL 执行失败。
	result, err := database.ApplyMigrations(context.Background(), db)
	if err != nil {
		return err
	}
	tableCount, err := database.TableCount(context.Background(), db, cfg.Database.Name)
	if err != nil {
		return err
	}

	log.Printf("migration completed: environment=%s database=%s:%d/%s applied=%v skipped=%v table_count=%d", cfg.App.Environment, cfg.Database.Host, cfg.Database.Port, cfg.Database.Name, result.AppliedVersions, result.SkippedVersions, tableCount)
	return nil
}
