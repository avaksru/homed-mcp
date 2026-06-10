package mqtt

import "github.com/google/uuid"

// newID returns a short random correlation id used in HOMEd request/response.
func newID() (string, error) {
	id, err := uuid.NewRandom()
	if err != nil {
		return "", err
	}
	return id.String(), nil
}