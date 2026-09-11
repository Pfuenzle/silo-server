package livetv

type SectionKind string

const (
	SectionCurrentlyAiring            SectionKind = "currently_airing"
	SectionFavoriteChannelsAiring     SectionKind = "favorite_channels_currently_airing"
	SectionFavoriteProgrammesAiring   SectionKind = "favorite_programmes_currently_airing"
	SectionTopRatedFavoriteProgrammes SectionKind = "top_rated_favorite_programmes"
	SectionUpcomingFavoriteProgrammes SectionKind = "upcoming_favorite_programmes"
)

var liveTVSectionKinds = []SectionKind{
	SectionCurrentlyAiring,
	SectionFavoriteChannelsAiring,
	SectionFavoriteProgrammesAiring,
	SectionTopRatedFavoriteProgrammes,
	SectionUpcomingFavoriteProgrammes,
}

func LiveTVSectionKinds() []SectionKind {
	return append([]SectionKind(nil), liveTVSectionKinds...)
}

func (k SectionKind) Valid() bool {
	for _, supported := range liveTVSectionKinds {
		if k == supported {
			return true
		}
	}
	return false
}
