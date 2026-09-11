// Package cache 提供 Redis 连接和短期键值访问。
package cache

import (
	"context"
	"fmt"
	"github.com/redis/go-redis/v9"
	"xingyanshu-backend/internal/config"
)

// OpenRedis 创建 Redis 客户端并验证连接。
func OpenRedis(ctx context.Context, cfg config.RedisConfig) (*redis.Client, error) {
	client := redis.NewClient(&redis.Options{Addr: cfg.Address, Password: cfg.Password, DB: cfg.Database})
	if err := client.Ping(ctx).Err(); err != nil {
		client.Close()
		return nil, fmt.Errorf("ping redis: %w", err)
	}
	return client, nil
}
