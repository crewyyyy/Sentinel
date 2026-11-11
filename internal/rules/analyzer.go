package rules

import (
	"fmt"
	"sort"

	"sentinel/internal/domain"
	"sentinel/internal/rules/match"
)

type MatcherConfig struct {
	Mode  string   `json:"mode" yaml:"mode"`
	Terms []string `json:"terms" yaml:"terms"`
}

type ModuleConfig struct {
	Enabled    *bool                    `json:"enabled" yaml:"enabled"`
	Matchers   map[string]MatcherConfig `json:"matchers" yaml:"matchers"`
	Thresholds map[string]int           `json:"thresholds" yaml:"thresholds"`
}

type Options struct {
	Modules    map[string]ModuleConfig `json:"modules" yaml:"modules"`
	FamilyCaps map[string]int          `json:"family_caps" yaml:"family_caps"`
}

// Finding is detector evidence, not policy: only the aggregator assigns weights,
// mandatory status, deduplication, trust discount and the final risk band.
type Finding struct {
	Code       string
	Evidence   string
	Confidence float64
}

type input struct {
	message  domain.Message
	urls     []domain.URL
	first    bool
	text     match.Prepared
	parts    []match.Prepared
	contexts []match.Context
}

func (in input) matches(matcher *match.Matcher) bool {
	return matcher.MatchParts(in.text, in.parts)
}

type detector interface{ Detect(input) []Finding }
type detectorFunc func(input) []Finding

func (f detectorFunc) Detect(in input) []Finding { return f(in) }

type stage struct {
	order    int
	detector detector
}
type moduleSettings struct {
	matchers   map[string]*match.Matcher
	thresholds map[string]int
}
type buildContext struct{ modules map[string]moduleSettings }

// Analyzer is compiled once at startup and is safe for concurrent assessments.
// It owns copies of every caller-supplied setting and never mutates inputs.
type Analyzer struct {
	config     Config
	options    Options
	stages     []stage
	signals    map[string]SignalSpec
	contextual bool
}

type ResolvedSnapshot struct {
	FeaturesVersion      int     `json:"features_version"`
	NormalizationVersion string  `json:"normalization_version"`
	EngineVersion        string  `json:"engine_version"`
	Config               Config  `json:"config"`
	Options              Options `json:"options"`
	Catalog              []Spec  `json:"catalog"`
}

// Snapshot returns independent, JSON-serializable copies of the resolved policy
// and implementation versions for durable ruleset registration and replay.
func (a *Analyzer) Snapshot() any {
	return ResolvedSnapshot{EngineVersion: "modular-v1", FeaturesVersion: domain.CurrentFeaturesVersion, NormalizationVersion: match.NormalizationVersion, Config: copyConfig(a.config), Options: copyOptions(a.options), Catalog: Catalog()}
}

func NewAnalyzer(c Config, options Options) (*Analyzer, error) {
	specs := Catalog()
	resolved, modules, err := resolveOptions(specs, options)
	if err != nil {
		return nil, err
	}
	a := &Analyzer{config: copyConfig(c), options: resolved, signals: map[string]SignalSpec{}}
	context := buildContext{modules: modules}
	for _, spec := range specs {
		for _, signal := range spec.Signals {
			a.signals[signal.Code] = signal
		}
		if *resolved.Modules[spec.ID].Enabled {
			if _, ok := spec.Thresholds["policy"]; ok {
				a.contextual = true
			}
			a.stages = append(a.stages, spec.build(context)...)
		}
	}
	sort.SliceStable(a.stages, func(i, j int) bool { return a.stages[i].order < a.stages[j].order })
	return a, nil
}

func ValidateOptions(options Options) error {
	_, _, err := resolveOptions(Catalog(), options)
	return err
}

