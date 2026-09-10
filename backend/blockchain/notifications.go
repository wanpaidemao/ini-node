// Copyright (c) 2013-2016 The btcsuite developers
// Use of this source code is governed by an ISC
// license that can be found in the LICENSE file.

// Asher_Mod_Start_20260910_112851
package blockchain

import (
	"fmt"
)

// notifyQueueCapacity bounds the asynchronous notification queue.  A burst of
// accepted/connected/disconnected events (e.g. an orphan flood or a large reorg)
// is absorbed by the buffer; once full, sendNotification falls back to an inline
// dispatch so a notification is never silently dropped.
// notifyQueueCapacity 限制异步通知队列的容量。突发事件(孤儿洪流或大 reorg)
// 由缓冲吸收;队列满后 sendNotification 回退为内联派发,保证通知永不静默丢失。
const notifyQueueCapacity = 1024

// NotificationType represents the type of a notification message.
type NotificationType int

// NotificationCallback is used for a caller to provide a callback for
// notifications about various chain events.
type NotificationCallback func(*Notification)

// Constants for the type of a notification message.
const (
	// NTBlockAccepted indicates the associated block was accepted into
	// the block chain.  Note that this does not necessarily mean it was
	// added to the main chain.  For that, use NTBlockConnected.
	NTBlockAccepted NotificationType = iota

	// NTBlockConnected indicates the associated block was connected to the
	// main chain.
	NTBlockConnected

	// NTBlockDisconnected indicates the associated block was disconnected
	// from the main chain.
	NTBlockDisconnected
)

// notificationTypeStrings is a map of notification types back to their constant
// names for pretty printing.
var notificationTypeStrings = map[NotificationType]string{
	NTBlockAccepted:     "NTBlockAccepted",
	NTBlockConnected:    "NTBlockConnected",
	NTBlockDisconnected: "NTBlockDisconnected",
}

// String returns the NotificationType in human-readable form.
func (n NotificationType) String() string {
	if s, ok := notificationTypeStrings[n]; ok {
		return s
	}
	return fmt.Sprintf("Unknown Notification Type (%d)", int(n))
}

// Notification defines notification that is sent to the caller via the callback
// function provided during the call to New and consists of a notification type
// as well as associated data that depends on the type as follows:
//   - NTBlockAccepted:     *btcutil.Block
//   - NTBlockConnected:    *btcutil.Block
//   - NTBlockDisconnected: *btcutil.Block
type Notification struct {
	Type NotificationType
	Data interface{}
}

// Subscribe to block chain notifications. Registers a callback to be executed
// when various events take place. See the documentation on Notification and
// NotificationType for details on the types and contents of notifications.
//
// The callback is invoked from the notification-bus goroutine, never from the
// chain-processing goroutine, so a slow subscriber cannot stall block
// processing.  Subscribers that touch SyncManager state should forward the
// event to their own handler goroutine instead of mutating shared state in the
// callback.
// 注册区块链事件回调。回调由通知总线 goroutine 调用,而非链处理 goroutine,
// 因此慢订阅者不会卡住块处理。会触碰 SyncManager 状态的订阅者应把事件转发
// 到自己的处理 goroutine,而不是在回调里直接改共享状态。
func (b *BlockChain) Subscribe(callback NotificationCallback) {
	b.notificationsLock.Lock()
	b.notifications = append(b.notifications, callback)
	b.notificationsLock.Unlock()
}

// sendNotification publishes a notification to the asynchronous bus.  The
// caller (often holding the chain state lock, or momentarily releasing it as in
// the "unlock, notify, re-lock" pattern) only performs a non-blocking enqueue,
// so the publish cost no longer includes running every subscriber.  When the
// queue is full the notification is dispatched inline to preserve ordering and
// guarantee delivery; this only happens under extreme subscriber backlog.
// sendNotification 把通知发布到异步总线。调用方(通常持链状态锁,或如
// "解锁-通知-重锁"模式一样短暂释放)只做非阻塞入队,发布成本不再包含运行
// 所有订阅者。队列满时改为内联派发以保持顺序并保证送达;仅在订阅者极端
// 积压时发生。
func (b *BlockChain) sendNotification(typ NotificationType, data interface{}) {
	n := Notification{Type: typ, Data: data}
	select {
	case b.notifyChan <- n:
	default:
		b.dispatchNotification(&n)
	}
}

// notifyLoop is the notification-bus consumer goroutine.  It drains the queue
// in FIFO order and runs every registered callback on a single thread, so
// subscribers observe events in the same order they were published.
// notifyLoop 是通知总线消费 goroutine:按 FIFO 顺序取队并发到单线程上执行
// 所有已注册回调,订阅者观察到的顺序与发布顺序一致。
func (b *BlockChain) notifyLoop() {
	defer close(b.notifyDone)
	for {
		select {
		case n := <-b.notifyChan:
			b.dispatchNotification(&n)
		case <-b.notifyQuit:
			// Drain whatever is still queued so no published event is
			// left undelivered during shutdown.
			// 关闭前清空剩余队列,保证已发布的事件在关闭期间仍被送达。
			for {
				select {
				case n := <-b.notifyChan:
					b.dispatchNotification(&n)
				default:
					return
				}
			}
		}
	}
}

// StopNotifications stops the notification bus.  It is idempotent and drains
// the remaining queue before exiting; after it returns, already-published
// events may still be dispatched inline by sendNotification (the non-blocking
// enqueue to a closed-away loop simply becomes the inline fallback, whose
// subscribers still receive the event).
// StopNotifications 停止通知总线。幂等,退出前会排空剩余队列;返回后仍已
// 发布的事件可能由 sendNotification 内联派发兜底,订阅者依然能收到。
func (b *BlockChain) StopNotifications() {
	b.notifyStopped.Do(func() {
		close(b.notifyQuit)
		<-b.notifyDone
	})
}

// dispatchNotification runs the notification against every registered callback.
// It is called either from notifyLoop (common case) or inline from
// sendNotification when the queue is full.
// dispatchNotification 把通知分发给所有已注册回调。通常由 notifyLoop 调用,
// 队列满时由 sendNotification 内联调用。
func (b *BlockChain) dispatchNotification(n *Notification) {
	b.notificationsLock.RLock()
	for _, callback := range b.notifications {
		callback(n)
	}
	b.notificationsLock.RUnlock()
}
// Asher_Mod_End_20260910_112851
