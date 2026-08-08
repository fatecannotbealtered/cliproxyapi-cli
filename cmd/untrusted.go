package cmd

func authFileListUntrustedFields() []string {
	return []string{
		"items.id", "items.auth_index", "items.name", "items.provider", "items.type",
		"items.account", "items.email", "items.status", "items.status_message", "items.updated_at",
	}
}

func authFileStatusUntrustedFields() []string {
	fields := append([]string{}, authFileStatusResultUntrustedFields()...)
	return append(fields, authFileStatusPreviewUntrustedFields()...)
}

func authFileStatusPreviewUntrustedFields() []string {
	return []string{"preview.name", "preview.auth_index", "preview.version.id", "preview.version.updated_at"}
}

func authFileStatusResultUntrustedFields() []string {
	return []string{"name", "auth_index", "updated_at"}
}

func quotaInspectionUntrustedFields() []string {
	return []string{"items.target", "items.name", "items.auth_index", "items.evidence"}
}

func guardResultUntrustedFields() []string {
	return []string{"decisions.identity", "decisions.name", "decisions.auth_index", "decisions.provider"}
}
