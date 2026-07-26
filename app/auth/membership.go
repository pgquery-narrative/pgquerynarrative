package auth

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"os"
	"strings"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

// ErrNoOrganizationMembership is returned when a user has no org membership.
var ErrNoOrganizationMembership = errors.New("user is not a member of any organization")

// MembershipStore resolves organization membership for authenticated users.
type MembershipStore struct {
	pool            *pgxpool.Pool
	autoJoinDefault bool
}

// NewMembershipStore creates a membership resolver backed by PostgreSQL.
func NewMembershipStore(pool *pgxpool.Pool, autoJoinDefault bool) *MembershipStore {
	if pool == nil {
		return nil
	}
	return &MembershipStore{pool: pool, autoJoinDefault: autoJoinDefault}
}

// Membership is one organization membership row.
type Membership struct {
	OrgID  string `json:"organization_id"`
	Role   string `json:"role"`
	UserID string `json:"user_id,omitempty"`
}

// ResolvePrincipal maps an authenticated user to an org membership.
// preferredOrgID may come from X-Organization-ID; empty uses sole membership or default org.
func (s *MembershipStore) ResolvePrincipal(ctx context.Context, userID, preferredOrgID, fallbackRole string) (Principal, error) {
	if s == nil || s.pool == nil {
		if membershipRequired() {
			return Principal{}, ErrNoOrganizationMembership
		}
		return Principal{UserID: userID, OrgID: DefaultOrgID(), Role: normalizeRole(fallbackRole)}, nil
	}
	userID = strings.TrimSpace(userID)
	if userID == "" {
		return Principal{}, ErrNoOrganizationMembership
	}

	members, err := s.listMemberships(ctx, userID)
	if err != nil {
		return Principal{}, err
	}
	if len(members) == 0 {
		if s.autoJoinDefault {
			m, joinErr := s.ensureDefaultMembership(ctx, userID, fallbackRole)
			if joinErr != nil {
				return Principal{}, joinErr
			}
			members = []Membership{m}
		} else {
			return Principal{}, ErrNoOrganizationMembership
		}
	}

	preferredOrgID = strings.TrimSpace(preferredOrgID)
	if preferredOrgID != "" {
		for _, m := range members {
			if m.OrgID == preferredOrgID {
				return Principal{UserID: userID, OrgID: m.OrgID, Role: m.Role}, nil
			}
		}
		return Principal{}, ErrNoOrganizationMembership
	}

	def := DefaultOrgID()
	for _, m := range members {
		if m.OrgID == def {
			return Principal{UserID: userID, OrgID: m.OrgID, Role: m.Role}, nil
		}
	}
	// Stable choice: first membership by org id order from query.
	m := members[0]
	return Principal{UserID: userID, OrgID: m.OrgID, Role: m.Role}, nil
}

// ResolveFromGroupClaims maps OIDC group claims to organizations when configured.
func (s *MembershipStore) ResolveFromGroupClaims(ctx context.Context, userID, preferredOrgID, fallbackRole string, groups []string) (Principal, error) {
	if s == nil || s.pool == nil {
		if membershipRequired() {
			return Principal{}, ErrNoOrganizationMembership
		}
		return Principal{UserID: userID, OrgID: DefaultOrgID(), Role: normalizeRole(fallbackRole)}, nil
	}
	if len(groups) > 0 {
		rows, err := s.pool.Query(ctx, `
			SELECT organization_id::text, role, group_claim
			FROM app.oidc_group_org_mappings
			WHERE group_claim = ANY($1)
			ORDER BY group_claim
		`, groups)
		if err != nil {
			return Principal{}, err
		}
		defer rows.Close()
		type mapping struct {
			orgID string
			role  string
		}
		seenOrgs := map[string]mapping{}
		for rows.Next() {
			var orgID, role, claim string
			if scanErr := rows.Scan(&orgID, &role, &claim); scanErr != nil {
				return Principal{}, scanErr
			}
			seenOrgs[orgID] = mapping{orgID: orgID, role: role}
		}
		if err := rows.Err(); err != nil {
			return Principal{}, err
		}
		if len(seenOrgs) > 1 {
			// Ambiguous multi-group mapping must not silently pick the first organisation.
			return Principal{}, ErrNoOrganizationMembership
		}
		if len(seenOrgs) == 1 {
			var m mapping
			for _, v := range seenOrgs {
				m = v
			}
			role := normalizeRole(m.role)
			if role == "" {
				role = normalizeRole(fallbackRole)
			}
			_, _ = s.pool.Exec(ctx, `
				INSERT INTO app.organization_members (organization_id, user_id, role)
				VALUES ($1::uuid, $2, $3)
				ON CONFLICT (organization_id, user_id) DO UPDATE SET role = EXCLUDED.role
			`, m.orgID, strings.TrimSpace(userID), role)
			if preferredOrgID != "" && preferredOrgID != m.orgID {
				return Principal{}, ErrNoOrganizationMembership
			}
			return Principal{UserID: userID, OrgID: m.orgID, Role: role}, nil
		}
	}
	return s.ResolvePrincipal(ctx, userID, preferredOrgID, fallbackRole)
}

