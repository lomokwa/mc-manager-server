package services

import "testing"

// TestNotifyBackupConfigChanged_DoesNotBlock exercises the buffered-channel
// contract NotifyBackupConfigChanged relies on: callers (e.g.
// UpdateBackupConfigHandler) must never block even if no scheduler goroutine
// is currently draining reloadCh, and repeated notifications before a drain
// must coalesce rather than accumulate.
func TestNotifyBackupConfigChanged_DoesNotBlock(t *testing.T) {
	// Drain any pending signal left over from another test/package init so
	// this test starts from a known state.
	select {
	case <-reloadCh:
	default:
	}

	done := make(chan struct{})
	go func() {
		NotifyBackupConfigChanged()
		NotifyBackupConfigChanged()
		NotifyBackupConfigChanged()
		close(done)
	}()
	<-done

	select {
	case <-reloadCh:
	default:
		t.Fatal("expected a pending reload signal after NotifyBackupConfigChanged")
	}

	// A second receive must not block: repeated notifications should have
	// coalesced into (at most) one buffered signal.
	select {
	case <-reloadCh:
		t.Fatal("expected only one coalesced signal, got a second one")
	default:
	}
}
