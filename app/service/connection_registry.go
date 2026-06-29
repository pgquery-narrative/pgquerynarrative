package service

import (
	"strings"

	"github.com/pgquerynarrative/pgquerynarrative/app/catalog"
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

func (r connectionResolver) normalizedConnectionID(connectionID *string) string {
	if connectionID == nil || strings.TrimSpace(*connectionID) == "" {
		return r.defaultConnectionID
	}
	if _, ok := r.runners[strings.TrimSpace(*connectionID)]; ok {
		return strings.TrimSpace(*connectionID)
	}
	return r.defaultConnectionID
}

func (r connectionResolver) runnerFor(connectionID *string) *queryrunner.Runner {
	id := r.normalizedConnectionID(connectionID)
	return r.runners[id]
}

func (r connectionResolver) loaderFor(connectionID *string) *catalog.Loader {
	id := r.normalizedConnectionID(connectionID)
	return r.loaders[id]
}

func (r connectionResolver) readOnlyUserFor(connectionID *string) string {
	id := r.normalizedConnectionID(connectionID)
	if user, ok := r.readonlyUsers[id]; ok && strings.TrimSpace(user) != "" {
		return strings.TrimSpace(user)
	}
	return "pgquerynarrative_readonly"
}
