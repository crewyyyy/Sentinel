package storage

import (
	"context"
	"encoding/json"
	"fmt"

	"github.com/jackc/pgx/v5"
	"sentinel/internal/rules"
)

type ModuleSetting struct {
	Version   int64
	Overrides rules.Options
}

func CurrentModules(ctx context.Context, db interface {
	QueryRow(context.Context, string, ...any) pgx.Row
}, chat int64) (ModuleSetting, error) {
	var setting ModuleSetting
	var raw []byte
	err := db.QueryRow(ctx, `SELECT COALESCE((SELECT version FROM chat_module_settings WHERE chat_id=$1),0),COALESCE((SELECT overrides FROM chat_module_settings WHERE chat_id=$1),'{}'::jsonb)`, chat).Scan(&setting.Version, &raw)
	if err != nil {
		return setting, err
	}
	if len(raw) > 65536 || json.Unmarshal(raw, &setting.Overrides) != nil {
		return setting, fmt.Errorf("invalid saved module settings")
	}
	return setting, nil
}
