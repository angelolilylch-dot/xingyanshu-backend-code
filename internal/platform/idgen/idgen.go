// Package idgen 提供基于当前时间的单进程递增整数 ID，用于一期尚未接入分布式 ID 服务的内部主键。
package idgen

import (
	"sync/atomic"
	"time"
)

var lastID atomic.Uint64

// Next 返回递增的 64 位整数；同一进程内的并发调用不会重复。
func Next() uint64 {
	for {
		now := uint64(time.Now().UnixNano())
		last := lastID.Load()
		if now <= last {
			now = last + 1
		}
		if lastID.CompareAndSwap(last, now) {
			return now
		}
	}
}