func resolveOptions(specs []Spec, options Options) (Options, map[string]moduleSettings, error) {
	known := map[string]Spec{}
	families := map[string]bool{}
	for _, spec := range specs {
		known[spec.ID] = spec
		for _, signal := range spec.Signals {
			families[signal.Family] = true
		}
	}
	for _, id := range sortedKeys(options.Modules) {
		if _, ok := known[id]; !ok {
			return Options{}, nil, fmt.Errorf("unknown analysis module %q", id)
		}
	}
	for _, family := range sortedKeys(options.FamilyCaps) {
		cap := options.FamilyCaps[family]
		if !families[family] {
			return Options{}, nil, fmt.Errorf("unknown analysis family %q", family)
		}
		if cap < 0 || cap > 100 {
			return Options{}, nil, fmt.Errorf("analysis family %q cap must be in 0..100", family)
		}
	}
	resolved := Options{Modules: map[string]ModuleConfig{}, FamilyCaps: map[string]int{}}
	for family, cap := range options.FamilyCaps {
		resolved.FamilyCaps[family] = cap
	}
	modules := map[string]moduleSettings{}
	for _, spec := range specs {
		override := options.Modules[spec.ID]
		enabled := spec.DefaultEnabled
		if override.Enabled != nil {
			enabled = *override.Enabled
		}
		if spec.Required && !enabled {
			return Options{}, nil, fmt.Errorf("analysis module %q supplies mandatory triggers and cannot be disabled", spec.ID)
		}
		for _, name := range sortedKeys(override.Matchers) {
			if _, ok := spec.Matchers[name]; !ok {
				return Options{}, nil, fmt.Errorf("unknown matcher %q for module %q", name, spec.ID)
			}
		}
		for _, name := range sortedKeys(override.Thresholds) {
			if _, ok := spec.Thresholds[name]; !ok {
				return Options{}, nil, fmt.Errorf("unknown threshold %q for module %q", name, spec.ID)
			}
		}
		config := ModuleConfig{Enabled: &enabled, Matchers: map[string]MatcherConfig{}, Thresholds: map[string]int{}}
		settings := moduleSettings{matchers: map[string]*match.Matcher{}, thresholds: map[string]int{}}
		for _, name := range sortedKeys(spec.Matchers) {
			matcherSpec := spec.Matchers[name]
			matcherConfig := matcherSpec.Default
			if value, ok := override.Matchers[name]; ok {
				if value.Mode != "" {
					matcherConfig.Mode = value.Mode
				}
				if value.Terms != nil {
					matcherConfig.Terms = value.Terms
				}
			}
			allowed := false
			for _, mode := range matcherSpec.Modes {
				allowed = allowed || matcherConfig.Mode == mode
			}
			if !allowed {
				return Options{}, nil, fmt.Errorf("invalid mode %q for matcher %s.%s", matcherConfig.Mode, spec.ID, name)
			}
			matcher, err := match.New(matcherConfig.Mode, matcherConfig.Terms)
			if err != nil {
				return Options{}, nil, fmt.Errorf("matcher %s.%s: %w", spec.ID, name, err)
			}
			matcherConfig.Terms = append([]string(nil), matcherConfig.Terms...)
			config.Matchers[name] = matcherConfig
			settings.matchers[name] = matcher
		}
		for _, name := range sortedKeys(spec.Thresholds) {
			thresholdSpec := spec.Thresholds[name]
			value := thresholdSpec.Default
			if v, ok := override.Thresholds[name]; ok {
				value = v
			}
			if value < thresholdSpec.Min || value > thresholdSpec.Max {
				return Options{}, nil, fmt.Errorf("threshold %s.%s must be in %d..%d", spec.ID, name, thresholdSpec.Min, thresholdSpec.Max)
			}
			config.Thresholds[name] = value
			settings.thresholds[name] = value
		}
		resolved.Modules[spec.ID] = config
		modules[spec.ID] = settings
	}
	return resolved, modules, nil
}

