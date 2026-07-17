package db_test

import (
	"context"
	"testing"

	"github.com/pgquerynarrative/pgquerynarrative/app/config"
	"github.com/pgquerynarrative/pgquerynarrative/app/db"
)

func TestNewPools_GlobalConnectionBudget(t *testing.T) {
	_, err := db.NewPools(context.Background(), config.DatabaseConfig{
		Host:             "localhost",
		Port:             5432,
		Database:         "pgquerynarrative",
		User:             "app",
		Password:         "app",
		ReadOnlyUser:     "readonly",
		ReadOnlyPassword: "readonly",
		SSLMode:          "disable",
		MaxConnections:   10,
		GlobalMaxConns:   15,
		Connections: []config.DataConnectionConfig{
			{ID: "a", Host: "localhost", Port: 5432, Database: "pgquerynarrative", ReadOnlyUser: "ro", ReadOnlyPassword: "ro", SSLMode: "disable"},
			{ID: "b", Host: "localhost", Port: 5432, Database: "pgquerynarrative", ReadOnlyUser: "ro", ReadOnlyPassword: "ro", SSLMode: "disable"},
		},
	})
	if err == nil {
		t.Fatal("expected global connection budget error before opening pools")
	}
}
