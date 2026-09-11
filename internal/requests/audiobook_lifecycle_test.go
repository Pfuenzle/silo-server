package requests

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/Silo-Server/silo-server/internal/models"
)

func TestAudiobookApprovalRoutesThroughGenericProviderOnce(t *testing.T) {
	// Given an unapproved audiobook and a configured generic router.
	store := newFakeStore()
	store.integrations = []Integration{routerInst("router-1")}
	request := &Request{
		ID: "book-1", Provider: "audiobook-metadata", ProviderItemID: "aud-1",
		MediaType: MediaTypeAudiobook, Title: "The Book", Status: StatusPending,
		Outcome: OutcomeActive, RequestedByUserID: 1,
	}
	store.requests[request.ID] = request
	router := &fakeRouterProvider{targetsOverride: []RouterTarget{{
		Quality: Quality1080p, ConnectionID: "router-1", ExternalID: "download-1",
		ExternalStatus: "queued", Status: StatusQueued,
		Metadata: &RouterMetadata{ExternalCorrelationID: "library-1", StatusText: "Queued"},
	}}}
	service := newTestService(store)
	service.SetRouterProvider(router)

	// When an administrator approves the request.
	got, err := service.Approve(context.Background(), Viewer{UserID: 1, IsAdmin: true}, request.ID)

	// Then generic fulfillment is called once and lifecycle IDs are durable.
	if err != nil {
		t.Fatalf("Approve() error = %v", err)
	}
	if router.fulfillCalls != 1 {
		t.Fatalf("fulfill calls = %d, want 1", router.fulfillCalls)
	}
	if got.ExternalLibraryID != "library-1" || got.ExternalDownloadID != "download-1" {
		t.Fatalf("lifecycle IDs = %+v, want library-1/download-1", got)
	}
}

func TestAudiobookTerminalFulfillWithoutImportedPathIsRetryable(t *testing.T) {
	// Given an approved audiobook whose generic provider reports completion without
	// the metadata required to map, scan, and link the imported file.
	store := newFakeStore()
	store.integrations = []Integration{routerInst("router-1")}
	request := &Request{
		ID: "book-1", Provider: "audiobook-metadata", ProviderItemID: "aud-1",
		MediaType: MediaTypeAudiobook, Title: "The Book", Status: StatusPending,
		Outcome: OutcomeActive,
	}
	store.requests[request.ID] = request
	router := &fakeRouterProvider{targetsOverride: []RouterTarget{{
		Quality: Quality1080p, ConnectionID: "router-1", ExternalID: "download-1",
		ExternalStatus: "completed", Status: StatusCompleted,
	}}}
	queue := &importQueue{run: &models.ScanRun{ID: "scan-1", Status: "completed"}}
	service := newTestService(store)
	service.SetRouterProvider(router)
	service.SetAudiobookImportLinker(newImportLinker(queue, importFiles{contentID: "book-1"}, importItems{
		item: &models.MediaItem{ContentID: "book-1", Type: "audiobook"},
	}))

	// When an administrator approves the request.
	got, err := service.Approve(context.Background(), Viewer{UserID: 1, IsAdmin: true}, request.ID)

	// Then completion is not claimed before generic import processing succeeds.
	if err != nil {
		t.Fatalf("Approve() error = %v", err)
	}
	if got.Outcome != OutcomeFailed || !got.Retryable {
		t.Fatalf("request = %+v, want retryable failure", got)
	}
	if got.Status != StatusQueued || got.SubmissionState != SubmissionStateFailed {
		t.Fatalf("request = %+v, want queued failed-submission aggregate", got)
	}
	if queue.enqueues != 0 || queue.waits != 0 {
		t.Fatalf("scan queue = %+v, want no scan for malformed metadata", queue)
	}
}

