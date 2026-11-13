package app

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"testing"

	"sentinel/internal/domain"
	"sentinel/internal/notify"
	"sentinel/internal/rules"
	"sentinel/internal/storage"
)

func modulePanelState(t *testing.T, e *Engine, id int64) notify.ModulePanel {
	t.Helper()
	r, err := readModulePanel(context.Background(), e.Store.Pool, id, false)
	if err != nil {
		t.Fatal(err)
	}
	return r.State
}
func openModules(t *testing.T, e *Engine, update int64) int64 {
	t.Helper()
	m := msg(update, 111, fmt.Sprintf("/modules %d", testChat))
	m.ChatID = adminChat
	deliver(t, e, domain.Update{ID: update, Message: &m})
	id := number(t, e, `SELECT id FROM module_panels ORDER BY id DESC LIMIT 1`)
	drainModelOutbox(t, e, &notifierFake{})
	return id
}
func clickModule(t *testing.T, e *Engine, update, id int64, action string, value int) {
	t.Helper()
	p := modulePanelState(t, e, id)
	deliver(t, e, domain.Update{ID: update, Callback: &domain.Callback{ID: fmt.Sprintf("module-callback-%d", update), UserID: 111, ChatID: adminChat, MessageID: 77, Data: fmt.Sprintf("mod:%d:%d:%s:%d", id, p.Revision, action, value)}})
	drainModelOutbox(t, e, &notifierFake{})
}
func moduleIndex(t *testing.T, id string) int {
	t.Helper()
	for i, spec := range rules.Catalog() {
		if spec.ID == id {
			return i
		}
	}
	t.Fatal("missing module", id)
	return -1
}
func selectModule(t *testing.T, e *Engine, update, id int64, name string) {
	t.Helper()
	index := moduleIndex(t, name)
	p := modulePanelState(t, e, id)
	if p.Page != index/notify.ModulesPerPage {
		clickModule(t, e, update, id, "p", index/notify.ModulesPerPage)
	}
	clickModule(t, e, update+1, id, "m", index)
}

func TestModuleDetailsAreOnDemandAndRemainEditable(t *testing.T) {
	e := testEngine(t)
	id := openModules(t, e, 1)
	selectModule(t, e, 2, id, "sales")
	clickModule(t, e, 4, id, "params", 0)
	if modulePanelState(t, e, id).View != "parameters" {
		t.Fatal("parameters did not open")
	}
	clickModule(t, e, 5, id, "window", 16)
	clickModule(t, e, 6, id, "cap", 25)
	m := msg(7, 111, fmt.Sprintf("/modulelimit %d sensitivity 1", id))
	m.ChatID = adminChat
	deliver(t, e, domain.Update{ID: 7, Message: &m})
	state := modulePanelState(t, e, id)
	if state.Draft.Modules["sales"].Thresholds["sensitivity"] != 1 || state.Draft.FamilyCaps["sales"] != 25 {
		t.Fatal("parameter controls did not edit draft")
	}
	clickModule(t, e, 8, id, "back", 0)
	clickModule(t, e, 9, id, "dict", 0)
	m = msg(10, 111, fmt.Sprintf("/modulewords %d intent phrase продаю доступ", id))
	m.ChatID = adminChat
	deliver(t, e, domain.Update{ID: 10, Message: &m})
	state = modulePanelState(t, e, id)
	if state.View != "dictionary" || len(state.Draft.Modules["sales"].Matchers["intent"].Terms) != 1 {
		t.Fatal("dictionary view cannot edit its own terms")
	}
	clickModule(t, e, 11, id, "back", 0)
	clickModule(t, e, 12, id, "save", 0)
	clickModule(t, e, 13, id, "apply", 0)
	setting, err := storage.CurrentModules(context.Background(), e.Store.Pool, testChat)
	if err != nil || setting.Version != 1 || setting.Overrides.Modules["sales"].Thresholds["match_window_tokens"] != 16 {
		t.Fatal("on-demand settings were not persisted", err)
	}
	selectModule(t, e, 14, id, "observation")
	before := modulePanelState(t, e, id)
	clickModule(t, e, 16, id, "params", 0)
	if state := modulePanelState(t, e, id); state.View != "parameters" || state.Dirty || state.Revision != before.Revision+1 {
		t.Fatal("required module parameters are not read-only")
	}
	before = modulePanelState(t, e, id)
	clickModule(t, e, 17, id, "cap", 0)
	if modulePanelState(t, e, id).Revision != before.Revision {
		t.Fatal("required parameter was editable")
	}
}

