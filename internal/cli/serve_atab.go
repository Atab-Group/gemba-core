package cli

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"os"
	"strings"
	"time"

	"github.com/GembaCore/gemba-core/core"
	"github.com/GembaCore/gemba-core/internal/adapter/atab"
	"github.com/GembaCore/gemba-core/internal/adapter/registry"
	"github.com/GembaCore/gemba-core/internal/config"
	"github.com/GembaCore/gemba-core/internal/shader"
	"github.com/GembaCore/gemba-core/internal/transport/api"
)

// atabSourcesFile is the on-disk shape of --atab-sources.
//
// Durations are strings ("60s", "10m") rather than raw nanosecond
// integers, because the file is written by a person and a person should
// not have to count zeros to say one minute.
type atabSourcesFile struct {
	Sources []atabSourceEntry `json:"sources"`
}

type atabSourceEntry struct {
	ID              string            `json:"id"`
	Org             string            `json:"org"`
	ProjectNumber   int               `json:"project_number"`
	Repos           []string          `json:"repos,omitempty"`
	Display         string            `json:"display,omitempty"`
	FieldNames      map[string]string `json:"field_names,omitempty"`
	Authority       string            `json:"authority,omitempty"`
	RefreshInterval string            `json:"refresh_interval,omitempty"`
	StaleAfter      string            `json:"stale_after,omitempty"`
	// Fixture replays a recorded file for this source instead of
	// calling GitHub. Useful for a demo rig, and for keeping one source
	// offline while the others stay live.
	Fixture string `json:"fixture,omitempty"`
}

// loadATABSources reads the allowlist file and returns one normalised
// SourceConfig per entry.
//
// There is no discovery path and no wildcard: an org absent from this
// file is an org the adaptor will not read. That is what makes the
// allowlist a boundary rather than a preference.
func loadATABSources(path string) ([]atab.SourceConfig, []string, error) {
	raw, err := os.ReadFile(path)
	if err != nil {
		return nil, nil, fmt.Errorf("read --atab-sources %q: %w", path, err)
	}
	var file atabSourcesFile
	dec := json.NewDecoder(strings.NewReader(string(raw)))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&file); err != nil {
		return nil, nil, fmt.Errorf("parse --atab-sources %q: %w", path, err)
	}
	if len(file.Sources) == 0 {
		return nil, nil, fmt.Errorf(
			"--atab-sources %q declares no sources; a board with no source has nothing to show",
			path)
	}

	cfgs := make([]atab.SourceConfig, 0, len(file.Sources))
	fixtures := make([]string, 0, len(file.Sources))
	for i, e := range file.Sources {
		cfg := atab.SourceConfig{
			ID:            atab.SourceID(e.ID),
			Org:           e.Org,
			ProjectNumber: e.ProjectNumber,
			Repos:         e.Repos,
			Display:       e.Display,
			FieldNames:    e.FieldNames,
			Authority:     atab.Authority(e.Authority),
		}
		if e.RefreshInterval != "" {
			d, err := time.ParseDuration(e.RefreshInterval)
			if err != nil {
				return nil, nil, fmt.Errorf(
					"--atab-sources %q: sources[%d].refresh_interval %q: %w",
					path, i, e.RefreshInterval, err)
			}
			cfg.RefreshInterval = d
		}
		if e.StaleAfter != "" {
			d, err := time.ParseDuration(e.StaleAfter)
			if err != nil {
				return nil, nil, fmt.Errorf(
					"--atab-sources %q: sources[%d].stale_after %q: %w",
					path, i, e.StaleAfter, err)
			}
			cfg.StaleAfter = d
		}
		if err := cfg.Normalize(); err != nil {
			return nil, nil, fmt.Errorf("--atab-sources %q: sources[%d]: %w", path, i, err)
		}
		cfgs = append(cfgs, cfg)
		fixtures = append(fixtures, e.Fixture)
	}
	return cfgs, fixtures, nil
}

// buildATABRegistry assembles the allowlisted source set and binds a
// client to each one.
func buildATABRegistry(cfg config.ServeConfig) (*atab.Registry, string, error) {
	reg := atab.NewRegistry()

	switch {
	case cfg.ATABFixture != "":
		client, file, err := atab.LoadFixture(cfg.ATABFixture)
		if err != nil {
			return nil, "", err
		}
		source := atab.DemoSourceConfig()
		source.ID = file.Source
		if err := reg.Add(source, client); err != nil {
			return nil, "", err
		}
		return reg, "fixture " + cfg.ATABFixture, nil

	case cfg.ATABSources != "":
		sources, fixtures, err := loadATABSources(cfg.ATABSources)
		if err != nil {
			return nil, "", err
		}
		for i, s := range sources {
			var client atab.Client
			if fixtures[i] != "" {
				fc, _, err := atab.LoadFixture(fixtures[i])
				if err != nil {
					return nil, "", err
				}
				client = fc
			} else {
				client = atab.NewGHClient(s)
			}
			if err := reg.Add(s, client); err != nil {
				return nil, "", err
			}
		}
		return reg, cfg.ATABSources, nil

	default:
		source := atab.DemoSourceConfig()
		if err := reg.Add(source, atab.NewGHClient(source)); err != nil {
			return nil, "", err
		}
		return reg, "built-in Atab-Group Project #1", nil
	}
}

// registerATABWorkPlane binds the read-only GitHub / ATAB WorkPlane.
//
// The plane is registered with the adaptor registry under a probe that
// reports per-source health, so the SPA's degraded banner reacts to one
// org going unreadable without waiting for a query to fail.
func registerATABWorkPlane(
	ctx context.Context, host *api.Host, cfg config.ServeConfig, sh core.Shader,
) (*workPlaneReg, error) {
	reg, sourceLabel, err := buildATABRegistry(cfg)
	if err != nil {
		return nil, err
	}

	adaptor := atab.New(reg, core.TransportAPI)
	wrapped := shader.Wrap(adaptor, sh)
	bound, err := host.RegisterWorkPlane(ctx, wrapped)
	if err != nil {
		return nil, fmt.Errorf("register atab workplane: %w", err)
	}
	manifest, err := wrapped.Describe(ctx)
	if err != nil {
		return nil, fmt.Errorf("describe atab workplane: %w", err)
	}

	registry.Register(registry.Adaptor{
		Name:  atab.AdaptorName,
		Plane: registry.WorkPlane,
		Detect: func() registry.DetectResult {
			if len(reg.Sources()) == 0 {
				return registry.DetectResult{Reason: "no sources are allowlisted"}
			}
			return registry.DetectResult{Ok: true}
		},
		Probe: func() registry.DetectResult {
			ok, reason := adaptor.Probe(context.Background())
			return registry.DetectResult{Ok: ok, Reason: reason}
		},
	})

	sources := reg.Sources()
	names := make([]string, 0, len(sources))
	for _, s := range sources {
		names = append(names, string(s))
	}
	slog.Info("workplane adaptor registered",
		"adaptor", bound.AdaptorName,
		"version", bound.AdaptorVersion,
		"protocol", bound.ProtocolVersion,
		"transport", bound.Transport,
		"mode", "atab",
		"read_only", manifest.ReadOnly,
		"sources", strings.Join(names, ","))

	return &workPlaneReg{
		Host:       host,
		Manifest:   manifest,
		Mode:       "atab",
		SourceKind: "github",
		Source:     fmt.Sprintf("%s (%s)", sourceLabel, strings.Join(names, ", ")),
	}, nil
}
