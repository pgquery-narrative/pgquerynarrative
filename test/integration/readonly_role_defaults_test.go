package integration

import (
	"testing"
	"time"

	"github.com/pgquerynarrative/pgquerynarrative/app/queryrunner"
)

// A role can change its own stored defaults (ALTER ROLE ... RESET) whenever it holds a read-write
// transaction, and PostgreSQL offers no way to stop it. The application must therefore never rely on
// those defaults: every pooled connection sets the search path and the timeouts itself, and every user
// statement runs in an explicitly READ ONLY transaction. This wipes the role's defaults and proves it.
func TestReadOnlyGuardsDoNotDependOnRoleDefaults(t *testing.T) {
	admin, connStr, ctx := multiOrgPostgres(t)
	pools, _, _ := multiOrgPools(t, admin, connStr)
	t.Cleanup(pools.Close)

	// After the pools exist (the helper installs the role's defaults) and before the lazily opened
	// read-only pool makes its first connection.
	if _, err := admin.Exec(ctx, `ALTER ROLE pgquerynarrative_readonly RESET ALL`); err != nil {
		t.Fatal(err)
	}
	var stored []string
	if err := admin.QueryRow(ctx, `SELECT COALESCE(rolconfig, '{}') FROM pg_roles WHERE rolname = 'pgquerynarrative_readonly'`).Scan(&stored); err != nil {
		t.Fatal(err)
	}
	if len(stored) != 0 {
		t.Fatalf("the role still has stored defaults %v; the test needs them gone", stored)
	}

	pool := pools.ReadOnly(ctx, "default")
	if pool == nil {
		t.Fatal("no read-only pool")
	}
	runner := queryrunner.NewRunner(pool, queryrunner.NewValidator([]string{"demo"}, 100000), 10, 30*time.Second)
	res, err := runner.Run(ctx, `SELECT current_setting('statement_timeout') AS st, current_setting('search_path') AS sp,
		current_setting('transaction_read_only') AS ro, current_setting('default_transaction_read_only') AS dro`, 1)
	if err != nil {
		t.Fatalf("run: %v", err)
	}
	if len(res.Rows) != 1 {
		t.Fatalf("rows = %v", res.Rows)
	}
	row := res.Rows[0]
	if row[0] == "0" || row[0] == "0ms" {
		t.Errorf("statement_timeout = %v with the role defaults gone: the application did not set its own", row[0])
	}
	if sp, _ := row[1].(string); sp != "demo" && sp != `"demo"` {
		t.Errorf("search_path = %v, want the configured schemas", row[1])
	}
	if row[2] != "on" {
		t.Errorf("transaction_read_only = %v, the statement must run in a READ ONLY transaction whatever the role default is", row[2])
	}
	t.Logf("with the role's defaults wiped, the application's own session: statement_timeout=%v search_path=%v default_transaction_read_only=%v", row[0], row[1], row[3])
}