func TestLeavingModuleDiscardsDraftAndKeepsSavedSettings(t *testing.T) {
	e := testEngine(t)
	id := openModules(t, e, 1)
	selectModule(t, e, 2, id, "sales")
	clickModule(t, e, 4, id, "mode", 2)
	clickModule(t, e, 5, id, "save", 0)
	clickModule(t, e, 6, id, "apply", 0)
	selectModule(t, e, 7, id, "sales")
	clickModule(t, e, 9, id, "mode", 0)
	clickModule(t, e, 10, id, "back", 0)
	state := modulePanelState(t, e, id)
	if state.View != "list" || state.Dirty || state.Selected != -1 {
		t.Fatal("draft leaked to module list")
	}
	clickModule(t, e, 11, id, "p", 0)
	clickModule(t, e, 12, id, "m", moduleIndex(t, "observation"))
	if modulePanelState(t, e, id).Dirty {
		t.Fatal("another module inherited save/cancel state")
	}
	clickModule(t, e, 13, id, "params", 0)
	clickModule(t, e, 14, id, "back", 0)
	clickModule(t, e, 15, id, "back", 0)
	clickModule(t, e, 16, id, "p", 1)
	clickModule(t, e, 17, id, "m", moduleIndex(t, "sales"))
	state = modulePanelState(t, e, id)
	if state.Draft.Modules["sales"].Enabled == nil || !*state.Draft.Modules["sales"].Enabled || state.SettingVersion != 1 {
		t.Fatal("leaving a module changed saved settings")
	}
	clickModule(t, e, 18, id, "mode", 0)
	clickModule(t, e, 19, id, "save", 0)
	clickModule(t, e, 20, id, "back", 0)
	if state := modulePanelState(t, e, id); state.View != "module" || !state.Dirty {
		t.Fatal("confirmation back lost its own draft")
	}
	clickModule(t, e, 21, id, "cancel", 0)
	state = modulePanelState(t, e, id)
	if state.View != "module" || state.Dirty || !*state.Draft.Modules["sales"].Enabled {
		t.Fatal("cancel did not restore the current module")
	}
	clickModule(t, e, 22, id, "save", 0)
	if number(t, e, `SELECT count(*) FROM module_change_events`) != 1 {
		t.Fatal("discarded draft created a saved change")
	}
}

