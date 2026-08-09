package httpx

import "net/http"

// Operation declares one API endpoint once: the mux mounts Handler at
// Pattern, and the contracts generator (internal/contracts) reflects
// Request/Response/Status into the OpenAPI document. Route and contract
// can never drift because they are the same value.
type Operation struct {
	Pattern  string // ServeMux pattern: "POST /api/v1/auth/login"
	ID       string // OpenAPI operationId
	Summary  string
	Request  any // zero value of the request body struct; nil = no body
	Response any // zero value of the success response struct; nil = no body
	Status   int // success status code
	// Errors are the additional non-2xx statuses this endpoint really answers
	// with. 400 is documented on every operation; list only the extras. It
	// lives here rather than in the generator because an endpoint's answer set
	// is a property of the endpoint, which is what keeps route and contract in
	// one value.
	Errors  []int
	Handler http.Handler
}

// Mount registers every operation on mux.
func Mount(mux *http.ServeMux, ops []Operation) {
	for _, op := range ops {
		mux.Handle(op.Pattern, op.Handler)
	}
}
