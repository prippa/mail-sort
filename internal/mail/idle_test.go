package mail

import (
	"context"
	"testing"
	"time"

	"github.com/emersion/go-imap/v2"
)

func TestPollTimer(t *testing.T) {
	// The pinned test server advertises IDLE for every authenticated
	// IMAP4rev1 session, so the fallback timer is exercised directly.
	session := &Session{}
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	started := time.Now()
	wake, err := session.poll(ctx, 40*time.Millisecond)
	if err != nil {
		t.Fatal(err)
	}
	if wake != WakePoll {
		t.Fatalf("wake = %s", wake)
	}
	if time.Since(started) > time.Second {
		t.Fatalf("poll took %s", time.Since(started))
	}
	done := make(chan error, 1)
	live, stop := context.WithCancel(context.Background())
	go func() {
		_, err := session.poll(live, time.Minute)
		done <- err
	}()
	stop()
	select {
	case err := <-done:
		if err == nil {
			t.Fatal("poll ignored cancel")
		}
	case <-time.After(time.Second):
		t.Fatal("poll did not stop")
	}
}

func TestWaitIdleSeesNewMailAndLeavesItUnread(t *testing.T) {
	session, raw, _ := newSession(t, fullCaps())
	first := appendRaw(t, raw, "INBOX", textMessage("first@example.com", "First", "stay unread"))
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	wakeCh := make(chan Wake, 1)
	errCh := make(chan error, 1)
	go func() {
		wake, err := session.Wait(ctx, "INBOX", time.Minute, false)
		if err != nil {
			errCh <- err
			return
		}
		wakeCh <- wake
	}()
	deadline := time.Now().Add(4 * time.Second)
	for time.Now().Before(deadline) {
		select {
		case wake := <-wakeCh:
			if wake != WakeIdle {
				t.Fatalf("wake = %s", wake)
			}
			if _, err := raw.Select("INBOX", &imap.SelectOptions{ReadOnly: true}).Wait(); err != nil {
				t.Fatal(err)
			}
			if seen(t, raw, first) {
				t.Fatal("idle set \\Seen")
			}
			return
		case err := <-errCh:
			t.Fatal(err)
		default:
			appendRaw(t, raw, "INBOX", textMessage("next@example.com", "Next", "arrive"))
			time.Sleep(30 * time.Millisecond)
		}
	}
	t.Fatal("idle did not report new mail")
}

func TestWaitCancelAndProtected(t *testing.T) {
	session, _, _ := newSession(t, imap.CapSet{imap.CapIMAP4rev1: {}})
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := session.Wait(ctx, "INBOX", time.Minute, false); err == nil {
		t.Fatal("cancelled wait returned")
	}
	if _, err := session.Wait(context.Background(), "Trash", time.Minute, false); err == nil {
		t.Fatal("Trash was opened")
	}
}

func TestWaitCancelDuringPoll(t *testing.T) {
	session, _, _ := newSession(t, imap.CapSet{imap.CapIMAP4rev1: {}})
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() {
		_, err := session.Wait(ctx, "INBOX", time.Minute, false)
		done <- err
	}()
	time.Sleep(20 * time.Millisecond)
	cancel()
	select {
	case err := <-done:
		if err == nil {
			t.Fatal("poll ignored cancel")
		}
	case <-time.After(time.Second):
		t.Fatal("poll did not stop")
	}
}
