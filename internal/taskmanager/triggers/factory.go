package triggers

import (
	"time"

	"github.com/Silo-Server/silo-server/internal/taskmanager"
)

// New creates a live Trigger from a TriggerConfig.
func New(cfg taskmanager.TriggerConfig) taskmanager.Trigger {
	return NewWithClock(cfg, time.Now)
}

func NewWithClock(cfg taskmanager.TriggerConfig, now func() time.Time) taskmanager.Trigger {
	switch cfg.Type {
	case taskmanager.TriggerTypeInterval:
		return NewIntervalTrigger(cfg)
	case taskmanager.TriggerTypeDaily:
		return NewDailyTriggerWithClock(cfg, now)
	case taskmanager.TriggerTypeWeekly:
		return NewWeeklyTrigger(cfg)
	case taskmanager.TriggerTypeStartup:
		return NewStartupTrigger(cfg)
	default:
		return nil
	}
}
