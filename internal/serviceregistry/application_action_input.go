package serviceregistry

func DecodeApplicationActionRequest(raw []byte) (ApplicationActionRequest, error) {
	var q ApplicationActionRequest
	if applicationDecodeJSON(raw, &q) != nil || !q.valid() {
		return q, applicationError("action.request_invalid")
	}
	return q, nil
}
