package service

import (
	"context"
	"errors"

	"github.com/pgquerynarrative/pgquerynarrative/app/auth"
)

// orgNotFound wraps resource-not-found errors for cross-organization access attempts.
func orgNotFound() error {
	return errors.New("resource not found")
}

func orgID(ctx context.Context) string {
	return auth.OrgIDFromContext(ctx)
}
