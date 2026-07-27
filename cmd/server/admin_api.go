package main

import (
	"encoding/json"
	"net/http"
	"strings"
	"time"

	"github.com/pgquerynarrative/pgquerynarrative/app/audit"
	"github.com/pgquerynarrative/pgquerynarrative/app/auth"
	"github.com/pgquerynarrative/pgquerynarrative/app/config"
	"github.com/pgquerynarrative/pgquerynarrative/app/db"
)

type adminDeps struct {
	keys       *auth.ManagedKeyStore
	membership *auth.MembershipStore
	connAuthz  *auth.ConnectionAuthorizer
	auditStore *audit.Store
	sessions   *auth.SessionManager
	encKey     string
	orgSecrets *auth.OrgConnectionSecretStore
	pools      adminPoolInvalidator
}

type adminPoolInvalidator interface {
	InvalidateOrgReadOnlyPool(orgID, connectionID string)
}

func mountAdminAPI(mux *http.ServeMux, deps adminDeps) {
	mux.HandleFunc("/api/v1/admin/api-keys", func(w http.ResponseWriter, r *http.Request) {
		if !auth.RequireAdmin(w, r) {
			return
		}
		switch r.Method {
		case http.MethodGet:
			adminListAPIKeys(w, r, deps)
		case http.MethodPost:
			adminCreateAPIKey(w, r, deps)
		default:
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		}
	})
	mux.HandleFunc("/api/v1/admin/api-keys/", func(w http.ResponseWriter, r *http.Request) {
		if !auth.RequireAdmin(w, r) {
			return
		}
		path := strings.TrimPrefix(r.URL.Path, "/api/v1/admin/api-keys/")
		parts := strings.Split(strings.Trim(path, "/"), "/")
		if len(parts) == 2 && parts[1] == "revoke" && r.Method == http.MethodPost {
			adminRevokeAPIKey(w, r, deps, parts[0])
			return
		}
		http.NotFound(w, r)
	})
	mux.HandleFunc("/api/v1/admin/organizations", func(w http.ResponseWriter, r *http.Request) {
		if !auth.RequirePlatformAdmin(w, r) {
			return
		}
		switch r.Method {
		case http.MethodGet:
			adminListOrganizations(w, r, deps)
		case http.MethodPost:
			adminCreateOrganization(w, r, deps)
		default:
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		}
	})
	mux.HandleFunc("/api/v1/admin/memberships", func(w http.ResponseWriter, r *http.Request) {
		if !auth.RequireAdmin(w, r) {
			return
		}
		switch r.Method {
		case http.MethodGet:
			adminListMemberships(w, r, deps)
		case http.MethodPost:
			adminUpsertMembership(w, r, deps)
		case http.MethodDelete:
			adminRevokeMembership(w, r, deps)
		default:
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		}
	})
	mux.HandleFunc("/api/v1/admin/connection-assignments", func(w http.ResponseWriter, r *http.Request) {
		if !auth.RequireAdmin(w, r) {
			return
		}
		switch r.Method {
		case http.MethodGet:
			adminListConnectionAssignments(w, r, deps)
		case http.MethodPost:
			adminAssignConnection(w, r, deps)
		case http.MethodDelete:
			adminUnassignConnection(w, r, deps)
		default:
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		}
	})
	mux.HandleFunc("/api/v1/admin/connection-permissions", func(w http.ResponseWriter, r *http.Request) {
		if !auth.RequireAdmin(w, r) {
			return
		}
		switch r.Method {
		case http.MethodPost:
			adminGrantConnectionPermission(w, r, deps)
		case http.MethodDelete:
			adminRevokeConnectionPermission(w, r, deps)
		default:
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		}
	})
	mux.HandleFunc("/api/v1/admin/connection-secrets", func(w http.ResponseWriter, r *http.Request) {
		if !auth.RequireAdmin(w, r) {
			return
		}
		switch r.Method {
		case http.MethodGet:
			adminListConnectionSecrets(w, r, deps)
		case http.MethodPost:
			adminUpsertConnectionSecret(w, r, deps)
		case http.MethodDelete:
			adminDeleteConnectionSecret(w, r, deps)
		default:
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		}
	})
}

