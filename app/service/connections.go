package service

import (
	"context"
	"sort"

	"github.com/pgquerynarrative/pgquerynarrative/api/gen/connections"
	"github.com/pgquerynarrative/pgquerynarrative/app/auth"
)

// ConnectionsService exposes configured data connections (safe metadata only).
type ConnectionsService struct {
	items []*connections.ConnectionInfo
	authz ConnectionAuthorizer
}

// NewConnectionsService creates a connections service from id/name map.
func NewConnectionsService(items []*connections.ConnectionInfo) *ConnectionsService {
	return &ConnectionsService{items: items}
}

// SetAuthorizer wires connection-level authorization. Nil is permissive.
// Intended to be called only once, from narrative.NewClient, before the
// service is handed to any HTTP handler or background worker.
func (s *ConnectionsService) SetAuthorizer(authz ConnectionAuthorizer) {
	if s != nil {
		s.authz = authz
	}
}

// List returns connection IDs and names the current principal may use for query.
func (s *ConnectionsService) List(ctx context.Context) (*connections.ConnectionListResult, error) {
	out := make([]*connections.ConnectionInfo, 0, len(s.items))
	p := auth.PrincipalFromContext(ctx)
	configured := make([]string, 0, len(s.items))
	byID := map[string]*connections.ConnectionInfo{}
	for _, item := range s.items {
		configured = append(configured, item.ID)
		byID[item.ID] = item
	}
	allowed := configured
	if s.authz != nil {
		allowed = s.authz.AllowedConnections(ctx, p.OrgID, p.UserID, p.Role, configured, auth.ActionQuery)
	}
	for _, id := range allowed {
		if item, ok := byID[id]; ok {
			out = append(out, item)
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].ID < out[j].ID })
	return &connections.ConnectionListResult{Items: out}, nil
}
