// Package migrations holds canonical SQL migrations, embedded for Store.Migrate.
package migrations

import _ "embed"

// Up is the initial schema, retained for isolated baseline/upgrade tests.
//
//go:embed 0001_init.up.sql
var Up string

//go:embed 0001_init.down.sql
var initialDown string

//go:embed 0002_runtime_models.up.sql
var runtimeModelsUp string

//go:embed 0002_runtime_models.down.sql
var runtimeModelsDown string

//go:embed 0003_chat_onboarding.up.sql
var onboardingUp string

//go:embed 0003_chat_onboarding.down.sql
var onboardingDown string

//go:embed 0004_module_settings.up.sql
var moduleSettingsUp string

//go:embed 0004_module_settings.down.sql
var moduleSettingsDown string

//go:embed 0005_control_panels.up.sql
var controlPanelsUp string

//go:embed 0005_control_panels.down.sql
var controlPanelsDown string

type Migration struct {
	Version string
	SQL     string
}

// Ordered returns fresh values so callers cannot modify the embedded migration list.
func Ordered() []Migration {
	return []Migration{{"0001_init", Up}, {"0002_runtime_models", runtimeModelsUp}, {"0003_chat_onboarding", onboardingUp}, {"0004_module_settings", moduleSettingsUp}, {"0005_control_panels", controlPanelsUp}}
}

// Down reverses all embedded migrations, only for disposable databases/explicit destructive rollback.
var Down = controlPanelsDown + "\n" + moduleSettingsDown + "\n" + onboardingDown + "\n" + runtimeModelsDown + "\n" + initialDown
