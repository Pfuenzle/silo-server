package api

import (
	"context"
	"errors"
	"github.com/Silo-Server/silo-server/internal/access"
	apimw "github.com/Silo-Server/silo-server/internal/api/middleware"
	"github.com/Silo-Server/silo-server/internal/autoscan"
	"github.com/Silo-Server/silo-server/internal/catalog"
	"github.com/Silo-Server/silo-server/internal/metadata"
	"github.com/Silo-Server/silo-server/internal/scantrigger"

	"github.com/Silo-Server/silo-server/internal/plugins"
	mediarequests "github.com/Silo-Server/silo-server/internal/requests"
)

// PluginRequestRouterAdapter adapts plugins.Service to
// mediarequests.RouterClientResolver. The concrete *pluginhost.RequestRouterClient
// satisfies mediarequests.RouterClient, so the adapter names the interface as its
// return type (Go has no return-type covariance).
type PluginRequestRouterAdapter struct {
	Svc *plugins.Service
}

func (a PluginRequestRouterAdapter) RequestRouterClient(ctx context.Context, installationID int, capabilityID string) (mediarequests.RouterClient, error) {
	if a.Svc == nil {
		return nil, errors.New("request router plugin service is not configured")
	}
	return a.Svc.RequestRouterClient(ctx, installationID, capabilityID)
}

// RouterFeatures reads the optional request_router.v1 features the
// capability's stored manifest declares, without launching the plugin.
func (a PluginRequestRouterAdapter) RouterFeatures(ctx context.Context, installationID int, capabilityID string) (mediarequests.RouterFeatures, error) {
	if a.Svc == nil {
		return mediarequests.RouterFeatures{}, errors.New("request router plugin service is not configured")
	}
	descriptor, err := a.Svc.RequestRouterDescriptor(ctx, installationID, capabilityID)
	if err != nil {
		return mediarequests.RouterFeatures{}, err
	}
	return mediarequests.RouterFeatures{
		SupportsSeasons:         descriptor.GetSupportsSeasons(),
		ReportsDownloadProgress: descriptor.GetReportsDownloadProgress(),
		APIKeyOptional:          descriptor.GetApiKeyOptional(),
		BaseURLOptional:         descriptor.GetBaseUrlOptional(),
	}, nil
}

// AttachRequestRouter wires the plugin-backed router provider onto a requests
// service. Both the HTTP handler and the reconcile task call this so the wiring
// lives in one place. With either dependency absent (e.g. a build without the
// plugin service) it leaves the requests service router-less, so fulfillment
// degrades to the existing "no backend configured" failure instead of panicking.
func AttachRequestRouter(svc *mediarequests.Service, pluginService *plugins.Service) {
	if svc == nil || pluginService == nil {
		return
	}
	svc.SetRouterProvider(mediarequests.NewPluginRouterProvider(PluginRequestRouterAdapter{pluginService}))
}

// ConfigureAudiobookRequests gives HTTP submissions the same provider search
// and scan-confirmed import lifecycle as the background reconcile service.
func ConfigureAudiobookRequests(svc *mediarequests.Service, deps Dependencies, items *catalog.ItemRepository, resolver apimw.ViewerResolver) {
	if deps.FolderRepo != nil && deps.LibraryScanQueue != nil && deps.FileRepo != nil && items != nil {
		linker := mediarequests.NewAudiobookImportLinker(deps.LibraryScanQueue, scantrigger.NewResolver(deps.FolderRepo), deps.FileRepo, items, nil)
		linker.SetPathMapperFactory(func(config map[string]any) (mediarequests.PathMapper, error) {
			return autoscan.NewPathMapperFromConfig(config)
		})
		svc.SetAudiobookImportLinker(linker)
	}
	svc.SetAudiobookSearcher(mediarequests.AudiobookSearchFunc(func(ctx context.Context, viewer mediarequests.Viewer, query string) ([]mediarequests.AudiobookSearchResult, error) {
		out := make([]mediarequests.AudiobookSearchResult, 0, 24)
		if resolver != nil && items != nil {
			scope, err := resolver.Resolve(ctx, access.ResolveInput{UserID: viewer.UserID, ProfileID: viewer.ProfileID, SkipPINVerification: true})
			if err != nil {
				return nil, err
			}
			filter := catalog.AccessFilter{UserID: viewer.UserID, ProfileID: viewer.ProfileID, MaturityLimits: scope.MaturityLimits, AllowedLibraryIDs: scope.AllowedLibraryIDs, DisabledLibraryIDs: scope.DisabledLibraryIDs}
			matches, _, err := items.Search(ctx, query, []string{"audiobook"}, 20, 0, filter)
			if err != nil {
				return nil, err
			}
			for _, item := range matches {
				out = append(out, mediarequests.AudiobookSearchResult{Provider: "silo", ProviderItemID: item.ContentID, Available: true, Title: item.Title, Year: item.Year, Overview: item.Overview})
			}
		}
		searcher, ok := deps.MetadataService.(interface {
			SearchAudiobookProviders(context.Context, string) ([]metadata.SearchResult, error)
		})
		if !ok {
			return out, nil
		}
		results, err := searcher.SearchAudiobookProviders(ctx, query)
		if err != nil {
			return nil, err
		}
		for _, result := range results {
			id := result.ProviderIDs[result.Provider]
			if id == "" || result.Name == "" {
				continue
			}
			out = append(out, mediarequests.AudiobookSearchResult{Provider: "audiobook-metadata", ProviderItemID: id, Title: result.Name, OriginalTitle: result.OriginalTitle, Year: result.Year, Overview: result.Overview, ImageURL: result.ImageURL})
		}
		return out, nil
	}))
}
