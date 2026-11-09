CREATE TABLE managed_chats (
    chat_id bigint PRIMARY KEY CHECK (chat_id < 0),
    active boolean NOT NULL,
    source text NOT NULL CHECK (source IN ('configured','onboarding')),
    approved_by bigint,
    approved_at timestamptz,
    membership_at timestamptz NOT NULL DEFAULT '-infinity',
    updated_at timestamptz NOT NULL DEFAULT now()
);
CREATE TABLE chat_connection_requests (
    id bigint GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
    chat_id bigint NOT NULL CHECK (chat_id < 0),
    owner_user_id bigint NOT NULL CHECK (owner_user_id > 0),
    title text NOT NULL DEFAULT '',
    chat_type text NOT NULL CHECK (chat_type IN ('group','supergroup')),
    status text NOT NULL DEFAULT 'pending' CHECK (status IN ('pending','approved','rejected','cancelled')),
    revision bigint NOT NULL DEFAULT 1,
    telegram_message_id bigint,
    membership_at timestamptz NOT NULL,
    expires_at timestamptz NOT NULL DEFAULT (now() + interval '24 hours'),
    created_at timestamptz NOT NULL DEFAULT now(),
    updated_at timestamptz NOT NULL DEFAULT now(),
    UNIQUE(chat_id,owner_user_id)
);
CREATE INDEX chat_connection_requests_expiry ON chat_connection_requests(expires_at);
CREATE TABLE chat_connection_events (
    id bigint GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
    idempotency_key text NOT NULL UNIQUE,
    chat_id bigint NOT NULL,
    actor_user_id bigint,
    action text NOT NULL CHECK (action IN ('discovered','approved','rejected','disconnected')),
    created_at timestamptz NOT NULL DEFAULT now()
);
