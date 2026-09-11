-- +goose Up
-- +goose StatementBegin
CREATE TABLE public.live_tv_sources (
    id bigint GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
    library_id integer NOT NULL REFERENCES public.media_folders(id) ON DELETE CASCADE,
    kind text NOT NULL CHECK (kind IN ('playlist', 'epg')),
    source_key text NOT NULL CHECK (btrim(source_key) <> ''),
    name text NOT NULL CHECK (btrim(name) <> ''),
    location text NOT NULL CHECK (btrim(location) <> ''),
    config jsonb NOT NULL DEFAULT '{}'::jsonb,
    enabled boolean NOT NULL DEFAULT true,
    refresh_state text NOT NULL DEFAULT 'never' CHECK (refresh_state IN ('never', 'running', 'ready', 'stale', 'error')),
    refresh_error text NOT NULL DEFAULT '',
    last_refresh_at timestamp with time zone,
    created_at timestamp with time zone NOT NULL DEFAULT now(),
    updated_at timestamp with time zone NOT NULL DEFAULT now(),
    UNIQUE (library_id, source_key),
    UNIQUE (id, library_id)
);

CREATE TABLE public.live_tv_channels (
    id bigint GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
    library_id integer NOT NULL REFERENCES public.media_folders(id) ON DELETE CASCADE,
    source_id bigint NOT NULL REFERENCES public.live_tv_sources(id) ON DELETE CASCADE,
    external_id text NOT NULL CHECK (btrim(external_id) <> ''),
    stable_id text NOT NULL CHECK (btrim(stable_id) <> ''),
    name text NOT NULL,
    channel_number text NOT NULL DEFAULT '',
    category text NOT NULL DEFAULT '',
    stream_url text NOT NULL DEFAULT '',
    artwork jsonb NOT NULL DEFAULT '{}'::jsonb,
    rating jsonb NOT NULL DEFAULT '{}'::jsonb,
    created_at timestamp with time zone NOT NULL DEFAULT now(),
    updated_at timestamp with time zone NOT NULL DEFAULT now(),
    UNIQUE (source_id, external_id),
    UNIQUE (library_id, stable_id),
    UNIQUE (id, library_id),
    FOREIGN KEY (source_id, library_id) REFERENCES public.live_tv_sources(id, library_id) ON DELETE CASCADE
);

CREATE TABLE public.live_tv_programmes (
    id bigint GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
    library_id integer NOT NULL REFERENCES public.media_folders(id) ON DELETE CASCADE,
    source_id bigint NOT NULL REFERENCES public.live_tv_sources(id) ON DELETE CASCADE,
    channel_id bigint NOT NULL,
    external_id text NOT NULL CHECK (btrim(external_id) <> ''),
    stable_id text NOT NULL CHECK (btrim(stable_id) <> ''),
    title text NOT NULL,
    description text NOT NULL DEFAULT '',
    starts_at timestamp with time zone NOT NULL,
    ends_at timestamp with time zone NOT NULL,
    artwork jsonb NOT NULL DEFAULT '{}'::jsonb,
    rating jsonb NOT NULL DEFAULT '{}'::jsonb,
    created_at timestamp with time zone NOT NULL DEFAULT now(),
    updated_at timestamp with time zone NOT NULL DEFAULT now(),
    CHECK (ends_at > starts_at),
    UNIQUE (source_id, external_id, starts_at),
    UNIQUE (library_id, stable_id),
    FOREIGN KEY (source_id, library_id) REFERENCES public.live_tv_sources(id, library_id) ON DELETE CASCADE,
    FOREIGN KEY (channel_id, library_id) REFERENCES public.live_tv_channels(id, library_id) ON DELETE CASCADE
);

CREATE TABLE public.live_tv_channel_epg_mappings (
    library_id integer NOT NULL REFERENCES public.media_folders(id) ON DELETE CASCADE,
    channel_id bigint NOT NULL REFERENCES public.live_tv_channels(id) ON DELETE CASCADE,
    epg_source_id bigint NOT NULL REFERENCES public.live_tv_sources(id) ON DELETE CASCADE,
    epg_channel_id text NOT NULL CHECK (btrim(epg_channel_id) <> ''),
    created_at timestamp with time zone NOT NULL DEFAULT now(),
    PRIMARY KEY (channel_id, epg_source_id),
    UNIQUE (epg_source_id, epg_channel_id),
    FOREIGN KEY (channel_id, library_id) REFERENCES public.live_tv_channels(id, library_id) ON DELETE CASCADE,
    FOREIGN KEY (epg_source_id, library_id) REFERENCES public.live_tv_sources(id, library_id) ON DELETE CASCADE
);

CREATE INDEX live_tv_sources_library_idx ON public.live_tv_sources (library_id, enabled, id);
CREATE INDEX live_tv_channels_library_number_idx ON public.live_tv_channels (library_id, channel_number, name);
CREATE INDEX live_tv_programmes_channel_time_idx ON public.live_tv_programmes (channel_id, starts_at, ends_at);
CREATE INDEX live_tv_programmes_library_time_idx ON public.live_tv_programmes (library_id, starts_at, ends_at);
-- Live TV owns normalized rows only; no media_items, media_files, or media_folder_paths rows are created.
-- +goose StatementEnd

-- +goose StatementBegin
CREATE TABLE public.live_tv_favorites (
    user_id integer NOT NULL REFERENCES public.users(id) ON DELETE CASCADE,
    profile_id text NOT NULL,
    library_id integer NOT NULL REFERENCES public.media_folders(id) ON DELETE CASCADE,
    entity_kind text NOT NULL CHECK (entity_kind IN ('channel', 'programme')),
    stable_id text NOT NULL CHECK (btrim(stable_id) <> ''),
    added_at timestamp with time zone NOT NULL DEFAULT now(),
    PRIMARY KEY (user_id, profile_id, library_id, entity_kind, stable_id),
    FOREIGN KEY (user_id, profile_id) REFERENCES public.user_profiles(user_id, id) ON DELETE CASCADE
);

CREATE INDEX live_tv_favorites_profile_idx
    ON public.live_tv_favorites (user_id, profile_id, library_id, entity_kind, added_at DESC);
-- Favorite rows intentionally retain stable IDs when a refresh temporarily omits an entity.
-- Reads inner-join the current snapshot, so orphaned favorites remain hidden and can reappear.
-- +goose StatementEnd

-- +goose Down
-- +goose StatementBegin
DROP TABLE IF EXISTS public.live_tv_favorites;
DROP TABLE IF EXISTS public.live_tv_channel_epg_mappings;
DROP TABLE IF EXISTS public.live_tv_programmes;
DROP TABLE IF EXISTS public.live_tv_channels;
DROP TABLE IF EXISTS public.live_tv_sources;
-- +goose StatementEnd
