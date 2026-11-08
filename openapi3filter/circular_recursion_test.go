package openapi3filter_test

import (
	"context"
	"net/http"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/getkin/kin-openapi/openapi3"
	"github.com/getkin/kin-openapi/openapi3filter"
	"github.com/getkin/kin-openapi/routers/gorillamux"
)

// These tests cover a fatal stack overflow triggered by a schema whose
// allOf/anyOf/oneOf chain refers back to itself. Both the spec loader and
// Schema.validate() accept such schemas, but the runtime visitor
// (Schema.visitJSON -> visitXOFOperations) and the request-decoding
// recursion (openapi3filter.decodeValue) lacked cycle detection and
// recursed until the goroutine stack was exhausted, aborting the process
// with an unrecoverable runtime error. Both paths must now terminate
// with a clean error instead.

func TestCircularAllOf_VisitJSONNoCrash(t *testing.T) {
	recursive := &openapi3.Schema{
		Type: &openapi3.Types{"object"},
		Properties: openapi3.Schemas{
			"name": {Value: &openapi3.Schema{Type: &openapi3.Types{"string"}}},
		},
	}
	recursive.AllOf = openapi3.SchemaRefs{{Value: recursive}}

	require.NoError(t, recursive.Validate(context.Background()))
	_ = recursive.VisitJSON(map[string]any{"name": "test"})
}

const circularAllOfSpec = `
openapi: 3.0.0
info: {title: t, version: '1.0'}
paths:
  /q:
    get:
      parameters:
        - name: data
          in: query
          schema: {$ref: '#/components/schemas/Recursive'}
      responses: {"200": {description: OK}}
components:
  schemas:
    Recursive:
      type: object
      allOf:
        - $ref: '#/components/schemas/Recursive'
      properties:
        name: {type: string}
`

func TestCircularAllOf_DecodeValueNoCrash(t *testing.T) {
	loader := openapi3.NewLoader()
	doc, err := loader.LoadFromData([]byte(circularAllOfSpec))
	require.NoError(t, err)
	require.NoError(t, doc.Validate(loader.Context))
	router, err := gorillamux.NewRouter(doc)
	require.NoError(t, err)

	req, err := http.NewRequest(http.MethodGet, "/q?data=x", strings.NewReader(""))
	require.NoError(t, err)
	route, pathParams, err := router.FindRoute(req)
	require.NoError(t, err)

	err = openapi3filter.ValidateRequest(context.Background(), &openapi3filter.RequestValidationInput{
		Request: req, PathParams: pathParams, Route: route,
		Options: &openapi3filter.Options{AuthenticationFunc: openapi3filter.NoopAuthenticationFunc},
	})
	require.Error(t, err, "must return a clean error, not crash")
}

// validateCircularRequest loads spec, then routes and validates a GET request for target.
func validateCircularRequest(t *testing.T, spec, target string) error {
	t.Helper()

	loader := openapi3.NewLoader()
	doc, err := loader.LoadFromData([]byte(spec))
	require.NoError(t, err)
	require.NoError(t, doc.Validate(loader.Context))
	router, err := gorillamux.NewRouter(doc)
	require.NoError(t, err)

	req, err := http.NewRequest(http.MethodGet, target, strings.NewReader(""))
	require.NoError(t, err)
	route, pathParams, err := router.FindRoute(req)
	require.NoError(t, err)

	return openapi3filter.ValidateRequest(context.Background(), &openapi3filter.RequestValidationInput{
		Request: req, PathParams: pathParams, Route: route,
		Options: &openapi3filter.Options{AuthenticationFunc: openapi3filter.NoopAuthenticationFunc},
	})
}

func TestCircularAnyOfOneOf_VisitJSONNoCrash(t *testing.T) {
	for _, keyword := range []string{"anyOf", "oneOf"} {
		keyword := keyword
		t.Run(keyword, func(t *testing.T) {
			recursive := &openapi3.Schema{
				Type: &openapi3.Types{"object"},
				Properties: openapi3.Schemas{
					"name": {Value: &openapi3.Schema{Type: &openapi3.Types{"string"}}},
				},
			}
			if keyword == "anyOf" {
				recursive.AnyOf = openapi3.SchemaRefs{{Value: recursive}}
			} else {
				recursive.OneOf = openapi3.SchemaRefs{{Value: recursive}}
			}

			require.NoError(t, recursive.Validate(context.Background()))
			_ = recursive.VisitJSON(map[string]any{"name": "test"})
		})
	}
}

func TestCircularAnyOfOneOf_DecodeValueNoCrash(t *testing.T) {
	for _, keyword := range []string{"anyOf", "oneOf"} {
		keyword := keyword
		t.Run(keyword, func(t *testing.T) {
			spec := strings.Replace(circularAllOfSpec, "allOf:", keyword+":", 1)
			_ = validateCircularRequest(t, spec, "/q?data=x")
		})
	}
}

// The items of a query array are decoded by urlValuesDecoder.parseValue,
// which recursed through allOf/anyOf/oneOf without cycle detection.
const circularArrayItemsSpec = `
openapi: 3.0.0
info: {title: t, version: '1.0'}
paths:
  /q:
    get:
      parameters:
        - name: data
          in: query
          schema:
            type: array
            items: {$ref: '#/components/schemas/Recursive'}
      responses: {"200": {description: OK}}
components:
  schemas:
    Recursive:
      type: string
      allOf:
        - $ref: '#/components/schemas/Recursive'
`

func TestCircularXOf_ParseValueNoCrash(t *testing.T) {
	for _, keyword := range []string{"allOf", "anyOf", "oneOf"} {
		keyword := keyword
		t.Run(keyword, func(t *testing.T) {
			spec := strings.Replace(circularArrayItemsSpec, "allOf:", keyword+":", 1)
			err := validateCircularRequest(t, spec, "/q?data=x")
			require.Error(t, err, "must return a clean error, not crash")
		})
	}

	err := validateCircularRequest(t, circularArrayItemsSpec, "/q?data=x")
	require.ErrorContains(t, err, "circular schema reference detected while decoding")
}

// The properties of a deepObject query parameter are decoded by buildResObj,
// which recursed through allOf/anyOf/oneOf (buildFromSchemas) without cycle detection.
const circularDeepObjectSpec = `
openapi: 3.0.0
info: {title: t, version: '1.0'}
paths:
  /q:
    get:
      parameters:
        - name: data
          in: query
          style: deepObject
          explode: true
          schema:
            type: object
            properties:
              inner: {$ref: '#/components/schemas/Recursive'}
      responses: {"200": {description: OK}}
components:
  schemas:
    Recursive:
      allOf:
        - $ref: '#/components/schemas/Recursive'
      properties:
        name: {type: string}
`

func TestCircularXOf_BuildResObjNoCrash(t *testing.T) {
	for _, keyword := range []string{"allOf", "anyOf", "oneOf"} {
		keyword := keyword
		t.Run(keyword, func(t *testing.T) {
			spec := strings.Replace(circularDeepObjectSpec, "allOf:", keyword+":", 1)
			_ = validateCircularRequest(t, spec, "/q?data%5Binner%5D=x")
		})
	}
}
