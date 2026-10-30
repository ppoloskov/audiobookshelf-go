// Package schemaassets embeds oracle-exported schema and fresh-init settings.
package schemaassets

import _ "embed"

//go:embed schema.sql
var SchemaSQL string

//go:embed default-settings.json
var DefaultSettingsJSON []byte

//go:embed migration-state.json
var MigrationStateJSON []byte
