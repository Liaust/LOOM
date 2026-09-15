package artifacts

const (
	StatusCreated  = "created"
	StatusIndexed  = "indexed"
	StatusFailed   = "failed"
	StatusArchived = "archived"
)

var allowedStatuses = map[string]struct{}{
	StatusCreated:  {},
	StatusIndexed:  {},
	StatusFailed:   {},
	StatusArchived: {},
}

func ValidStatus(status string) bool {
	_, ok := allowedStatuses[status]
	return ok
}
