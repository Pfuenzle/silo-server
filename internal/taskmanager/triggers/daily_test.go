package triggers

import (
	"testing"
	"time"

	"github.com/Silo-Server/silo-server/internal/taskmanager"
)

func TestDailyTriggerWithClock_usesInjectedServerLocalTime(t *testing.T) {
	// Given a daily 02:00 trigger and a clock in the server's local timezone.
	location := time.FixedZone("fixture", 2*60*60)
	now := time.Date(2026, 9, 10, 1, 59, 0, 0, location)
	trigger := NewDailyTriggerWithClock(taskmanager.TriggerConfig{
		Type:      taskmanager.TriggerTypeDaily,
		TimeOfDay: "02:00",
	}, func() time.Time { return now })

	// When the trigger is started.
	trigger.Start(nil)
	t.Cleanup(trigger.Stop)

	// Then its next run is 02:00 in that same local timezone.
	want := time.Date(2026, 9, 10, 2, 0, 0, 0, location)
	if got := trigger.NextRunTime(); !got.Equal(want) || got.Location() != location {
		t.Fatalf("next run = %v (%s), want %v (%s)", got, got.Location(), want, location)
	}
}

type fakeDailyTimer struct {
	ch chan time.Time
}

func (t *fakeDailyTimer) Stop() bool             { return true }
func (t *fakeDailyTimer) Chan() <-chan time.Time { return t.ch }

func TestDailyTriggerWithClockAndTimer_firesWithoutWallClock(t *testing.T) {
	// Given a daily trigger with an injected clock and controllable timer.
	location := time.FixedZone("fixture", 2*60*60)
	now := time.Date(2026, 9, 10, 1, 59, 0, 0, location)
	var fakeTimer *fakeDailyTimer
	trigger := NewDailyTriggerWithClockAndTimer(taskmanager.TriggerConfig{
		Type: taskmanager.TriggerTypeDaily, TimeOfDay: "02:00",
	}, func() time.Time { return now }, func(time.Duration) timer {
		fakeTimer = &fakeDailyTimer{ch: make(chan time.Time, 1)}
		return fakeTimer
	})

	// When the injected timer fires.
	trigger.Start(nil)
	t.Cleanup(trigger.Stop)
	fakeTimer.ch <- now.Add(time.Minute)

	// Then the trigger emits exactly one scheduled signal without sleeping.
	select {
	case <-trigger.C():
	case <-time.After(time.Second):
		t.Fatal("daily trigger did not fire from injected timer")
	}
}
