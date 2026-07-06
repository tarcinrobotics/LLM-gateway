package auth

import (
	"database/sql"
	"strings"

	"llm_gateway/internal/database"
)

// IsModelAllowed checks if a given role is allowed to access the specified model name.
func IsModelAllowed(db *sql.DB, roleID int64, modelName string) (bool, error) {
	perms, err := database.GetPermissionsByRole(db, roleID)
	if err != nil {
		return false, err
	}

	// Normalise model name to lowercase for comparison
	modelNameLower := strings.ToLower(modelName)

	for _, p := range perms {
		if p.ModelName == "*" {
			return true, nil
		}
		if strings.ToLower(p.ModelName) == modelNameLower {
			return true, nil
		}
		// Also support prefixes, e.g. "llama3:" matches "llama3:latest" or "llama3"
		if strings.HasPrefix(modelNameLower, strings.ToLower(p.ModelName)+":") {
			return true, nil
		}
	}

	return false, nil
}
