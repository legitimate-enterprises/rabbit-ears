// Package sso holds the OpenID Connect providers adults sign in with and the
// identities that link an account to each, with their repository.
package sso

import (
	"database/sql"
	"time"

	"github.com/legitimate-enterprises/rabbit-ears/server/internal/db"
	"github.com/legitimate-enterprises/rabbit-ears/server/internal/uid"
)

// Provider is an OpenID Connect issuer and this server's client at it. An
// empty ClientSecret is a public client; an empty RequiredGroup requires
// none. LinksByEmail is the operator saying the provider's accounts are
// theirs, so its email claim may claim an account.
type Provider struct {
	ID            uid.ID
	Name          string
	Issuer        string
	ClientID      string
	ClientSecret  string
	RequiredGroup string
	LinksByEmail  bool
	Enabled       bool
	CreatedAt     time.Time
	ModifiedAt    time.Time
	CreatedBy     *uid.ID
	ModifiedBy    *uid.ID
}

// Identity links an account to the subject a provider knows it by: one per
// account and provider, one account per subject.
type Identity struct {
	ID         uid.ID
	AccountID  uid.ID
	ProviderID uid.ID
	Subject    string
	Email      string
	CreatedAt  time.Time
	ModifiedAt time.Time
}

// Repo persists providers and identities.
type Repo struct{ d db.DBTX }

// New returns a repository over d.
func New(d db.DBTX) *Repo { return &Repo{d} }

const cols = `id, name, issuer, client_id, client_secret, required_group, links_by_email, enabled, created_at, modified_at, created_by, modified_by`

func scan(s db.Scanner) (*Provider, error) {
	var p Provider
	var secret, group sql.NullString
	var created, modified int64
	if err := s.Scan(&p.ID, &p.Name, &p.Issuer, &p.ClientID, &secret, &group, &p.LinksByEmail, &p.Enabled, &created, &modified,
		&p.CreatedBy, &p.ModifiedBy); err != nil {
		return nil, db.Wrap(err)
	}
	p.ClientSecret, p.RequiredGroup = secret.String, group.String
	p.CreatedAt, p.ModifiedAt = db.FromMs(created), db.FromMs(modified)
	return &p, nil
}

// Create inserts p, minting its ID unless set, and fills the audit fields.
func (r *Repo) Create(p *Provider, by *uid.ID) error {
	if p.ID.IsZero() {
		p.ID = uid.New()
	}
	now := time.Now().UTC().Truncate(time.Millisecond)
	p.CreatedAt, p.ModifiedAt, p.CreatedBy, p.ModifiedBy = now, now, by, by
	_, err := r.d.Exec(`INSERT INTO sso_provider (id, name, issuer, client_id, client_secret, required_group, links_by_email, enabled,
		created_at, modified_at, created_by, modified_by) VALUES (?,?,?,?,?,?,?,?,?,?,?,?)`, p.ID, p.Name, p.Issuer, p.ClientID,
		db.NullStr(p.ClientSecret), db.NullStr(p.RequiredGroup), p.LinksByEmail, p.Enabled, db.Ms(now), db.Ms(now), by, by)
	return db.Wrap(err)
}

// Get returns one provider or db.ErrNotFound.
func (r *Repo) Get(id uid.ID) (*Provider, error) {
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
func (r *Repo) Update(p *Provider, by *uid.ID) error {
	p.ModifiedAt, p.ModifiedBy = time.Now().UTC().Truncate(time.Millisecond), by
	return db.ExecOne(r.d, `UPDATE sso_provider SET name=?, issuer=?, client_id=?, client_secret=?, required_group=?, links_by_email=?, enabled=?,
		modified_at=?, modified_by=? WHERE id=?`, p.Name, p.Issuer, p.ClientID, db.NullStr(p.ClientSecret), db.NullStr(p.RequiredGroup), p.LinksByEmail,
		p.Enabled, db.Ms(p.ModifiedAt), by, p.ID)
}

// Delete removes a provider and its identities.
func (r *Repo) Delete(id uid.ID) error {
	return db.ExecOne(r.d, `DELETE FROM sso_provider WHERE id = ?`, id)
}

const identityCols = `id, account_id, provider_id, subject, email, created_at, modified_at`

func scanIdentity(s db.Scanner) (*Identity, error) {
	var i Identity
	var email sql.NullString
	var created, modified int64
	if err := s.Scan(&i.ID, &i.AccountID, &i.ProviderID, &i.Subject, &email, &created, &modified); err != nil {
		return nil, db.Wrap(err)
	}
	i.Email, i.CreatedAt, i.ModifiedAt = email.String, db.FromMs(created), db.FromMs(modified)
	return &i, nil
}

// CreateIdentity inserts i, minting its ID unless set; db.ErrConflict when
// the subject or the account already has one at the provider.
func (r *Repo) CreateIdentity(i *Identity) error {
	if i.ID.IsZero() {
		i.ID = uid.New()
	}
	now := time.Now().UTC().Truncate(time.Millisecond)
	i.CreatedAt, i.ModifiedAt = now, now
	_, err := r.d.Exec(`INSERT INTO account_identity (id, account_id, provider_id, subject, email, created_at, modified_at) VALUES (?,?,?,?,?,?,?)`,
		i.ID, i.AccountID, i.ProviderID, i.Subject, db.NullStr(i.Email), db.Ms(now), db.Ms(now))
	return db.Wrap(err)
}

// IdentityBySubject finds the identity a provider's subject signs in as.
func (r *Repo) IdentityBySubject(providerID uid.ID, subject string) (*Identity, error) {
	return scanIdentity(r.d.QueryRow(`SELECT `+identityCols+` FROM account_identity WHERE provider_id = ? AND subject = ?`, providerID, subject))
}

// Identities lists an account's identities, oldest first.
func (r *Repo) Identities(accountID uid.ID) ([]*Identity, error) {
	rows, err := r.d.Query(`SELECT `+identityCols+` FROM account_identity WHERE account_id = ? ORDER BY id`, accountID)
	return db.Collect(rows, err, scanIdentity)
}

// DeleteIdentity unlinks one of an account's identities.
func (r *Repo) DeleteIdentity(accountID, id uid.ID) error {
	return db.ExecOne(r.d, `DELETE FROM account_identity WHERE id = ? AND account_id = ?`, id, accountID)
}
