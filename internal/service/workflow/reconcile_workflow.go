package workflow

import (
	"log"
	"sync"
	"time"

	"github.com/qs-lzh/flash-sale/internal/cache"
	"github.com/qs-lzh/flash-sale/internal/service/domain"
)

// ReconcileWorkflow 是对账任务，消息可靠性的第三层（兜底）防线。
//
// 前两层（confirm+回滚、幂等消费+有限重试）覆盖了可预见的失败路径，但
// 分布式系统总有缝隙：confirm 后进程崩溃且延时消息也丢失、broker 数据
// 损坏、未知的 bug……对账任务刻意不依赖 MQ（它要兜底的场景可能正是
// MQ 本身坏了），而是定期 SCAN Redis 中的订单，直接调 domain service
// 补偿：
//
//	PAID    → 补写入库（CreateOrderFromReservation 内部幂等，已存在则 no-op）
//	RESERVED 且超龄 → 延时消息丢失 → 直接超时取消，回滚库存
type ReconcileWorkflow struct {
	cache          *cache.RedisCache
	orderService   domain.OrderService
	paymentService domain.PaymentService

	interval     time.Duration
	timeoutAfter time.Duration

	stopCh chan struct{}
	wg     sync.WaitGroup
}

func NewReconcileWorkflow(cache *cache.RedisCache, orderService domain.OrderService, paymentService domain.PaymentService) *ReconcileWorkflow {
	return &ReconcileWorkflow{
		cache:          cache,
		orderService:   orderService,
		paymentService: paymentService,
		interval:       time.Minute,
		// 15 分钟支付窗口 + 1 分钟宽限：正常路径的延时消息可能迟到，
		// 对账不跟它抢；即使撞上，Lua 状态机也保证先到者赢
		timeoutAfter: 16 * time.Minute,
		stopCh:       make(chan struct{}),
	}
}

// Start launches the periodic reconcile loop in the background.
func (w *ReconcileWorkflow) Start() {
	w.wg.Add(1)
	go func() {
		defer w.wg.Done()
		ticker := time.NewTicker(w.interval)
		defer ticker.Stop()
		for {
			select {
			case <-ticker.C:
				w.reconcileOnce()
			case <-w.stopCh:
				return
			}
		}
	}()
	log.Printf("Reconcile workflow started (interval %s, timeout after %s)", w.interval, w.timeoutAfter)
}

// Stop signals the loop to exit and waits for the in-flight round to finish.
func (w *ReconcileWorkflow) Stop() {
	close(w.stopCh)
	w.wg.Wait()
}

func (w *ReconcileWorkflow) reconcileOnce() {
	// 后台任务的 panic 绝不能杀掉主进程
	defer func() {
		if r := recover(); r != nil {
			log.Printf("reconcile: recovered from panic: %v", r)
		}
	}()

	now := time.Now().Unix()
	err := w.cache.ScanReservations(func(info cache.ReservationInfo) error {
		switch info.Status {
		case cache.ReservationStatusPaid:
			// PAID 但订单未落库（入库消息丢失）→ 补写
			if err := w.orderService.CreateOrderFromReservation(info.ID); err != nil {
				log.Printf("reconcile: failed to persist paid reservation %d: %v", info.ID, err)
			}
		case cache.ReservationStatusReserved:
			age := time.Duration(now-info.CreatedAt) * time.Second
			if age > w.timeoutAfter {
				// 滞留的 RESERVED：15 分钟延时消息丢失，直接超时取消回滚库存
				if err := w.paymentService.MarkTimeout(info.ID); err != nil {
					log.Printf("reconcile: failed to time out stranded reservation %d: %v", info.ID, err)
				} else {
					log.Printf("reconcile: timed out stranded reservation %d (age %s)", info.ID, age)
				}
			}
		}
		return nil // 单个订单补偿失败不中断本轮扫描
	})
	if err != nil {
		log.Printf("reconcile: scan failed: %v", err)
	}
}
