package service

import (
	"fmt"
	"strings"

	"github.com/pgquerynarrative/pgquerynarrative/app/catalog"
	apperrors "github.com/pgquerynarrative/pgquerynarrative/app/errors"
	"github.com/pgquerynarrative/pgquerynarrative/app/queryrunner"
)

type connectionResolver struct {
	defaultConnectionID string
	runners             map[string]*queryrunner.Runner
	loaders             map[string]*catalog.Loader
	readonlyUsers       map[string]string
}

func newConnectionResolver(defaultID string, runners map[string]*queryrunner.Runner, loaders map[string]*catalog.Loader, readonlyUsers map[string]string) connectionResolver {
	if readonlyUsers == nil {
		readonlyUsers = map[string]string{}
	}
	return connectionResolver{
		defaultConnectionID: defaultID,
		runners:             runners,
		loaders:             loaders,
		readonlyUsers:       readonlyUsers,
	}
}

// resolveConnectionID returns the default connection when id is nil/blank.
// Unknown non-empty IDs return ErrConnectionNotFound (no silent fallback).
func (r connectionResolver) resolveConnectionID(connectionID *string) (string, error) {
	if connectionID == nil || strings.TrimSpace(*connectionID) == "" {
		return r.defaultConnectionID, nil
	}
	id := strings.TrimSpace(*connectionID)
	if r.runners != nil {
		if _, ok := r.runners[id]; ok {
			return id, nil
		}
		if len(r.runners) > 0 {
			return "", fmt.Errorf("%w: %q", apperrors.ErrConnectionNotFound, id)
		}
	}
	if r.loaders != nil {
		if _, ok := r.loaders[id]; ok {
			return id, nil
		}
		if len(r.loaders) > 0 {
			return "", fmt.Errorf("%w: %q", apperrors.ErrConnectionNotFound, id)
		}
	}
	return "", fmt.Errorf("%w: %q", apperrors.ErrConnectionNotFound, id)
}

func (r connectionResolver) runnerFor(connectionID *string) (*queryrunner.Runner, error) {
	id, err := r.resolveConnectionID(connectionID)
	if err != nil {
		return nil, err
	}
	runner := r.runners[id]
	if runner == nil {
		return nil, fmt.Errorf("%w: %q", apperrors.ErrConnectionNotFound, id)
	}
	return runner, nil
}

func (r connectionResolver) loaderFor(connectionID *string) (*catalog.Loader, error) {
	id, err := r.resolveConnectionID(connectionID)
	if err != nil {
		return nil, err
	}
	loader := r.loaders[id]
	if loader == nil {
		return nil, fmt.Errorf("%w: %q", apperrors.ErrConnectionNotFound, id)
	}
	return loader, nil
}

func (r connectionResolver) readOnlyUserFor(connectionID *string) (string, error) {
	id, err := r.resolveConnectionID(connectionID)
	if err != nil {
		return "", err
	}
	if user, ok := r.readonlyUsers[id]; ok && strings.TrimSpace(user) != "" {
		return strings.TrimSpace(user), nil
	}
	return "pgquerynarrative_readonly", nil
}
