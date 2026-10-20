package app

import (
	"context"
	"fmt"
	"log"
	"time"

	"github.com/gin-gonic/gin"

	"xingyanshu-backend/internal/bootstrap"
	"xingyanshu-backend/internal/config"
	"xingyanshu-backend/internal/database"
	"xingyanshu-backend/internal/modules/activities"
	"xingyanshu-backend/internal/modules/activitybookings"
	"xingyanshu-backend/internal/modules/authorders"
	"xingyanshu-backend/internal/modules/authorizations"
	"xingyanshu-backend/internal/modules/avatar"
	"xingyanshu-backend/internal/modules/creators"
	"xingyanshu-backend/internal/modules/files"
	"xingyanshu-backend/internal/modules/foundation"
	"xingyanshu-backend/internal/modules/identity"
	"xingyanshu-backend/internal/modules/ips"
	"xingyanshu-backend/internal/modules/market"
	"xingyanshu-backend/internal/modules/payments"
	"xingyanshu-backend/internal/modules/plans"
	"xingyanshu-backend/internal/modules/productorders"
	"xingyanshu-backend/internal/modules/products"
	"xingyanshu-backend/internal/modules/publiccontent"
	"xingyanshu-backend/internal/modules/works"
	"xingyanshu-backend/internal/platform/cache"
	"xingyanshu-backend/internal/platform/contentmoderation"
	"xingyanshu-backend/internal/platform/evidence"
	"xingyanshu-backend/internal/platform/filestore"
	"xingyanshu-backend/internal/platform/httpx"
	"xingyanshu-backend/internal/platform/logx"
)

