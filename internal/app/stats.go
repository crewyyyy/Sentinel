package app

import (
	"context"
	"github.com/jackc/pgx/v5"
)

func (e *Engine) syncAggregates(ctx context.Context, tx pgx.Tx, chat, user int64) error {
	_, err := tx.Exec(ctx, `UPDATE chat_user_stats SET
 active_days=(SELECT count(*) FROM daily_activity WHERE chat_id=$1 AND user_id=$2 AND day>=(now() AT TIME ZONE 'UTC')::date-$3::int AND messages_count>0),
 admin_replies_30d=COALESCE((SELECT sum(admin_replies) FROM daily_activity WHERE chat_id=$1 AND user_id=$2 AND day>=(now() AT TIME ZONE 'UTC')::date-$3::int),0),
 distinct_repliers_30d=(SELECT count(DISTINCT r) FROM daily_activity d,unnest(d.replier_ids) r WHERE d.chat_id=$1 AND d.user_id=$2 AND day>=(now() AT TIME ZONE 'UTC')::date-$3::int)
 WHERE chat_id=$1 AND user_id=$2`, chat, user, e.Config.Trust.StatsWindowDays)
	return err
}
