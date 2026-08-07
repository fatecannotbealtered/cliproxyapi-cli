package cliproxyapicli

import _ "embed"

// ChangelogMarkdown is embedded from CHANGELOG.md, the single changelog source.
//
//go:embed CHANGELOG.md
var ChangelogMarkdown string
