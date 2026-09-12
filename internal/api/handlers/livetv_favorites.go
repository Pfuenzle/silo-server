package handlers

import (
	"errors"
	"net/http"
	"strings"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/jackc/pgx/v5"

	apimw "github.com/Silo-Server/silo-server/internal/api/middleware"
	"github.com/Silo-Server/silo-server/internal/livetv"
)

type liveTVFavoriteListResponse struct {
	Channels   []liveTVChannelResponse   `json:"channels,omitempty"`
	Programmes []liveTVProgrammeResponse `json:"programmes,omitempty"`
}

type liveTVHomeSectionsResponse struct {
	CurrentlyAiring            []liveTVProgrammeResponse `json:"currently_airing"`
	FavoriteChannelsAiring     []liveTVChannelResponse   `json:"favorite_channels_currently_airing"`
	FavoriteProgrammesAiring   []liveTVProgrammeResponse `json:"favorite_programmes_currently_airing"`
	TopRatedFavoriteProgrammes []liveTVProgrammeResponse `json:"top_rated_favorite_programmes"`
	UpcomingFavoriteProgrammes []liveTVProgrammeResponse `json:"upcoming_favorite_programmes"`
}

func (h *LiveTVHandler) favoriteScope(r *http.Request, kind livetv.FavoriteKind) (livetv.Favorite, error) {
	library, err := h.liveTVLibrary(r, true)
	if err != nil {
		return livetv.Favorite{}, err
	}
	claims := apimw.GetClaims(r.Context())
	profileID := apimw.GetProfileID(r.Context())
	if claims == nil || claims.UserID <= 0 || profileID == "" {
		return livetv.Favorite{}, &liveTVHTTPError{status: http.StatusUnauthorized, code: "unauthorized", message: "Authentication required"}
	}
	return livetv.Favorite{UserID: claims.UserID, ProfileID: profileID, LibraryID: library.ID, Kind: kind}, nil
}

func (h *LiveTVHandler) favoriteIdentity(r *http.Request, kind livetv.FavoriteKind, stableID string) (livetv.Favorite, error) {
	favorite, err := h.favoriteScope(r, kind)
	if err != nil {
		return livetv.Favorite{}, err
	}
	if !kind.Valid() || stableID == "" {
		return livetv.Favorite{}, &liveTVHTTPError{status: http.StatusBadRequest, code: "bad_request", message: "Invalid favorite identity"}
	}
	sourceKey, externalID, found := strings.Cut(stableID, "|")
	if !found {
		return livetv.Favorite{}, &liveTVHTTPError{status: http.StatusBadRequest, code: "bad_request", message: "Invalid favorite identity"}
	}
	parsedID, parseErr := livetv.NewSourceQualifiedID(sourceKey, externalID)
	if parseErr != nil || parsedID.String() != stableID {
		return livetv.Favorite{}, &liveTVHTTPError{status: http.StatusBadRequest, code: "bad_request", message: "Invalid favorite identity"}
	}
	favorite.StableID = parsedID
	favorite.AddedAt = time.Now().UTC()
	return favorite, nil
}

func (h *LiveTVHandler) mutateFavorite(w http.ResponseWriter, r *http.Request, kind livetv.FavoriteKind, stableID string, remove bool) {
	favorite, err := h.favoriteIdentity(r, kind, stableID)
	if err != nil {
		writeLiveTVError(w, err)
		return
	}
	if !remove {
		if kind == livetv.FavoriteKindChannel {
			_, err = h.repo.GetChannel(r.Context(), favorite.LibraryID, favorite.StableID)
		} else {
			_, err = h.repo.GetProgramme(r.Context(), favorite.LibraryID, favorite.StableID)
		}
		if errors.Is(err, pgx.ErrNoRows) {
			writeError(w, http.StatusNotFound, "not_found", "Live TV entity not found")
			return
		}
		if err != nil {
			writeError(w, http.StatusInternalServerError, "internal_error", "Failed to resolve Live TV entity")
			return
		}
	}
	if remove {
		err = h.repo.RemoveFavorite(r.Context(), favorite)
	} else {
		err = h.repo.AddFavorite(r.Context(), favorite)
	}
	if err != nil {
		writeError(w, http.StatusInternalServerError, "internal_error", "Failed to update Live TV favorite")
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func (h *LiveTVHandler) HandleAddFavoriteChannel(w http.ResponseWriter, r *http.Request) {
	h.mutateFavorite(w, r, livetv.FavoriteKindChannel, chi.URLParam(r, "channel_id"), false)
}

func (h *LiveTVHandler) HandleRemoveFavoriteChannel(w http.ResponseWriter, r *http.Request) {
	h.mutateFavorite(w, r, livetv.FavoriteKindChannel, chi.URLParam(r, "channel_id"), true)
}

func (h *LiveTVHandler) HandleAddFavoriteProgramme(w http.ResponseWriter, r *http.Request) {
	h.mutateFavorite(w, r, livetv.FavoriteKindProgramme, chi.URLParam(r, "programme_id"), false)
}

func (h *LiveTVHandler) HandleRemoveFavoriteProgramme(w http.ResponseWriter, r *http.Request) {
	h.mutateFavorite(w, r, livetv.FavoriteKindProgramme, chi.URLParam(r, "programme_id"), true)
}

func (h *LiveTVHandler) HandleListFavoriteChannels(w http.ResponseWriter, r *http.Request) {
	favorite, err := h.favoriteScope(r, livetv.FavoriteKindChannel)
	if err != nil {
		writeLiveTVError(w, err)
		return
	}
	favorites, err := h.repo.ListFavorites(r.Context(), favorite.UserID, favorite.ProfileID, favorite.LibraryID, favorite.Kind, liveTVMaxPageSize, 0)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "internal_error", "Failed to list Live TV favorites")
		return
	}
	response := liveTVFavoriteListResponse{Channels: make([]liveTVChannelResponse, 0, len(favorites))}
	for _, entry := range favorites {
		channel, getErr := h.repo.GetChannel(r.Context(), entry.LibraryID, entry.StableID)
		if getErr != nil {
			continue
		}
		response.Channels = append(response.Channels, liveTVChannelResponse{ID: channel.StableID.String(), Name: channel.Name, Number: channel.Number, Category: string(channel.Category), Artwork: channel.Artwork, Rating: channel.Rating})
	}
	writeJSON(w, http.StatusOK, response)
}