func TestChatModulesConfirmPersistScopeAndPreserveEvidence(t *testing.T) {
	e := testEngine(t)
	e.Config.AI.Enabled = false
	e.Config.AI.OptionalDailyRequestLimit = 0
	m := msg(1, 123, "Привет")
	deliver(t, e, domain.Update{ID: 1, Message: &m})
	id := openModules(t, e, 10)
	selectModule(t, e, 11, id, "sales")
	clickModule(t, e, 13, id, "mode", 2)
	if number(t, e, `SELECT count(*) FROM chat_module_settings`) != 0 {
		t.Fatal("draft changed live policy")
	}
	clickModule(t, e, 14, id, "save", 0)
	if number(t, e, `SELECT count(*) FROM chat_module_settings`) != 0 {
		t.Fatal("save skipped confirmation")
	}
	clickModule(t, e, 15, id, "apply", 0)
	if number(t, e, `SELECT version FROM chat_module_settings WHERE chat_id=$1`, testChat) != 1 || number(t, e, `SELECT count(*) FROM module_change_events`) != 1 {
		t.Fatal("policy not saved or audited")
	}
	if number(t, e, `SELECT count(*) FROM ai_jobs WHERE payload->'assessment'->>'ruleset_version'='v1'`) != 1 {
		t.Fatal("old pending job rewritten")
	}
	m = msg(2, 123, "Продам доступ к API. Пишите в лс.")
	deliver(t, e, domain.Update{ID: 20, Message: &m})
	if number(t, e, `SELECT count(*) FROM ai_jobs WHERE message_id=2 AND priority='mandatory' AND status='pending' AND payload->'assessment'->'policy_checks' @> '[{"module":"sales","mode":"prohibited"}]'`) != 1 {
		t.Fatal("prohibited sales lost mandatory contextual review")
	}
	if number(t, e, `SELECT count(*) FROM incidents WHERE chat_id=$1`, testChat) != 1 {
		t.Fatal("prohibited policy did not create a pending card")
	}
	other := msg(2, 123, "Продам доступ к API. Пишите в лс.")
	other.ChatID = testChat - 1
	deliver(t, e, domain.Update{ID: 21, Message: &other})
	if number(t, e, `SELECT count(*) FROM risk_assessments WHERE chat_id=$1 AND payload ? 'policy_checks'`, testChat-1) != 0 {
		t.Fatal("policy leaked between chats")
	}
	e = mustEngine(t, e.Store, e.Config, e.Admins, e.BotID, e.Username)
	m = msg(3, 123, "Продам доступ к API на месяц")
	deliver(t, e, domain.Update{ID: 22, Message: &m})
	if number(t, e, `SELECT count(*) FROM ai_jobs WHERE message_id=3 AND payload->'assessment'->'policy_checks' @> '[{"module":"sales","mode":"prohibited"}]'`) != 1 {
		t.Fatal("restart lost policy")
	}
	selectModule(t, e, 23, id, "sales")
	clickModule(t, e, 25, id, "mode", 0)
	clickModule(t, e, 26, id, "save", 0)
	clickModule(t, e, 27, id, "apply", 0)
	// Re-delivery under a new policy is not a content revision or an implicit replay.
	deliver(t, e, domain.Update{ID: 28, Message: &m})
	if number(t, e, `SELECT count(*) FROM risk_assessments WHERE chat_id=$1 AND message_id=3`, testChat) != 1 {
		t.Fatal("policy change silently reassessed old content")
	}
	m = msg(4, 123, "Продам доступ к API на год")
	deliver(t, e, domain.Update{ID: 29, Message: &m})
	if number(t, e, `SELECT count(*) FROM risk_assessments WHERE chat_id=$1 AND message_id=4 AND payload ? 'policy_checks'`, testChat) != 0 {
		t.Fatal("disabled module kept triggering")
	}
}

func TestModulePanelsRejectStaleRequiredAndForgedControls(t *testing.T) {
	e := testEngine(t)
	id := openModules(t, e, 1)
	selectModule(t, e, 2, id, "observation")
	before := modulePanelState(t, e, id)
	clickModule(t, e, 4, id, "mode", 0)
	if modulePanelState(t, e, id).Revision != before.Revision {
		t.Fatal("required module was editable")
	}
	clickModule(t, e, 5, id, "back", 0)
	selectModule(t, e, 6, id, "sales")
	p := modulePanelState(t, e, id)
	for i, cb := range []domain.Callback{
		{UserID: 999, ChatID: adminChat, MessageID: 77},
		{UserID: 111, ChatID: adminChat, MessageID: 88},
		{UserID: 111, ChatID: adminChat + 10, MessageID: 77},
	} {
		cb.ID = fmt.Sprintf("forged-%d", i)
		cb.Data = fmt.Sprintf("mod:%d:%d:mode:2", id, p.Revision)
		deliver(t, e, domain.Update{ID: int64(20 + i), Callback: &cb})
	}
	if modulePanelState(t, e, id).Revision != p.Revision {
		t.Fatal("forged callback mutated draft")
	}
	e.Admins = &adminFake{false}
	clickModule(t, e, 30, id, "mode", 2)
	if modulePanelState(t, e, id).Revision != p.Revision {
		t.Fatal("revoked administrator changed draft")
	}
	e.Admins = &adminFake{true}
	if _, err := e.Store.Pool.Exec(context.Background(), `UPDATE module_panels SET expires_at=now()-interval '1 second' WHERE id=$1`, id); err != nil {
		t.Fatal(err)
	}
	clickModule(t, e, 31, id, "mode", 2)
	if modulePanelState(t, e, id).Revision != p.Revision {
		t.Fatal("expired panel changed draft")
	}
}

