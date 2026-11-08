package openapi3filter_test

import (
	"bytes"
	"mime/multipart"
	"net/http"
	"net/textproto"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/getkin/kin-openapi/openapi3"
	"github.com/getkin/kin-openapi/openapi3filter"
	"github.com/getkin/kin-openapi/routers/gorillamux"
)

// GHSA-mmfr-pmjx-hw9w: a nil-pointer dereference in convertParseError (reached
// via ConvertErrors / ValidationErrorEncoder) lets an unauthenticated client
// crash a server with a single multipart/form-data request.
//
// convertParseError dereferences e.Parameter.In in the "query" branch without a
// nil guard. For request-body errors e.Parameter is always nil, and only the
// multipart decoder produces the nested *ParseError shape that reaches that
// branch: it wraps the *ParseError returned while decoding a single part, so
// any part whose decoding fails with KindInvalidFormat panics. In this version
// that is a part carrying its own JSON/YAML Content-Type with malformed
// content, or a part whose content cannot be read (e.g. a truncated body).
//
// Sending the crafted request through ValidateRequest and then ConvertErrors
// must not panic; it must return a 400 with a meaningful message. The
// application/json control cases confirm the JSON body path is unaffected.
func TestGHSA_mmfr_pmjx_hw9w_ConvertErrors_NoPanic(t *testing.T) {
	spec := `
openapi: '3.0.3'
info:
  title: t
  version: '1.0.0'
paths:
  /upload:
    post:
      requestBody:
        required: true
        content:
          multipart/form-data:
            schema:
              type: object
              properties:
                age: {type: integer}
          application/json:
            schema:
              type: object
              properties:
                age: {type: integer}
      responses:
        '200': {description: ok}
`[1:]

	loader := openapi3.NewLoader()
	ctx := loader.Context

	doc, err := loader.LoadFromData([]byte(spec))
	require.NoError(t, err)
	require.NoError(t, doc.Validate(ctx))

	router, err := gorillamux.NewRouter(doc)
	require.NoError(t, err)

	// multipartBody builds a multipart/form-data body with a single "age"
	// part. A non-empty partContentType is sent as the part's own
	// Content-Type, which selects the body decoder used for that part.
	// truncated leaves out the closing boundary, so reading the part fails.
	multipartBody := func(value, partContentType string, truncated bool) (string, string) {
		var buf bytes.Buffer
		w := multipart.NewWriter(&buf)
		h := make(textproto.MIMEHeader)
		h.Set("Content-Disposition", `form-data; name="age"`)
		if partContentType != "" {
			h.Set("Content-Type", partContentType)
		}
		pw, err := w.CreatePart(h)
		require.NoError(t, err)
		_, err = pw.Write([]byte(value))
		require.NoError(t, err)
		if !truncated {
			require.NoError(t, w.Close())
		}
		return w.FormDataContentType(), buf.String()
	}

	for _, tc := range []struct {
		name            string
		contentType     string
		body            string
		partContentType string
		truncated       bool
		wantStatus      int
	}{
		{
			// The crash: a JSON part with malformed content fails decoding.
			// e.Parameter == nil and the cause is a nested *ParseError.
			name:            "multipart malformed json part (the crash)",
			body:            "notanumber",
			partContentType: "application/json",
			wantStatus:      http.StatusBadRequest,
		},
		{
			// The crash: same shape through the YAML part decoder.
			name:            "multipart malformed yaml part (the crash)",
			body:            "[1, 2",
			partContentType: "application/yaml",
			wantStatus:      http.StatusBadRequest,
		},
		{
			// The crash: a plain part that cannot be read to the end
			// (missing closing boundary) fails decoding the same way.
			name:       "multipart truncated part (the crash)",
			body:       "42",
			truncated:  true,
			wantStatus: http.StatusBadRequest,
		},
		{
			// Control: a malformed scalar in a plain part. Plain parts are
			// not parsed as primitives here, so the value is rejected by
			// schema validation (convertSchemaError, 422).
			name:       "multipart malformed scalar",
			body:       "notanumber",
			wantStatus: http.StatusUnprocessableEntity,
		},
		{
			// Control: malformed JSON body. *ParseError whose cause is an
			// encoding/json error, so the type assertion fails and the safe
			// fallback branch is taken.
			name:        "json malformed body",
			contentType: "application/json",
			body:        `{"age": `,
			wantStatus:  http.StatusBadRequest,
		},
		{
			// Control: wrong-type JSON body. Routed through convertSchemaError
			// (422), never reaching convertParseError.
			name:        "json wrong-type body",
			contentType: "application/json",
			body:        `{"age": "notanumber"}`,
			wantStatus:  http.StatusUnprocessableEntity,
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			contentType, body := tc.contentType, tc.body
			if contentType == "" {
				contentType, body = multipartBody(tc.body, tc.partContentType, tc.truncated)
			}

			req, err := http.NewRequest(http.MethodPost, "/upload", strings.NewReader(body))
			require.NoError(t, err)
			req.Header.Set("Content-Type", contentType)

			route, pathParams, err := router.FindRoute(req)
			require.NoError(t, err)

			reqErr := openapi3filter.ValidateRequest(ctx, &openapi3filter.RequestValidationInput{
				Request:    req,
				PathParams: pathParams,
				Route:      route,
				Options:    &openapi3filter.Options{AuthenticationFunc: openapi3filter.NoopAuthenticationFunc},
			})
			require.Error(t, reqErr)

			// This is what a typical error-rendering middleware calls; it must
			// not panic.
			var converted error
			require.NotPanics(t, func() {
				converted = openapi3filter.ConvertErrors(reqErr)
			})

			var validationErr *openapi3filter.ValidationError
			require.ErrorAs(t, converted, &validationErr)
			require.Equal(t, tc.wantStatus, validationErr.Status)
			require.NotEmpty(t, validationErr.Title, "converted error should carry a meaningful message")
		})
	}
}