func sortedKeys[V any](values map[string]V) []string {
	keys := make([]string, 0, len(values))
	for key := range values {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	return keys
}

func copyConfig(c Config) Config {
	if c.Weights != nil {
		weights := make(map[string]int, len(c.Weights))
		for code, weight := range c.Weights {
			weights[code] = weight
		}
		c.Weights = weights
	}
	if c.Thresholds != nil {
		c.Thresholds = append([]int{}, c.Thresholds...)
	}
	return c
}

func copyOptions(options Options) Options {
	result := Options{Modules: map[string]ModuleConfig{}, FamilyCaps: map[string]int{}}
	for family, cap := range options.FamilyCaps {
		result.FamilyCaps[family] = cap
	}
	for id, config := range options.Modules {
		copy := ModuleConfig{Matchers: map[string]MatcherConfig{}, Thresholds: map[string]int{}}
		if config.Enabled != nil {
			enabled := *config.Enabled
			copy.Enabled = &enabled
		}
		for name, matcher := range config.Matchers {
			matcher.Terms = append([]string(nil), matcher.Terms...)
			copy.Matchers[name] = matcher
		}
		for name, value := range config.Thresholds {
			copy.Thresholds[name] = value
		}
		result.Modules[id] = copy
	}
	return result
}

func (a *Analyzer) Assess(m domain.Message, urls []domain.URL, first bool, trust int) domain.Assessment {
	assessment := domain.Assessment{Trust: max(0, min(100, trust)), URLs: urls, Version: a.config.Version, Signals: []domain.Signal{}}
	text, parts := match.PrepareParts(m.Content(), m.QuoteText)
	in := input{message: m, urls: urls, first: first, text: text, parts: parts}
	if a.contextual {
		in.contexts = preparePolicyContexts(m)
	}
	seen := map[string]bool{}
	familyScores := map[string]int{}
	for _, stage := range a.stages {
		for _, finding := range stage.detector.Detect(in) {
			spec, known := a.signals[finding.Code]
			if !known {
				// A new module with a missing catalog entry must not silently suppress review.
				finding = Finding{Code: "ANALYSIS_MODULE_ERROR", Evidence: "unknown detector signal; analysis requires review", Confidence: .5}
				spec = a.signals[finding.Code]
			}
			if seen[finding.Code] {
				continue
			}
			seen[finding.Code] = true
			weight, ok := a.config.Weights[finding.Code]
			if !ok {
				weight = spec.DefaultWeight
			}
			assessment.Signals = append(assessment.Signals, domain.Signal{Code: finding.Code, Weight: weight, Evidence: finding.Evidence, Confidence: finding.Confidence, MandatoryAI: spec.Mandatory, RulesetVersion: a.config.Version})
			assessment.Score += weight
			familyScores[spec.Family] += weight
			assessment.Mandatory = assessment.Mandatory || spec.Mandatory
		}
	}
	// Caps constrain score contributions only; original evidence and configured
	// weights remain visible, and zero caps never suppress mandatory review.
	for family, cap := range a.options.FamilyCaps {
		if total := familyScores[family]; total > cap {
			assessment.Score -= total - cap
		}
	}
	discount := min(20, max(0, a.config.MaxTrustDiscount)) * assessment.Trust / 100
	assessment.Score = max(0, min(100, assessment.Score-discount))
	thresholds := a.config.Thresholds
	if len(thresholds) != 4 {
		thresholds = []int{25, 45, 70, 85}
	}
	bands := [...]string{"normal", "unusual", "suspicious", "high", "critical"}
	tier := 0
	for _, threshold := range thresholds {
		if assessment.Score >= threshold {
			tier++
		}
	}
	assessment.Band = bands[tier]
	assessment.ReviewRequired = assessment.Mandatory || assessment.Score >= a.config.AIThreshold
	checked := map[string]bool{}
	for _, signal := range assessment.Signals {
		family := a.signals[signal.Code].Family
		module := a.options.Modules[family]
		policy, configurable := module.Thresholds["policy"]
		if !configurable || checked[family] || module.Enabled == nil || !*module.Enabled {
			continue
		}
		checked[family] = true
		mode := "review"
		if policy == 1 {
			mode = "prohibited"
		}
		assessment.PolicyChecks = append(assessment.PolicyChecks, domain.PolicyCheck{Module: family, Mode: mode})
	}
	return assessment
}