func RunServer(cfg config.Config, configPath string) error {
	gin.SetMode(gin.ReleaseMode)
	router := gin.New()
	// 所有路由先建立请求链路编号，再由统一恢复中间件返回标准错误结构。
	router.Use(httpx.CORSMiddleware(cfg.CORS), httpx.RequestIDMiddleware(), httpx.DiagnosticRequestMiddleware(cfg.App.Environment == "test"), httpx.AccessLogMiddleware(), httpx.RecoveryMiddleware())
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
	loginService := identity.NewLoginService(db, captchaService, redisClient, cfg.Auth, cfg.App.Environment)
	accountService := identity.NewAccountService(db)
	organizationService := identity.NewOrganizationService(db)
	foundationService := foundation.NewService(db)
	fileService := files.NewService(db, storage, filePolicy, cfg.File)
	evidenceService := evidence.NewService(db, evidence.NewProvider(cfg.Evidence.Provider), cfg.Evidence.CallbackSecret)
	planService := plans.NewService(db)
	ipService := ips.NewService(db, contentmoderation.NewProvider(cfg.ContentSafety), cfg.Auth.JWTSecret, evidenceService, storage, cfg.File)
	creatorService := creators.NewService(db, redisClient, cfg.Auth, cfg.App.Environment)
	authOrderService := authorders.NewService(db)
	paymentService := payments.NewService(db)
	productOrderService := productorders.NewService(db)
	activityBookingService := activitybookings.NewService(db)
	authorizationService := authorizations.NewService(db, fileService)
	workService := works.NewService(db, contentmoderation.NewProvider(cfg.ContentSafety), evidenceService)
	productService := products.NewService(db, contentmoderation.NewProvider(cfg.ContentSafety), storage)
	activityService := activities.NewService(db, contentmoderation.NewProvider(cfg.ContentSafety))
	avatarService := avatar.NewService(db, redisClient, cfg.Auth, cfg.App.Environment, storage)
	apiGroup := router.Group("/api/v1")
	identity.RegisterPublicRoutes(apiGroup, captchaService, loginService)
	identity.RegisterUnifiedRoutes(apiGroup, db, cfg.Auth, loginService)
	identity.RegisterConsumerRoutes(apiGroup, loginService, identity.RequireConsumerAuthentication(db, cfg.Auth))
	avatar.RegisterRoutes(apiGroup, avatarService, avatar.RequireAuthentication(db, cfg.Auth))
	avatar.RegisterAdminRoutes(apiGroup, avatarService, identity.RequireAdminAuthentication(db, cfg.Auth))
	productorders.RegisterRoutes(apiGroup, productOrderService, identity.RequireConsumerAuthentication(db, cfg.Auth), cfg.App.Environment)
	activitybookings.RegisterRoutes(apiGroup, activityBookingService, identity.RequireConsumerAuthentication(db, cfg.Auth), cfg.App.Environment)
	creators.RegisterRoutes(apiGroup, creatorService, loginService, fileService, creators.RequireAuthentication(db, cfg.Auth), identity.RequireAdminAuthentication(db, cfg.Auth))
	foundation.RegisterPublicRoutes(apiGroup, foundationService)
	identity.RegisterProtectedRoutes(apiGroup, loginService, accountService, organizationService, identity.RequireAdminAuthentication(db, cfg.Auth))
	foundation.RegisterProtectedRoutes(apiGroup, foundationService, identity.RequireAdminAuthentication(db, cfg.Auth))
	files.RegisterProtectedRoutes(apiGroup, fileService, identity.RequireAdminAuthentication(db, cfg.Auth))
	plans.RegisterRoutes(apiGroup, planService, identity.RequireAdminAuthentication(db, cfg.Auth))
	ips.RegisterRoutes(apiGroup, ipService, identity.RequireAdminAuthentication(db, cfg.Auth))
	market.RegisterRoutes(apiGroup, db, creators.RequireAuthentication(db, cfg.Auth))
	authorders.RegisterRoutes(apiGroup, authOrderService, creators.RequireAuthentication(db, cfg.Auth))
	payments.RegisterMockRoute(apiGroup, paymentService, creators.RequireAuthentication(db, cfg.Auth), cfg.App.Environment)
	authorizations.RegisterRoutes(apiGroup, authorizationService, creators.RequireAuthentication(db, cfg.Auth))
	works.RegisterRoutes(apiGroup, workService, creators.RequireAuthentication(db, cfg.Auth), identity.RequireAdminAuthentication(db, cfg.Auth))
	products.RegisterRoutes(apiGroup, productService, creators.RequireAuthentication(db, cfg.Auth), identity.RequireAdminAuthentication(db, cfg.Auth))
	activities.RegisterRoutes(apiGroup, activityService, creators.RequireAuthentication(db, cfg.Auth))
	publiccontent.RegisterRoutes(apiGroup, publiccontent.NewService(db, storage))
	evidence.RegisterCallbackRoute(apiGroup, evidenceService)

	log.Printf("server starting: environment=%s address=%s", cfg.App.Environment, cfg.App.Server.Address)
	return router.Run(cfg.App.Server.Address)
}

