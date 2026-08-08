package cliproxyapicli

import (
	_ "embed"
	"encoding/json"
)

//go:embed package.json
var packageJSON []byte

// Version is read from package.json, the repository's version source of truth.
var Version = func() string {
	var metadata struct {
		Version string `json:"version"`
	}
	if err := json.Unmarshal(packageJSON, &metadata); err != nil || metadata.Version == "" {
		panic("invalid package.json version")
	}
	return metadata.Version
}()