func TestAudiobookReconcileUsesPersistedIDsAfterRestart(t *testing.T) {
	// Given a request restored after restart with an existing external target.
	store := newFakeStore()
	store.integrations = []Integration{routerInst("router-1")}
	request := &Request{
		ID: "book-1", Provider: "audiobook-metadata", ProviderItemID: "aud-1",
		MediaType: MediaTypeAudiobook, Title: "The Book", Status: StatusDownloading,
		Outcome: OutcomeActive, ExternalLibraryID: "library-1", ExternalDownloadID: "download-1",
	}
	store.requests[request.ID] = request
	store.candidates = []*Request{request}
	if _, err := store.CreateTarget(context.Background(), Target{
		RequestID: request.ID, IntegrationID: "router-1", Quality: Quality1080p,
		ExternalID: "download-1", ExternalStatus: "downloading", Status: StatusDownloading,
	}); err != nil {
		t.Fatalf("CreateTarget() error = %v", err)
	}
	router := &fakeRouterProvider{statuses: []RouterTargetStatus{{
		Quality: Quality1080p, ConnectionID: "router-1", Status: StatusDownloading,
		ExternalStatus: "downloading", Metadata: &RouterMetadata{
			ExternalCorrelationID: "library-1", StatusText: "Still downloading",
		},
	}}}

	// When a new service instance reconciles the restored row.
	service := newTestService(store)
	service.SetRouterProvider(router)
	_, err := service.ReconcileRequests(context.Background(), 100)

	// Then status resumes through the persisted target without re-fulfillment.
	if err != nil {
		t.Fatalf("ReconcileRequests() error = %v", err)
	}
	if router.fulfillCalls != 0 || router.statusCalls != 1 {
		t.Fatalf("fulfill/status calls = %d/%d, want 0/1", router.fulfillCalls, router.statusCalls)
	}
	if store.requests[request.ID].ExternalDetail != "Still downloading" {
		t.Fatalf("external detail = %q, want persisted status detail", store.requests[request.ID].ExternalDetail)
	}
}

func TestAudiobookRepeatedReconcileDoesNotFulfillAgain(t *testing.T) {
	// Given an audiobook that was approved and already has its external target.
	store := newFakeStore()
	store.integrations = []Integration{routerInst("router-1")}
	request := &Request{
		ID: "book-1", Provider: "audiobook-metadata", ProviderItemID: "aud-1",
		MediaType: MediaTypeAudiobook, Title: "The Book", Status: StatusQueued,
		Outcome: OutcomeActive, ExternalLibraryID: "library-1", ExternalDownloadID: "download-1",
	}
	store.requests[request.ID] = request
	store.candidates = []*Request{request}
	if _, err := store.CreateTarget(context.Background(), Target{
		RequestID: request.ID, IntegrationID: "router-1", Quality: Quality1080p,
		ExternalID: "download-1", ExternalStatus: "queued", Status: StatusQueued,
	}); err != nil {
		t.Fatalf("CreateTarget() error = %v", err)
	}
	router := &fakeRouterProvider{statuses: []RouterTargetStatus{{
		Quality: Quality1080p, ConnectionID: "router-1", Status: StatusQueued,
		ExternalStatus: "queued",
	}}}
	service := newTestService(store)
	service.SetRouterProvider(router)

	// When the same persisted request is reconciled twice.
	for range 2 {
		if _, err := service.ReconcileRequests(context.Background(), 100); err != nil {
			t.Fatalf("ReconcileRequests() error = %v", err)
		}
	}

	// Then polling repeats, but external creation never does.
	if router.fulfillCalls != 0 || router.statusCalls != 2 {
		t.Fatalf("fulfill/status calls = %d/%d, want 0/2", router.fulfillCalls, router.statusCalls)
	}
}

func TestAudiobookSparseStatusMetadataDoesNotEraseEarlierLifecycleState(t *testing.T) {
	// Given two live audiobook targets and metadata on only the first status.
	store := newFakeStore()
	store.integrations = []Integration{routerInst("router-1")}
	request := &Request{
		ID: "book-1", Provider: "audiobook-metadata", ProviderItemID: "aud-1",
		MediaType: MediaTypeAudiobook, Status: StatusDownloading, Outcome: OutcomeActive,
	}
	store.requests[request.ID] = request
	store.candidates = []*Request{request}
	for _, quality := range []Quality{Quality1080p, Quality2160p} {
		if _, err := store.CreateTarget(context.Background(), Target{
			RequestID: request.ID, IntegrationID: "router-1", Quality: quality,
			ExternalID: string(quality), Status: StatusDownloading,
		}); err != nil {
			t.Fatalf("CreateTarget() error = %v", err)
		}
	}
	router := &fakeRouterProvider{statuses: []RouterTargetStatus{
		{Quality: Quality1080p, ConnectionID: "router-1", Status: StatusDownloading,
			Metadata: &RouterMetadata{ExternalCorrelationID: "library-1", ImportedPath: "/mapped/book"}},
		{Quality: Quality2160p, ConnectionID: "router-1", Status: StatusDownloading},
	}}
	service := newTestService(store)
	service.SetRouterProvider(router)

	// When one reconcile cycle receives both statuses.
	if _, err := service.ReconcileRequests(context.Background(), 100); err != nil {
		t.Fatalf("ReconcileRequests() error = %v", err)
	}

	// Then sparse metadata from the second target does not erase the first.
	got := store.requests[request.ID]
	if got.ExternalLibraryID != "library-1" || got.ImportedPath != "/mapped/book" {
		t.Fatalf("lifecycle = %+v, want first target metadata retained", got)
	}
}

