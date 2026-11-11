package localclient

import (
	"context"
	"net/http"

	"loom.local/loom/internal/knowledge"
	"loom.local/loom/internal/projects"
	"loom.local/loom/internal/response"
)

type BoxMembershipInventoryResult struct {
	Kind          string `json:"kind"`
	CatalogDigest string `json:"catalog_digest"`
	projects.BoxMembershipInventoryPage
}

func (c Client) BoxMembershipInventory(ctx context.Context, correlationID, kind string, input projects.BoxMembershipInventoryInput) (response.Envelope[BoxMembershipInventoryResult], error) {
	return doJSON[BoxMembershipInventoryResult](c.WithBoxContext("", true), ctx, http.MethodPost, "/v1/boxes/membership/inventory", correlationID, struct {
		Kind string `json:"kind"`
		projects.BoxMembershipInventoryInput
	}{kind, input})
}

func (c Client) ReconcileProjectBoxMembership(ctx context.Context, correlationID string, input projects.BoxMembershipReconcileInput) (response.Envelope[projects.BoxMembershipReconcileResult], error) {
	return doJSON[projects.BoxMembershipReconcileResult](c.WithBoxContext("", true), ctx, http.MethodPost, "/v1/boxes/membership/projects", correlationID, input)
}

func (c Client) ReconcileSourceBoxMembership(ctx context.Context, correlationID string, input knowledge.SourceBoxMembershipInput) (response.Envelope[knowledge.SourceBoxMembershipResult], error) {
	return doJSON[knowledge.SourceBoxMembershipResult](c.WithBoxContext("", true), ctx, http.MethodPost, "/v1/boxes/membership/sources", correlationID, input)
}
