CREATE TABLE chat_module_settings (
    chat_id bigint PRIMARY KEY CHECK (chat_id < 0),
    version bigint NOT NULL CHECK (version > 0),
    overrides jsonb NOT NULL CHECK (jsonb_typeof(overrides) = 'object' AND octet_length(overrides::text) <= 65536),
    updated_at timestamptz NOT NULL DEFAULT now()
);
CREATE TABLE module_change_events (
    id bigint GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
    idempotency_key text NOT NULL UNIQUE,
    chat_id bigint NOT NULL,
    admin_user_id bigint NOT NULL,
    version bigint NOT NULL,
    old_overrides jsonb NOT NULL,
    new_overrides jsonb NOT NULL,
    ruleset_version text NOT NULL,
    created_at timestamptz NOT NULL DEFAULT now(),
    UNIQUE(chat_id,version)
);
CREATE TABLE module_panels (
    id bigint GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
    destination_chat_id bigint NOT NULL,
    owner_user_id bigint NOT NULL CHECK (owner_user_id > 0),
    origin_chat_id bigint NOT NULL DEFAULT 0,
    telegram_message_id bigint,
    payload jsonb NOT NULL CHECK (octet_length(payload::text) <= 131072),
    expires_at timestamptz NOT NULL DEFAULT (now() + interval '30 minutes'),
    created_at timestamptz NOT NULL DEFAULT now(),
    updated_at timestamptz NOT NULL DEFAULT now()
);
CREATE INDEX module_panels_expiry ON module_panels(expires_at);
