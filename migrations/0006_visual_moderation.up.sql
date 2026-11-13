CREATE TABLE visual_checks (
    cache_key text PRIMARY KEY,
    media jsonb NOT NULL,
    status text NOT NULL DEFAULT 'pending' CHECK (status IN ('pending','processing','done')),
    attempts integer NOT NULL DEFAULT 0,
    lease_token bigint NOT NULL DEFAULT 0,
    lease_until timestamptz,
    available_at timestamptz NOT NULL DEFAULT now(),
    result jsonb,
    last_error text NOT NULL DEFAULT '',
    expires_at timestamptz NOT NULL DEFAULT (now()+interval '30 days'),
    updated_at timestamptz NOT NULL DEFAULT now()
);
CREATE INDEX visual_checks_claim ON visual_checks(available_at) WHERE status <> 'done';
CREATE TABLE visual_requests (
    id bigint GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
    chat_id bigint NOT NULL,
    message_id bigint NOT NULL,
    edit_key text NOT NULL,
    payload jsonb NOT NULL,
    result jsonb,
    deferred_notified boolean NOT NULL DEFAULT false,
    check_keys text[] NOT NULL CHECK (cardinality(check_keys) BETWEEN 1 AND 8),
    status text NOT NULL DEFAULT 'pending' CHECK (status IN ('pending','done')),
    created_at timestamptz NOT NULL DEFAULT now(),
    UNIQUE(chat_id,message_id,edit_key)
);
CREATE INDEX visual_requests_pending ON visual_requests(id) WHERE status='pending';
CREATE TABLE visual_frame_cache (
    digest text PRIMARY KEY,
    probability double precision NOT NULL CHECK(probability BETWEEN 0 AND 1),
    expires_at timestamptz NOT NULL
);
CREATE TABLE visual_budget (
    day date PRIMARY KEY,
    requests integer NOT NULL DEFAULT 0 CHECK(requests>=0)
);
