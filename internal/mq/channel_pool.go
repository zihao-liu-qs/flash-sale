package mq

import (
	amqp "github.com/rabbitmq/amqp091-go"
)

// ChannelPool 复用 AMQP channel。
//
// 背景：创建 channel 是一次到 broker 的网络往返（channel.open），在
// 「每请求新建 channel」的热路径上是可测量的开销；而 amqp.Channel 不是
// 并发安全的，不能直接共享。池化让 channel 在请求间复用——借用时独占、
// 用完归还——天然规避并发共享问题。
//
// 池是「有界复用、无界借用」：池空时新建 channel（不阻塞请求），归还时
// 池满则直接关闭。稳态下 channel 创建次数 ≈ 池大小，而非 ≈ 请求数。
type ChannelPool struct {
	newChannel func() (*amqp.Channel, error)
	pool       chan *amqp.Channel
}

// NewConfirmChannelPool creates a pool of confirm-mode channels backed by
// conn, keeping up to size channels for reuse.
func NewConfirmChannelPool(conn *amqp.Connection, size int) *ChannelPool {
	return &ChannelPool{
		newChannel: func() (*amqp.Channel, error) {
			return NewConfirmingChannel(conn)
		},
		pool: make(chan *amqp.Channel, size),
	}
}

// Get borrows a channel from the pool, creating a fresh one when the pool is
// empty. Channels that were closed (e.g. after a broker restart) are
// discarded and replaced.
func (p *ChannelPool) Get() (*amqp.Channel, error) {
	select {
	case ch := <-p.pool:
		if !ch.IsClosed() {
			return ch, nil
		}
		// 连接断开后旧 channel 已失效，丢弃，走下面新建
	default:
	}
	return p.newChannel()
}

// Put returns a channel to the pool. Closed channels are discarded; when the
// pool is full the channel is closed instead of pooled.
func (p *ChannelPool) Put(ch *amqp.Channel) {
	if ch == nil || ch.IsClosed() {
		return
	}
	select {
	case p.pool <- ch:
	default:
		ch.Close()
	}
}