func RunWorker(cfg config.Config) error {
	db, err := database.OpenMySQLConfig(cfg.Database)
	if err != nil {
		return fmt.Errorf("open database: %w", err)
	}
	sqlDB, err := db.DB()
	if err != nil {
		return fmt.Errorf("get sql database: %w", err)
	}
	defer sqlDB.Close()
	storage, err := filestore.New(cfg.File.Storage)
	if err != nil {
		return fmt.Errorf("open file storage: %w", err)
	}
	fileService := files.NewService(db, storage, config.NewFilePolicyProvider(cfg.File.Policy), cfg.File)
	evidenceService := evidence.NewService(db, evidence.NewProvider(cfg.Evidence.Provider), cfg.Evidence.CallbackSecret)
	service := authorders.NewService(db)
	productOrderService := productorders.NewService(db)
	activityBookingService := activitybookings.NewService(db)
	activityService := activities.NewService(db, contentmoderation.NewProvider(cfg.ContentSafety))
	avatarService := avatar.NewService(db, nil, cfg.Auth, cfg.App.Environment, storage)
	contractProcessor := payments.NewContractProcessor(db, fileService, evidenceService)
	interval := time.Minute
	log.Printf("worker started: environment=%s interval=%s jobs=auth-order-timeout,product-order-timeout,activity-booking-timeout,activity-booking-expiry,contract-generation,evidence-retry,activity-end,avatar-quality,avatar-export", cfg.App.Environment, interval)
	for {
		if count, err := service.CloseExpired(context.Background()); err != nil {
			logx.Errorf("module=authorders action=close_expired stage=worker error_type=%T error=%q", err, logx.Redact(err.Error()))
			log.Printf("close expired auth orders failed: %v", err)
		} else if count > 0 {
			log.Printf("closed expired auth orders: count=%d", count)
		}
		if count, err := productOrderService.CloseExpired(context.Background()); err != nil {
			logx.Errorf("module=productorders action=close_expired stage=worker error_type=%T error=%q", err, logx.Redact(err.Error()))
			log.Printf("close expired product orders failed: %v", err)
		} else if count > 0 {
			log.Printf("closed expired product orders: count=%d", count)
		}
		if count, err := activityBookingService.CloseExpired(context.Background()); err != nil {
			logx.Errorf("module=activitybookings action=close_expired stage=worker error_type=%T error=%q", err, logx.Redact(err.Error()))
			log.Printf("close expired activity bookings failed: %v", err)
		} else if count > 0 {
			log.Printf("closed expired activity bookings: count=%d", count)
		}
		if count, err := activityBookingService.ExpireEnded(context.Background()); err != nil {
			logx.Errorf("module=activitybookings action=expire_ended stage=worker error_type=%T error=%q", err, logx.Redact(err.Error()))
			log.Printf("expire ended activity bookings failed: %v", err)
		} else if count > 0 {
			log.Printf("expired ended activity bookings: count=%d", count)
		}
		if count, err := contractProcessor.ProcessDueContracts(context.Background(), 20); err != nil {
			logx.Errorf("module=payments action=process_due_contracts stage=worker error_type=%T error=%q", err, logx.Redact(err.Error()))
			log.Printf("process authorization contracts failed: %v", err)
		} else if count > 0 {
			log.Printf("processed authorization contract tasks: count=%d", count)
		}
		if count, err := contractProcessor.RetryDueEvidence(context.Background(), 20); err != nil {
			logx.Errorf("module=payments action=retry_due_evidence stage=worker error_type=%T error=%q", err, logx.Redact(err.Error()))
			log.Printf("retry authorization contract evidence failed: %v", err)
		} else if count > 0 {
			log.Printf("retried authorization contract evidence: count=%d", count)
		}
		if count, err := activityService.CloseEnded(context.Background()); err != nil {
			logx.Errorf("module=activities action=close_ended stage=worker error_type=%T error=%q", err, logx.Redact(err.Error()))
			log.Printf("close ended activities failed: %v", err)
		} else if count > 0 {
			log.Printf("closed ended activities: count=%d", count)
		}
		if count, err := avatarService.ProcessDueQuality(context.Background(), 20); err != nil {
			logx.Errorf("module=avatar action=process_due_quality stage=worker error_type=%T error=%q", err, logx.Redact(err.Error()))
			log.Printf("process avatar quality tasks failed: %v", err)
		} else if count > 0 {
			log.Printf("processed avatar quality tasks: count=%d", count)
		}
		if count, err := avatarService.ProcessDueExports(context.Background(), 20); err != nil {
			logx.Errorf("module=avatar action=process_due_exports stage=worker error_type=%T error=%q", err, logx.Redact(err.Error()))
			log.Printf("process avatar export tasks failed: %v", err)
		} else if count > 0 {
			log.Printf("processed avatar export tasks: count=%d", count)
		}
		if count, err := avatarService.ProcessDueDeletion(context.Background(), 20); err != nil {
			logx.Errorf("module=avatar action=process_due_deletion stage=worker error_type=%T error=%q", err, logx.Redact(err.Error()))
			log.Printf("process avatar deletion tasks failed: %v", err)
		} else if count > 0 {
			log.Printf("processed avatar deletion tasks: count=%d", count)
		}
		time.Sleep(interval)
	}
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
