package cli

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"github.com/The-LibreTimes/libretimes-cli/internal/api"
)

// The type registry (`GET /work-types`) is the one definition of what a
// file's `type` may be and what it decides. lt asks it rather than keeping a
// list, so a kind the platform adds is usable the day it ships.
//
// Both checks below are a courtesy, not the control: the server enforces the
// same rules on every write. So when the registry cannot be read -- an older
// server, a blip -- they step aside and let the write speak for itself,
// rather than failing a run over a lookup the server would have answered
// anyway.

// registryOf returns the registry, or nil when it is unavailable or empty.
func registryOf(ctx context.Context, client *api.Client) *api.WorkTypes {
	types, err := client.WorkTypes(ctx)
	if err != nil || types == nil || len(types.Kinds) == 0 {
		return nil
	}
	return types
}

// checkKind refuses a `type` the registry does not hold, naming the ones it
// does. Before any request that writes.
func checkKind(ctx context.Context, client *api.Client, kind string) error {
	types := registryOf(ctx, client)
	if types == nil || types.Kind(kind) != nil {
		return nil
	}
	return &api.Error{Code: api.CodeValidation, Message: fmt.Sprintf(
		"`type` is %q, which is not a kind of work LibreTimes hosts. It is one of: %s "+
			"(`lt types` describes each)", kind, strings.Join(types.KindNames(), ", "))}
}

// checkVenue refuses, before any write, a run that would take a venue kind
// out of draft -- the publish the server answers 409 VENUE_REQUIRED. publishes
// is whether this run would publish: a create not marked `is_draft: true`, or
// a draft the file now marks `is_draft: false`. Updating a work that is
// already published is not a publish, and is never refused here.
//
// This is what keeps --dry-run honest: it reports the refusal the real run
// would meet, instead of promising a publish that cannot happen.
func checkVenue(ctx context.Context, client *api.Client, kind string, publishes bool) error {
	if !publishes {
		return nil
	}
	entry := registryOf(ctx, client).Kind(kind)
	if entry == nil || entry.Publish != api.PublishVenue {
		return nil
	}
	return api.VenueRequired(kind)
}

// missingOf is the field keys a WORK_INCOMPLETE named, for --json results.
func missingOf(err error) []string {
	var refusal *api.Refusal
	if errors.As(err, &refusal) {
		return refusal.Missing
	}
	return nil
}