func TestAudiobookUnapprovedNeverCallsProvider(t *testing.T) {
	// Given an unapproved audiobook in the reconciliation candidate source.
	store := newFakeStore()
	store.integrations = []Integration{routerInst("router-1")}
	request := &Request{
		ID: "book-1", Provider: "audiobook-metadata", ProviderItemID: "aud-1",
		MediaType: MediaTypeAudiobook, Title: "The Book", Status: StatusPending,
		Outcome: OutcomeActive,
	}
	store.requests[request.ID] = request
	store.candidates = []*Request{request}
	router := &fakeRouterProvider{}
	service := newTestService(store)
	service.SetRouterProvider(router)

	// When reconciliation sees the pending request.
	if _, err := service.ReconcileRequests(context.Background(), 100); err != nil {
		t.Fatalf("ReconcileRequests() error = %v", err)
	}

	// Then approval remains the only route into generic fulfillment.
	if router.fulfillCalls != 0 || router.statusCalls != 0 {
		t.Fatalf("fulfill/status calls = %d/%d, want 0/0", router.fulfillCalls, router.statusCalls)
	}
}

func TestAudiobookExternalFailureRetainsCorrelationAndIsRetryable(t *testing.T) {
	// Given an approved audiobook whose generic provider reports a terminal failure.
	store := newFakeStore()
	store.integrations = []Integration{routerInst("router-1")}
	request := &Request{
		ID: "book-1", Provider: "audiobook-metadata", ProviderItemID: "aud-1",
		MediaType: MediaTypeAudiobook, Title: "The Book", Status: StatusQueued,
		Outcome: OutcomeActive, ExternalLibraryID: "library-1", ExternalDownloadID: "download-1",
	}
	store.requests[request.ID] = request
	store.candidates = []*Request{request}
	if _, err := store.CreateTarget(context.Background(), Target{
		RequestID: request.ID, IntegrationID: "router-1", Quality: Quality1080p,
		ExternalID: "download-1", ExternalStatus: "queued", Status: StatusQueued,
	}); err != nil {
		t.Fatalf("CreateTarget() error = %v", err)
	}
	router := &fakeRouterProvider{statuses: []RouterTargetStatus{{
		Quality: Quality1080p, ConnectionID: "router-1", Status: StatusFailed,
		ExternalStatus: "failed", Message: "indexer unavailable",
	}}}
	service := newTestService(store)
	service.SetRouterProvider(router)

	// When reconciliation observes the external failure.
	_, err := service.ReconcileRequests(context.Background(), 100)

	// Then the correlation IDs survive and an explicit retry remains possible.
	if err != nil {
		t.Fatalf("ReconcileRequests() error = %v", err)
	}
	failed := store.requests[request.ID]
	if failed.Outcome != OutcomeFailed || !failed.Retryable || failed.ExternalDownloadID != "download-1" {
		t.Fatalf("failed request = %+v, want retryable failure with correlation", failed)
	}
	if _, err := service.Retry(context.Background(), Viewer{UserID: 1, IsAdmin: true}, request.ID); err != nil {
		t.Fatalf("Retry() error = %v, want retryable request", err)
	}
}

