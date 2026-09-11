package scanqueue

import (
	"context"
	"fmt"
	"time"

	"github.com/Silo-Server/silo-server/internal/models"
)

func (s *Service) EnqueueScan(ctx context.Context, folderID int, mode, path, trigger string) (bool, error) {
	_, created, err := s.EnqueueScanRun(ctx, folderID, mode, path, trigger)
	return created, err
}

// EnqueueScanRun creates or reuses an active run and returns its durable ID.
func (s *Service) EnqueueScanRun(ctx context.Context, folderID int, mode, path, trigger string) (*models.ScanRun, bool, error) {
	if s == nil || s.repo == nil {
		return nil, false, fmt.Errorf("scan queue is not configured")
	}
	run, created, err := s.repo.Create(ctx, CreateInput{LibraryID: folderID, Mode: mode, Path: path, Trigger: trigger})
	if err != nil {
		return nil, false, err
	}
	if created {
		s.publish(ctx, "scan.accepted", run)
	}
	return run, created, nil
}

// Wait observes persisted state until a scan run reaches a terminal status.
func (s *Service) Wait(ctx context.Context, id string) (*models.ScanRun, error) {
	if s == nil || s.repo == nil {
		return nil, fmt.Errorf("scan queue is not configured")
	}
	interval := s.pollInterval
	if interval <= 0 {
		interval = defaultPollInterval
	}
	for {
		run, err := s.repo.GetByID(ctx, id)
		if err != nil {
			return nil, fmt.Errorf("load scan run %s: %w", id, err)
		}
		switch run.Status {
		case StatusCompleted, StatusFailed, StatusCancelled:
			return run, nil
		}
		timer := time.NewTimer(interval)
		select {
		case <-ctx.Done():
			timer.Stop()
			return nil, fmt.Errorf("wait for scan run %s: %w", id, ctx.Err())
		case <-timer.C:
		}
	}
}
