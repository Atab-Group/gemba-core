package atab

import (
	"strings"
	"time"

	"github.com/GembaCore/gemba-core/core"
)

// Default board field names, as Atab-Group Project #1 spells them. A
// federated source whose board uses different names overrides them in
// its own SourceConfig rather than forcing a code change.
const (
	FieldStatus   = "Status"
	FieldPriority = "Priority"
	FieldArea     = "Area"
	FieldEstimate = "Estimate"
)

// Default freshness budgets. A snapshot older than StaleAfter is served
// with an explicit stale marker rather than silently presented as
// current; refresh is attempted no more often than RefreshInterval.
const (
	DefaultRefreshInterval = 60 * time.Second
	DefaultStaleAfter      = 10 * time.Minute
)

// SourceConfig declares one work source: an org, a project board and the
// repositories whose issues the board draws from.
//
// Nothing here is discovered at runtime. A source the operator has not
// written down is a source the adaptor will not read, which is what
// makes the Stage 2 allowlist meaningful.
type SourceConfig struct {
	// ID is the stable, collision-safe prefix for every work item this
	// source produces.
	ID SourceID `json:"id" toml:"id"`

	// Org is the GitHub organisation login. It also org-locks
	// cross-repo atab-meta edges: an edge naming another org is dropped
	// with a warning rather than followed.
	Org string `json:"org" toml:"org"`

	// ProjectNumber is the org-level Projects v2 number (1 for
	// Atab-Group's "Atab Group Tasks" board). Zero means the source has
	// no board; every issue then projects from its GitHub state alone.
	ProjectNumber int `json:"project_number" toml:"project_number"`

	// Repos lists the repositories to read, without the org prefix.
	// Empty means "every repository in the org the token can see", which
	// is only sensible for a small org.
	Repos []string `json:"repos,omitempty" toml:"repos"`

	// Display is the human label for the source in the UI. Defaults to
	// the org name.
	Display string `json:"display,omitempty" toml:"display"`

	// FieldNames overrides the board field names. A missing entry falls
	// back to the Field* constants above.
	FieldNames map[string]string `json:"field_names,omitempty" toml:"field_names"`

	// Authority declares what this source is trusted to say. See
	// [Authority].
	Authority Authority `json:"authority,omitempty" toml:"authority"`

	// RefreshInterval bounds how often the source is re-fetched.
	RefreshInterval time.Duration `json:"refresh_interval,omitempty" toml:"refresh_interval"`
	// StaleAfter is the age at which a cached snapshot is marked stale.
	StaleAfter time.Duration `json:"stale_after,omitempty" toml:"stale_after"`
}

// Authority says how much of the projection a source is allowed to own.
// A source can be canonical for its own issues while contributing
// nothing to another source's dependency graph, which is what stops a
// low-trust source from silently blocking work it does not own.
type Authority string

const (
	// AuthorityCanonical: the source owns its issues and its edges may
	// resolve into any other allowlisted source.
	AuthorityCanonical Authority = "canonical"
	// AuthorityReference: the source's issues are shown, but its edges
	// never block work in another source. The default for a source the
	// operator has not classified.
	AuthorityReference Authority = "reference"
)

// Normalize fills defaults in place and returns a validation error for
// anything that cannot be defaulted.
func (c *SourceConfig) Normalize() error {
	if err := c.ID.Validate(); err != nil {
		return err
	}
	if strings.TrimSpace(c.Org) == "" {
		return core.NewAdaptorError(core.KindValidation,
			"atab: source %q must declare an org", c.ID)
	}
	if c.ProjectNumber < 0 {
		return core.NewAdaptorError(core.KindValidation,
			"atab: source %q has a negative project number %d", c.ID, c.ProjectNumber)
	}
	if c.Display == "" {
		c.Display = c.Org
	}
	switch c.Authority {
	case "":
		c.Authority = AuthorityReference
	case AuthorityCanonical, AuthorityReference:
	default:
		return core.NewAdaptorError(core.KindValidation,
			"atab: source %q has unknown authority %q", c.ID, c.Authority)
	}
	if c.RefreshInterval <= 0 {
		c.RefreshInterval = DefaultRefreshInterval
	}
	if c.StaleAfter <= 0 {
		c.StaleAfter = DefaultStaleAfter
	}
	if c.StaleAfter < c.RefreshInterval {
		return core.NewAdaptorError(core.KindValidation,
			"atab: source %q has stale_after (%s) below refresh_interval (%s); "+
				"every snapshot would be born stale",
			c.ID, c.StaleAfter, c.RefreshInterval)
	}
	return nil
}

// FieldName resolves a logical board field to the name this source's
// board actually uses.
func (c SourceConfig) FieldName(logical string) string {
	if c.FieldNames != nil {
		if v, ok := c.FieldNames[logical]; ok && v != "" {
			return v
		}
	}
	return logical
}
