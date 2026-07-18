package auth

import (
	"context"
	"errors"
	"net/http"
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
	OrgID string
	Role  string
}

// ResolvePrincipal maps an authenticated user to an org membership.
// preferredOrgID may come from X-Organization-ID; empty uses sole membership or default org.
func (s *MembershipStore) ResolvePrincipal(ctx context.Context, userID, preferredOrgID, fallbackRole string) (Principal, error) {
	if s == nil || s.pool == nil {
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
