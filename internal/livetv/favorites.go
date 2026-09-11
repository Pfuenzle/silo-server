package livetv

import (
	"context"
	"fmt"
	"time"
)

type FavoriteRepository interface {
	AddFavorite(ctx context.Context, favorite Favorite) error
	RemoveFavorite(ctx context.Context, favorite Favorite) error
	IsFavorite(ctx context.Context, favorite Favorite) (bool, error)
	ListFavorites(ctx context.Context, userID int, profileID string, libraryID int, kind FavoriteKind, limit, offset int) ([]Favorite, error)
	ListFavoriteChannelsNow(ctx context.Context, userID int, profileID string, libraryID int, now time.Time, limit int) ([]Channel, error)
	ListFavoriteProgrammesNow(ctx context.Context, userID int, profileID string, libraryID int, now time.Time, limit int) ([]Programme, error)
	ListProgrammesNow(ctx context.Context, libraryID int, now time.Time, limit int) ([]Programme, error)
	ListFavoriteProgrammes(ctx context.Context, userID int, profileID string, libraryID int, from, to time.Time, ratedOnly bool, limit int) ([]Programme, error)
	ListUpcomingFavoriteProgrammes(ctx context.Context, userID int, profileID string, libraryID int, after, until time.Time, limit int) ([]Programme, error)
}

func (r *PostgresRepository) ListProgrammesNow(ctx context.Context, libraryID int, now time.Time, limit int) ([]Programme, error) {
	rows, err := r.pool.Query(ctx, `SELECT id, library_id, source_id, channel_id, external_id, stable_id, title, description, starts_at, ends_at, artwork, rating
FROM live_tv_programmes WHERE library_id = $1 AND starts_at <= $2 AND ends_at > $2
ORDER BY starts_at, stable_id LIMIT $3`, libraryID, now.UTC(), limit)
	if err != nil {
		return nil, fmt.Errorf("list currently airing Live TV programmes: %w", err)
	}
	defer rows.Close()
	result := make([]Programme, 0)
	for rows.Next() {
		programme, scanErr := scanProgramme(rows)
		if scanErr != nil {
			return nil, scanErr
		}
		result = append(result, programme)
	}
	return result, rows.Err()
}

func (r *PostgresRepository) AddFavorite(ctx context.Context, favorite Favorite) error {
	if !favorite.Kind.Valid() {
		return ErrInvalidFavoriteKind
	}
	if favorite.AddedAt.IsZero() {
		favorite.AddedAt = time.Now().UTC()
	}
	_, err := r.pool.Exec(ctx, `INSERT INTO live_tv_favorites (user_id, profile_id, library_id, entity_kind, stable_id, added_at)
VALUES ($1, $2, $3, $4, $5, $6) ON CONFLICT DO NOTHING`, favorite.UserID, favorite.ProfileID, favorite.LibraryID, favorite.Kind, favorite.StableID, favorite.AddedAt.UTC())
	if err != nil {
		return fmt.Errorf("add Live TV favorite: %w", err)
	}
	return nil
}

func (r *PostgresRepository) RemoveFavorite(ctx context.Context, favorite Favorite) error {
	if !favorite.Kind.Valid() {
		return ErrInvalidFavoriteKind
	}
	_, err := r.pool.Exec(ctx, `DELETE FROM live_tv_favorites
WHERE user_id = $1 AND profile_id = $2 AND library_id = $3 AND entity_kind = $4 AND stable_id = $5`, favorite.UserID, favorite.ProfileID, favorite.LibraryID, favorite.Kind, favorite.StableID)
	if err != nil {
		return fmt.Errorf("remove Live TV favorite: %w", err)
	}
	return nil
}

func (r *PostgresRepository) IsFavorite(ctx context.Context, favorite Favorite) (bool, error) {
	var exists bool
	err := r.pool.QueryRow(ctx, `SELECT EXISTS (SELECT 1 FROM live_tv_favorites
WHERE user_id = $1 AND profile_id = $2 AND library_id = $3 AND entity_kind = $4 AND stable_id = $5)`, favorite.UserID, favorite.ProfileID, favorite.LibraryID, favorite.Kind, favorite.StableID).Scan(&exists)
	if err != nil {
		return false, fmt.Errorf("check Live TV favorite: %w", err)
	}
	return exists, nil
}

func (r *PostgresRepository) ListFavorites(ctx context.Context, userID int, profileID string, libraryID int, kind FavoriteKind, limit, offset int) ([]Favorite, error) {
	if !kind.Valid() {
		return nil, ErrInvalidFavoriteKind
	}
	rows, err := r.pool.Query(ctx, `SELECT user_id, profile_id, library_id, entity_kind, stable_id, added_at
FROM live_tv_favorites WHERE user_id = $1 AND profile_id = $2 AND library_id = $3 AND entity_kind = $4
ORDER BY added_at DESC, stable_id LIMIT $5 OFFSET $6`, userID, profileID, libraryID, kind, limit, offset)
	if err != nil {
		return nil, fmt.Errorf("list Live TV favorites: %w", err)
	}
	defer rows.Close()
	result := make([]Favorite, 0)
	for rows.Next() {
		var favorite Favorite
		if err := rows.Scan(&favorite.UserID, &favorite.ProfileID, &favorite.LibraryID, &favorite.Kind, &favorite.StableID, &favorite.AddedAt); err != nil {
			return nil, fmt.Errorf("scan Live TV favorite: %w", err)
		}
		result = append(result, favorite)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate Live TV favorites: %w", err)
	}
	return result, nil
}

