package storage

import (
	"context"
	"github.com/jackc/pgx/v5"
)

type ModelSetting struct {
	Model   string
	Version int64
}

// CurrentModel falls back to the deployment configuration only until an explicit runtime choice is saved.
func CurrentModel(ctx context.Context, tx interface {
	QueryRow(context.Context, string, ...any) pgx.Row
}, fallback string) (ModelSetting, error) {
	var setting ModelSetting
	err := tx.QueryRow(ctx, `SELECT COALESCE((SELECT model FROM runtime_ai_model WHERE id=true),$1::text),COALESCE((SELECT version FROM runtime_ai_model WHERE id=true),0)`, fallback).Scan(&setting.Model, &setting.Version)
	return setting, err
}