func adminListAPIKeys(w http.ResponseWriter, r *http.Request, deps adminDeps) {
	p := auth.PrincipalFromContext(r.Context())
	keys, err := deps.keys.List(r.Context(), p.OrgID)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	type item struct {
		ID        string     `json:"id"`
		Prefix    string     `json:"prefix"`
		Role      string     `json:"role"`
		Scopes    []string   `json:"scopes"`
		ExpiresAt *time.Time `json:"expires_at,omitempty"`
		RevokedAt *time.Time `json:"revoked_at,omitempty"`
		CreatedBy string     `json:"created_by,omitempty"`
		CreatedAt time.Time  `json:"created_at"`
	}
	out := make([]item, 0, len(keys))
	for _, k := range keys {
		it := item{
			ID: k.ID, Prefix: k.Prefix, Role: k.Role, Scopes: k.Scopes,
			CreatedBy: k.CreatedBy, CreatedAt: k.CreatedAt,
		}
		if !k.ExpiresAt.IsZero() {
			t := k.ExpiresAt
			it.ExpiresAt = &t
		}
		if !k.RevokedAt.IsZero() {
			t := k.RevokedAt
			it.RevokedAt = &t
		}
		out = append(out, it)
	}
	writeJSON(w, http.StatusOK, map[string]any{"items": out})
}

func adminCreateAPIKey(w http.ResponseWriter, r *http.Request, deps adminDeps) {
	var body struct {
		Role      string   `json:"role"`
		Scopes    []string `json:"scopes"`
		ExpiresAt string   `json:"expires_at"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		http.Error(w, "invalid JSON body", http.StatusBadRequest)
		return
	}
	p := auth.PrincipalFromContext(r.Context())
	if !auth.CanAssignRole(p.Role, body.Role) {
		auth.WriteForbidden(w)
		return
	}
	var expires time.Time
	if strings.TrimSpace(body.ExpiresAt) != "" {
		t, err := time.Parse(time.RFC3339, body.ExpiresAt)
		if err != nil {
			http.Error(w, "expires_at must be RFC3339", http.StatusBadRequest)
			return
		}
		expires = t
	}
	issued, err := deps.keys.Create(r.Context(), p.OrgID, body.Role, p.UserID, body.Scopes, expires)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	id := issued.ID
	if !recordAdminAudit(w, r, deps, audit.Entry{
		EventType:  audit.EventManagedKeyCreate,
		EntityType: "api_key",
		EntityID:   &id,
		UserID:     p.UserID,
		HighRisk:   true,
		Details:    map[string]interface{}{"prefix": issued.Prefix, "role": issued.Role},
	}) {
		return
	}
	writeJSON(w, http.StatusCreated, map[string]any{
		"id":         issued.ID,
		"prefix":     issued.Prefix,
		"role":       issued.Role,
		"scopes":     issued.Scopes,
		"secret":     issued.Secret, // returned once
		"created_at": issued.CreatedAt,
		"expires_at": nullTimePtr(issued.ExpiresAt),
	})
}

func adminRevokeAPIKey(w http.ResponseWriter, r *http.Request, deps adminDeps, keyID string) {
	p := auth.PrincipalFromContext(r.Context())
	ok, err := deps.keys.Revoke(r.Context(), p.OrgID, keyID)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	if !ok {
		http.Error(w, "api key not found or already revoked", http.StatusNotFound)
		return
	}
	id := keyID
	if !recordAdminAudit(w, r, deps, audit.Entry{
		EventType:  audit.EventManagedKeyRevoke,
		EntityType: "api_key",
		EntityID:   &id,
		UserID:     p.UserID,
		HighRisk:   true,
	}) {
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func adminUpsertMembership(w http.ResponseWriter, r *http.Request, deps adminDeps) {
	var body struct {
		UserID string `json:"user_id"`
		OrgID  string `json:"organization_id"`
		Role   string `json:"role"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		http.Error(w, "invalid JSON body", http.StatusBadRequest)
		return
	}
	p := auth.PrincipalFromContext(r.Context())
	orgID, ok := auth.ResolveAdminOrgScope(w, r, body.OrgID)
	if !ok {
		return
	}
	if !auth.CanAssignRole(p.Role, body.Role) {
		auth.WriteForbidden(w)
		return
	}
	if err := deps.membership.UpsertMembership(r.Context(), body.UserID, orgID, body.Role); err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	if deps.sessions != nil {
		if store := deps.sessions.ServerStore(); store != nil {
			_, _ = store.RevokeUserSessions(r.Context(), body.UserID, orgID)
		}
	}
	if !recordAdminAudit(w, r, deps, audit.Entry{
		EventType:  audit.EventMembershipChange,
		EntityType: "membership",
		UserID:     p.UserID,
		HighRisk:   true,
		Details: map[string]interface{}{
			"source_organization_id": p.OrgID,
			"target_user_id":         body.UserID,
			"organization_id":        orgID,
			"role":                   body.Role,
		},
	}) {
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"ok": true})
}

