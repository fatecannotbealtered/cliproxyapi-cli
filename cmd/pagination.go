package cmd

import "github.com/fatecannotbealtered/cliproxyapi-cli/internal/output"

const defaultListLimit = 100

func validatePagination(limit, offset int) error {
	if limit <= 0 {
		return output.NewError("E_VALIDATION", "--limit must be greater than zero", nil)
	}
	if offset < 0 {
		return output.NewError("E_VALIDATION", "--offset must be zero or greater", nil)
	}
	return nil
}

func paginate[T any](items []T, limit, offset int) ([]T, int, bool) {
	if offset >= len(items) {
		return []T{}, offset, false
	}
	remaining := len(items) - offset
	if limit >= remaining {
		return items[offset:], len(items), false
	}
	end := offset + limit
	return items[offset:end], end, true
}

func addPaginationFields(data map[string]any, offset, nextOffset int, hasMore bool) {
	data["offset"] = offset
	data["has_more"] = hasMore
	data["next_offset"] = nil
	if hasMore {
		data["next_offset"] = nextOffset
	}
}
