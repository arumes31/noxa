package query

import (
	"context"
	"fmt"
	"time"

	"noxa/internal/auth"
	"noxa/internal/netproto"
)

func (b *metadataQueryBackend) WithIntegrationCustomMetadata(ctx context.Context, _ auth.IntegrationPrincipal, request netproto.CustomMetadataQuery, deliver func(context.Context, netproto.CustomMetadataPage) error) error {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.customCalls++
	if b.err != nil {
		return b.err
	}
	if request.AfterKey != "before" || request.Limit != 1 {
		return fmt.Errorf("lost page request")
	}
	return deliver(ctx, netproto.CustomMetadataPage{UniqueID: request.UniqueID, Entries: []netproto.CustomMetadataEntry{{Key: "key|ü", Value: "value | with\nlines"}}, NextAfterKey: "key|ü"})
}

func (b *metadataQueryBackend) ChangeIntegrationCustomMetadata(ctx context.Context, _ auth.IntegrationPrincipal, request netproto.CustomMetadataChange) (netproto.CustomMetadataResult, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.customCalls++
	if b.err != nil {
		return netproto.CustomMetadataResult{}, b.err
	}
	if deadline, ok := ctx.Deadline(); !ok || time.Until(deadline) > 10*time.Second {
		return netproto.CustomMetadataResult{}, fmt.Errorf("unbounded custom change")
	}
	if request.Key != "key|ü" || (!request.Delete && *request.Value != "" && *request.Value != "value | with\nlines") {
		return netproto.CustomMetadataResult{}, fmt.Errorf("lost custom change fields")
	}
	return netproto.CustomMetadataResult{UniqueID: request.UniqueID, Key: request.Key, Deleted: request.Delete}, nil
}
