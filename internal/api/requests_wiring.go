package api

import (
	"context"
	"errors"

	"github.com/Silo-Server/silo-server/internal/autoscan"
	"github.com/Silo-Server/silo-server/internal/catalog"
	"github.com/Silo-Server/silo-server/internal/plugins"
	mediarequests "github.com/Silo-Server/silo-server/internal/requests"
	"github.com/Silo-Server/silo-server/internal/scanner"
	"github.com/Silo-Server/silo-server/internal/scanqueue"
	"github.com/Silo-Server/silo-server/internal/scantrigger"
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

func AttachAudiobookImport(svc *mediarequests.Service, queue *scanqueue.Service, folders *catalog.FolderRepository, files *scanner.FileRepository, items *catalog.ItemRepository) error {
	if svc == nil || queue == nil || folders == nil || files == nil || items == nil {
		return nil
	}
	linker := mediarequests.NewAudiobookImportLinker(queue, scantrigger.NewResolver(folders), files, items, nil)
	linker.SetPathMapperFactory(func(raw map[string]any) (mediarequests.PathMapper, error) {
		return autoscan.NewPathMapperFromConfig(raw)
	})
	svc.SetAudiobookImportLinker(linker)
	return nil
}
