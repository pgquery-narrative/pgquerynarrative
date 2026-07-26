package main

import (
	"encoding/json"
	"net/http"
	"strings"

	"github.com/pgquerynarrative/pgquerynarrative/app/auth"
)

type meDeps struct {
	membership *auth.MembershipStore
	sessions   *auth.SessionManager
}

func mountMeAPI(mux *http.ServeMux, deps meDeps) {
	mux.HandleFunc("/api/v1/me", func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet {
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
			return
		}
		p := auth.PrincipalFromContext(r.Context())
		writeJSON(w, http.StatusOK, map[string]any{
			"user_id":         p.UserID,
			"organization_id": p.OrgID,
			"role":            p.Role,
		})
	})
	mux.HandleFunc("/api/v1/me/organizations", func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet {
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
			return
		}
		p := auth.PrincipalFromContext(r.Context())
		if deps.membership == nil {
			writeJSON(w, http.StatusOK, map[string]any{"organizations": []any{}})
			return
		}
		details, err := deps.membership.ListMembershipDetails(r.Context(), p.UserID)
		if err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
		if details == nil {
			details = []auth.MembershipDetail{}
		}
		writeJSON(w, http.StatusOK, map[string]any{"organizations": details})
	})
	mux.HandleFunc("/api/v1/me/organization", func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
			return
		}
		var body struct {
			OrgID string `json:"organization_id"`
		}
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			http.Error(w, "invalid JSON body", http.StatusBadRequest)
			return
		}
		orgID := strings.TrimSpace(body.OrgID)
		if orgID == "" {
			http.Error(w, "organization_id is required", http.StatusBadRequest)
			return
		}
		p := auth.PrincipalFromContext(r.Context())
		if deps.membership == nil {
			http.Error(w, "membership store is not configured", http.StatusServiceUnavailable)
			return
		}
		resolved, err := deps.membership.ResolvePrincipal(r.Context(), p.UserID, orgID, p.Role)
		if err != nil {
			http.Error(w, "not a member of organization", http.StatusForbidden)
			return
		}
		if deps.sessions != nil && deps.sessions.Enabled() {
			if s, readErr := deps.sessions.Read(r); readErr == nil && s != nil {
				s.OrgID = resolved.OrgID
				s.Role = resolved.Role
				if issueErr := deps.sessions.Issue(w, *s); issueErr != nil {
					http.Error(w, "failed to update session", http.StatusInternalServerError)
					return
				}
			}
		}
		writeJSON(w, http.StatusOK, map[string]any{
			"user_id":         resolved.UserID,
			"organization_id": resolved.OrgID,
			"role":            resolved.Role,
		})
	})
}
