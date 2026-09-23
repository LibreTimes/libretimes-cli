package cli

import (
	"context"
	"fmt"
	"regexp"
	"slices"
	"strings"

	"github.com/The-LibreTimes/libretimes-cli/internal/api"
	"github.com/The-LibreTimes/libretimes-cli/internal/frontmatter"
)

// What a byline credit resolves to. Only additive outcomes exist: `lt` never
// removes a name, and never changes the role of a name already there.
const (
	creditAdd     = "add"
	creditPresent = "present"
)

// contributorResult is one credit's outcome, reported under its file.
//
// CurrentRole is set only when the byline already holds this person under a
// different role than the file asks for. The role is left alone -- a claimed
// profile's credit is the claimant's to discuss, not a re-run's to rewrite --
// and the difference is surfaced instead of silently ignored.
type contributorResult struct {
	Profile     string `json:"profile"`
	ProfileID   string `json:"profile_id"`
	Role        string `json:"role"`
	Action      string `json:"action"`
	CurrentRole string `json:"current_role,omitempty"`

	// self marks the signed-in account. It owns the publication and is on the
	// byline already, so it is never invited: the API answers that with a 400.
	self bool
}

// uuidPattern recognises a profile_id written in place of a handle. It is
// consulted only for a value without a leading `@`: account-less handles are
// minted with dashes (libretimes-handles' grammar), so shape alone cannot rule
// out a handle, and `@` is what settles it.
var uuidPattern = regexp.MustCompile(
	`^[0-9a-fA-F]{8}-[0-9a-fA-F]{4}-[0-9a-fA-F]{4}-[0-9a-fA-F]{4}-[0-9a-fA-F]{12}$`)

// profileResolver turns what a file writes into profile_ids, once per run.
// A course is twenty lectures crediting one lecturer, which should be one
// lookup, not twenty.
type profileResolver struct {
	client   *api.Client
	handles  map[string]string
	selfID   string
	selfDone bool
}

func newProfileResolver(client *api.Client) *profileResolver {
	return &profileResolver{client: client, handles: map[string]string{}}
}

// self is the signed-in account's profile_id, fetched on first need only, so a
// file with no contributors costs no extra request.
func (r *profileResolver) self(ctx context.Context) (string, error) {
	if r.selfDone {
		return r.selfID, nil
	}
	me, err := r.client.Me(ctx)
	if err != nil {
		return "", fmt.Errorf("reading the signed-in profile: %w", err)
	}
	r.selfID, r.selfDone = strings.ToLower(me.ProfileID), true
	return r.selfID, nil
}

func (r *profileResolver) profileID(ctx context.Context, written string) (string, error) {
	if !strings.HasPrefix(written, "@") && uuidPattern.MatchString(written) {
		// Not looked up. An unknown id is refused by the API on the write, and
		// there is no public by-id read to ask first.
		return strings.ToLower(written), nil
	}
	handle := strings.TrimPrefix(written, "@")
	if handle == "" {
		return "", fmt.Errorf("contributor %q is not a handle or a profile_id", written)
	}
	if id, ok := r.handles[handle]; ok {
		return id, nil
	}
	p, err := r.client.ProfileByHandle(ctx, handle)
	if err != nil {
		return "", fmt.Errorf("contributor %s: %w", written, err)
	}
	id := strings.ToLower(p.ProfileID)
	r.handles[handle] = id
	return id, nil
}

// resolveContributors validates a file's contributors and resolves each to a
// profile_id. Everything that can be refused locally is refused before any
// request, and nothing is written until every name resolves: a publication
// created without the credit it was meant to carry is the failure this exists
// to prevent.
func resolveContributors(ctx context.Context, r *profileResolver, list []frontmatter.Contributor) ([]contributorResult, error) {
	if len(list) == 0 {
		return nil, nil
	}

	results := make([]contributorResult, 0, len(list))
	for i, c := range list {
		written := strings.TrimSpace(c.Profile)
		if written == "" {
			return nil, fmt.Errorf("contributor %d has no `profile`", i+1)
		}
		role := strings.TrimSpace(c.Role)
		if role == "" {
			role = "author"
		}
		if !slices.Contains(api.ContributorRoleValues, role) {
			return nil, fmt.Errorf("contributor %s: role %q is not one of %s",
				written, role, strings.Join(api.ContributorRoleValues, ", "))
		}
		results = append(results, contributorResult{Profile: written, Role: role})
	}

	self, err := r.self(ctx)
	if err != nil {
		return nil, err
	}

	seen := map[string]string{}
	for i := range results {
		id, err := r.profileID(ctx, results[i].Profile)
		if err != nil {
			return nil, err
		}
		if first, dup := seen[id]; dup {
			return nil, fmt.Errorf("contributors %s and %s are the same profile; list each person once",
				first, results[i].Profile)
		}
		seen[id] = results[i].Profile
		results[i].ProfileID = id
		results[i].self = id == self
	}
	return results, nil
}

// applyToCreate puts the credits on a create body. Every name is new, so every
// action is `add`; the signed-in account's role rides as creator_role, since
// the creator's row is made by the create itself.
func applyToCreate(input *api.PublicationInput, credits []contributorResult) {
	for i := range credits {
		credits[i].Action = creditAdd
		if credits[i].self {
			input.CreatorRole = credits[i].Role
			continue
		}
		input.Authors = append(input.Authors, api.AuthorInvite{
			ProfileID: credits[i].ProfileID,
			Role:      credits[i].Role,
			Access:    api.AccessCredited,
		})
	}
}

// diffByline decides, for an existing publication, which credits are missing.
//
// A row in any status counts as present. A pending invitation is already
// asked; a declined one was answered, and a re-publish asking again would be
// pestering someone who said no (the API refuses it with a 409 anyway).
func diffByline(credits []contributorResult, byline []api.BylineEntry) error {
	current := make(map[string]api.BylineEntry, len(byline))
	for _, entry := range byline {
		current[strings.ToLower(entry.ProfileID)] = entry
	}
	for i := range credits {
		entry, ok := current[credits[i].ProfileID]
		if !ok {
			if credits[i].self {
				// Possible on an organization-owned publication a manager
				// edits without being on its byline. Inviting yourself is
				// refused by the API, and adding yourself to a byline is an
				// editor action, not an import's.
				return fmt.Errorf("contributor %s is you, and you are not on this byline; "+
					"lt does not add the signed-in account. Remove it from `contributors`",
					credits[i].Profile)
			}
			credits[i].Action = creditAdd
			continue
		}
		credits[i].Action = creditPresent
		if entry.Role != "" && entry.Role != credits[i].Role {
			credits[i].CurrentRole = entry.Role
		}
	}
	return nil
}

// inviteMissing adds every `add` credit to an existing publication, in file
// order, so the byline's order follows the file's.
func inviteMissing(ctx context.Context, client *api.Client, publicationID string, credits []contributorResult) error {
	for _, c := range credits {
		if c.Action != creditAdd {
			continue
		}
		_, err := client.InviteAuthor(ctx, publicationID, api.AuthorInvite{
			ProfileID: c.ProfileID,
			Role:      c.Role,
			Access:    api.AccessCredited,
		})
		if err != nil {
			return fmt.Errorf("updated, but crediting %s failed: %w", c.Profile, err)
		}
	}
	return nil
}