func (s *MembershipStore) listMemberships(ctx context.Context, userID string) ([]Membership, error) {
	rows, err := s.pool.Query(ctx, `
		SELECT organization_id::text, role
		FROM app.organization_members
		WHERE user_id = $1
		ORDER BY organization_id
	`, userID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []Membership
	for rows.Next() {
		var m Membership
		if err := rows.Scan(&m.OrgID, &m.Role); err != nil {
			return nil, err
		}
		m.Role = normalizeRole(m.Role)
		out = append(out, m)
	}
	return out, rows.Err()
}

// MembershipDetail includes organisation name/slug for UI switchers.
type MembershipDetail struct {
	OrgID  string `json:"organization_id"`
	Role   string `json:"role"`
	Name   string `json:"name"`
	Slug   string `json:"slug"`
	UserID string `json:"user_id,omitempty"`
}

// ListMembershipDetails returns memberships joined with organisation metadata.
func (s *MembershipStore) ListMembershipDetails(ctx context.Context, userID string) ([]MembershipDetail, error) {
	if s == nil || s.pool == nil {
		return nil, fmt.Errorf("membership store is not configured")
	}
	userID = strings.TrimSpace(userID)
	if userID == "" {
		return nil, fmt.Errorf("user_id is required")
	}
	rows, err := s.pool.Query(ctx, `
		SELECT m.organization_id::text, m.role, o.name, o.slug
		FROM app.organization_members m
		JOIN app.organizations o ON o.id = m.organization_id
		WHERE m.user_id = $1
		ORDER BY o.slug
	`, userID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []MembershipDetail
	for rows.Next() {
		var d MembershipDetail
		if err := rows.Scan(&d.OrgID, &d.Role, &d.Name, &d.Slug); err != nil {
			return nil, err
		}
		d.Role = normalizeRole(d.Role)
		d.UserID = userID
		out = append(out, d)
	}
	return out, rows.Err()
}

// ListMemberships returns all organisation memberships for userID.
func (s *MembershipStore) ListMemberships(ctx context.Context, userID string) ([]Membership, error) {
	if s == nil || s.pool == nil {
		return nil, fmt.Errorf("membership store is not configured")
	}
	userID = strings.TrimSpace(userID)
	if userID == "" {
		return nil, fmt.Errorf("user_id is required")
	}
	return s.listMemberships(ctx, userID)
}

// Organization is a tenant row.
type Organization struct {
	ID   string `json:"id"`
	Name string `json:"name"`
	Slug string `json:"slug"`
}

// ListOrganizations returns all organisations (admin inventory).
func (s *MembershipStore) ListOrganizations(ctx context.Context) ([]Organization, error) {
	if s == nil || s.pool == nil {
		return nil, fmt.Errorf("membership store is not configured")
	}
	rows, err := s.pool.Query(ctx, `
		SELECT id::text, name, slug FROM app.organizations ORDER BY slug
	`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []Organization
	for rows.Next() {
		var o Organization
		if err := rows.Scan(&o.ID, &o.Name, &o.Slug); err != nil {
			return nil, err
		}
		out = append(out, o)
	}
	return out, rows.Err()
}

// CreateOrganization inserts a new organisation and returns its id.
func (s *MembershipStore) CreateOrganization(ctx context.Context, name, slug string) (string, error) {
	if s == nil || s.pool == nil {
		return "", fmt.Errorf("membership store is not configured")
	}
	name = strings.TrimSpace(name)
	slug = strings.TrimSpace(strings.ToLower(slug))
	if name == "" || slug == "" {
		return "", fmt.Errorf("name and slug are required")
	}
	var id string
	err := s.pool.QueryRow(ctx, `
		INSERT INTO app.organizations (name, slug)
		VALUES ($1, $2)
		RETURNING id::text
	`, name, slug).Scan(&id)
	return id, err
}

// ListOrgMembers returns memberships for one organisation.
func (s *MembershipStore) ListOrgMembers(ctx context.Context, orgID string) ([]Membership, error) {
	if s == nil || s.pool == nil {
		return nil, fmt.Errorf("membership store is not configured")
	}
	orgID = strings.TrimSpace(orgID)
	if orgID == "" {
		return nil, fmt.Errorf("organization_id is required")
	}
	rows, err := s.pool.Query(ctx, `
		SELECT organization_id::text, role, user_id
		FROM app.organization_members
		WHERE organization_id = $1::uuid
		ORDER BY user_id
	`, orgID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []Membership
	for rows.Next() {
		var m Membership
		if err := rows.Scan(&m.OrgID, &m.Role, &m.UserID); err != nil {
			return nil, err
		}
		m.Role = normalizeRole(m.Role)
		out = append(out, m)
	}
	return out, rows.Err()
}

// RevokeMembership removes a user from an organisation.
func (s *MembershipStore) RevokeMembership(ctx context.Context, userID, orgID string) error {
	if s == nil || s.pool == nil {
		return fmt.Errorf("membership store is not configured")
	}
	userID = strings.TrimSpace(userID)
	orgID = strings.TrimSpace(orgID)
	if userID == "" || orgID == "" {
		return fmt.Errorf("user_id and organization_id are required")
	}
	return execWithOrg(ctx, s.pool, orgID, `
		DELETE FROM app.organization_members
		WHERE organization_id = $1::uuid AND user_id = $2
	`, orgID, userID)
}

func (s *MembershipStore) ensureDefaultMembership(ctx context.Context, userID, fallbackRole string) (Membership, error) {
	role := normalizeRole(fallbackRole)
	if role == "" {
		role = RoleAnalyst
	}
	orgID := DefaultOrgID()
	_, err := s.pool.Exec(ctx, `
		INSERT INTO app.organization_members (organization_id, user_id, role)
		VALUES ($1::uuid, $2, $3)
		ON CONFLICT (organization_id, user_id) DO NOTHING
	`, orgID, userID, role)
	if err != nil {
		return Membership{}, err
	}
	var m Membership
	err = s.pool.QueryRow(ctx, `
		SELECT organization_id::text, role
		FROM app.organization_members
		WHERE organization_id = $1::uuid AND user_id = $2
	`, orgID, userID).Scan(&m.OrgID, &m.Role)
	if errors.Is(err, pgx.ErrNoRows) {
		return Membership{}, ErrNoOrganizationMembership
	}
	if err != nil {
		return Membership{}, err
	}
	m.Role = normalizeRole(m.Role)
	return m, nil
}

// OrganizationHeader is the optional request header selecting an active organization.
const OrganizationHeader = "X-Organization-ID"

// PreferredOrgFromRequest reads the optional active-org header.
func PreferredOrgFromRequest(r *http.Request) string {
	if r == nil {
		return ""
	}
	return strings.TrimSpace(r.Header.Get(OrganizationHeader))
}

// membershipRequired is true in StrictMode: never fall back to an implicit default org
// when the membership store is unavailable.
func membershipRequired() bool {
	env := strings.ToLower(strings.TrimSpace(os.Getenv("APP_ENV")))
	if env == "production" || env == "prod" {
		return true
	}
	return strings.EqualFold(strings.TrimSpace(os.Getenv("SECURITY_STRICT")), "true")
}
