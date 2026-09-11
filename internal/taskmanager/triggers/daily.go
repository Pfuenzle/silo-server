package triggers

import (
	"fmt"
	"sync"
	"time"

	"github.com/Silo-Server/silo-server/internal/taskmanager"
)

// DailyTrigger fires at a specific time each day in server-local time.
type DailyTrigger struct {
	cfg      taskmanager.TriggerConfig
	hour     int
	minute   int
	ch       chan struct{}
	nextRun  time.Time
	timer    timer
	stopCh   chan struct{}
	now      func() time.Time
	newTimer func(time.Duration) timer
	mu       sync.Mutex
}

type timer interface {
	Stop() bool
	Chan() <-chan time.Time
}

type timeTimer struct{ *time.Timer }

func (t timeTimer) Chan() <-chan time.Time { return t.C }

func NewDailyTrigger(cfg taskmanager.TriggerConfig) *DailyTrigger {
	return NewDailyTriggerWithClockAndTimer(cfg, time.Now, func(delay time.Duration) timer {
		return timeTimer{time.NewTimer(delay)}
	})
}

func NewDailyTriggerWithClock(cfg taskmanager.TriggerConfig, now func() time.Time) *DailyTrigger {
	return NewDailyTriggerWithClockAndTimer(cfg, now, func(delay time.Duration) timer {
		return timeTimer{time.NewTimer(delay)}
	})
}

func NewDailyTriggerWithClockAndTimer(cfg taskmanager.TriggerConfig, now func() time.Time, newTimer func(time.Duration) timer) *DailyTrigger {
	var h, m int
	fmt.Sscanf(cfg.TimeOfDay, "%d:%d", &h, &m)
	if now == nil {
		now = time.Now
	}
	if newTimer == nil {
		newTimer = func(delay time.Duration) timer { return timeTimer{time.NewTimer(delay)} }
	}
	return &DailyTrigger{
		cfg:      cfg,
		hour:     h,
		minute:   m,
		ch:       make(chan struct{}, 1),
		now:      now,
		newTimer: newTimer,
	}
}

func (d *DailyTrigger) calcNextRun(now time.Time) time.Time {
	today := time.Date(now.Year(), now.Month(), now.Day(), d.hour, d.minute, 0, 0, now.Location())
	if today.After(now) {
		return today
	}
	return today.Add(24 * time.Hour)
}

func (d *DailyTrigger) Start(_ *taskmanager.ExecutionResult) {
	d.mu.Lock()
	defer d.mu.Unlock()

	// Drain any stale signal from a previous timer fire.
	select {
	case <-d.ch:
	default:
	}

	stopCh := make(chan struct{})
	now := d.now()
	d.nextRun = d.calcNextRun(now)
	timer := d.newTimer(d.nextRun.Sub(now))
	d.stopCh = stopCh
	d.timer = timer

	go func() {
		select {
		case <-stopCh:
			timer.Stop()
			return
		case <-timer.Chan():
			select {
			case d.ch <- struct{}{}:
			default:
			}
		}
	}()
}

func (d *DailyTrigger) Stop() {
	d.mu.Lock()
	defer d.mu.Unlock()
	if d.stopCh != nil {
		select {
		case <-d.stopCh:
		default:
			close(d.stopCh)
		}
	}
}

func (d *DailyTrigger) NextRunTime() time.Time {
	d.mu.Lock()
	defer d.mu.Unlock()
	return d.nextRun
}

func (d *DailyTrigger) Config() taskmanager.TriggerConfig { return d.cfg }
func (d *DailyTrigger) C() <-chan struct{}                { return d.ch }