func adminAssignConnection(w http.ResponseWriter, r *http.Request, deps adminDeps) {
	var body struct {
		OrgID        string `json:"organization_id"`
		ConnectionID string `json:"connection_id"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		http.Error(w, "invalid JSON body", http.StatusBadRequest)
		return
	}
	p := auth.PrincipalFromContext(r.Context())
	orgID, ok := auth.ResolveAdminOrgScope(w, r, body.OrgID)
	if !ok {
		return
	}
	if err := deps.connAuthz.AssignConnection(r.Context(), orgID, body.ConnectionID); err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	if !recordAdminAudit(w, r, deps, audit.Entry{
		EventType:  audit.EventConnectionAuthz,
		EntityType: "connection_assignment",
		UserID:     p.UserID,
		HighRisk:   true,
		Details: map[string]interface{}{
			"organization_id": orgID,
			"connection_id":   body.ConnectionID,
			"action":          "assign",
		},
	}) {
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"ok": true})
}

func adminGrantConnectionPermission(w http.ResponseWriter, r *http.Request, deps adminDeps) {
	var body struct {
		OrgID        string          `json:"organization_id"`
		ConnectionID string          `json:"connection_id"`
		PrincipalID  string          `json:"principal_id"`
		Actions      map[string]bool `json:"actions"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		http.Error(w, "invalid JSON body", http.StatusBadRequest)
		return
	}
	p := auth.PrincipalFromContext(r.Context())
	orgID, ok := auth.ResolveAdminOrgScope(w, r, body.OrgID)
	if !ok {
		return
	}
	if err := deps.connAuthz.GrantPermission(r.Context(), orgID, body.ConnectionID, body.PrincipalID, body.Actions); err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	if !recordAdminAudit(w, r, deps, audit.Entry{
		EventType:  audit.EventConnectionAuthz,
		EntityType: "connection_permission",
		UserID:     p.UserID,
		HighRisk:   true,
		Details: map[string]interface{}{
			"organization_id": orgID,
			"connection_id":   body.ConnectionID,
			"principal_id":    body.PrincipalID,
			"action":          "grant",
		},
	}) {
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"ok": true})
}

