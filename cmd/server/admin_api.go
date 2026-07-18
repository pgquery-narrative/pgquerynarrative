package main

import (
	"encoding/json"
	"net/http"
	"strings"
	"time"

	"github.com/pgquerynarrative/pgquerynarrative/app/audit"
	"github.com/pgquerynarrative/pgquerynarrative/app/auth"
)

type adminDeps struct {
	keys       *auth.ManagedKeyStore
	membership *auth.MembershipStore
	connAuthz  *auth.ConnectionAuthorizer
	auditStore *audit.Store
}

func requireAdmin(w http.ResponseWriter, r *http.Request) bool {
	if auth.RoleFromContext(r.Context()) != auth.RoleAdmin {
		auth.WriteForbidden(w)
		return false
	}
	return true
}

func mountAdminAPI(mux *http.ServeMux, deps adminDeps) {
	mux.HandleFunc("/api/v1/admin/api-keys", func(w http.ResponseWriter, r *http.Request) {
		if !requireAdmin(w, r) {
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
		if !requireAdmin(w, r) {
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
	mux.HandleFunc("/api/v1/admin/memberships", func(w http.ResponseWriter, r *http.Request) {
		if !requireAdmin(w, r) {
			return
		}
		if r.Method != http.MethodPost {
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
			return
		}
		adminUpsertMembership(w, r, deps)
	})
	mux.HandleFunc("/api/v1/admin/connection-assignments", func(w http.ResponseWriter, r *http.Request) {
		if !requireAdmin(w, r) {
			return
		}
		if r.Method != http.MethodPost {
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
			return
		}
		adminAssignConnection(w, r, deps)
	})
	mux.HandleFunc("/api/v1/admin/connection-permissions", func(w http.ResponseWriter, r *http.Request) {
		if !requireAdmin(w, r) {
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
	if deps.auditStore != nil {
		id := issued.ID
		_ = deps.auditStore.Record(r.Context(), audit.Entry{
			EventType:  audit.EventManagedKeyCreate,
			EntityType: "api_key",
			EntityID:   &id,
			UserID:     p.UserID,
			HighRisk:   true,
			Details:    map[string]interface{}{"prefix": issued.Prefix, "role": issued.Role},
		})
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
	if deps.auditStore != nil {
		id := keyID
		_ = deps.auditStore.Record(r.Context(), audit.Entry{
			EventType:  audit.EventManagedKeyRevoke,
			EntityType: "api_key",
			EntityID:   &id,
			UserID:     p.UserID,
			HighRisk:   true,
		})
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
	orgID := strings.TrimSpace(body.OrgID)
	if orgID == "" {
		orgID = p.OrgID
	}
	if err := deps.membership.UpsertMembership(r.Context(), body.UserID, orgID, body.Role); err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	if deps.auditStore != nil {
		_ = deps.auditStore.Record(r.Context(), audit.Entry{
			EventType:  audit.EventMembershipChange,
			EntityType: "membership",
			UserID:     p.UserID,
			HighRisk:   true,
			Details: map[string]interface{}{
				"target_user_id":  body.UserID,
				"organization_id": orgID,
				"role":            body.Role,
			},
		})
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
	orgID := strings.TrimSpace(body.OrgID)
	if orgID == "" {
		orgID = p.OrgID
	}
	if err := deps.connAuthz.AssignConnection(r.Context(), orgID, body.ConnectionID); err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	if deps.auditStore != nil {
		_ = deps.auditStore.Record(r.Context(), audit.Entry{
			EventType:  audit.EventConnectionAuthz,
			EntityType: "connection_assignment",
			UserID:     p.UserID,
			HighRisk:   true,
			Details: map[string]interface{}{
				"organization_id": orgID,
				"connection_id":   body.ConnectionID,
				"action":          "assign",
			},
		})
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
	orgID := strings.TrimSpace(body.OrgID)
	if orgID == "" {
		orgID = p.OrgID
	}
	if err := deps.connAuthz.GrantPermission(r.Context(), orgID, body.ConnectionID, body.PrincipalID, body.Actions); err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	if deps.auditStore != nil {
		_ = deps.auditStore.Record(r.Context(), audit.Entry{
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
		})
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
	orgID := strings.TrimSpace(body.OrgID)
	if orgID == "" {
		orgID = p.OrgID
	}
	if err := deps.connAuthz.RevokePermission(r.Context(), orgID, body.ConnectionID, body.PrincipalID); err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	if deps.auditStore != nil {
		_ = deps.auditStore.Record(r.Context(), audit.Entry{
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
		})
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
