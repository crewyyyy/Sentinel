CREATE TABLE runtime_ai_model (
    id boolean PRIMARY KEY DEFAULT true CHECK (id),
    model text NOT NULL CHECK (length(model) BETWEEN 1 AND 160),
    version bigint NOT NULL CHECK (version > 0),
    updated_at timestamptz NOT NULL DEFAULT now()
);
CREATE TABLE model_change_events (
    id bigint GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
    idempotency_key text NOT NULL UNIQUE,
    admin_user_id bigint NOT NULL,
    old_model text NOT NULL,
    new_model text NOT NULL,
    created_at timestamptz NOT NULL DEFAULT now()
);
CREATE TABLE model_panels (
    id bigint GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
    chat_id bigint NOT NULL,
    owner_user_id bigint NOT NULL,
    telegram_message_id bigint,
    payload jsonb NOT NULL,
    expires_at timestamptz NOT NULL DEFAULT (now() + interval '30 minutes'),
    created_at timestamptz NOT NULL DEFAULT now(),
    updated_at timestamptz NOT NULL DEFAULT now()
);
CREATE INDEX model_panels_expiry ON model_panels(expires_at);
ALTER TABLE ai_attempts ADD COLUMN model text NOT NULL DEFAULT '';
ALTER TABLE ai_attempts ADD COLUMN prompt_version text NOT NULL DEFAULT '';