func adminRevokeConnectionPermission(w http.ResponseWriter, r *http.Request, deps adminDeps) {
	var body struct {
		OrgID        string `json:"organization_id"`
		ConnectionID string `json:"connection_id"`
		PrincipalID  string `json:"principal_id"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		http.Error(w, "invalid JSON body", http.StatusBadRequest)
		return
	}
	p := auth.PrincipalFromContext(r.Context())
	orgID, ok := auth.ResolveAdminOrgScope(w, r, body.OrgID)
	if !ok {
		return
	}
	if err := deps.connAuthz.RevokePermission(r.Context(), orgID, body.ConnectionID, body.PrincipalID); err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	if !recordAdminAudit(w, r, deps, audit.Entry{
		EventType:  audit.EventConnectionAuthz,
		EntityType: "connection_permission",
		UserID:     p.UserID,
		HighRisk:   true,
		Details: map[string]interface{}{
			"organization_id": orgID,
			"connection_id":   body.ConnectionID,
			"principal_id":    body.PrincipalID,
			"action":          "revoke",
		},
	}) {
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func adminListOrganizations(w http.ResponseWriter, r *http.Request, deps adminDeps) {
	orgs, err := deps.membership.ListOrganizations(r.Context())
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	if orgs == nil {
		orgs = []auth.Organization{}
	}
	writeJSON(w, http.StatusOK, map[string]any{"organizations": orgs})
}

func adminCreateOrganization(w http.ResponseWriter, r *http.Request, deps adminDeps) {
	var body struct {
		Name string `json:"name"`
		Slug string `json:"slug"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		http.Error(w, "invalid JSON body", http.StatusBadRequest)
		return
	}
	id, err := deps.membership.CreateOrganization(r.Context(), body.Name, body.Slug)
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	p := auth.PrincipalFromContext(r.Context())
	if !recordAdminAudit(w, r, deps, audit.Entry{
		EventType:  audit.EventMembershipChange,
		EntityType: "organization",
		EntityID:   &id,
		UserID:     p.UserID,
		HighRisk:   true,
		Details: map[string]interface{}{
			"name":   body.Name,
			"slug":   body.Slug,
			"action": "create",
		},
	}) {
		return
	}
	writeJSON(w, http.StatusCreated, map[string]any{"id": id, "name": body.Name, "slug": body.Slug})
}

func adminListMemberships(w http.ResponseWriter, r *http.Request, deps adminDeps) {
	orgID, ok := auth.ResolveAdminOrgScope(w, r, r.URL.Query().Get("organization_id"))
	if !ok {
		return
	}
	members, err := deps.membership.ListOrgMembers(r.Context(), orgID)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	if members == nil {
		members = []auth.Membership{}
	}
	writeJSON(w, http.StatusOK, map[string]any{"memberships": members})
}

func adminRevokeMembership(w http.ResponseWriter, r *http.Request, deps adminDeps) {
	var body struct {
		UserID string `json:"user_id"`
		OrgID  string `json:"organization_id"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		http.Error(w, "invalid JSON body", http.StatusBadRequest)
		return
	}
	p := auth.PrincipalFromContext(r.Context())
	orgID, ok := auth.ResolveAdminOrgScope(w, r, body.OrgID)
	if !ok {
		return
	}
	if err := deps.membership.RevokeMembership(r.Context(), body.UserID, orgID); err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	if deps.sessions != nil {
		if store := deps.sessions.ServerStore(); store != nil {
			_, _ = store.RevokeUserSessions(r.Context(), body.UserID, orgID)
		}
	}
	if !recordAdminAudit(w, r, deps, audit.Entry{
		EventType:  audit.EventMembershipChange,
		EntityType: "membership",
		UserID:     p.UserID,
		HighRisk:   true,
		Details: map[string]interface{}{
			"target_user_id":  body.UserID,
			"organization_id": orgID,
			"action":          "revoke",
		},
	}) {
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func adminListConnectionAssignments(w http.ResponseWriter, r *http.Request, deps adminDeps) {
	orgID, ok := auth.ResolveAdminOrgScope(w, r, r.URL.Query().Get("organization_id"))
	if !ok {
		return
	}
	ids, err := deps.connAuthz.ListAssignedConnections(r.Context(), orgID)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	if ids == nil {
		ids = []string{}
	}
	writeJSON(w, http.StatusOK, map[string]any{"connection_ids": ids, "organization_id": orgID})
}

func adminUnassignConnection(w http.ResponseWriter, r *http.Request, deps adminDeps) {
	var body struct {
		OrgID        string `json:"organization_id"`
		ConnectionID string `json:"connection_id"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		http.Error(w, "invalid JSON body", http.StatusBadRequest)
		return
	}
	p := auth.PrincipalFromContext(r.Context())
	orgID, ok := auth.ResolveAdminOrgScope(w, r, body.OrgID)
	if !ok {
		return
	}
	if err := deps.connAuthz.UnassignConnection(r.Context(), orgID, body.ConnectionID); err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	if deps.pools != nil {
		deps.pools.InvalidateOrgReadOnlyPool(orgID, body.ConnectionID)
	}
	if !recordAdminAudit(w, r, deps, audit.Entry{
		EventType:  audit.EventConnectionAuthz,
		EntityType: "connection_assignment",
		UserID:     p.UserID,
		HighRisk:   true,
		Details: map[string]interface{}{
			"organization_id": orgID,
			"connection_id":   body.ConnectionID,
			"action":          "unassign",
		},
	}) {
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func adminListConnectionSecrets(w http.ResponseWriter, r *http.Request, deps adminDeps) {
	if deps.orgSecrets == nil {
		http.Error(w, "connection secrets store is not configured", http.StatusServiceUnavailable)
		return
	}
	orgID, ok := auth.ResolveAdminOrgScope(w, r, r.URL.Query().Get("organization_id"))
	if !ok {
		return
	}
	items, err := deps.orgSecrets.List(r.Context(), orgID)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"secrets": items, "organization_id": orgID})
}

