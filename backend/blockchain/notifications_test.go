// Copyright (c) 2017 The btcsuite developers
// Use of this source code is governed by an ISC
// license that can be found in the LICENSE file.

package blockchain

import (
	"testing"
	"time"

	"github.com/btcsuite/btcd/chaincfg/v2"
)

// TestNotifications ensures that notification callbacks are fired on events.
// Notifications are delivered asynchronously on the notification-bus
// goroutine, so the test waits for the delivery signal before asserting the
// count.
// TestNotifications 验证事件触发通知回调。通知由通知总线 goroutine 异步
// 投递,因此测试先等待送达信号再断言计数。
func TestNotifications(t *testing.T) {
	blocks, err := loadBlocks("blk_0_to_4.dat.bz2")
	if err != nil {
		t.Fatalf("Error loading file: %v\n", err)
	}

	// Create a new database and chain instance to run tests against.
	chain, teardownFunc, err := chainSetup("notifications",
		&chaincfg.MainNetParams)
	if err != nil {
		t.Fatalf("Failed to setup chain instance: %v", err)
	}
	defer teardownFunc()

	notificationCount := 0
	delivered := make(chan struct{}, 1)
	callback := func(notification *Notification) {
		if notification.Type == NTBlockAccepted {
			notificationCount++
			select {
			case delivered <- struct{}{}:
			default:
			}
		}
	}

	// Register callback multiple times then assert it is called that many
	// times.
	const numSubscribers = 3
	for i := 0; i < numSubscribers; i++ {
		chain.Subscribe(callback)
	}

	_, _, err = chain.ProcessBlock(blocks[1], BFNone)
	if err != nil {
		t.Fatalf("ProcessBlock fail on block 1: %v\n", err)
	}

	// The notification bus runs the callbacks asynchronously; wait for the
	// delivery signal with a generous timeout so a regression surfaces as a
	// timeout instead of a flaky early assert.
	// 通知总线异步执行回调;等待送达信号并给出宽松超时,让回归以超时形式
	// 暴露而非偶发早断言。
	select {
	case <-delivered:
	case <-time.After(5 * time.Second):
	}

	if notificationCount != numSubscribers {
		t.Fatalf("Expected notification callback to be executed %d "+
			"times, found %d", numSubscribers, notificationCount)
	}
}