func TestAudiobookMissingProviderTargetIsRetryable(t *testing.T) {
	// Given an approved audiobook whose provider returns no usable target.
	store := newFakeStore()
	store.integrations = []Integration{routerInst("router-1")}
	request := &Request{
		ID: "book-1", Provider: "audiobook-metadata", ProviderItemID: "aud-1",
		MediaType: MediaTypeAudiobook, Title: "The Book", Status: StatusPending,
		Outcome: OutcomeActive,
	}
	store.requests[request.ID] = request
	router := &fakeRouterProvider{noTargets: true, fulfillMsg: "provider unavailable"}
	service := newTestService(store)
	service.SetRouterProvider(router)

	// When approval submits the request.
	got, err := service.Approve(context.Background(), Viewer{UserID: 1, IsAdmin: true}, request.ID)

	// Then the terminal failure has durable detail and can be retried.
	if err != nil {
		t.Fatalf("Approve() error = %v", err)
	}
	if got.Outcome != OutcomeFailed || !got.Retryable || got.ExternalDetail != "provider unavailable" {
		t.Fatalf("request = %+v, want retryable provider failure", got)
	}
}

func TestAudiobookProviderErrorIsRetryable(t *testing.T) {
	// Given an approved audiobook and a provider transport failure.
	store := newFakeStore()
	store.integrations = []Integration{routerInst("router-1")}
	request := &Request{
		ID: "book-1", Provider: "audiobook-metadata", ProviderItemID: "aud-1",
		MediaType: MediaTypeAudiobook, Title: "The Book", Status: StatusPending,
		Outcome: OutcomeActive,
	}
	store.requests[request.ID] = request
	service := newTestService(store)
	service.SetRouterProvider(&fakeRouterProvider{fulfillErr: errors.New("provider unavailable")})

	// When approval crosses the failing provider boundary.
	got, err := service.Approve(context.Background(), Viewer{UserID: 1, IsAdmin: true}, request.ID)

	// Then the request is visibly failed and retryable.
	if err != nil {
		t.Fatalf("Approve() error = %v", err)
	}
	if got.Outcome != OutcomeFailed || !got.Retryable || got.ExternalDetail != "provider unavailable" {
		t.Fatalf("request = %+v, want retryable provider error", got)
	}
}

func TestAudiobookInterruptionReplaysStableKeyWithoutDuplicateProviderCalls(t *testing.T) {
	// Given a generic provider and a local target persistence interruption.

	store := newFakeStore()
	store.integrations = []Integration{routerInst("router-1")}
	request := &Request{
		ID: "book-1", Provider: "audiobook-metadata", ProviderItemID: "aud-1",
		MediaType: MediaTypeAudiobook, Title: "The Book", Status: StatusPending,
		Outcome: OutcomeActive,
	}
	store.requests[request.ID] = request
	store.createTargetFailures = 1
	router := &idempotentRouter{executions: make(map[string]int), seen: make(map[string]bool)}
	first := newTestService(store)
	first.SetRouterProvider(router)
	first.SetEntitlementResolver(fixedCeiling{q: "1080p"})

	// When the first process receives external success but fails before target persistence.
	if _, err := first.Approve(context.Background(), Viewer{UserID: 1, IsAdmin: true}, request.ID); err == nil {
		t.Fatal("Approve() error = nil, want interrupted local persistence")
	}
	key := store.requests[request.ID].FulfillmentKey
	if key == "" || store.requests[request.ID].SubmissionState != SubmissionStateInFlight {
		t.Fatalf("submission state = %+v, want durable in-flight key", store.requests[request.ID])
	}

	// When a new process reclaims the stale lease and retries with the same key.
	second := newTestService(store)
	second.SetRouterProvider(router)
	second.SetEntitlementResolver(fixedCeiling{q: "1080p"})
	second.Now = func() time.Time { return time.Now().UTC().Add(submissionLease + time.Second) }
	store.candidates = []*Request{store.requests[request.ID]}
	if _, err := second.ReconcileRequests(context.Background(), 100); err != nil {
		t.Fatalf("restart reconcile error = %v", err)
	}

	// Then the provider executes the stable-key mutation once while replay completes local state.
	if router.executions[key] != 1 {
		t.Fatalf("provider executions for key %q = %d, want 1", key, router.executions[key])
	}
	if store.requests[request.ID].FulfillmentKey != key || store.requests[request.ID].SubmissionState != SubmissionStateFulfilled {
		t.Fatalf("final submission state = %+v, want fulfilled key %q", store.requests[request.ID], key)
	}
}

