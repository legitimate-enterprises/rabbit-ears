-- Friends: two adults who offer each other as share and Watch together
-- targets without either joining the other's household.

-- A pair of adults, stored once with the smaller id (bytewise) first.
CREATE TABLE friendship (
  a_id BLOB NOT NULL REFERENCES account(id) ON DELETE CASCADE,
  b_id BLOB NOT NULL REFERENCES account(id) ON DELETE CASCADE,
  created_at INTEGER NOT NULL,
  sync_version INTEGER NOT NULL DEFAULT 0,
  PRIMARY KEY (a_id, b_id),
  CHECK (a_id < b_id)
);
CREATE INDEX friendship_b ON friendship(b_id);
CREATE INDEX friendship_sync ON friendship(sync_version);

-- A request names an email, never an account, and no column of it changes
-- after it is made: sending one reads nothing of whoever holds the email,
-- and nothing the sender can read of it tells what became of it. Its
-- receiver is whichever adult holds the email, the sender aside.
CREATE TABLE friend_request (
  id BLOB NOT NULL PRIMARY KEY,
  from_id BLOB NOT NULL REFERENCES account(id) ON DELETE CASCADE,
  email TEXT NOT NULL COLLATE NOCASE,
  expires_at INTEGER NOT NULL,
  created_at INTEGER NOT NULL,
  modified_at INTEGER NOT NULL,
  created_by BLOB REFERENCES account(id) ON DELETE SET NULL,
  modified_by BLOB REFERENCES account(id) ON DELETE SET NULL,
  sync_version INTEGER NOT NULL DEFAULT 0,
  UNIQUE (from_id, email)
);
CREATE INDEX friend_request_email ON friend_request(email);
CREATE INDEX friend_request_expires ON friend_request(expires_at);
CREATE INDEX friend_request_sync ON friend_request(sync_version);

-- A receiver's decline, kept apart from the request and keyed by the
-- receiver, so neither it nor its tombstone reaches the sender.
CREATE TABLE friend_request_decline (
  account_id BLOB NOT NULL REFERENCES account(id) ON DELETE CASCADE,
  request_id BLOB NOT NULL REFERENCES friend_request(id) ON DELETE CASCADE,
  created_at INTEGER NOT NULL,
  sync_version INTEGER NOT NULL DEFAULT 0,
  PRIMARY KEY (account_id, request_id)
);
CREATE INDEX friend_request_decline_request ON friend_request_decline(request_id);
CREATE INDEX friend_request_decline_sync ON friend_request_decline(sync_version);
