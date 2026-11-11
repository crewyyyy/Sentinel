package app

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"fmt"

	"github.com/jackc/pgx/v5"
	"sentinel/internal/config"
	"sentinel/internal/rules"
	"sentinel/internal/storage"
	"sentinel/prompts"
)

const (
	maxRuntimePolicyBytes = 48 << 10 // Leave room for JSONB spacing and panel metadata.
	maxCachedPolicies     = 64
)

func (e *Engine) policySnapshot(analyzer *rules.Analyzer) any {
	return struct {
		Rules    rules.Config
		Links    config.Links
		Trust    config.Trust
		Analysis any
	}{analyzer.Resolved().Config, e.Config.Links, e.Config.Trust, analyzer.Snapshot()}
}

func (e *Engine) policyDigest() (string, error) {
	raw, err := json.Marshal(e.policySnapshot(e.Analyzer))
	if err != nil {
		return "", fmt.Errorf("encode base module policy: %w", err)
	}
	return fmt.Sprintf("%x", sha256.Sum256(raw)), nil
}

func (e *Engine) registerAnalyzer(ctx context.Context, tx pgx.Tx, analyzer *rules.Analyzer, source string) error {
	snapshot := e.policySnapshot(analyzer)
	version := analyzer.Resolved().Config.Version
	if _, err := tx.Exec(ctx, `INSERT INTO ruleset_versions(version,rules,model,prompt_version,source) VALUES($1,$2,$3,$4,$5) ON CONFLICT DO NOTHING`, version, snapshot, e.Config.AI.Model, prompts.CurrentVersion, source); err != nil {
		return err
	}
	var same bool
	if err := tx.QueryRow(ctx, `SELECT rules=$2::jsonb FROM ruleset_versions WHERE version=$1`, version, snapshot).Scan(&same); err != nil {
		return err
	}
	if !same {
		return fmt.Errorf("ruleset_version already exists with different rules; use a new version")
	}
	return nil
}

// Compiled policies are immutable; the instance-local cache has a fixed size.
// PostgreSQL remains authoritative for the selected revision, including after restart.
func (e *Engine) compileModulePolicy(chat int64, setting storage.ModuleSetting) (*rules.Analyzer, error) {
	if setting.Version == 0 && len(setting.Overrides.Modules) == 0 && len(setting.Overrides.FamilyCaps) == 0 {
		return e.Analyzer, nil
	}
	baseDigest, err := e.policyDigest()
	if err != nil {
		return nil, err
	}
	raw, err := json.Marshal(struct {
		Base           string
		Chat, Revision int64
		Overrides      rules.Options
	}{baseDigest, chat, setting.Version, setting.Overrides})
	if err != nil {
		return nil, fmt.Errorf("encode module policy")
	}
	if len(raw) > maxRuntimePolicyBytes {
		return nil, fmt.Errorf("module settings too large")
	}
	version := fmt.Sprintf("rt-%x", sha256.Sum256(raw))
	e.analysisMu.Lock()
	defer e.analysisMu.Unlock()
	if analyzer := e.analysisCache[version]; analyzer != nil {
		return analyzer, nil
	}
	base := e.Analyzer.Resolved()
	c := base.Config
	c.Version = version
	analyzer, err := rules.NewAnalyzer(c, rules.MergeOptions(base.Options, setting.Overrides))
	if err != nil {
		return nil, fmt.Errorf("invalid module policy")
	}
	if len(e.analysisCache) >= maxCachedPolicies {
		e.analysisCache = nil
	}
	if e.analysisCache == nil {
		e.analysisCache = map[string]*rules.Analyzer{}
	}
	e.analysisCache[version] = analyzer
	return analyzer, nil
}

func (e *Engine) chatAnalyzer(ctx context.Context, tx pgx.Tx, chat int64) (*rules.Analyzer, error) {
	setting, err := storage.CurrentModules(ctx, tx, chat)
	if err != nil {
		return nil, err
	}
	analyzer, err := e.compileModulePolicy(chat, setting)
	if err != nil {
		return nil, err
	}
	if setting.Version > 0 {
		if err = e.registerAnalyzer(ctx, tx, analyzer, "telegram_module_settings"); err != nil {
			return nil, err
		}
	}
	return analyzer, nil
}
