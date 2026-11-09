package prompts

import _ "embed"

//go:embed review_v1.md
var ReviewV1 string

// CurrentVersion identifies the system prompt actually used for new review attempts.
const CurrentVersion = "review_v2"

//go:embed review_v2.md
var ReviewV2 string