func (h *LiveTVHandler) HandleListFavoriteProgrammes(w http.ResponseWriter, r *http.Request) {
	favorite, err := h.favoriteScope(r, livetv.FavoriteKindProgramme)
	if err != nil {
		writeLiveTVError(w, err)
		return
	}
	favorites, err := h.repo.ListFavorites(r.Context(), favorite.UserID, favorite.ProfileID, favorite.LibraryID, favorite.Kind, liveTVMaxPageSize, 0)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "internal_error", "Failed to list Live TV favorites")
		return
	}
	response := liveTVFavoriteListResponse{Programmes: make([]liveTVProgrammeResponse, 0, len(favorites))}
	for _, entry := range favorites {
		programme, getErr := h.repo.GetProgramme(r.Context(), entry.LibraryID, entry.StableID)
		if getErr != nil {
			continue
		}
		response.Programmes = append(response.Programmes, h.programmeResponse(r.Context(), programme, nil))
	}
	writeJSON(w, http.StatusOK, response)
}

func (h *LiveTVHandler) HandleHomeSections(w http.ResponseWriter, r *http.Request) {
	favorite, err := h.favoriteScope(r, livetv.FavoriteKindProgramme)
	if err != nil {
		writeLiveTVError(w, err)
		return
	}
	now := time.Now().UTC()
	channels, err := h.repo.ListFavoriteChannelsNow(r.Context(), favorite.UserID, favorite.ProfileID, favorite.LibraryID, now, liveTVDefaultPageSize)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "internal_error", "Failed to load Live TV sections")
		return
	}
	allCurrent, err := h.repo.ListProgrammesNow(r.Context(), favorite.LibraryID, now, liveTVDefaultPageSize)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "internal_error", "Failed to load Live TV sections")
		return
	}
	channelsForCurrent, _, err := h.repo.ListChannels(r.Context(), favorite.LibraryID, liveTVMaxPageSize, 0)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "internal_error", "Failed to load Live TV sections")
		return
	}
	channelsByID := make(map[int64]*livetv.Channel, len(channelsForCurrent))
	for index := range channelsForCurrent {
		channelsByID[channelsForCurrent[index].ID] = &channelsForCurrent[index]
	}
	current, err := h.repo.ListFavoriteProgrammesNow(r.Context(), favorite.UserID, favorite.ProfileID, favorite.LibraryID, now, liveTVDefaultPageSize)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "internal_error", "Failed to load Live TV sections")
		return
	}
	upcoming, err := h.repo.ListUpcomingFavoriteProgrammes(r.Context(), favorite.UserID, favorite.ProfileID, favorite.LibraryID, now, now.Add(24*time.Hour), liveTVDefaultPageSize)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "internal_error", "Failed to load Live TV sections")
		return
	}
	topRated, err := h.repo.ListFavoriteProgrammes(r.Context(), favorite.UserID, favorite.ProfileID, favorite.LibraryID, now, now.Add(24*time.Hour), true, liveTVDefaultPageSize)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "internal_error", "Failed to load Live TV sections")
		return
	}
	response := liveTVHomeSectionsResponse{CurrentlyAiring: make([]liveTVProgrammeResponse, 0, len(allCurrent)), FavoriteChannelsAiring: make([]liveTVChannelResponse, 0, len(channels)), FavoriteProgrammesAiring: make([]liveTVProgrammeResponse, 0, len(current)), TopRatedFavoriteProgrammes: make([]liveTVProgrammeResponse, 0, len(topRated)), UpcomingFavoriteProgrammes: make([]liveTVProgrammeResponse, 0, len(upcoming))}
	for _, programme := range allCurrent {
		response.CurrentlyAiring = append(response.CurrentlyAiring, h.programmeResponse(r.Context(), programme, channelsByID[programme.ChannelID]))
	}
	for _, channel := range channels {
		response.FavoriteChannelsAiring = append(response.FavoriteChannelsAiring, liveTVChannelResponse{ID: channel.StableID.String(), Name: channel.Name, Number: channel.Number, Category: string(channel.Category), Artwork: h.authorizeArtwork(r.Context(), channel.Artwork), Rating: channel.Rating})
	}
	for _, programme := range current {
		response.FavoriteProgrammesAiring = append(response.FavoriteProgrammesAiring, h.programmeResponse(r.Context(), programme, nil))
	}
	for _, programme := range topRated {
		response.TopRatedFavoriteProgrammes = append(response.TopRatedFavoriteProgrammes, h.programmeResponse(r.Context(), programme, nil))
	}
	for _, programme := range upcoming {
		response.UpcomingFavoriteProgrammes = append(response.UpcomingFavoriteProgrammes, h.programmeResponse(r.Context(), programme, nil))
	}
	writeJSON(w, http.StatusOK, response)
}
