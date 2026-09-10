package requests

import pluginv1 "github.com/Silo-Server/silo-plugin-sdk/pkg/pluginproto/silo/plugin/v1"

func routerMetadataFromProto(metadata *pluginv1.RouterMetadata) *RouterMetadata {
	if metadata == nil {
		return nil
	}
	return &RouterMetadata{
		ExternalCorrelationID: metadata.GetExternalCorrelationId(),
		StatusText:            metadata.GetStatusText(),
		ExternalURL:           metadata.GetExternalUrl(),
		LibraryURL:            metadata.GetLibraryUrl(),
		ImportedPath:          metadata.GetImportedPath(),
		ScanLinkState:         ScanLinkState(metadata.GetScanLinkState()),
	}
}
