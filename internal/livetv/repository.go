package livetv

import (
	"context"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

type SourceRepository interface {
	ListSources(ctx context.Context, libraryID int) ([]Source, error)
	CreateSource(ctx context.Context, source Source) (Source, error)
	GetSource(ctx context.Context, libraryID int, sourceKey string) (Source, error)
	UpdateSource(ctx context.Context, source Source) (Source, error)
	DeleteSource(ctx context.Context, libraryID int, sourceKey string) error
	UpdateRefreshState(ctx context.Context, sourceID int64, state, message string, refreshedAt *time.Time) error
}

type ChannelRepository interface {
	ListChannels(ctx context.Context, libraryID int, limit, offset int) ([]Channel, int, error)
	CreateChannel(ctx context.Context, channel Channel) (Channel, error)
	GetChannel(ctx context.Context, libraryID int, stableID SourceQualifiedID) (Channel, error)
}

func (r *PostgresRepository) ResolveLiveChannel(ctx context.Context, libraryID int, stableID SourceQualifiedID) (Channel, error) {
	channel, err := r.GetChannel(ctx, libraryID, stableID)
	if err != nil {
		return Channel{}, fmt.Errorf("load Live TV channel for playback: %w", err)
	}
	var enabled bool
	if err := r.pool.QueryRow(ctx, `SELECT enabled FROM live_tv_sources WHERE id = $1 AND library_id = $2 AND kind = $3`, channel.SourceID, libraryID, SourceKindPlaylist).Scan(&enabled); err != nil {
		return Channel{}, fmt.Errorf("load Live TV source for playback: %w", err)
	}
	if !enabled {
		return Channel{}, ErrLivePlaybackUnavailable
	}
	return channel, nil
}

type ProgrammeRepository interface {
	CreateProgramme(ctx context.Context, programme Programme) (Programme, error)
	ListProgrammes(ctx context.Context, libraryID int, channelID int64, from, to time.Time) ([]Programme, error)
	GetProgramme(ctx context.Context, libraryID int, stableID SourceQualifiedID) (Programme, error)
}

type ChannelEPGMappingRepository interface {
	UpsertChannelEPGMapping(ctx context.Context, mapping ChannelEPGMapping) error
	ListChannelEPGMappings(ctx context.Context, libraryID int, channelID int64) ([]ChannelEPGMapping, error)
}

type PostgresRepository struct{ pool *pgxpool.Pool }

func (r *PostgresRepository) ApplySnapshot(ctx context.Context, sourceID int64, snapshot SourceSnapshot, state, message string, refreshedAt *time.Time) error {
	tx, err := r.pool.Begin(ctx)
	if err != nil {
		return fmt.Errorf("begin Live TV snapshot transaction: %w", err)
	}
	defer func() { _ = tx.Rollback(ctx) }()
	if state == "ready" {
		var libraryID int
		if err := tx.QueryRow(ctx, `SELECT library_id FROM live_tv_sources WHERE id = $1 FOR UPDATE`, sourceID).Scan(&libraryID); err != nil {
			return fmt.Errorf("load Live TV source ownership: %w", err)
		}
		if _, err := tx.Exec(ctx, `DELETE FROM live_tv_programmes WHERE source_id = $1`, sourceID); err != nil {
			return fmt.Errorf("clear Live TV programmes: %w", err)
		}
		if _, err := tx.Exec(ctx, `DELETE FROM live_tv_channels WHERE source_id = $1`, sourceID); err != nil {
			return fmt.Errorf("clear Live TV channels: %w", err)
		}
		channelIDs := make(map[string]int64, len(snapshot.Channels))
		for _, channel := range snapshot.Channels {
			var channelID int64
			if err := tx.QueryRow(ctx, `INSERT INTO live_tv_channels (library_id, source_id, external_id, stable_id, name, channel_number, category, stream_url, artwork, rating) VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10) RETURNING id`, libraryID, sourceID, channel.ExternalID, channel.StableID, channel.Name, channel.Number, channel.Category, channel.StreamURL, normalizeJSON(channel.Artwork), normalizeJSON(channel.Rating)).Scan(&channelID); err != nil {
				return fmt.Errorf("insert Live TV channel: %w", err)
			}
			channelIDs[channel.ExternalID] = channelID
		}
		for _, parsed := range snapshot.Programmes {
			channelID, ok := channelIDs[parsed.ChannelExternalID]
			if !ok {
				if err := tx.QueryRow(ctx, `SELECT id FROM live_tv_channels WHERE library_id = $1 AND external_id = $2`, libraryID, parsed.ChannelExternalID).Scan(&channelID); err != nil {
					continue
				}
			}
			programme := parsed.Programme
			if err := tx.QueryRow(ctx, `INSERT INTO live_tv_programmes (library_id, source_id, channel_id, external_id, stable_id, title, description, starts_at, ends_at, artwork, rating) VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11) RETURNING id`, libraryID, sourceID, channelID, programme.ExternalID, programme.StableID, programme.Title, programme.Description, programme.StartsAt, programme.EndsAt, normalizeJSON(programme.Artwork), normalizeJSON(programme.Rating)).Scan(&programme.ID); err != nil {
				return fmt.Errorf("insert Live TV programme: %w", err)
			}
		}
	}
	if _, err := tx.Exec(ctx, `UPDATE live_tv_sources SET refresh_state = $2, refresh_error = $3, last_refresh_at = $4, updated_at = now() WHERE id = $1`, sourceID, state, message, refreshedAt); err != nil {
		return fmt.Errorf("update Live TV refresh state: %w", err)
	}
	if err := tx.Commit(ctx); err != nil {
		return fmt.Errorf("commit Live TV snapshot transaction: %w", err)
	}
	return nil
}

func NewPostgresRepository(pool *pgxpool.Pool) *PostgresRepository {
	return &PostgresRepository{pool: pool}
}

func (r *PostgresRepository) ListSources(ctx context.Context, libraryID int) ([]Source, error) {
	rows, err := r.pool.Query(ctx, `SELECT id, library_id, kind, source_key, name, location, config, enabled, last_refresh_at, refresh_state, refresh_error
FROM live_tv_sources WHERE library_id = $1 ORDER BY kind, source_key`, libraryID)
	if err != nil {
		return nil, fmt.Errorf("listing Live TV sources: %w", err)
	}
	defer rows.Close()
	sources := make([]Source, 0)
	for rows.Next() {
		source, scanErr := scanSourceRow(rows)
		if scanErr != nil {
			return nil, scanErr
		}
		sources = append(sources, source)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterating Live TV sources: %w", err)
	}
	return sources, nil
}

func (r *PostgresRepository) CreateSource(ctx context.Context, source Source) (Source, error) {
	if err := ValidateSource(source.Kind, source.SourceKey, source.Name, source.Location); err != nil {
		return Source{}, err
	}
	if len(source.Config) == 0 {
		source.Config = []byte(`{}`)
	}
	const query = `INSERT INTO live_tv_sources (library_id, kind, source_key, name, location, config, enabled)
VALUES ($1, $2, $3, $4, $5, $6, $7)
RETURNING id, library_id, kind, source_key, name, location, config, enabled, last_refresh_at, refresh_state, refresh_error`
	return scanSource(r.pool.QueryRow(ctx, query, source.LibraryID, source.Kind, source.SourceKey, source.Name, source.Location, source.Config, source.Enabled))
}

func (r *PostgresRepository) GetSource(ctx context.Context, libraryID int, sourceKey string) (Source, error) {
	const query = `SELECT id, library_id, kind, source_key, name, location, config, enabled, last_refresh_at, refresh_state, refresh_error
FROM live_tv_sources WHERE library_id = $1 AND source_key = $2`
	return scanSource(r.pool.QueryRow(ctx, query, libraryID, sourceKey))
}

func (r *PostgresRepository) UpdateSource(ctx context.Context, source Source) (Source, error) {
	if err := ValidateSource(source.Kind, source.SourceKey, source.Name, source.Location); err != nil {
		return Source{}, err
	}
	if len(source.Config) == 0 {
		source.Config = []byte(`{}`)
	}
	const query = `UPDATE live_tv_sources SET kind = $3, name = $4, location = $5, config = $6, enabled = $7, updated_at = now()
WHERE library_id = $1 AND source_key = $2
RETURNING id, library_id, kind, source_key, name, location, config, enabled, last_refresh_at, refresh_state, refresh_error`
	return scanSource(r.pool.QueryRow(ctx, query, source.LibraryID, source.SourceKey, source.Kind, source.Name, source.Location, source.Config, source.Enabled))
}

func (r *PostgresRepository) DeleteSource(ctx context.Context, libraryID int, sourceKey string) error {
	tag, err := r.pool.Exec(ctx, `DELETE FROM live_tv_sources WHERE library_id = $1 AND source_key = $2`, libraryID, sourceKey)
	if err != nil {
		return fmt.Errorf("deleting Live TV source: %w", err)
	}
	if tag.RowsAffected() == 0 {
		return pgx.ErrNoRows
	}
	return nil
}

func (r *PostgresRepository) UpdateRefreshState(ctx context.Context, sourceID int64, state, message string, refreshedAt *time.Time) error {
	const query = `UPDATE live_tv_sources SET refresh_state = $2, refresh_error = $3, last_refresh_at = $4, updated_at = now() WHERE id = $1`
	tag, err := r.pool.Exec(ctx, query, sourceID, state, message, refreshedAt)
	if err != nil {
		return fmt.Errorf("updating Live TV refresh state: %w", err)
	}
	if tag.RowsAffected() == 0 {
		return pgx.ErrNoRows
	}
	return nil
}

func (r *PostgresRepository) CreateChannel(ctx context.Context, channel Channel) (Channel, error) {
	const query = `INSERT INTO live_tv_channels (library_id, source_id, external_id, stable_id, name, channel_number, category, stream_url, artwork, rating)
VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10)
RETURNING id, library_id, source_id, external_id, stable_id, name, channel_number, category, stream_url, artwork, rating`
	return scanChannel(r.pool.QueryRow(ctx, query, channel.LibraryID, channel.SourceID, channel.ExternalID, channel.StableID, channel.Name, channel.Number, channel.Category, channel.StreamURL, normalizeJSON(channel.Artwork), normalizeJSON(channel.Rating)))
}

func normalizeJSON(value []byte) []byte {
	if len(value) == 0 {
		return []byte(`{}`)
	}
	return value
}

func (r *PostgresRepository) GetChannel(ctx context.Context, libraryID int, stableID SourceQualifiedID) (Channel, error) {
	const query = `SELECT id, library_id, source_id, external_id, stable_id, name, channel_number, category, stream_url, artwork, rating
FROM live_tv_channels WHERE library_id = $1 AND stable_id = $2`
	return scanChannel(r.pool.QueryRow(ctx, query, libraryID, stableID))
}

func (r *PostgresRepository) ListChannels(ctx context.Context, libraryID, limit, offset int) ([]Channel, int, error) {
	rows, err := r.pool.Query(ctx, `SELECT id, library_id, source_id, external_id, stable_id, name, channel_number, category, stream_url, artwork, rating
	FROM live_tv_channels WHERE library_id = $1
	-- Numeric-only values sort by value; compound and nonnumeric labels keep text fallback ordering.
	ORDER BY CASE
		WHEN channel_number ~ '^[0-9]+$' THEN channel_number::numeric
		ELSE NULL
	END NULLS LAST,
	channel_number NULLS LAST, name, stable_id LIMIT $2 OFFSET $3`, libraryID, limit, offset)
	if err != nil {
		return nil, 0, fmt.Errorf("listing Live TV channels: %w", err)
	}
	defer rows.Close()
	channels := make([]Channel, 0)
	for rows.Next() {
		channel, scanErr := scanChannel(rows)
		if scanErr != nil {
			return nil, 0, scanErr
		}
		channels = append(channels, channel)
	}
	if err := rows.Err(); err != nil {
		return nil, 0, fmt.Errorf("iterating Live TV channels: %w", err)
	}
	var total int
	if err := r.pool.QueryRow(ctx, `SELECT COUNT(*) FROM live_tv_channels WHERE library_id = $1`, libraryID).Scan(&total); err != nil {
		return nil, 0, fmt.Errorf("counting Live TV channels: %w", err)
	}
	return channels, total, nil
}

func (r *PostgresRepository) CreateProgramme(ctx context.Context, programme Programme) (Programme, error) {
	const query = `INSERT INTO live_tv_programmes (library_id, source_id, channel_id, external_id, stable_id, title, description, starts_at, ends_at, artwork, rating)
VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11)
RETURNING id, library_id, source_id, channel_id, external_id, stable_id, title, description, starts_at, ends_at, artwork, rating`
	return scanProgramme(r.pool.QueryRow(ctx, query, programme.LibraryID, programme.SourceID, programme.ChannelID, programme.ExternalID, programme.StableID, programme.Title, programme.Description, programme.StartsAt, programme.EndsAt, programme.Artwork, programme.Rating))
}

func (r *PostgresRepository) ListProgrammes(ctx context.Context, libraryID int, channelID int64, from, to time.Time) ([]Programme, error) {
	rows, err := r.pool.Query(ctx, `SELECT id, library_id, source_id, channel_id, external_id, stable_id, title, description, starts_at, ends_at, artwork, rating
FROM live_tv_programmes WHERE library_id = $1 AND channel_id = $2 AND starts_at < $4 AND ends_at > $3 ORDER BY starts_at, id`, libraryID, channelID, from, to)
	if err != nil {
		return nil, fmt.Errorf("listing Live TV programmes: %w", err)
	}
	defer rows.Close()
	programmes := make([]Programme, 0)
	for rows.Next() {
		programme, scanErr := scanProgramme(rows)
		if scanErr != nil {
			return nil, scanErr
		}
		programmes = append(programmes, programme)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterating Live TV programmes: %w", err)
	}
	return programmes, nil
}

func (r *PostgresRepository) GetProgramme(ctx context.Context, libraryID int, stableID SourceQualifiedID) (Programme, error) {
	const query = `SELECT id, library_id, source_id, channel_id, external_id, stable_id, title, description, starts_at, ends_at, artwork, rating
FROM live_tv_programmes WHERE library_id = $1 AND stable_id = $2`
	return scanProgramme(r.pool.QueryRow(ctx, query, libraryID, stableID))
}

func (r *PostgresRepository) UpsertChannelEPGMapping(ctx context.Context, mapping ChannelEPGMapping) error {
	_, err := r.pool.Exec(ctx, `INSERT INTO live_tv_channel_epg_mappings (library_id, channel_id, epg_source_id, epg_channel_id)
VALUES ($1, $2, $3, $4)
ON CONFLICT (channel_id, epg_source_id) DO UPDATE SET epg_channel_id = EXCLUDED.epg_channel_id`, mapping.LibraryID, mapping.ChannelID, mapping.EPGSourceID, mapping.EPGChannelID)
	if err != nil {
		return fmt.Errorf("upserting Live TV EPG mapping: %w", err)
	}
	return nil
}

func (r *PostgresRepository) ListChannelEPGMappings(ctx context.Context, libraryID int, channelID int64) ([]ChannelEPGMapping, error) {
	rows, err := r.pool.Query(ctx, `SELECT library_id, channel_id, epg_source_id, epg_channel_id
FROM live_tv_channel_epg_mappings WHERE library_id = $1 AND channel_id = $2 ORDER BY epg_source_id`, libraryID, channelID)
	if err != nil {
		return nil, fmt.Errorf("listing Live TV EPG mappings: %w", err)
	}
	defer rows.Close()
	mappings := make([]ChannelEPGMapping, 0)
	for rows.Next() {
		var mapping ChannelEPGMapping
		if err := rows.Scan(&mapping.LibraryID, &mapping.ChannelID, &mapping.EPGSourceID, &mapping.EPGChannelID); err != nil {
			return nil, fmt.Errorf("scanning Live TV EPG mapping: %w", err)
		}
		mappings = append(mappings, mapping)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterating Live TV EPG mappings: %w", err)
	}
	return mappings, nil
}

type rowScanner interface{ Scan(dest ...any) error }

func scanChannel(row rowScanner) (Channel, error) {
	var channel Channel
	if err := row.Scan(&channel.ID, &channel.LibraryID, &channel.SourceID, &channel.ExternalID, &channel.StableID, &channel.Name, &channel.Number, &channel.Category, &channel.StreamURL, &channel.Artwork, &channel.Rating); err != nil {
		return Channel{}, fmt.Errorf("scanning Live TV channel: %w", err)
	}
	return channel, nil
}

func scanProgramme(row rowScanner) (Programme, error) {
	var programme Programme
	if err := row.Scan(&programme.ID, &programme.LibraryID, &programme.SourceID, &programme.ChannelID, &programme.ExternalID, &programme.StableID, &programme.Title, &programme.Description, &programme.StartsAt, &programme.EndsAt, &programme.Artwork, &programme.Rating); err != nil {
		return Programme{}, fmt.Errorf("scanning Live TV programme: %w", err)
	}
	return programme, nil
}

func scanSource(row pgx.Row) (Source, error) {
	return scanSourceRow(row)
}

func scanSourceRow(row rowScanner) (Source, error) {
	var source Source
	var kind string
	var refreshedAt *time.Time
	if err := row.Scan(&source.ID, &source.LibraryID, &kind, &source.SourceKey, &source.Name, &source.Location, &source.Config, &source.Enabled, &refreshedAt, &source.RefreshState, &source.RefreshError); err != nil {
		return Source{}, fmt.Errorf("scanning Live TV source: %w", err)
	}
	source.Kind = SourceKind(kind)
	if refreshedAt != nil {
		source.LastRefreshAt = refreshedAt
	}
	return source, nil
}
