package mail

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/emersion/go-imap/v2"
	"github.com/emersion/go-imap/v2/imapclient"
)

const (
	// DefaultPoll is the wait used when the server does not advertise IDLE.
	DefaultPoll = 5 * time.Minute
)

// Wake is why Wait returned.
type Wake string

const (
	// WakeIdle means the server reported a new message count.
	// The pinned imapclient restarts IDLE every 28 minutes
	// (idleRestartInterval), inside the 29-minute bound from RFC 2177.
	WakeIdle Wake = "idle"
	// WakePoll means IDLE is unavailable and the poll interval elapsed.
	WakePoll Wake = "poll"
)

// Wait examines mailbox read-only, then blocks until new mail is reported or
// the poll interval elapses. IDLE is used when the server advertises it.
// The mailbox is not opened when its name is Trash, Junk, Drafts, or Sent
// unless allowProtected is set. Wait does not set \Seen.
//
// VERIFY: live Gmail and Microsoft IDLE were not run. The in-memory server covers this path.
func (s *Session) Wait(ctx context.Context, mailbox string, poll time.Duration, allowProtected bool) (Wake, error) {
	if err := ctx.Err(); err != nil {
		return "", err
	}
	if strings.TrimSpace(mailbox) == "" {
		mailbox = "INBOX"
	}
	if poll <= 0 {
		poll = DefaultPoll
	}
	if err := s.rejectProtected(ctx, mailbox, allowProtected); err != nil {
		return "", err
	}
	caps, err := s.Capabilities(ctx)
	if err != nil {
		return "", err
	}
	if !caps.Idle {
		return s.poll(ctx, poll)
	}
	baseline, err := s.examine(ctx, mailbox)
	if err != nil {
		return "", err
	}
	return s.idle(ctx, baseline)
}

func (s *Session) examine(ctx context.Context, mailbox string) (uint32, error) {
	var count uint32
	err := s.do(ctx, func() error {
		data, err := s.client.Select(mailbox, &imap.SelectOptions{ReadOnly: true}).Wait()
		if err != nil {
			return fmt.Errorf("imap: examine %s: %w", mailbox, err)
		}
		if data == nil {
			return fmt.Errorf("imap: examine %s: empty result", mailbox)
		}
		count = data.NumMessages
		return nil
	})
	return count, err
}

func (s *Session) poll(ctx context.Context, poll time.Duration) (Wake, error) {
	timer := time.NewTimer(poll)
	defer timer.Stop()
	select {
	case <-ctx.Done():
		return "", ctx.Err()
	case <-timer.C:
		return WakePoll, nil
	}
}

func (s *Session) idle(ctx context.Context, baseline uint32) (Wake, error) {
	if _, ok := s.nextCount(baseline); ok {
		return WakeIdle, nil
	}
	cmd, err := s.startIdle(ctx)
	if err != nil {
		return "", err
	}
	stopped := make(chan error, 1)
	go func() {
		stopped <- cmd.Wait()
	}()
	for {
		select {
		case <-ctx.Done():
			_ = s.finishIdle(cmd, stopped)
			return "", ctx.Err()
		case n := <-s.updates:
			if n == baseline {
				continue
			}
			if err := s.finishIdle(cmd, stopped); err != nil {
				return "", err
			}
			return WakeIdle, nil
		case err := <-stopped:
			if err != nil {
				return "", scrub(fmt.Errorf("imap: idle: %w", err), s.password)
			}
			return "", errors.New("imap: idle ended")
		}
	}
}

func (s *Session) nextCount(baseline uint32) (uint32, bool) {
	if s.updates == nil {
		return 0, false
	}
	for {
		select {
		case n := <-s.updates:
			if n != baseline {
				return n, true
			}
		default:
			return 0, false
		}
	}
}

type idleStart struct {
	cmd *imapclient.IdleCommand
	err error
}

func (s *Session) startIdle(ctx context.Context) (*imapclient.IdleCommand, error) {
	ch := make(chan idleStart, 1)
	go func() {
		cmd, err := s.client.Idle()
		ch <- idleStart{cmd, err}
	}()
	timer := time.NewTimer(commandTimeout)
	defer timer.Stop()
	select {
	case <-ctx.Done():
		s.closeConn()
		<-ch
		return nil, ctx.Err()
	case <-timer.C:
		s.closeConn()
		<-ch
		return nil, errors.New("imap: idle timed out")
	case got := <-ch:
		if got.err != nil {
			return nil, scrub(fmt.Errorf("imap: idle: %w", got.err), s.password)
		}
		return got.cmd, nil
	}
}

func (s *Session) finishIdle(cmd *imapclient.IdleCommand, stopped <-chan error) error {
	if cmd != nil {
		_ = cmd.Close()
	}
	timer := time.NewTimer(commandTimeout)
	defer timer.Stop()
	select {
	case err := <-stopped:
		if err != nil {
			return scrub(fmt.Errorf("imap: idle: %w", err), s.password)
		}
		return nil
	case <-timer.C:
		s.closeConn()
		<-stopped
		return errors.New("imap: idle did not stop")
	}
}