func TestRouterDescriptorCarriesDurableFulfillmentKey(t *testing.T) {
	// Given a request restored with its durable fulfillment identity.
	descriptor := routerDescriptor(Request{
		ID: "book-1", Provider: "audiobook-metadata", ProviderItemID: "aud-1", FulfillmentKey: "book-1", ExternalLibraryID: "library-1",
	})

	// When the generic provider descriptor is built.
	ids := descriptor.GetExternalIds()

	// Then the provider receives the same replay key without a provider-specific field.
	if ids["silo_request_id"] != "book-1" || ids["silo_fulfillment_key"] != "book-1" ||
		ids["provider"] != "audiobook-metadata" || ids["provider_item_id"] != "aud-1" ||
		ids["external_library_id"] != "library-1" {
		t.Fatalf("external ids = %#v, want durable generic replay identity", ids)
	}
}

type idempotentRouter struct {
	executions map[string]int
	seen       map[string]bool
}

func (r *idempotentRouter) Fulfill(_ context.Context, _ int, _ string, req Request, _ []Quality, _ []ResolvedRouterConnection) ([]RouterTarget, string, error) {
	if !r.seen[req.FulfillmentKey] {
		r.seen[req.FulfillmentKey] = true
		r.executions[req.FulfillmentKey]++
	}
	return []RouterTarget{{Quality: Quality1080p, ConnectionID: "router-1", ExternalID: "download-1", Status: StatusQueued}}, "", nil
}

func (r *idempotentRouter) CheckStatus(context.Context, int, string, Request, []RouterTargetRef, []ResolvedRouterConnection) ([]RouterTargetStatus, error) {
	return nil, nil
}

func (r *idempotentRouter) ListConfigOptions(context.Context, int, string, ResolvedRouterConnection) (map[string][]RouterOption, error) {
	return nil, nil
}

func (r *idempotentRouter) TestConnection(context.Context, int, string, ResolvedRouterConnection) (bool, string, error) {
	return true, "", nil
}

func (r *idempotentRouter) Validate(context.Context, int, string, ResolvedRouterConnection, []ResolvedRouterConnection) (map[string]string, string, error) {
	return nil, "", nil
}

func (f *fakeStore) UpdateRequestLifecycle(_ context.Context, id string, lifecycle RequestLifecycle) (*Request, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	req := f.requests[id]
	if req == nil {
		return nil, ErrNotFound
	}
	if lifecycle.ExternalLibraryID != "" {
		req.ExternalLibraryID = lifecycle.ExternalLibraryID
	}
	if lifecycle.ExternalDownloadID != "" {
		req.ExternalDownloadID = lifecycle.ExternalDownloadID
	}
	if lifecycle.ExternalStatus != "" {
		req.ExternalStatus = lifecycle.ExternalStatus
	}
	if lifecycle.ExternalDetail != "" {
		req.ExternalDetail = lifecycle.ExternalDetail
	}
	if lifecycle.ImportedPath != "" {
		req.ImportedPath = lifecycle.ImportedPath
	}
	if lifecycle.ScanRunID != "" {
		req.ScanRunID = lifecycle.ScanRunID
	}
	if lifecycle.SiloAudiobookID != "" {
		req.SiloAudiobookID = lifecycle.SiloAudiobookID
	}
	if lifecycle.SiloAudiobookLink != "" {
		req.SiloAudiobookLink = lifecycle.SiloAudiobookLink
	}
	req.Retryable = lifecycle.Retryable
	copy := *req
	return &copy, nil
}

func (f *fakeStore) ClaimRequestSubmission(_ context.Context, id, key string, now time.Time, lease time.Duration) (*Request, bool, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	req := f.requests[id]
	if req == nil {
		return nil, false, ErrNotFound
	}
	if req.FulfillmentKey != "" && req.FulfillmentKey != key {
		return nil, false, nil
	}
	claimable := req.SubmissionState == "" || req.SubmissionState == SubmissionStateFailed
	if req.SubmissionState == SubmissionStateInFlight && req.SubmissionStartedAt != nil && req.SubmissionStartedAt.Add(lease).Before(now) {
		claimable = true
	}
	if !claimable {
		return nil, false, nil
	}
	req.FulfillmentKey = key
	req.SubmissionState = SubmissionStateInFlight
	started := now
	req.SubmissionStartedAt = &started
	copy := *req
	return &copy, true, nil
}

func (f *fakeStore) SetRequestSubmissionState(_ context.Context, id, state string) (*Request, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	req := f.requests[id]
	if req == nil {
		return nil, ErrNotFound
	}
	req.SubmissionState = state
	if state != SubmissionStateInFlight {
		req.SubmissionStartedAt = nil
	}
	copy := *req
	return &copy, nil
}
