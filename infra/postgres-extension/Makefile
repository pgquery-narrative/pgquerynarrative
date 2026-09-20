# Makefile for building and installing PgQueryNarrative PostgreSQL extension

PG_CONFIG ?= pg_config
EXTENSION_NAME = pgquerynarrative
VERSION = 1.1

# Detect PostgreSQL installation
PGXS := $(shell $(PG_CONFIG) --pgxs)
include $(PGXS)

# Extension files
EXTENSION_SQL = $(EXTENSION_NAME)--$(VERSION).sql
EXTENSION_CONTROL = $(EXTENSION_NAME).control

# Installation target
install: $(EXTENSION_CONTROL) $(EXTENSION_SQL)
	$(MAKE) -C . install

# Clean build artifacts
clean:
	rm -f $(EXTENSION_NAME)--*.sql.bak

.PHONY: install clean
