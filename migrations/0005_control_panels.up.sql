CREATE TABLE control_panels (
    id bigint GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
    chat_id bigint NOT NULL,
    owner_user_id bigint NOT NULL CHECK (owner_user_id > 0),
    telegram_message_id bigint,
    view text NOT NULL DEFAULT 'home' CHECK (view IN ('home','chats','status','connect','help','requests')),
    page integer NOT NULL DEFAULT 0 CHECK (page BETWEEN 0 AND 3),
    revision bigint NOT NULL DEFAULT 1 CHECK (revision > 0),
    expires_at timestamptz NOT NULL DEFAULT (now() + interval '24 hours'),
    created_at timestamptz NOT NULL DEFAULT now(),
    updated_at timestamptz NOT NULL DEFAULT now()
);
CREATE INDEX control_panels_expiry ON control_panels(expires_at);
CREATE INDEX control_panels_owner_time ON control_panels(owner_user_id,created_at);