func adminUpsertConnectionSecret(w http.ResponseWriter, r *http.Request, deps adminDeps) {
	if deps.orgSecrets == nil {
		http.Error(w, "connection secrets store is not configured", http.StatusServiceUnavailable)
		return
	}
	var body struct {
		OrgID          string   `json:"organization_id"`
		ConnectionID   string   `json:"connection_id"`
		DSN            string   `json:"dsn"`
		AllowedSchemas []string `json:"allowed_schemas"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		http.Error(w, "invalid JSON body", http.StatusBadRequest)
		return
	}
	p := auth.PrincipalFromContext(r.Context())
	orgID, ok := auth.ResolveAdminOrgScope(w, r, body.OrgID)
	if !ok {
		return
	}
	if err := db.VerifyTenantDSNWithOptions(r.Context(), body.DSN, db.TenantDSNVerifyOptions{
		RequireTLS:     true,
		AllowedSchemas: body.AllowedSchemas,
	}); err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	if err := config.ValidateTenantAllowedSchemas(body.AllowedSchemas); err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	if err := deps.orgSecrets.Upsert(r.Context(), orgID, body.ConnectionID, body.DSN, body.AllowedSchemas); err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	if deps.pools != nil {
		deps.pools.InvalidateOrgReadOnlyPool(orgID, body.ConnectionID)
	}
	if !recordAdminAudit(w, r, deps, audit.Entry{
		EventType:  audit.EventConnectionAuthz,
		EntityType: "connection_secret",
		UserID:     p.UserID,
		HighRisk:   true,
		Details: map[string]interface{}{
			"organization_id": orgID,
			"connection_id":   body.ConnectionID,
			"action":          "upsert_secret",
		},
	}) {
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"ok": true})
}

func adminDeleteConnectionSecret(w http.ResponseWriter, r *http.Request, deps adminDeps) {
	if deps.orgSecrets == nil {
		http.Error(w, "connection secrets store is not configured", http.StatusServiceUnavailable)
		return
	}
	var body struct {
		OrgID        string `json:"organization_id"`
		ConnectionID string `json:"connection_id"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		http.Error(w, "invalid JSON body", http.StatusBadRequest)
		return
	}
	p := auth.PrincipalFromContext(r.Context())
	orgID, ok := auth.ResolveAdminOrgScope(w, r, body.OrgID)
	if !ok {
		return
	}
	if err := deps.orgSecrets.Delete(r.Context(), orgID, body.ConnectionID); err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	if deps.pools != nil {
		deps.pools.InvalidateOrgReadOnlyPool(orgID, body.ConnectionID)
	}
	if !recordAdminAudit(w, r, deps, audit.Entry{
		EventType:  audit.EventConnectionAuthz,
		EntityType: "connection_secret",
		UserID:     p.UserID,
		HighRisk:   true,
		Details: map[string]interface{}{
			"organization_id": orgID,
			"connection_id":   body.ConnectionID,
			"action":          "delete_secret",
		},
	}) {
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}

func nullTimePtr(t time.Time) *time.Time {
	if t.IsZero() {
		return nil
	}
	return &t
}