func TestModuleSettingsConcurrentPanelsAndDictionaryDraft(t *testing.T) {
	e := testEngine(t)
	first := openModules(t, e, 1)
	second := openModules(t, e, 2)
	selectModule(t, e, 3, first, "sales")
	selectModule(t, e, 6, second, "sales")
	clickModule(t, e, 8, first, "dict", 0)
	if modulePanelState(t, e, first).View != "dictionary" {
		t.Fatal("dictionary did not open")
	}
	clickModule(t, e, 9, first, "back", 0)
	p := modulePanelState(t, e, first)
	m := msg(10, 111, fmt.Sprintf("/modulewords %d intent phrase меняю доступ | продаётся доступ", first))
	m.ChatID = adminChat
	deliver(t, e, domain.Update{ID: 10, Message: &m})
	drainModelOutbox(t, e, &notifierFake{})
	updated := modulePanelState(t, e, first)
	if updated.Revision <= p.Revision || len(updated.Draft.Modules["sales"].Matchers["intent"].Terms) != 2 {
		t.Fatal("dictionary command did not change draft")
	}
	clickModule(t, e, 11, first, "mode", 1)
	clickModule(t, e, 12, first, "save", 0)
	clickModule(t, e, 13, first, "apply", 0)
	clickModule(t, e, 14, second, "mode", 2)
	clickModule(t, e, 15, second, "save", 0)
	clickModule(t, e, 16, second, "apply", 0)
	if number(t, e, `SELECT count(*) FROM module_change_events`) != 1 {
		t.Fatal("stale panel overwrote another admin's policy")
	}
	setting, err := storage.CurrentModules(context.Background(), e.Store.Pool, testChat)
	if err != nil {
		t.Fatal(err)
	}
	if setting.Overrides.Modules["sales"].Thresholds["policy"] != 0 {
		t.Fatal("stale prohibited setting took effect")
	}
}

type publicModuleNotifier struct{ *notifierFake }

func (*publicModuleNotifier) PrivateDestination(context.Context, int64) (bool, error) {
	return false, nil
}

func TestModuleDeliveryRightsPrivacyAndBaseVersion(t *testing.T) {
	for _, kind := range []string{"revoked", "public", "base_changed"} {
		t.Run(kind, func(t *testing.T) {
			e := testEngine(t)
			id := openModules(t, e, 1)
			if _, err := e.Store.Pool.Exec(context.Background(), `UPDATE outbox SET status='pending',available_at=now() WHERE kind='module_panel'`); err != nil {
				t.Fatal(err)
			}
			n := &notifierFake{}
			var notifier Notifier = n
			switch kind {
			case "revoked":
				e.Admins = &adminFake{false}
			case "public":
				notifier = &publicModuleNotifier{n}
			case "base_changed":
				c := *e.Config
				c.Moderation.RulesetVersion = "changed-base"
				e = mustEngine(t, e.Store, &c, e.Admins, e.BotID, e.Username)
			}
			worked, err := e.DeliverOne(context.Background(), notifier)
			if err != nil || !worked {
				t.Fatal(worked, err)
			}
			if n.sends != 0 || n.edits != 0 {
				t.Fatal("settings delivered after access or base changed")
			}
			_ = id
		})
	}
}

func TestModulePolicyCacheAndRebaseAreImmutable(t *testing.T) {
	e := testEngine(t)
	enabled := true
	setting := storage.ModuleSetting{Version: 1, Overrides: rules.Options{Modules: map[string]rules.ModuleConfig{"sales": {Enabled: &enabled, Thresholds: map[string]int{"policy": 1}}}}}
	first, err := e.compileModulePolicy(testChat, setting)
	if err != nil {
		t.Fatal(err)
	}
	second, err := e.compileModulePolicy(testChat, setting)
	if err != nil || first != second {
		t.Fatal("same policy did not reuse analyzer")
	}
	old, err := json.Marshal(first.Snapshot())
	if err != nil {
		t.Fatal(err)
	}
	setting.Overrides.Modules["sales"].Thresholds["policy"] = 0
	next, err := e.compileModulePolicy(testChat, setting)
	if err != nil {
		t.Fatal(err)
	}
	if next == first || next.Resolved().Config.Version == first.Resolved().Config.Version {
		t.Fatal("changed settings reused immutable version")
	}
	still, _ := json.Marshal(first.Snapshot())
	if string(still) != string(old) {
		t.Fatal("caller changed cached policy")
	}
	if !strings.HasPrefix(first.Resolved().Config.Version, "rt-") {
		t.Fatal("runtime policy lacks distinct ruleset")
	}
}