func (r *PostgresRepository) ListFavoriteChannelsNow(ctx context.Context, userID int, profileID string, libraryID int, now time.Time, limit int) ([]Channel, error) {
	rows, err := r.pool.Query(ctx, `SELECT c.id, c.library_id, c.source_id, c.external_id, c.stable_id, c.name, c.channel_number, c.category, c.stream_url, c.artwork, c.rating
FROM live_tv_favorites f JOIN live_tv_channels c ON c.library_id = f.library_id AND c.stable_id = f.stable_id
WHERE f.user_id = $1 AND f.profile_id = $2 AND f.library_id = $3 AND f.entity_kind = 'channel'
AND EXISTS (SELECT 1 FROM live_tv_programmes p WHERE p.channel_id = c.id AND p.starts_at <= $4 AND p.ends_at > $4)
ORDER BY f.added_at DESC, c.stable_id LIMIT $5`, userID, profileID, libraryID, now.UTC(), limit)
	if err != nil {
		return nil, fmt.Errorf("list current favorite Live TV channels: %w", err)
	}
	defer rows.Close()
	result := make([]Channel, 0)
	for rows.Next() {
		channel, scanErr := scanChannel(rows)
		if scanErr != nil {
			return nil, scanErr
		}
		result = append(result, channel)
	}
	return result, rows.Err()
}

func (r *PostgresRepository) ListFavoriteProgrammes(ctx context.Context, userID int, profileID string, libraryID int, from, to time.Time, ratedOnly bool, limit int) ([]Programme, error) {
	ratingPredicate := ""
	if ratedOnly {
		ratingPredicate = " AND jsonb_typeof(p.rating) IS NOT NULL AND p.rating <> '{}'::jsonb"
	}
	order := "p.starts_at, p.stable_id"
	if ratedOnly {
		order = "CASE WHEN (p.rating->>'value') ~ '^[0-9]+(\\.[0-9]+)?$' THEN (p.rating->>'value')::numeric END DESC NULLS LAST, p.starts_at, p.stable_id"
	}
	rows, err := r.pool.Query(ctx, `SELECT p.id, p.library_id, p.source_id, p.channel_id, p.external_id, p.stable_id, p.title, p.description, p.starts_at, p.ends_at, p.artwork, p.rating
FROM live_tv_favorites f JOIN live_tv_programmes p ON p.library_id = f.library_id AND p.stable_id = f.stable_id
WHERE f.user_id = $1 AND f.profile_id = $2 AND f.library_id = $3 AND f.entity_kind = 'programme'
AND p.starts_at < $5 AND p.ends_at > $4`+ratingPredicate+` ORDER BY `+order+` LIMIT $6`, userID, profileID, libraryID, from.UTC(), to.UTC(), limit)
	if err != nil {
		return nil, fmt.Errorf("list favorite Live TV programmes: %w", err)
	}
	defer rows.Close()
	result := make([]Programme, 0)
	for rows.Next() {
		programme, scanErr := scanProgramme(rows)
		if scanErr != nil {
			return nil, scanErr
		}
		result = append(result, programme)
	}
	return result, rows.Err()
}

func (r *PostgresRepository) ListFavoriteProgrammesNow(ctx context.Context, userID int, profileID string, libraryID int, now time.Time, limit int) ([]Programme, error) {
	rows, err := r.pool.Query(ctx, `SELECT p.id, p.library_id, p.source_id, p.channel_id, p.external_id, p.stable_id, p.title, p.description, p.starts_at, p.ends_at, p.artwork, p.rating
FROM live_tv_favorites f JOIN live_tv_programmes p ON p.library_id = f.library_id AND p.stable_id = f.stable_id
WHERE f.user_id = $1 AND f.profile_id = $2 AND f.library_id = $3 AND f.entity_kind = 'programme'
AND p.starts_at <= $4 AND p.ends_at > $4 ORDER BY p.starts_at, p.stable_id LIMIT $5`, userID, profileID, libraryID, now.UTC(), limit)
	if err != nil {
		return nil, fmt.Errorf("list current favorite Live TV programmes: %w", err)
	}
	defer rows.Close()
	result := make([]Programme, 0)
	for rows.Next() {
		programme, scanErr := scanProgramme(rows)
		if scanErr != nil {
			return nil, scanErr
		}
		result = append(result, programme)
	}
	return result, rows.Err()
}

func (r *PostgresRepository) ListUpcomingFavoriteProgrammes(ctx context.Context, userID int, profileID string, libraryID int, after, until time.Time, limit int) ([]Programme, error) {
	rows, err := r.pool.Query(ctx, `SELECT p.id, p.library_id, p.source_id, p.channel_id, p.external_id, p.stable_id, p.title, p.description, p.starts_at, p.ends_at, p.artwork, p.rating
FROM live_tv_favorites f JOIN live_tv_programmes p ON p.library_id = f.library_id AND p.stable_id = f.stable_id
WHERE f.user_id = $1 AND f.profile_id = $2 AND f.library_id = $3 AND f.entity_kind = 'programme'
AND p.starts_at > $4 AND p.starts_at < $5 ORDER BY p.starts_at, p.stable_id LIMIT $6`, userID, profileID, libraryID, after.UTC(), until.UTC(), limit)
	if err != nil {
		return nil, fmt.Errorf("list upcoming favorite Live TV programmes: %w", err)
	}
	defer rows.Close()
	result := make([]Programme, 0)
	for rows.Next() {
		programme, scanErr := scanProgramme(rows)
		if scanErr != nil {
			return nil, scanErr
		}
		result = append(result, programme)
	}
	return result, rows.Err()
}
