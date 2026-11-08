package openapi3

import (
	"net/http"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestPathItemGetOperationUnsupportedMethod(t *testing.T) {
	get := &Operation{Responses: NewResponses()}
	pathItem := &PathItem{Get: get}

	require.Same(t, get, pathItem.GetOperation(http.MethodGet))
	require.Nil(t, pathItem.GetOperation(http.MethodPost))

	// Methods are attacker-controlled when taken from an incoming request:
	// unhandled ones must yield no operation instead of panicking.
	for _, method := range []string{"FOO", "PROPFIND", "get", ""} {
		var operation *Operation
		require.NotPanics(t, func() { operation = pathItem.GetOperation(method) }, method)
		require.Nil(t, operation, method)
	}
}
