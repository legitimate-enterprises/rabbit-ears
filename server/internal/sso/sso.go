// Package sso holds the OpenID Connect providers adults sign in with and the
// identities that link an account to each, with their repository.
package sso

import (
	"database/sql"
	"time"

	"github.com/legitimate-enterprises/rabbit-ears/server/internal/db"
)

// Provider is an OpenID Connect issuer and this server's client at it. An
// empty ClientSecret is a public client; an empty RequiredGroup requires
// none. LinksByEmail is the operator saying the provider's accounts are
// theirs, so its email claim may claim an account.
type Provider struct {
	ID            int64
	Name          string
	Issuer        string
	ClientID      string
	ClientSecret  string
	RequiredGroup string
	LinksByEmail  bool
	Enabled       bool
	CreatedAt     time.Time
	UpdatedAt     time.Time
}

// Identity links an account to the subject a provider knows it by: one per
// account and provider, one account per subject.
type Identity struct {
	ID         int64
	AccountID  int64
	ProviderID int64
	Subject    string
	Email      string
	CreatedAt  time.Time
}

// Repo persists providers and identities.
type Repo struct{ d db.DBTX }

// New returns a repository over d.
func New(d db.DBTX) *Repo { return &Repo{d} }

const cols = `id, name, issuer, client_id, client_secret, required_group, links_by_email, enabled, created_at, updated_at`

func scan(s db.Scanner) (*Provider, error) {
	var p Provider
	var secret, group sql.NullString
	var created, updated int64
	if err := s.Scan(&p.ID, &p.Name, &p.Issuer, &p.ClientID, &secret, &group, &p.LinksByEmail, &p.Enabled, &created, &updated); err != nil {
		return nil, db.Wrap(err)
	}
	p.ClientSecret, p.RequiredGroup = secret.String, group.String
	p.CreatedAt, p.UpdatedAt = db.FromMs(created), db.FromMs(updated)
	return &p, nil
}

// Create inserts p and fills ID and timestamps.
func (r *Repo) Create(p *Provider) error {
	now := time.Now().UTC()
	p.CreatedAt, p.UpdatedAt = now, now
	res, err := r.d.Exec(`INSERT INTO sso_provider (name, issuer, client_id, client_secret, required_group, links_by_email, enabled, created_at, updated_at)
		VALUES (?,?,?,?,?,?,?,?,?)`, p.Name, p.Issuer, p.ClientID, db.NullStr(p.ClientSecret), db.NullStr(p.RequiredGroup), p.LinksByEmail, p.Enabled,
		db.Ms(now), db.Ms(now))
	if err != nil {
		return db.Wrap(err)
	}
	p.ID, _ = res.LastInsertId()
	return nil
}

// Get returns one provider or db.ErrNotFound.
func (r *Repo) Get(id int64) (*Provider, error) {
	return scan(r.d.QueryRow(`SELECT `+cols+` FROM sso_provider WHERE id = ?`, id))
}

// List returns every provider by name.
func (r *Repo) List() ([]*Provider, error) {
	return r.query(`SELECT ` + cols + ` FROM sso_provider ORDER BY name COLLATE NOCASE, id`)
}

// Enabled returns the providers a sign-in page offers, by name.
func (r *Repo) Enabled() ([]*Provider, error) {
	return r.query(`SELECT ` + cols + ` FROM sso_provider WHERE enabled = 1 ORDER BY name COLLATE NOCASE, id`)
}

func (r *Repo) query(q string, args ...any) ([]*Provider, error) {
	rows, err := r.d.Query(q, args...)
	return db.Collect(rows, err, scan)
}

// Update writes every mutable field of p.
func (r *Repo) Update(p *Provider) error {
	p.UpdatedAt = time.Now().UTC()
	return db.ExecOne(r.d, `UPDATE sso_provider SET name=?, issuer=?, client_id=?, client_secret=?, required_group=?, links_by_email=?, enabled=?,
		updated_at=? WHERE id=?`, p.Name, p.Issuer, p.ClientID, db.NullStr(p.ClientSecret), db.NullStr(p.RequiredGroup), p.LinksByEmail, p.Enabled,
		db.Ms(p.UpdatedAt), p.ID)
}

// Delete removes a provider and its identities.
func (r *Repo) Delete(id int64) error {
	return db.ExecOne(r.d, `DELETE FROM sso_provider WHERE id = ?`, id)
}

const identityCols = `id, account_id, provider_id, subject, email, created_at`

func scanIdentity(s db.Scanner) (*Identity, error) {
	var i Identity
	var email sql.NullString
	var created int64
	if err := s.Scan(&i.ID, &i.AccountID, &i.ProviderID, &i.Subject, &email, &created); err != nil {
		return nil, db.Wrap(err)
	}
	i.Email, i.CreatedAt = email.String, db.FromMs(created)
	return &i, nil
}

// CreateIdentity inserts i; db.ErrConflict when the subject or the
// account already has one at the provider.
func (r *Repo) CreateIdentity(i *Identity) error {
	i.CreatedAt = time.Now().UTC()
	res, err := r.d.Exec(`INSERT INTO account_identity (account_id, provider_id, subject, email, created_at) VALUES (?,?,?,?,?)`,
		i.AccountID, i.ProviderID, i.Subject, db.NullStr(i.Email), db.Ms(i.CreatedAt))
	if err != nil {
		return db.Wrap(err)
	}
	i.ID, _ = res.LastInsertId()
	return nil
}

// IdentityBySubject finds the identity a provider's subject signs in as.
func (r *Repo) IdentityBySubject(providerID int64, subject string) (*Identity, error) {
	return scanIdentity(r.d.QueryRow(`SELECT `+identityCols+` FROM account_identity WHERE provider_id = ? AND subject = ?`, providerID, subject))
}

// Identities lists an account's identities, oldest first.
func (r *Repo) Identities(accountID int64) ([]*Identity, error) {
	rows, err := r.d.Query(`SELECT `+identityCols+` FROM account_identity WHERE account_id = ? ORDER BY id`, accountID)
	return db.Collect(rows, err, scanIdentity)
}

// DeleteIdentity unlinks one of an account's identities.
func (r *Repo) DeleteIdentity(accountID, id int64) error {
	return db.ExecOne(r.d, `DELETE FROM account_identity WHERE id = ? AND account_id = ?`, id, accountID)
}
