// SPDX-License-Identifier: MPL-2.0

package jambonzdb

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"time"

	"github.com/google/uuid"
)

// APIKey is a row of jambonz's api_keys table.
//
// The scope of a key is which of the two foreign keys is set, and jambonz reads
// it exactly this way (lib/routes/api/api-keys.js):
//
//	both NULL             an admin key — every service provider, every account
//	service_provider_sid  scoped to one service provider
//	account_sid           scoped to one account
//
// The API server refuses to create a key whose scope is wider than the key
// making the request. Nothing enforces that here, because there is no requesting
// key: whoever holds the database credentials already has more than admin.
type APIKey struct {
	Sid                string
	Token              string
	AccountSid         *string
	ServiceProviderSid *string
	CreatedAt          time.Time
}

// ErrNotFound is returned by GetAPIKey when the row is gone, so the caller can
// tell "deleted out of band" apart from "the query failed".
var ErrNotFound = errors.New("api key not found")

// CreateAPIKey mints a key the same way the API server does.
//
// jambonz's POST /ApiKeys sets `req.body.token = uuidv4()` and lets its model
// layer assign the sid the same way; the token is returned to the caller once,
// in the create response, and is thereafter readable from the database in the
// clear. So: two v4 UUIDs and an INSERT, which is the whole of it.
func (c *Client) CreateAPIKey(ctx context.Context, accountSid, serviceProviderSid *string) (*APIKey, error) {
	key := &APIKey{
		Sid:                uuid.NewString(),
		Token:              uuid.NewString(),
		AccountSid:         accountSid,
		ServiceProviderSid: serviceProviderSid,
	}

	const stmt = `INSERT INTO api_keys (api_key_sid, token, account_sid, service_provider_sid) VALUES (?, ?, ?, ?)`
	if _, err := c.db.ExecContext(ctx, stmt, key.Sid, key.Token, key.AccountSid, key.ServiceProviderSid); err != nil {
		return nil, fmt.Errorf("failed to insert into api_keys: %w", err)
	}

	// created_at is a column default, so the row knows it and we do not. Reading
	// it back also proves the insert landed in the database we think it did.
	created, err := c.GetAPIKey(ctx, key.Sid)
	if err != nil {
		return nil, err
	}

	return created, nil
}

// GetAPIKey reads one row, returning ErrNotFound if it is no longer there.
func (c *Client) GetAPIKey(ctx context.Context, sid string) (*APIKey, error) {
	const stmt = `SELECT api_key_sid, token, account_sid, service_provider_sid, created_at FROM api_keys WHERE api_key_sid = ?`

	var key APIKey
	err := c.db.QueryRowContext(ctx, stmt, sid).Scan(
		&key.Sid,
		&key.Token,
		&key.AccountSid,
		&key.ServiceProviderSid,
		&key.CreatedAt,
	)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, fmt.Errorf("failed to read api_keys: %w", err)
	}

	return &key, nil
}

// DeleteAPIKey removes the row. A key that is already gone is not an error —
// the desired state is "no such key", and it holds.
func (c *Client) DeleteAPIKey(ctx context.Context, sid string) error {
	const stmt = `DELETE FROM api_keys WHERE api_key_sid = ?`
	if _, err := c.db.ExecContext(ctx, stmt, sid); err != nil {
		return fmt.Errorf("failed to delete from api_keys: %w", err)
	}

	return nil
}
