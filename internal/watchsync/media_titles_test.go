package watchsync

import (
	"testing"

	pluginv1 "github.com/Silo-Server/silo-plugin-sdk/pkg/pluginproto/silo/plugin/v1"
)

func TestEpisodeOrderingReachesPluginEventsWhenTitlesAlreadyPresent(t *testing.T) {
	repo := newServiceFakeRepo()
	repo.mediaTitles = map[string]MediaTitles{
		"season-two-episode-one": {
			Kind: "episode", Title: "Catalog episode", SeriesTitle: "Anime",
			AbsoluteEpisodeNumber: 13, SeriesEpisodeCount: 24,
		},
	}
	service := NewService(repo, NewRegistry())
	play := LocalPlay{
		MediaItemID: "season-two-episode-one", Kind: "episode", Title: "Existing title",
		SeasonNumber: 2, EpisodeNumber: 1,
	}
	input := []LocalPlay{play}
	plays := service.withPlayTitles(t.Context(), input)
	media := watchEventFromLocalPlay(plays[0], pluginv1.WatchSyncOrigin_WATCH_SYNC_ORIGIN_PLAYBACK_COMPLETION).GetMedia()
	if media.GetTitle() != "Existing title" || media.GetSeasonNumber() != 2 || media.GetEpisodeNumber() != 1 || media.GetAbsoluteEpisodeNumber() != 13 || media.GetSeriesEpisodeCount() != 24 {
		t.Fatalf("history media = %v", media)
	}
	if input[0].AbsoluteEpisodeNumber != 0 || input[0].SeriesEpisodeCount != 0 {
		t.Fatal("input play was modified")
	}
	event := service.withScrobbleTitles(t.Context(), ScrobbleEvent{
		MediaItemID: play.MediaItemID, Kind: "episode", Title: "Existing title",
		SeasonNumber: 2, EpisodeNumber: 1,
	})
	media = watchEventFromScrobble(event, pluginv1.WatchSyncOperation_WATCH_SYNC_OPERATION_SCROBBLE_STOP).GetMedia()
	if media.GetTitle() != "Existing title" || media.GetEpisodeNumber() != 1 || media.GetAbsoluteEpisodeNumber() != 13 || media.GetSeriesEpisodeCount() != 24 {
		t.Fatalf("scrobble media = %v", media)
	}
}

func TestEpisodeOrderingNeverCrossesCatalogIdentityKinds(t *testing.T) {
	event := scrobbleWithTitles(ScrobbleEvent{MediaItemID: "same-id", Kind: "movie"}, map[string]MediaTitles{
		"same-id": {Kind: "episode", AbsoluteEpisodeNumber: 13, SeriesEpisodeCount: 24},
	})
	if event.AbsoluteEpisodeNumber != 0 || event.SeriesEpisodeCount != 0 {
		t.Fatalf("episode ordering leaked into movie: %+v", event)
	}
}

func TestSeasonBoundaryOrderingReachesCompletedPluginScrobble(t *testing.T) {
	repo := newServiceFakeRepo()
	repo.mediaTitles = map[string]MediaTitles{
		"s2e1": {Kind: "episode", Title: "Episode one", SeriesTitle: "Anime", AbsoluteEpisodeNumber: 13, SeriesEpisodeCount: 24},
	}
	repo.scrobbleConnections = []Connection{{ID: "conn-1", Provider: testPluginProviderKey, UserID: 7, ProfileID: "profile-1", ScrobbleEnabled: true}}
	client := &fakeWatchSyncPluginClient{applyStatus: pluginv1.WatchSyncApplyStatus_WATCH_SYNC_APPLY_STATUS_APPLIED}
	registry := NewRegistry()
	if err := registry.Register(testPluginProviderWithDescriptor(t, client, &pluginv1.WatchSyncProviderDescriptor{
		AuthMethods:      []pluginv1.WatchSyncAuthMethod{pluginv1.WatchSyncAuthMethod_WATCH_SYNC_AUTH_METHOD_API_KEY},
		ScrobblePlayback: true, MaxBatchSize: 25,
	})); err != nil {
		t.Fatal(err)
	}
	if err := NewService(repo, registry).ScrobbleStopConfirmed(t.Context(), ScrobbleEvent{
		PlaybackSessionID: "boundary-playback", UserID: 7, ProfileID: "profile-1",
		MediaItemID: "s2e1", Kind: "episode", SeriesTMDBID: "123", SeasonNumber: 2, EpisodeNumber: 1,
		Title: "Already titled", Completed: true, PositionSeconds: 1440, DurationSeconds: 1440,
	}); err != nil {
		t.Fatal(err)
	}
	if client.applyRequest == nil || len(client.applyRequest.GetEvents()) != 1 {
		t.Fatalf("plugin request = %v", client.applyRequest)
	}
	media := client.applyRequest.GetEvents()[0].GetMedia()
	if media.GetSeasonNumber() != 2 || media.GetEpisodeNumber() != 1 || media.GetAbsoluteEpisodeNumber() != 13 || media.GetSeriesEpisodeCount() != 24 {
		t.Fatalf("dispatched media = %v", media)
	}
}
