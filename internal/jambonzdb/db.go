// SPDX-License-Identifier: MPL-2.0

// Package jambonzdb talks to jambonz's own MySQL database.
//
// Everything else in this provider goes through the REST API, which is where it
// belongs. This package exists for the one thing the API cannot do for us: mint
// the first API key. Every REST call authenticates with an API key, so a cluster
// built from nothing has a chicken-and-egg problem — the credential that would
// let Terraform create a credential is the credential we are trying to create.
// jambonz's own answer is db/create-admin-token.sql, an INSERT run by hand
// during install.
//
// So this is that INSERT, made declarative. It is deliberately the narrowest
// possible seam: one table, three statements, no schema migrations, no other
// resource routed this way.
//
// The trade is the same one any direct-to-database provider makes — nothing
// validates the write on the way in. What makes it defensible here is that
// api_keys is a trivial table (see db/jambones-sql.sql in jambonz-api-server):
// two CHAR(36) UUIDs and two nullable foreign keys. In particular the token is
// stored in CLEARTEXT — jambonz hashes user passwords, but an API key is
// compared as-is — so there is no derivation to reproduce and no way to write a
// row that the API server accepts now and rejects later.
package jambonzdb

import (
	"database/sql"
	"fmt"
	"net/url"
	"strings"

	"github.com/go-sql-driver/mysql"
)

// Client owns the connection pool. It is created once, at provider
// configuration, and shared by every resource that needs it.
type Client struct {
	db *sql.DB
}

// Open prepares a pool against the jambonz database.
//
// sql.Open does not dial, so an unreachable database does not fail provider
// configuration — it fails on the first statement, in the resource that needed
// it. That is the behaviour Terraform wants: a plan touching only API-backed
// resources should not require database connectivity.
func Open(dsn string) (*Client, error) {
	cfg, err := parseDSN(dsn)
	if err != nil {
		return nil, err
	}

	connector, err := mysql.NewConnector(cfg)
	if err != nil {
		return nil, fmt.Errorf("failed to build a MySQL connector: %w", err)
	}

	return &Client{db: sql.OpenDB(connector)}, nil
}

// Close releases the pool. Provider configuration has no teardown hook, so in
// practice this is only used by tests; the process exiting is what closes it in
// a real run.
func (c *Client) Close() error { return c.db.Close() }

// parseDSN accepts either form a practitioner is likely to have to hand: the
// go-sql-driver DSN (`user:pass@tcp(host:3306)/jambones`) and the URL form
// (`mysql://user:pass@host:3306/jambones`) that connection strings are usually
// stored as. Neither is more correct; refusing one of them would just mean the
// value has to be rewritten on the way in, which is one more place for it to be
// rewritten wrongly.
//
// ParseTime is forced on either way. Without it the driver hands back
// created_at as a []byte and the scan into time.Time fails — a detail of this
// package, not something a connection string should have to know.
func parseDSN(dsn string) (*mysql.Config, error) {
	if strings.TrimSpace(dsn) == "" {
		return nil, fmt.Errorf("the provider's `database` argument is required: it is the connection string for the jambonz MySQL database")
	}

	if strings.Contains(dsn, "://") {
		var err error
		dsn, err = dsnFromURL(dsn)
		if err != nil {
			return nil, err
		}
	}

	cfg, err := mysql.ParseDSN(dsn)
	if err != nil {
		return nil, fmt.Errorf("failed to parse the `database` connection string: %w", err)
	}
	cfg.ParseTime = true

	return cfg, nil
}

func dsnFromURL(raw string) (string, error) {
	u, err := url.Parse(raw)
	if err != nil {
		return "", fmt.Errorf("failed to parse the `database` connection string as a URL: %w", err)
	}
	if u.Scheme != "mysql" {
		return "", fmt.Errorf("unsupported scheme %q in the `database` connection string: jambonz stores its data in MySQL", u.Scheme)
	}

	cfg := mysql.NewConfig()
	cfg.Net = "tcp"
	cfg.Addr = u.Host
	if u.Port() == "" {
		cfg.Addr = u.Host + ":3306"
	}
	cfg.DBName = strings.TrimPrefix(u.Path, "/")
	if u.User != nil {
		cfg.User = u.User.Username()
		cfg.Passwd, _ = u.User.Password()
	}
	// Query parameters carry the driver's own options (tls, timeouts, ...), and
	// the driver is the thing that knows what they mean.
	out := cfg.FormatDSN()
	if q := u.RawQuery; q != "" {
		sep := "?"
		if strings.Contains(out, "?") {
			sep = "&"
		}
		out += sep + q
	}

	return out, nil
}
